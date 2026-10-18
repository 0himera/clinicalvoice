// Package escalation provides a durable outbox so urgent speech does not wait
// behind the shared MIS quota. Ambiguous POST outcomes require reconciliation.
package escalation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

var (
	reANI     = regexp.MustCompile(`(?i)(?:;\s*)?ANI:\s*[^;\n]+`)
	reTrigger = regexp.MustCompile(`(?i)(?:;\s*)?триггер:\s*[^;\n]+`)
	rePhone   = regexp.MustCompile(`(?:\+7|8|7)[\s\-\(]*[0-9]{3}[\s\-\)]*[0-9]{3}[\s\-]*[0-9]{2}[\s\-]*[0-9]{2}`)
)

// SanitizeDescription removes ANI, raw triggers, and phone numbers before storing or sending to external MIS.
func SanitizeDescription(desc string) string {
	d := reANI.ReplaceAllString(desc, "")
	d = reTrigger.ReplaceAllString(d, "")
	d = rePhone.ReplaceAllString(d, "")
	return strings.TrimSpace(d)
}

type Service struct {
	store *storage.Store
	mis   misclient.MISClient
}

func New(s *storage.Store, m misclient.MISClient) *Service { return &Service{store: s, mis: m} }

func (s *Service) Enqueue(ctx context.Context, clientID int, description string) error {
	description = SanitizeDescription(description)
	if len([]rune(description)) > 900 {
		return errors.New("escalation description exceeds 900 characters")
	}
	id := uuid.NewString()
	description += "\nОбращение " + id
	_, err := s.store.DB.ExecContext(ctx, "INSERT INTO escalations(id,client_id,description,created_at) VALUES(?,?,?,?)", id, clientID, description, time.Now().Unix())
	return err
}

func (s *Service) technicalClient(ctx context.Context) (int, error) {
	var id int
	if err := s.store.Get(ctx, "technical_client_id", &id); err == nil && id > 0 {
		return id, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	middle := "Технический"
	c, err := s.mis.CreateClient(ctx, models.ClientCreate{LastName: "Вызов", FirstName: "Срочный", MiddleName: &middle, BirthDate: "01.01.2000"})
	if err != nil {
		return 0, err
	}
	return c.ID, s.store.Put(ctx, "technical_client_id", c.ID)
}

// Drain sends one job. A single worker owns this outbox; an interrupted SENDING
// job is marked UNCERTAIN on restart rather than causing a duplicate POST.
func (s *Service) Drain(ctx context.Context) error {
	now := time.Now().Unix()
	if _, err := s.store.DB.ExecContext(ctx, "UPDATE escalations SET status='PENDING', lease_until=0 WHERE status='SENDING' AND lease_until<=?", now); err != nil {
		return err
	}

	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id, description string
	var clientID int
	err = tx.QueryRowContext(ctx, "SELECT id,client_id,description FROM escalations WHERE status='PENDING' ORDER BY created_at,id LIMIT 1").Scan(&id, &clientID, &description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	leaseUntil := time.Now().Add(30 * time.Second).Unix()
	res, err := tx.ExecContext(ctx, "UPDATE escalations SET status='SENDING',attempts=attempts+1,lease_until=? WHERE id=? AND status='PENDING'", leaseUntil, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	if clientID == 0 {
		clientID, err = s.technicalClient(ctx)
		if err != nil {
			_, _ = s.store.DB.ExecContext(context.WithoutCancel(ctx), "UPDATE escalations SET status='PENDING',lease_until=0 WHERE id=? AND status='SENDING'", id)
			return err
		}
	}
	cleanDesc := SanitizeDescription(description)
	e, sendErr := s.mis.CreateEscalation(ctx, models.EscalationCreate{ClientID: clientID, Description: cleanDesc})
	status := "SENT"
	misID := 0
	if sendErr != nil {
		status = "FAILED"
		var api *misclient.APIError
		if errors.As(sendErr, &api) && api.StatusCode == 429 {
			status = "PENDING"
		} else if misclient.Ambiguous(sendErr) {
			status = "UNCERTAIN"
		}
	} else {
		misID = e.ID
	}
	_, err = s.store.DB.ExecContext(context.WithoutCancel(ctx), "UPDATE escalations SET status=?,mis_id=?,lease_until=0 WHERE id=? AND status='SENDING'", status, misID, id)
	return errors.Join(sendErr, err)
}

func (s *Service) Run(ctx context.Context) {
	if _, err := s.store.DB.ExecContext(ctx, "UPDATE escalations SET status='UNCERTAIN' WHERE status='SENDING'"); err != nil {
		slog.Error("recover escalation outbox", "error", err)
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			err := s.Drain(jobCtx)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("escalation delivery pending review", "error", fmt.Sprint(err))
			}
		}
	}
}
