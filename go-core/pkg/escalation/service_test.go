package escalation

import (
	"context"
	"testing"

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
