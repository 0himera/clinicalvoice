package escalation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

type unknownWrite struct {
	misclient.MISClient
	calls int
}

func (m *unknownWrite) CreateEscalation(context.Context, models.EscalationCreate) (*models.Escalation, error) {
	m.calls++
	return nil, &misclient.APIError{StatusCode: 500}
}
func TestTechnicalPatientOutboxAndUnknownOutcome(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "sent", true: "uncertain"}[uncertain], func(t *testing.T) {
			s, err := storage.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			mock := misclient.NewMockMISClient()
			var mis misclient.MISClient = mock
			failed := &unknownWrite{MISClient: mock}
			if uncertain {
				mis = failed
			}
			service := New(s, mis)
			ctx := context.Background()
			if err = service.Enqueue(ctx, 0, "R-01: экстренное обращение"); err != nil {
				t.Fatal(err)
			}
			err = service.Drain(ctx)
			if !uncertain && err != nil {
				t.Fatal(err)
			}
			var status string
			var misID int
			if err = s.DB.QueryRow("SELECT status,mis_id FROM escalations").Scan(&status, &misID); err != nil {
				t.Fatal(err)
			}
			if uncertain {
				if status != "UNCERTAIN" {
					t.Fatal(status)
				}
				_ = service.Drain(ctx)
				if failed.calls != 1 {
					t.Fatal("ambiguous POST retried")
				}
			} else if status != "SENT" || misID <= 0 {
				t.Fatalf("not delivered: %s %d", status, misID)
			}
		})
	}
}

func TestEscalationSanitizationExcludesANIAndTrigger(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mock := misclient.NewMockMISClient()
	service := New(s, mock)
	ctx := context.Background()

	inputDesc := "R-01; звонок: 12345; ANI: +79991234567; триггер: не могу дышать"
	if err = service.Enqueue(ctx, 1, inputDesc); err != nil {
		t.Fatal(err)
	}
	if err = service.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	var storedDesc string
	if err = s.DB.QueryRow("SELECT description FROM escalations").Scan(&storedDesc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedDesc, "ANI") || strings.Contains(storedDesc, "+79991234567") {
		t.Fatalf("ANI phone was not stripped: %s", storedDesc)
	}
	if strings.Contains(storedDesc, "триггер") || strings.Contains(storedDesc, "не могу дышать") {
		t.Fatalf("trigger quote was not stripped: %s", storedDesc)
	}
	if !strings.Contains(storedDesc, "R-01") || !strings.Contains(storedDesc, "звонок: 12345") {
		t.Fatalf("action code or call id was lost: %s", storedDesc)
	}
}

func TestExpiredSendingIsVisibleAndNeverReposted(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mis := &unknownWrite{MISClient: misclient.NewMockMISClient()}
	service := New(store, mis)
	ctx := context.Background()
	if err := service.Enqueue(ctx, 1, "R-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("UPDATE escalations SET status='SENDING',lease_until=0"); err != nil {
		t.Fatal(err)
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.DB.QueryRow("SELECT status FROM escalations").Scan(&status); err != nil || status != "UNCERTAIN" {
		t.Fatalf("%s %v", status, err)
	}
	if mis.calls != 0 {
		t.Fatal("expired SENDING was reposted")
	}
}

func TestFailedFinalUpdateDoesNotRetrySuccessfulPost(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, misclient.NewMockMISClient())
	ctx := context.Background()
	if err := service.Enqueue(ctx, 1, "R-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`CREATE TRIGGER fail_sent BEFORE UPDATE OF status ON escalations WHEN NEW.status='SENT' BEGIN SELECT RAISE(ABORT,'injected final update failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := service.Drain(ctx); err == nil {
		t.Fatal("expected injected local failure")
	}
	if _, err := store.DB.Exec("UPDATE escalations SET lease_until=0"); err != nil {
		t.Fatal(err)
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.DB.QueryRow("SELECT status FROM escalations").Scan(&status); err != nil || status != "UNCERTAIN" {
		t.Fatalf("successful POST was queued again: %s %v", status, err)
	}
}

type delayedWrite struct{ misclient.MISClient }

func (m delayedWrite) CreateEscalation(ctx context.Context, req models.EscalationCreate) (*models.Escalation, error) {
	time.Sleep(3100 * time.Millisecond)
	return m.MISClient.CreateEscalation(ctx, req)
}
func TestSettlementDeadlineStartsAfterMISResponse(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, delayedWrite{misclient.NewMockMISClient()})
	ctx := context.Background()
	if err := service.Enqueue(ctx, 1, "R-01"); err != nil {
		t.Fatal(err)
	}
	if err := service.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.DB.QueryRow("SELECT status FROM escalations").Scan(&status); err != nil || status != "SENT" {
		t.Fatalf("settlement timed out before response: %s %v", status, err)
	}
}
