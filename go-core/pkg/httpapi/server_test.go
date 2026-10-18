package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/dialogue"
	"github.com/himera/tochka-voice/go-core/pkg/escalation"
	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/schedule"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

func TestDebugTraceExplainsMisunderstandings(t *testing.T) {
	api, _ := testAPI(t, "")
	var output bytes.Buffer
	api.cfg.Trace = slog.New(slog.NewJSONHandler(&output, nil))
	w := request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "")
	var started Response
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"неизвестная реплика", "снова непонятно", "ничего не понимаю"} {
		w = request(t, api, "POST", "/api/dialogue/turn", map[string]string{"call_id": started.CallID, "text": phrase}, "")
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
		t.Fatal(err)
	}
	if event["reason_code"] != "R-05" || event["fail_count"] != float64(3) || event["state"] != dialogue.StateTransfer || event["previous_state"] != dialogue.StateMenu || event["call_id"] != started.CallID || event["text"] != "ничего не понимаю" {
		t.Fatalf("trace does not explain failed dialogue: %v", event)
	}
}

func TestTechnicalEventsExplainFailuresWithoutPatientTextOrSecrets(t *testing.T) {
	api, _ := testAPI(t, "")
	var output bytes.Buffer
	api.cfg.Events = slog.New(slog.NewJSONHandler(&output, nil))
	w := request(t, api, "POST", "/api/dialogue/start", map[string]string{"caller_number": "secret-number", "channel_id": "private-channel"}, "")
	var call Response
	if err := json.Unmarshal(w.Body.Bytes(), &call); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Запишите к терапевту", "Иванов Иван Иванович 12.03.1985", "неизвестная реплика", "снова неизвестно", "непонятный ответ"} {
		w = request(t, api, "POST", "/api/dialogue/turn", map[string]string{"call_id": call.CallID, "turn_id": "secret-" + text, "text": text}, "")
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	for _, secret := range []string{"Иванов", "12.03.1985", "непонятный ответ", "secret-number", "private-channel", "secret-"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("technical log exposed input: %s", secret)
		}
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
		t.Fatal(err)
	}
	if event["reason_code"] != "R-05" || event["fail_count"] != float64(3) || event["input_issue"] != "slot_choice_unrecognized" || event["previous_state"] != dialogue.StateChooseSlot {
		t.Fatalf("missing safe diagnosis: %v", event)
	}
}

const defaultTestToken = "test-token"

func testAPI(t *testing.T, token string) (*Server, *misclient.MockMISClient) {
	t.Helper()
	if token == "" {
		token = defaultTestToken
	} else if token == "EMPTY" {
		token = ""
	}
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	doc := models.DemoDoctors()[0]
	at := time.Now().Add(48 * time.Hour)
	slots := slotmgr.New(store, schedule.Snapshot{Doctors: []models.Doctor{doc}, Entries: []schedule.Entry{{Free: true, Slot: models.Slot{DoctorID: doc.ID, Doctor: doc.FullName, Specialty: doc.Specialty, StartTime: at, Duration: 20}}}})
	m := misclient.NewMockMISClient()
	f := dialogue.NewFSMController(slots, m)
	api := New(Config{FSM: f, MIS: m, Slots: slots, Store: store, Token: token, Mode: "mock"})
	t.Cleanup(func() { _ = api.WaitBackground(context.Background()) })
	return api, m
}

type blockedMIS struct {
	misclient.MISClient
	started chan struct{}
	release chan struct{}
}

func (m *blockedMIS) CreateVisit(ctx context.Context, req models.VisitCreate) (*models.Visit, error) {
	close(m.started)
	select {
	case <-m.release:
		return m.MISClient.CreateVisit(ctx, req)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type recordingRedirect struct{ target chan string }

func (r recordingRedirect) Redirect(ctx context.Context, channel, target string) error {
	r.target <- target
	return nil
}

func TestUrgentInterruptDoesNotWaitForMISMutation(t *testing.T) {
	api, mis := testAPI(t, "")
	blocked := &blockedMIS{MISClient: mis, started: make(chan struct{}), release: make(chan struct{})}
	api.cfg.FSM = dialogue.NewFSMController(api.cfg.Slots, blocked)
	api.cfg.Escalate = escalation.New(api.cfg.Store, mis).Enqueue
	redirected := make(chan string, 1)
	api.cfg.Redirector = recordingRedirect{target: redirected}
	w := request(t, api, "POST", "/api/dialogue/start", map[string]string{"channel_id": "PJSIP/200-test", "caller_number": "200"}, "")
	var started Response
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Запишите к терапевту", "Иванов Иван Иванович 12.03.1985", "первый"} {
		w = request(t, api, "POST", "/api/dialogue/turn", map[string]string{"call_id": started.CallID, "text": text}, "")
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(t, api, "POST", "/api/dialogue/turn", map[string]string{"call_id": started.CallID, "text": "да"}, "")
	}()
	<-blocked.started
	urgent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		urgent <- request(t, api, "POST", "/api/dialogue/"+started.CallID+"/interrupt", map[string]string{"text": "Не могу дышать"}, "")
	}()
	select {
	case w = <-urgent:
	case <-time.After(2 * time.Second):
		close(blocked.release)
		t.Fatal("urgent warning waited for MIS")
	}
	var warning Response
	if err := json.Unmarshal(w.Body.Bytes(), &warning); err != nil {
		t.Fatal(err)
	}
	if warning.TransferTarget != "104" {
		close(blocked.release)
		t.Fatalf("urgent result missing: %+v", warning)
	}
	if err := api.WaitBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	var clientID int
	if err := api.cfg.Store.DB.QueryRow("SELECT client_id FROM escalations").Scan(&clientID); err != nil || clientID != 1 {
		close(blocked.release)
		t.Fatalf("identified patient context was lost: %d %v", clientID, err)
	}
	w = request(t, api, "POST", "/api/dialogue/"+started.CallID+"/transfer", nil, "")
	if w.Code != 200 {
		close(blocked.release)
		t.Fatal(w.Body)
	}
	select {
	case target := <-redirected:
		if target != "104" {
			t.Fatal(target)
		}
	default:
		t.Fatal("redirect waited for MIS")
	}
	close(blocked.release)
	w = <-done
	var result Response
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.TransferTarget != "104" || result.NextState != dialogue.StateTransfer {
		t.Fatalf("late response overrode interruption: %+v", result)
	}
	visits, err := mis.ListVisits(context.Background(), 1, models.VisitPlanned)
	if err != nil || len(visits) != 0 {
		t.Fatalf("in-flight mutation was not aborted on interrupt: %+v %v", visits, err)
	}
}
func request(t *testing.T, api *Server, method, path string, payload any, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token == "NO_AUTH" {
		// explicitly no token
	} else if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	} else if api.cfg.Token != "" {
		r.Header.Set("Authorization", "Bearer "+api.cfg.Token)
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}
func TestHTTPBookingIdempotencyAndValidation(t *testing.T) {
	api, mis := testAPI(t, "")
	w := request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "")
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	var res Response
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"Запишите к терапевту", "Иванов Иван Иванович 12.03.1985", "первый", "да"} {
		payload := map[string]any{"call_id": res.CallID, "turn_id": string(rune('a' + i)), "text": text}
		first := request(t, api, "POST", "/api/dialogue/turn", payload, "")
		replay := request(t, api, "POST", "/api/dialogue/turn", payload, "")
		if first.Code != 200 || first.Body.String() != replay.Body.String() {
			t.Fatalf("turn not replayable: %s / %s", first.Body, replay.Body)
		}
	}
	visits, err := mis.ListVisits(context.Background(), 1, models.VisitPlanned)
	if err != nil || len(visits) != 1 {
		t.Fatalf("duplicate booking: %+v %v", visits, err)
	}
	if w = request(t, api, "POST", "/api/dialogue/turn", map[string]string{"call_id": res.CallID, "turn_id": "d", "text": "нет"}, ""); w.Code != 409 {
		t.Fatalf("reused turn id should conflict: %d", w.Code)
	}
	if w = request(t, api, "POST", "/api/dialogue/turn", map[string]string{"text": "да"}, ""); w.Code != 400 {
		t.Fatal("missing call_id accepted")
	}
	if w = request(t, api, "GET", "/api/dialogue/start", nil, ""); w.Code != 405 {
		t.Fatal("GET created a session")
	}
}
func TestAuthenticationAndSessionExpiry(t *testing.T) {
	api, _ := testAPI(t, "secret")
	if w := request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "NO_AUTH"); w.Code != 401 {
		t.Fatal("unauthenticated start accepted")
	}
	w := request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "secret")
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	var res Response
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	api.Expire(context.Background(), time.Now().Add(11*time.Minute))
	if w = request(t, api, "POST", "/api/dialogue/turn", map[string]string{"call_id": res.CallID, "text": "да"}, "secret"); w.Code != 404 {
		t.Fatal("expired session remained accessible")
	}
}
func TestConcurrentTurnsDoNotRace(t *testing.T) {
	api, _ := testAPI(t, "")
	w := request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "")
	var res Response
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]string{"call_id": res.CallID, "text": "режим работы"})
			r := httptest.NewRequest(http.MethodPost, "/api/dialogue/turn", bytes.NewReader(payload))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+api.cfg.Token)
			rr := httptest.NewRecorder()
			api.ServeHTTP(rr, r)
			if rr.Code != 200 && rr.Code != 409 {
				t.Errorf("unexpected concurrent status %d", rr.Code)
			}
		}()
	}
	wg.Wait()
}

func TestAuthenticationRequiredAndHealthEndpointsExempt(t *testing.T) {
	// Server with empty token must reject non-health endpoints with 401
	apiEmpty, _ := testAPI(t, "EMPTY")
	w := request(t, apiEmpty, "POST", "/api/dialogue/start", map[string]any{}, "some-token")
	if w.Code != 401 {
		t.Fatalf("empty server token must reject non-health endpoints: got %d", w.Code)
	}

	// Health endpoints must pass without token
	for _, healthPath := range []string{"/healthz", "/readyz", "/health"} {
		w = request(t, apiEmpty, "GET", healthPath, nil, "NO_AUTH")
		if w.Code != 200 {
			t.Fatalf("%s must be accessible without token: got %d", healthPath, w.Code)
		}
	}

	// Server with configured token:
	api, _ := testAPI(t, "valid-token")
	for _, healthPath := range []string{"/healthz", "/readyz"} {
		w = request(t, api, "GET", healthPath, nil, "NO_AUTH")
		if w.Code != 200 {
			t.Fatalf("%s must be accessible without token: got %d", healthPath, w.Code)
		}
	}

	// Non-health endpoint fails without token
	w = request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "NO_AUTH")
	if w.Code != 401 {
		t.Fatalf("non-health endpoint without token must return 401: got %d", w.Code)
	}

	// Non-health endpoint fails with invalid token
	w = request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "invalid-token")
	if w.Code != 401 {
		t.Fatalf("non-health endpoint with invalid token must return 401: got %d", w.Code)
	}

	// Non-health endpoint succeeds with valid token
	w = request(t, api, "POST", "/api/dialogue/start", map[string]any{}, "valid-token")
	if w.Code != 201 {
		t.Fatalf("non-health endpoint with valid token must succeed: got %d", w.Code)
	}
}

func TestClosedCallDoesNotRunFSMAndOldTurnsRemainIdempotent(t *testing.T) {
	api, mis := testAPI(t, "")
	w := request(t, api, "POST", "/api/dialogue/start", map[string]string{}, "")
	var call Response
	if err := json.Unmarshal(w.Body.Bytes(), &call); err != nil {
		t.Fatal(err)
	}
	payload := map[string]string{"call_id": call.CallID, "turn_id": "first", "text": "повторите"}
	first := request(t, api, "POST", "/api/dialogue/turn", payload, "")
	for i := 0; i < 20; i++ {
		next := map[string]string{"call_id": call.CallID, "turn_id": time.Now().Format(time.RFC3339Nano), "text": "повторите"}
		if w := request(t, api, "POST", "/api/dialogue/turn", next, ""); w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	if replay := request(t, api, "POST", "/api/dialogue/turn", payload, ""); replay.Body.String() != first.Body.String() {
		t.Fatalf("old turn replayed through FSM: %s", replay.Body)
	}
	if w := request(t, api, "POST", "/api/dialogue/"+call.CallID+"/end", nil, ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := request(t, api, "POST", "/api/dialogue/turn", map[string]string{"call_id": call.CallID, "text": "Запишите к врачу"}, ""); w.Code != 410 {
		t.Fatalf("closed call accepted turn: %d", w.Code)
	}
	visits, err := mis.ListVisits(context.Background(), 0, "")
	if err != nil || len(visits) != 0 {
		t.Fatalf("closed/replayed call changed MIS: %+v %v", visits, err)
	}
}

func TestReadinessChecksWriterWithoutBreakingLiveness(t *testing.T) {
	api, _ := testAPI(t, "")
	tx, err := api.cfg.Store.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if w := request(t, api, "GET", "/healthz", nil, ""); w.Code != 200 {
		t.Fatalf("liveness blocked by writer: %d", w.Code)
	}
	if w := request(t, api, "GET", "/readyz", nil, ""); w.Code != 503 {
		t.Fatalf("busy writer reported ready: %d", w.Code)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if w := request(t, api, "GET", "/readyz", nil, ""); w.Code != 200 {
		t.Fatalf("writer did not recover: %d", w.Code)
	}
}

func TestEmergencyResponseDoesNotWaitForLocalWriter(t *testing.T) {
	api, mis := testAPI(t, "")
	api.cfg.Escalate = escalation.New(api.cfg.Store, mis).Enqueue
	w := request(t, api, "POST", "/api/dialogue/start", map[string]string{}, "")
	var call Response
	if err := json.Unmarshal(w.Body.Bytes(), &call); err != nil {
		t.Fatal(err)
	}
	tx, err := api.cfg.Store.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(t, api, "POST", "/api/dialogue/"+call.CallID+"/interrupt", map[string]string{"text": "нет, не могу дышать"}, "")
	}()
	select {
	case w = <-done:
		var result Response
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.TransferTarget != "104" {
			t.Fatalf("emergency lost: %s %v", w.Body, err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("emergency response waited for SQLite")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := api.WaitBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := api.cfg.Store.ReadDB().QueryRow("SELECT COUNT(*) FROM escalations WHERE status='PENDING'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox lost: %d %v", count, err)
	}
}

func TestLateOperatorPersistenceCannotOverwriteEmergency(t *testing.T) {
	api, _ := testAPI(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	api.cfg.Escalate = func(ctx context.Context, id int, description string) error {
		if strings.HasPrefix(description, "R-02") {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	w := request(t, api, "POST", "/api/dialogue/start", map[string]string{}, "")
	var call Response
	if err := json.Unmarshal(w.Body.Bytes(), &call); err != nil {
		t.Fatal(err)
	}
	request(t, api, "POST", "/api/dialogue/"+call.CallID+"/interrupt", map[string]string{"text": "оператор"}, "")
	<-entered
	request(t, api, "POST", "/api/dialogue/"+call.CallID+"/interrupt", map[string]string{"text": "не могу дышать"}, "")
	close(release)
	if err := api.WaitBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := api.find(call.CallID).interrupted.Load(); got.result.TransferTarget != "104" || got.priority != 4 {
		t.Fatalf("late result downgraded emergency: %+v", got)
	}
}
