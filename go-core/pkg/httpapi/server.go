// Package httpapi exposes the application boundary used by the phone bridge and
// deterministic integration tests. Transcript tracing requires an explicit local debug sink.
package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/himera/tochka-voice/go-core/pkg/campaign"
	"github.com/himera/tochka-voice/go-core/pkg/diagnostics"
	"github.com/himera/tochka-voice/go-core/pkg/dialogue"
	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/safety"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
	"github.com/himera/tochka-voice/go-core/pkg/telephony"
)

type Redirector interface {
	Redirect(context.Context, string, string) error
}
type Config struct {
	FSM         *dialogue.FSMController
	MIS         misclient.MISClient
	Slots       *slotmgr.SlotManager
	Store       *storage.Store
	Campaign    *campaign.Runner
	Token       string
	Mode        string
	MaxSessions int
	SessionTTL  time.Duration
	Redirector  Redirector
	Escalate    dialogue.EscalateFunc
	Gate        *safety.FastGate
	Trace       *slog.Logger
	Events      *slog.Logger
}
type Response struct {
	dialogue.StepResult
	CallID   string              `json:"call_id"`
	Scenario models.ScenarioCode `json:"scenario,omitempty"`
	TurnID   string              `json:"turn_id,omitempty"`
}
type cached struct {
	hash     [32]byte
	response Response
}
type session struct {
	mu          sync.Mutex
	call        *models.CallSession
	cache       map[string]cached
	id          string
	channel     string
	control     sync.Mutex
	turnCancel  context.CancelFunc
	interrupted atomic.Pointer[interruptResult]
	closed      atomic.Bool
	patientID   atomic.Int64
	turns       int
}
type interruptResult struct {
	result   dialogue.StepResult
	priority int
}
type Server struct {
	cfg        Config
	mu         sync.Mutex
	sessions   map[string]*session
	handler    http.Handler
	background sync.WaitGroup
	gate       *safety.FastGate
}

func New(cfg Config) *Server {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 256
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 10 * time.Minute
	}
	gate := cfg.Gate
	if gate == nil {
		gate = safety.NewFastGate()
	}
	s := &Server{cfg: cfg, sessions: map[string]*session{}, gate: gate}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.health)
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("HEAD /healthz", s.health)
	mux.HandleFunc("HEAD /readyz", s.health)
	mux.HandleFunc("HEAD /health", s.health)
	mux.HandleFunc("POST /api/dialogue/start", s.start)
	mux.HandleFunc("POST /api/dialogue/turn", s.turn)
	mux.HandleFunc("POST /api/dialogue/{id}/end", s.end)
	mux.HandleFunc("POST /api/dialogue/{id}/transfer", s.transfer)
	mux.HandleFunc("POST /api/dialogue/{id}/interrupt", s.interrupt)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/doctors", s.doctors)
	mux.HandleFunc("GET /api/visits", s.visits)
	mux.HandleFunc("GET /api/clients/{id}", s.client)
	mux.HandleFunc("PUT /api/contacts/{id}", s.contact)
	mux.HandleFunc("POST /api/reminders/dial", s.dial)
	mux.HandleFunc("GET /api/escalations", s.escalations)
	mux.HandleFunc("GET /api/escalations/review", s.reviewEscalations)
	mux.HandleFunc("GET /api/slot-claims", s.slotClaims)
	mux.HandleFunc("POST /api/slot-claims/reconcile", s.reconcileClaims)
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		cleanPath := strings.TrimSuffix(r.URL.Path, "/")
		isHealth := cleanPath == "/healthz" || cleanPath == "/readyz" || cleanPath == "/health"
		if !isHealth {
			authHeader := r.Header.Get("Authorization")
			token := ""
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}
			if cfg.Token == "" || token == "" {
				w.Header().Set("WWW-Authenticate", "Bearer")
				problem(w, 401, "unauthorized")
				return
			}
			want := sha256.Sum256([]byte(cfg.Token))
			got := sha256.Sum256([]byte(token))
			if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				problem(w, 401, "unauthorized")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("client disconnected during response")
	}
}
func problem(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"error": message})
}

func misProblem(w http.ResponseWriter, err error) {
	var api *misclient.APIError
	if errors.As(err, &api) && api.StatusCode == http.StatusTooManyRequests {
		seconds := max(1, int((api.RetryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		problem(w, http.StatusServiceUnavailable, "MIS quota temporarily unavailable; retry later")
		return
	}
	problem(w, http.StatusBadGateway, "MIS unavailable")
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || t != "application/json" {
		problem(w, 415, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		problem(w, 400, "invalid JSON request")
		return false
	}
	if err = d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		problem(w, 400, "request must contain one JSON object")
		return false
	}
	return true
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	c, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.cfg.Store.ReadDB().PingContext(c); err != nil {
		problem(w, 503, "storage unavailable")
		return
	}
	if strings.TrimSuffix(r.URL.Path, "/") == "/readyz" {
		if err := s.cfg.Store.WriteDB().PingContext(c); err != nil {
			problem(w, 503, "storage writer unavailable")
			return
		}
	}
	write(w, 200, map[string]any{"status": "ok", "mode": s.cfg.Mode, "doctors": len(s.cfg.Slots.ListDoctors())})
}
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CallerNumber    string `json:"caller_number"`
		ChannelID       string `json:"channel_id"`
		OutboundVisitID int    `json:"outbound_visit_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.CallerNumber) > 100 || len(req.ChannelID) > 200 || req.OutboundVisitID < 0 {
		problem(w, 400, "invalid call metadata")
		return
	}
	s.mu.Lock()
	if len(s.sessions) >= s.cfg.MaxSessions {
		s.mu.Unlock()
		problem(w, 503, "session capacity reached")
		return
	}
	now := time.Now()
	call := &models.CallSession{CallID: uuid.NewString(), CallerNumber: req.CallerNumber, ChannelID: req.ChannelID, CurrentState: dialogue.StateMenu, CreatedAt: now, LastActivityAt: now}
	entry := &session{call: call, cache: map[string]cached{}, id: call.CallID, channel: call.ChannelID}
	entry.mu.Lock()
	s.sessions[call.CallID] = entry
	s.mu.Unlock()
	defer entry.mu.Unlock()
	result := s.cfg.FSM.InitialGreeting()
	if req.OutboundVisitID > 0 {
		result = s.cfg.FSM.StartReminder(r.Context(), call, req.OutboundVisitID)
	}
	call.CurrentState = result.NextState
	call.PendingPrompt = result.BotSpeech
	if call.Patient != nil {
		entry.patientID.Store(int64(call.Patient.ID))
	}
	if err := s.cfg.Store.Event(r.Context(), "CALL_STARTED"); err != nil {
		slog.Error("record call metric", "error", err)
	}
	write(w, 201, Response{StepResult: result, CallID: call.CallID, Scenario: call.CurrentScenario})
	if s.cfg.Trace != nil {
		s.cfg.Trace.Info("call_started", "call_id", call.CallID, "state", result.NextState, "bot_speech", result.BotSpeech)
	}
	if s.cfg.Events != nil {
		s.cfg.Events.Info("call_started", "call_id", call.CallID, "state", result.NextState, "outbound", req.OutboundVisitID > 0)
	}
}
func (s *Server) find(id string) *session { s.mu.Lock(); defer s.mu.Unlock(); return s.sessions[id] }
func (s *Server) turn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CallID       string `json:"call_id"`
		Text         string `json:"text"`
		TurnID       string `json:"turn_id"`
		ASRUncertain bool   `json:"asr_uncertain,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, err := uuid.Parse(req.CallID); err != nil || len([]rune(req.Text)) > 2000 || len(req.TurnID) > 100 {
		problem(w, 400, "invalid turn parameters")
		return
	}
	e := s.find(req.CallID)
	if e == nil {
		problem(w, 404, "unknown or expired call")
		return
	}
	if !e.mu.TryLock() {
		problem(w, 409, "another turn is in progress")
		return
	}
	defer e.mu.Unlock()
	hash := sha256.Sum256([]byte(req.Text + "\x00" + strconv.FormatBool(req.ASRUncertain)))
	if c, ok := e.cache[req.TurnID]; req.TurnID != "" && ok {
		if c.hash != hash {
			problem(w, 409, "turn_id was already used for another utterance")
			return
		}
		response := c.response
		if pending := e.interrupted.Load(); pending != nil {
			response.StepResult = pending.result
		}
		write(w, 200, response)
		return
	}
	if e.closed.Load() {
		problem(w, 410, "call is closed")
		return
	}
	if pending := e.interrupted.Load(); pending != nil {
		write(w, 200, Response{StepResult: pending.result, CallID: e.id, TurnID: req.TurnID})
		return
	}
	if e.turns >= 256 {
		problem(w, 429, "call turn capacity reached")
		return
	}
	requestContext := diagnostics.WithTranscript(diagnostics.WithCall(r.Context(), e.id), s.cfg.Trace)
	ctx, cancel := context.WithTimeout(requestContext, 35*time.Second)
	defer cancel()

	e.control.Lock()
	if pending := e.interrupted.Load(); pending != nil || e.closed.Load() {
		e.control.Unlock()
		if pending != nil {
			write(w, 200, Response{StepResult: pending.result, CallID: e.id, TurnID: req.TurnID})
		} else {
			problem(w, 410, "call is closed")
		}
		return
	}
	e.turnCancel = cancel
	e.control.Unlock()
	defer func() {
		e.control.Lock()
		e.turnCancel = nil
		e.control.Unlock()
	}()

	if e.call.OutboundVisitID > 0 && s.cfg.Campaign != nil {
		if err := s.cfg.Campaign.ExtendLease(ctx, e.call.OutboundVisitID, 4*time.Minute); err != nil {
			slog.Warn("failed to extend reminder lease", "visit_id", e.call.OutboundVisitID, "error", err)
		}
	}

	previousState := e.call.CurrentState
	optionsBefore := len(e.call.Options)
	e.turns++
	started := time.Now()
	result := s.cfg.FSM.ProcessRecognizedInput(ctx, e.call, req.Text, req.ASRUncertain)
	patientID := int64(0)
	if e.call.Patient != nil {
		patientID = int64(e.call.Patient.ID)
	}
	e.patientID.Store(patientID)
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer settleCancel()
	response := Response{StepResult: result, CallID: e.call.CallID, Scenario: e.call.CurrentScenario, TurnID: req.TurnID}
	if pending := e.interrupted.Load(); pending != nil {
		response.StepResult = pending.result
		e.call.CurrentState = dialogue.StateTransfer
		e.call.TransferTarget = pending.result.TransferTarget
		if err := s.cfg.Slots.Release(settleCtx, e.id); err != nil {
			slog.Error("release interrupted hold", "error", err)
		}
	}
	if req.TurnID != "" {
		e.cache[req.TurnID] = cached{hash: hash, response: response}

	}
	if result.Outcome != "" {
		if err := s.cfg.Store.Event(settleCtx, result.Outcome); err != nil {
			slog.Error("record outcome metric", "error", err)
		}
		if e.call.OutboundVisitID > 0 && s.cfg.Campaign != nil {
			if err := s.cfg.Campaign.Outcome(settleCtx, e.call.OutboundVisitID, result.Outcome); err != nil {
				slog.Error("record reminder outcome", "error", err)
			}
		}
	}
	if s.cfg.Trace != nil {
		s.cfg.Trace.Info("dialogue_turn", "call_id", e.id, "turn_id", req.TurnID,
			"text", req.Text, "parsed_intent", dialogue.ParseIntent(req.Text),
			"previous_state", previousState, "state", response.NextState, "scenario", response.Scenario,
			"fail_count", e.call.FailCount, "bot_speech", response.BotSpeech,
			"outcome", response.Outcome, "reason_code", response.ReasonCode,
			"transfer_target", response.TransferTarget, "latency_ms", time.Since(started).Milliseconds(),
			"nlu_attempted", e.call.NLUAttempted, "llm_used", response.LLMUsed, "nlu_intent", e.call.NLUIntent, "nlu_latency_ms", e.call.NLULatencyMS, "nlu_error", e.call.NLUError)
	}
	if s.cfg.Events != nil {
		s.cfg.Events.Info("dialogue_turn", "call_id", e.id, "turn", e.turns,
			"previous_state", previousState, "state", response.NextState, "scenario", response.Scenario,
			"input_chars", len([]rune(req.Text)), "confirmation", dialogue.Confirmation(req.Text), "options_before", optionsBefore,
			"fail_count", e.call.FailCount, "input_issue", result.DiagnosticCode,
			"outcome", response.Outcome, "reason_code", response.ReasonCode, "transfer_target", response.TransferTarget,
			"latency_ms", time.Since(started).Milliseconds(), "nlu_attempted", e.call.NLUAttempted,
			"llm_used", response.LLMUsed, "nlu_intent", e.call.NLUIntent, "nlu_latency_ms", e.call.NLULatencyMS, "nlu_error", e.call.NLUError)
	}
	write(w, 200, response)
}
func (s *Server) end(w http.ResponseWriter, r *http.Request) {
	e := s.find(r.PathValue("id"))
	if e == nil {
		problem(w, 404, "unknown call")
		return
	}
	e.control.Lock()
	e.closed.Store(true)
	if e.turnCancel != nil {
		e.turnCancel()
	}
	e.control.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := s.cfg.Slots.Release(r.Context(), e.call.CallID); err != nil {
		problem(w, 503, "could not release call resources")
		return
	}
	e.call.CurrentState = dialogue.StateDone
	e.closed.Store(true)
	e.call.LastActivityAt = time.Now()
	if s.cfg.Events != nil {
		s.cfg.Events.Info("call_ended", "call_id", e.id, "turns", e.turns)
	}
	write(w, 200, map[string]string{"status": "closed"})
}

func (s *Server) transfer(w http.ResponseWriter, r *http.Request) {
	e := s.find(r.PathValue("id"))
	if e == nil {
		problem(w, 404, "unknown call")
		return
	}
	if pending := e.interrupted.Load(); pending != nil {
		if s.cfg.Redirector == nil {
			problem(w, 503, "operator redirect is not configured")
			return
		}
		if err := s.cfg.Redirector.Redirect(r.Context(), e.channel, pending.result.TransferTarget); err != nil {
			problem(w, 502, "operator redirect failed; escalation remains queued")
			return
		}
		write(w, 200, map[string]string{"status": "transferred"})
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.call.CurrentState != dialogue.StateTransfer || e.call.TransferTarget == "" {
		problem(w, 409, "call is not awaiting transfer")
		return
	}
	if s.cfg.Redirector == nil {
		problem(w, 503, "operator redirect is not configured")
		return
	}
	if err := s.cfg.Redirector.Redirect(r.Context(), e.call.ChannelID, e.call.TransferTarget); err != nil {
		problem(w, 502, "operator redirect failed; escalation remains queued")
		return
	}
	write(w, 200, map[string]string{"status": "transferred"})
}

// interrupt has a separate control lock: an in-flight MIS write must not delay
// emergency speech or operator handoff. The original mutation still settles.
func (s *Server) interrupt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len([]rune(req.Text)) > 2000 {
		problem(w, 400, "utterance is too long")
		return
	}
	e := s.find(r.PathValue("id"))
	if e == nil {
		problem(w, 404, "unknown call")
		return
	}
	if s.cfg.Trace != nil {
		s.cfg.Trace.Info("asr_observed", "call_id", e.id, "text", req.Text)
	}
	if e.closed.Load() {
		problem(w, 410, "call is closed")
		return
	}
	gate := s.gate.CheckWithContext(r.Context(), req.Text)
	if gate.Level == models.FlagNone {
		write(w, 200, map[string]bool{"interrupted": false})
		return
	}
	e.control.Lock()
	if e.closed.Load() {
		e.control.Unlock()
		problem(w, 410, "call is closed")
		return
	}
	if old := e.interrupted.Load(); old != nil && old.priority >= gate.Priority() {
		e.control.Unlock()
		write(w, 200, Response{StepResult: old.result, CallID: e.id})
		return
	}
	if e.turnCancel != nil {
		e.turnCancel()
	}
	result := dialogue.StepResult{NextState: dialogue.StateTransfer, BotSpeech: gate.BotResponse, TransferTarget: gate.Extension(), Outcome: "TRANSFERRED"}
	result.ReasonCode = gate.ActionCode
	pending := &interruptResult{result: result, priority: gate.Priority()}
	e.interrupted.Store(pending)
	e.control.Unlock()

	// Disk contention must not delay emergency speech. Persistence gets a
	// bounded independent context; publication uses CAS so a late low-priority
	// job cannot overwrite a newer emergency interruption.
	patientID := int(e.patientID.Load())
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.cfg.Slots.Release(c, e.id); err != nil {
			slog.Error("release interrupted hold", "error", err)
		}
		if s.cfg.Escalate != nil {
			if err := s.cfg.Escalate(c, patientID, gate.ActionCode+"; звонок: "+e.id); err == nil {
				queued := pending.result
				queued.EscalationQueued = true
				e.interrupted.CompareAndSwap(pending, &interruptResult{result: queued, priority: pending.priority})
			} else {
				slog.Error("persist call interruption", "error", err)
			}
		}
		if err := s.cfg.Store.Event(c, "INTERRUPTED"); err != nil {
			slog.Error("record interruption metric", "error", err)
		}
	}()

	if s.cfg.Trace != nil {
		s.cfg.Trace.Info("call_interrupted", "call_id", e.id, "text", req.Text, "reason_code", gate.ActionCode, "transfer_target", result.TransferTarget, "bot_speech", result.BotSpeech)
	}
	write(w, 200, Response{StepResult: result, CallID: e.id})
}
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	counts, err := s.cfg.Store.Stats(r.Context())
	if err != nil {
		problem(w, 503, "statistics unavailable")
		return
	}
	s.mu.Lock()
	n := len(s.sessions)
	s.mu.Unlock()
	write(w, 200, map[string]any{"outcomes": counts, "retained_sessions": n, "mode": s.cfg.Mode})
}
func (s *Server) doctors(w http.ResponseWriter, r *http.Request) {
	write(w, 200, s.cfg.Slots.ListDoctors())
}
func (s *Server) visits(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	v, err := s.cfg.MIS.ListVisits(ctx, 0, "")
	if err != nil {
		misProblem(w, err)
		return
	}
	write(w, 200, v)
}

func (s *Server) client(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		problem(w, 400, "positive client id required")
		return
	}
	c, err := s.cfg.MIS.GetClient(r.Context(), id)
	if err != nil {
		var api *misclient.APIError
		if errors.As(err, &api) && api.StatusCode == 404 {
			problem(w, 404, "client not found")
		} else {
			misProblem(w, err)
		}
		return
	}
	write(w, 200, c)
}
func (s *Server) contact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
		Consent  bool   `json:"reminder_consent"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := 0
	for _, c := range r.PathValue("id") {
		if c < '0' || c > '9' {
			problem(w, 400, "invalid client id")
			return
		}
		id = id*10 + int(c-'0')
		if id > 2147483647 {
			problem(w, 400, "invalid client id")
			return
		}
	}
	if id <= 0 || !telephony.ValidEndpoint(req.Endpoint) {
		problem(w, 400, "valid client id and PJSIP endpoint required")
		return
	}
	if _, err := s.cfg.MIS.GetClient(r.Context(), id); err != nil {
		var api *misclient.APIError
		if errors.As(err, &api) && api.StatusCode == 404 {
			problem(w, 404, "client not found")
			return
		}
		misProblem(w, err)
		return
	}
	if err := s.cfg.Store.SaveContact(r.Context(), storage.Contact{ClientID: id, Endpoint: req.Endpoint, ReminderConsent: req.Consent}); err != nil {
		problem(w, 503, "contact could not be saved")
		return
	}
	write(w, 200, map[string]string{"status": "saved"})
}
func (s *Server) dial(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VisitID int `json:"visit_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.VisitID <= 0 {
		problem(w, 400, "positive visit_id required")
		return
	}
	if s.cfg.Campaign == nil {
		problem(w, 503, "outbound calling unavailable")
		return
	}
	if err := s.cfg.Campaign.Dial(r.Context(), req.VisitID, time.Now()); err != nil {
		problem(w, 409, err.Error())
		return
	}
	write(w, 202, map[string]string{"status": "queued"})
}
func (s *Server) escalations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.cfg.Store.ReadDB().QueryContext(r.Context(), "SELECT status,COUNT(*) FROM escalations GROUP BY status")
	if err != nil {
		problem(w, 503, "outbox unavailable")
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err = rows.Scan(&status, &n); err != nil {
			problem(w, 503, "outbox unavailable")
			return
		}
		counts[status] = n
	}
	if rows.Err() != nil {
		problem(w, 503, "outbox unavailable")
		return
	}
	write(w, 200, counts)
}

func (s *Server) Expire(ctx context.Context, now time.Time) {
	s.mu.Lock()
	entries := make(map[string]*session, len(s.sessions))
	for id, e := range s.sessions {
		entries[id] = e
	}
	s.mu.Unlock()
	for id, e := range entries {
		if !e.mu.TryLock() {
			continue
		}
		if !e.closed.Load() && e.call.OutboundVisitID > 0 && s.cfg.Campaign != nil && e.call.CurrentState != dialogue.StateTransfer && e.call.CurrentState != dialogue.StateDone && now.Sub(e.call.LastActivityAt) <= s.cfg.SessionTTL {
			if err := s.cfg.Campaign.ExtendLease(ctx, e.call.OutboundVisitID, 4*time.Minute); err != nil {
				slog.Warn("extend active reminder lease", "error", err)
			}
		}
		ttl := s.cfg.SessionTTL
		if e.call.CurrentState == dialogue.StateDone || e.call.CurrentState == dialogue.StateTransfer {
			ttl = time.Minute
		}
		if now.Sub(e.call.LastActivityAt) > ttl {
			if err := s.cfg.Slots.Release(ctx, id); err == nil {
				s.mu.Lock()
				delete(s.sessions, id)
				s.mu.Unlock()
			} else {
				slog.Warn("expire call resources", "error", err)
			}
		}
		e.mu.Unlock()
	}
}
func (s *Server) RunJanitor(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	purgeTicker := time.NewTicker(30 * time.Second)
	defer t.Stop()
	defer purgeTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.Expire(ctx, now)
		case <-purgeTicker.C:
			if s.cfg.Slots != nil {
				if err := s.cfg.Slots.PurgeExpired(ctx); err != nil {
					slog.Warn("purge expired slot claims", "error", err)
				}
			}
		}
	}
}

func (s *Server) WaitBackground(ctx context.Context) error {
	done := make(chan struct{})
	go func() { s.background.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) slotClaims(w http.ResponseWriter, r *http.Request) {
	claims, err := s.cfg.Slots.ReviewClaims(r.Context())
	if err != nil {
		problem(w, 503, "slot claims unavailable")
		return
	}
	write(w, 200, claims)
}

func (s *Server) reconcileClaims(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	visits, err := s.cfg.MIS.ListVisits(ctx, 0, "")
	if err != nil {
		misProblem(w, err)
		return
	}
	if err := s.cfg.Slots.Reconcile(ctx, visits); err != nil {
		problem(w, 503, "slot reconciliation failed")
		return
	}
	s.slotClaims(w, r)
}

func (s *Server) reviewEscalations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.cfg.Store.ReadDB().QueryContext(r.Context(), "SELECT id,status,mis_id,attempts FROM escalations WHERE status IN ('UNCERTAIN','SENDING','FAILED') ORDER BY created_at,id")
	if err != nil {
		problem(w, 503, "outbox unavailable")
		return
	}
	defer rows.Close()
	type item struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		MISID    int    `json:"mis_id"`
		Attempts int    `json:"attempts"`
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.ID, &i.Status, &i.MISID, &i.Attempts); err != nil {
			problem(w, 503, "outbox unavailable")
			return
		}
		items = append(items, i)
	}
	if rows.Err() != nil {
		problem(w, 503, "outbox unavailable")
		return
	}
	write(w, 200, items)
}
