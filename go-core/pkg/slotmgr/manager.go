package slotmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/schedule"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

var ErrUnavailable = errors.New("slot is unavailable or lease expired")

type SlotManager struct {
	store    *storage.Store
	doctors  []models.Doctor
	entries  []schedule.Entry
	bookable map[string]bool
}

func New(store *storage.Store, snapshot schedule.Snapshot) *SlotManager {
	bookable := map[string]bool{}
	active := map[int]bool{}
	for _, d := range snapshot.Doctors {
		active[d.ID] = d.IsActive
	}
	for _, e := range snapshot.Entries {
		if e.Free && active[e.Slot.DoctorID] {
			bookable[slotKey(e.Slot.DoctorID, e.Slot.StartTime)] = true
		}
	}
	return &SlotManager{store: store, doctors: snapshot.Doctors, entries: snapshot.Entries, bookable: bookable}
}

type Query struct {
	BirthDate       string
	Specialty       string
	DoctorID        int
	After           time.Time
	Before          time.Time
	MinHour         int
	MaxHour         int
	Weekday         *time.Weekday
	ExcludedWeekday *time.Weekday
	Limit           int
}

func (sm *SlotManager) Available(ctx context.Context, q Query) ([]models.Slot, error) {
	now := time.Now()
	if q.After.Before(now.Add(30 * time.Minute)) {
		q.After = now.Add(30 * time.Minute)
	}
	horizon := now.In(models.ClinicLocation).AddDate(0, 0, 14)
	horizon = time.Date(horizon.Year(), horizon.Month(), horizon.Day(), 0, 0, 0, 0, models.ClinicLocation)
	if q.Before.IsZero() || q.Before.After(horizon) {
		q.Before = horizon
	}
	if q.Limit <= 0 || q.Limit > 3 {
		q.Limit = 2
	}
	rows, err := sm.store.ReadDB().QueryContext(ctx, "SELECT doctor_id,starts_at FROM slot_claims WHERE status!='held' OR expires_at>?", now.Unix())
	if err != nil {
		return nil, err
	}
	claims := map[string]bool{}
	for rows.Next() {
		var id int
		var at string
		if err = rows.Scan(&id, &at); err != nil {
			_ = rows.Close()
			return nil, err
		}
		claims[fmt.Sprintf("%d:%s", id, at)] = true
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	active := map[int]bool{}
	for _, d := range sm.doctors {
		active[d.ID] = d.IsActive
		if q.BirthDate != "" {
			age, err := models.AgeOn(q.BirthDate, now)
			if err != nil {
				return nil, err
			}
			active[d.ID] = d.IsActive && d.AcceptsAge(age)
		}
	}
	result := []models.Slot{}
	for _, e := range sm.entries {
		s := e.Slot
		if !e.Free || !active[s.DoctorID] || s.StartTime.Before(q.After) || !s.StartTime.Before(q.Before) {
			continue
		}
		if q.DoctorID > 0 && s.DoctorID != q.DoctorID {
			continue
		}
		if q.Specialty != "" && models.SpecialtyKey(s.Specialty) != models.SpecialtyKey(q.Specialty) {
			continue
		}
		if s.StartTime.Hour() < q.MinHour || (q.MaxHour > 0 && s.StartTime.Hour() >= q.MaxHour) {
			continue
		}
		if q.Weekday != nil && s.StartTime.Weekday() != *q.Weekday {
			continue
		}
		if q.ExcludedWeekday != nil && s.StartTime.Weekday() == *q.ExcludedWeekday {
			continue
		}
		if claims[slotKey(s.DoctorID, s.StartTime)] {
			continue
		}
		result = append(result, s)
		if len(result) >= q.Limit {
			break
		}
	}
	return result, nil
}

func slotKey(id int, t time.Time) string {
	return fmt.Sprintf("%d:%s", id, t.UTC().Format(time.RFC3339))
}

func (sm *SlotManager) HoldSlot(ctx context.Context, id int, at time.Time, owner string, ttl time.Duration) (string, error) {
	if owner == "" || ttl <= 0 {
		return "", ErrUnavailable
	}
	if !sm.bookable[slotKey(id, at)] || at.Before(time.Now().Add(30*time.Minute)) {
		return "", ErrUnavailable
	}
	res, err := sm.store.DB.ExecContext(ctx, `INSERT INTO slot_claims(doctor_id,starts_at,owner,status,expires_at) VALUES(?,?,?,'held',?)
 ON CONFLICT(doctor_id,starts_at) DO UPDATE SET owner=excluded.owner,status='held',expires_at=excluded.expires_at
 WHERE slot_claims.status='held' AND (slot_claims.expires_at<=? OR slot_claims.owner=excluded.owner)`, id, at.UTC().Format(time.RFC3339), owner, time.Now().Add(ttl).Unix(), time.Now().Unix())
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrUnavailable
	}
	return slotKey(id, at), nil
}

func (sm *SlotManager) PurgeExpired(ctx context.Context) error {
	_, err := sm.store.DB.ExecContext(ctx, "DELETE FROM slot_claims WHERE status='held' AND expires_at<=?", time.Now().Unix())
	return err
}

func (sm *SlotManager) Release(ctx context.Context, owner string) error {
	_, err := sm.store.DB.ExecContext(ctx, "DELETE FROM slot_claims WHERE owner=? AND status='held'", owner)
	return err
}
func (sm *SlotManager) MarkPending(ctx context.Context, s models.Slot, owner string) error {
	if !sm.bookable[slotKey(s.DoctorID, s.StartTime)] || s.StartTime.Before(time.Now().Add(30*time.Minute)) {
		return ErrUnavailable
	}
	r, err := sm.store.DB.ExecContext(ctx, "UPDATE slot_claims SET status='pending' WHERE doctor_id=? AND starts_at=? AND owner=? AND status='held' AND expires_at>?", s.DoctorID, s.StartTime.UTC().Format(time.RFC3339), owner, time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrUnavailable
	}
	return nil
}
func (sm *SlotManager) Resolve(ctx context.Context, s models.Slot, owner string, visitID int, uncertain bool) error {
	status := "booked"
	if uncertain {
		status = "uncertain"
	}
	if visitID == 0 && !uncertain {
		_, err := sm.store.DB.ExecContext(ctx, "DELETE FROM slot_claims WHERE doctor_id=? AND starts_at=? AND owner=? AND status='pending'", s.DoctorID, s.StartTime.UTC().Format(time.RFC3339), owner)
		return err
	}
	tx, err := sm.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if visitID > 0 {
		if _, err = tx.ExecContext(ctx, "DELETE FROM slot_claims WHERE visit_id=? AND NOT(doctor_id=? AND starts_at=?)", visitID, s.DoctorID, s.StartTime.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	r, err := tx.ExecContext(ctx, "UPDATE slot_claims SET status=?,visit_id=?,expires_at=0 WHERE doctor_id=? AND starts_at=? AND owner=? AND status='pending'", status, visitID, s.DoctorID, s.StartTime.UTC().Format(time.RFC3339), owner)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrUnavailable
	}
	return tx.Commit()
}
func (sm *SlotManager) Cancel(ctx context.Context, visitID int) error {
	_, err := sm.store.DB.ExecContext(ctx, "DELETE FROM slot_claims WHERE visit_id=? AND status='booked'", visitID)
	return err
}
func (sm *SlotManager) SyncVisits(ctx context.Context, visits []models.Visit) error {
	tx, err := sm.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM slot_claims WHERE status='booked'"); err != nil {
		return err
	}
	for _, v := range visits {
		if v.Status != models.VisitPlanned {
			continue
		}
		at, err := models.ParseDateTime(v.ScheduledAt)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO slot_claims(doctor_id,starts_at,owner,status,visit_id) VALUES(?,?,'','booked',?)
 ON CONFLICT(doctor_id,starts_at) DO UPDATE SET status='booked',visit_id=excluded.visit_id,expires_at=0`, v.Doctor.ID, at.UTC().Format(time.RFC3339), v.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (sm *SlotManager) IsDirectBookingAllowed(s string) bool {
	s = strings.TrimPrefix(models.SpecialtyKey(s), "детский ")
	for _, spec := range []string{"терапевт", "педиатр", "хирург", "офтальмолог", "оториноларинголог", "акушер-гинеколог", "стоматолог", "стоматолог-терапевт", "дерматовенеролог", "лор"} {
		if s == spec {
			return true
		}
	}
	return false
}
func (sm *SlotManager) GetDoctor(id int) (models.Doctor, bool) {
	for _, d := range sm.doctors {
		if d.ID == id {
			return d, true
		}
	}
	return models.Doctor{}, false
}
func (sm *SlotManager) ListDoctors() []models.Doctor {
	return append([]models.Doctor(nil), sm.doctors...)
}
