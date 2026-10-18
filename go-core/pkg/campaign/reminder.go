package campaign

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
	"github.com/himera/tochka-voice/go-core/pkg/telephony"
)

type Dialer interface {
	Originate(context.Context, string, int) error
}
type Runner struct {
	store         *storage.Store
	mis           misclient.MISClient
	dialer        Dialer
	holidays      map[string]bool
	allowDemo     bool
	maxConcurrent int
}

func New(s *storage.Store, m misclient.MISClient, d Dialer, holidays map[string]bool, allowDemo bool) *Runner {
	return &Runner{store: s, mis: m, dialer: d, holidays: holidays, allowDemo: allowDemo, maxConcurrent: 2}
}

func IsAllowedCallingHour(t time.Time) bool {
	t = t.In(models.ClinicLocation)
	if t.Weekday() == time.Sunday {
		return false
	}
	end := 19
	if t.Weekday() == time.Saturday {
		end = 15
	}
	return t.Hour() >= 10 && t.Hour() < end
}
func (r *Runner) allowed(t time.Time) bool {
	return IsAllowedCallingHour(t) && !r.holidays[t.In(models.ClinicLocation).Format("2006-01-02")]
}
func (r *Runner) previousBusinessDay(t time.Time) time.Time {
	for i := 0; i < 370; i++ {
		t = t.AddDate(0, 0, -1)
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday && !r.holidays[t.Format("2006-01-02")] {
			return t
		}
	}
	return t
}

func (r *Runner) Schedule(ctx context.Context, now time.Time) error {
	if !r.allowed(now) {
		return nil
	}
	visits, err := r.mis.ListVisits(ctx, 0, models.VisitPlanned)
	if err != nil {
		return err
	}
	today := now.In(models.ClinicLocation).Format("02.01.2006")
	for _, v := range visits {
		at, err := models.ParseDateTime(v.ScheduledAt)
		if err != nil {
			return err
		}
		if !at.After(now) || r.previousBusinessDay(at).Format("02.01.2006") != today {
			continue
		}
		contact, err := r.store.Contact(ctx, v.ClientID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if !contact.ReminderConsent {
			continue
		}
		if _, err = r.store.DB.ExecContext(ctx, "INSERT OR IGNORE INTO reminders(visit_id,scheduled_at) VALUES(?,?)", v.ID, v.ScheduledAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) ExtendLease(ctx context.Context, visitID int, duration time.Duration) error {
	if r == nil || r.store == nil || visitID <= 0 {
		return nil
	}
	until := time.Now().Add(duration).Unix()
	_, err := r.store.DB.ExecContext(ctx, "UPDATE reminders SET lease_until=? WHERE visit_id=? AND status='DIALING'", until, visitID)
	return err
}

func (r *Runner) Dial(ctx context.Context, id int, now time.Time) error {
	if !r.allowed(now) && !r.allowDemo {
		return errors.New("calling window is closed")
	}
	if r.dialer == nil {
		return errors.New("outbound telephony is not configured")
	}
	v, err := r.mis.GetVisit(ctx, id)
	if err != nil {
		return err
	}
	if v.Status != models.VisitPlanned {
		return errors.New("visit is not planned")
	}
	contact, err := r.store.Contact(ctx, v.ClientID)
	if err != nil {
		return err
	}
	if !contact.ReminderConsent || !telephony.ValidEndpoint(contact.Endpoint) {
		return errors.New("reminder consent and a valid endpoint are required")
	}
	at, err := models.ParseDateTime(v.ScheduledAt)
	if err != nil || !at.After(now) {
		return errors.New("visit must be in the future")
	}
	var existingAttempts int
	var existingStatus string
	if err = r.store.ReadDB().QueryRowContext(ctx, "SELECT attempts, status FROM reminders WHERE visit_id=?", id).Scan(&existingAttempts, &existingStatus); err == nil {
		if existingAttempts >= 2 || existingStatus == "NO_ANSWER" {
			return errors.New("reminder attempts exhausted")
		}
	}
	if _, err = r.store.DB.ExecContext(ctx, "INSERT OR IGNORE INTO reminders(visit_id,scheduled_at) VALUES(?,?)", v.ID, v.ScheduledAt); err != nil {
		return err
	}
	return r.dispatch(ctx, id, now)
}

func (r *Runner) dispatch(ctx context.Context, id int, now time.Time) error {
	if r.dialer == nil {
		return nil
	}
	if _, err := r.store.DB.ExecContext(ctx, "UPDATE reminders SET status=CASE WHEN attempts>=2 THEN 'NO_ANSWER' ELSE 'PENDING' END WHERE status='DIALING' AND lease_until<=?", now.Unix()); err != nil {
		return err
	}
	tx, err := r.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM reminders WHERE status='DIALING'").Scan(&active); err != nil {
		return err
	}
	if active >= r.maxConcurrent {
		return nil
	}
	var visitID int
	var at string
	query := "SELECT visit_id,scheduled_at FROM reminders WHERE status='PENDING' AND attempts<2 AND next_attempt<=?"
	args := []any{now.Unix()}
	if id > 0 {
		query += " AND visit_id=?"
		args = append(args, id)
	}
	query += " ORDER BY scheduled_at,visit_id LIMIT 1"
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&visitID, &at); errors.Is(err, sql.ErrNoRows) {
		if id > 0 {
			var att int
			if errCheck := tx.QueryRowContext(ctx, "SELECT attempts FROM reminders WHERE visit_id=?", id).Scan(&att); errCheck == nil && att >= 2 {
				return errors.New("reminder attempts exhausted")
			}
		}
		return nil
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE reminders SET status='DIALING',attempts=attempts+1,next_attempt=?,lease_until=? WHERE visit_id=? AND scheduled_at=?", now.Add(2*time.Hour).Unix(), now.Add(4*time.Minute).Unix(), visitID, at); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	v, err := r.mis.GetVisit(ctx, visitID)
	if err != nil {
		_, _ = r.store.DB.ExecContext(context.WithoutCancel(ctx), "UPDATE reminders SET status='PENDING', lease_until=0 WHERE visit_id=? AND scheduled_at=? AND status='DIALING'", visitID, at)
		return err
	}
	if v.Status != models.VisitPlanned || v.ScheduledAt != at {
		return r.Outcome(ctx, visitID, "STALE")
	}
	visitTime, err := models.ParseDateTime(v.ScheduledAt)
	if err != nil {
		_, _ = r.store.DB.ExecContext(context.WithoutCancel(ctx), "UPDATE reminders SET status='PENDING', lease_until=0 WHERE visit_id=? AND scheduled_at=? AND status='DIALING'", visitID, at)
		return err
	}
	if !visitTime.After(now) {
		return r.Outcome(ctx, visitID, "STALE")
	}
	contact, err := r.store.Contact(ctx, v.ClientID)
	if err != nil {
		_, _ = r.store.DB.ExecContext(context.WithoutCancel(ctx), "UPDATE reminders SET status='PENDING', lease_until=0 WHERE visit_id=? AND scheduled_at=? AND status='DIALING'", visitID, at)
		return err
	}
	if !contact.ReminderConsent {
		return r.Outcome(ctx, visitID, "OPTED_OUT")
	}
	if err = r.dialer.Originate(ctx, contact.Endpoint, visitID); err != nil {
		_, _ = r.store.DB.ExecContext(context.WithoutCancel(ctx), "UPDATE reminders SET status='PENDING', lease_until=0 WHERE visit_id=? AND scheduled_at=? AND status='DIALING'", visitID, at)
		return fmt.Errorf("originate reminder: %w", err)
	}
	return nil
}

func (r *Runner) Outcome(ctx context.Context, id int, status string) error {
	switch status {
	case "CONFIRMED", "RESCHEDULED", "CANCELLED", "WRONG_PERSON", "STALE", "OPTED_OUT", "TRANSFERRED":
	default:
		return nil
	}
	_, err := r.store.DB.ExecContext(ctx, "UPDATE reminders SET status=?,lease_until=0 WHERE visit_id=? AND status IN ('DIALING', 'PENDING', 'NO_ANSWER')", status, id)
	return err
}
func (r *Runner) Run(ctx context.Context) {
	if r.dialer == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastSync := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !r.allowed(now) {
				continue
			}
			if now.Sub(lastSync) >= 30*time.Minute {
				if err := r.Schedule(ctx, now); err != nil {
					slog.Warn("schedule reminders", "error", err)
				} else {
					lastSync = now
				}
			}
			for i := 0; i < r.maxConcurrent; i++ {
				dispCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err := r.dispatch(dispCtx, 0, now)
				cancel()
				if err != nil {
					slog.Warn("dispatch reminder", "error", err)
					break
				}
			}
		}
	}
}
