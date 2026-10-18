package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/campaign"
	"github.com/himera/tochka-voice/go-core/pkg/config"
	"github.com/himera/tochka-voice/go-core/pkg/diagnostics"
	"github.com/himera/tochka-voice/go-core/pkg/dialogue"
	"github.com/himera/tochka-voice/go-core/pkg/escalation"
	"github.com/himera/tochka-voice/go-core/pkg/httpapi"
	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/schedule"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
	"github.com/himera/tochka-voice/go-core/pkg/telephony"
)

func run() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	eventsFile, err := diagnostics.Open(cfg.TechnicalLogDir, "core-events")
	if err != nil {
		return fmt.Errorf("open technical log: %w", err)
	}
	defer eventsFile.Close()
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, eventsFile), nil)))
	var trace *slog.Logger
	var encrypted *diagnostics.Encrypted
	if cfg.Transcripts {
		encrypted, err = diagnostics.OpenEncrypted(cfg.TranscriptDir, cfg.TranscriptKey, time.Duration(cfg.TranscriptHours)*time.Hour)
		if err != nil {
			return fmt.Errorf("open encrypted transcript: %w", err)
		}
		defer encrypted.Close()
		trace = slog.New(slog.NewJSONHandler(encrypted, nil))
		slog.Info("encrypted text transcripts enabled", "retention_hours", cfg.TranscriptHours)
	}
	if cfg.DebugDialogue {
		file, err := diagnostics.Open(cfg.DebugLogDir, "core")
		if err != nil {
			return fmt.Errorf("open dialogue trace: %w", err)
		}
		defer file.Close()
		trace = slog.New(slog.NewJSONHandler(file, nil))
		slog.Warn("local dialogue transcript debugging enabled", "directory", cfg.DebugLogDir)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if encrypted != nil {
		go encrypted.Run(ctx.Done())
	}
	store, err := storage.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	namespace := "mock"
	if cfg.MISToken != "" {
		namespace = fmt.Sprintf("stand:%x", sha256.Sum256([]byte(cfg.MISURL+":"+cfg.MISToken)))
	}
	if err = store.Bind(ctx, namespace); err != nil {
		return err
	}
	snapshot, err := schedule.LoadXLSX(cfg.Schedule)
	if err != nil {
		return fmt.Errorf("load schedule: %w", err)
	}
	var mis misclient.MISClient
	mode := "mock"
	if cfg.MISToken == "" {
		mis, err = misclient.NewPersistentMock(ctx, store, snapshot.Doctors)
		if err != nil {
			return err
		}
	} else {
		mode = "stand"
		mis = misclient.NewRealMISClient(cfg.MISURL, cfg.MISToken)
		startup, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		doctors, err := mis.ListDoctors(startup)
		if err != nil {
			return err
		}
		if err = snapshot.MatchDoctors(doctors); err != nil {
			return err
		}
	}
	slots := slotmgr.New(store, snapshot)
	startup, cancel := context.WithTimeout(ctx, 2*time.Minute)
	visits, err := mis.ListVisits(startup, 0, models.VisitPlanned)
	cancel()
	if err != nil {
		return err
	}
	if err = slots.SyncVisits(ctx, visits); err != nil {
		return err
	}
	outbox := escalation.New(store, mis)
	fsm := dialogue.NewFSMController(slots, mis)
	fsm.SetEscalator(outbox.Enqueue)
	fsm.SetContactSaver(func(ctx context.Context, id int, number string, consent bool) error {
		endpoint := "PJSIP/" + number
		if !telephony.ValidEndpoint(endpoint) {
			return errors.New("caller endpoint is invalid")
		}
		return store.SaveContact(ctx, storage.Contact{ClientID: id, Endpoint: endpoint, ReminderConsent: consent})
	})
	if cfg.DecisionModelEnabled {
		decisionClient := llmclient.NewDecisionClient(cfg.DecisionModelURL, cfg.AIAPIToken, cfg.DecisionModelEnabled)
		fsm.SetDecisionClient(decisionClient)
		slog.Info("decision model enabled", "url", cfg.DecisionModelURL)
	}
	if os.Getenv("LLM_PROVIDER") != "" && os.Getenv("LLM_PROVIDER") != "none" {
		llm := llmclient.NewUniversalLLMClient(llmclient.LoadConfigFromEnv())
		if err := llm.Validate(); err != nil {
			return fmt.Errorf("configure dialogue NLU: %w", err)
		}
		fsm.SetLLMClient(llm)
		slog.Info("dialogue NLU enabled", "provider", os.Getenv("LLM_PROVIDER"), "model", os.Getenv("LLM_MODEL"), "remote", !llm.IsLocal())
	}
	var dialer campaign.Dialer
	var redirector httpapi.Redirector
	if cfg.AMISecret != "" {
		dialer = telephony.AMI{Address: cfg.AMIAddress, Username: cfg.AMIUser, Secret: cfg.AMISecret}
		redirector = telephony.AMI{Address: cfg.AMIAddress, Username: cfg.AMIUser, Secret: cfg.AMISecret}
	}
	reminders := campaign.New(store, mis, dialer, snapshot.Holidays, cfg.DemoCalls)
	api := httpapi.New(httpapi.Config{FSM: fsm, MIS: mis, Slots: slots, Store: store, Campaign: reminders, Token: cfg.APIToken, Mode: mode, Redirector: redirector, Escalate: outbox.Enqueue, Gate: fsm.Gate(), Trace: trace, Events: slog.Default()})
	server := &http.Server{Addr: cfg.Address, Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	workers, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	done := make(chan struct{}, 3)
	go func() { defer func() { done <- struct{}{} }(); api.RunJanitor(workers) }()
	go func() { defer func() { done <- struct{}{} }(); outbox.Run(workers) }()
	go func() { defer func() { done <- struct{}{} }(); reminders.Run(workers) }()
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServe() }()
	slog.Info("core ready", "address", cfg.Address, "mode", mode, "doctors", len(snapshot.Doctors), "slots", len(snapshot.Entries), "outbound_enabled", dialer != nil)
	select {
	case err = <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	stopWorkers()
	shutdownErr := server.Shutdown(shutdown)
	shutdownErr = errors.Join(shutdownErr, api.WaitBackground(shutdown))
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-shutdown.Done():
			return errors.Join(err, shutdownErr, shutdown.Err())
		}
	}
	return errors.Join(err, shutdownErr)
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
