package campaign

import (
	"context"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

type fakeDialer struct{ calls int }

func (d *fakeDialer) Originate(context.Context, string, int) error { d.calls++; return nil }
func TestCallingWindowUsesClinicTimezone(t *testing.T) {
	for input, want := range map[string]bool{"2026-10-16T05:59:00Z": false, "2026-10-16T06:00:00Z": true, "2026-10-16T15:00:00Z": false, "2026-10-17T10:59:00Z": true, "2026-10-17T11:00:00Z": false, "2026-10-18T08:00:00Z": false} {
		at, err := time.Parse(time.RFC3339, input)
		if err != nil {
			t.Fatal(err)
		}
		if got := IsAllowedCallingHour(at); got != want {
			t.Errorf("%s allowed=%t, want %t", input, got, want)
		}
	}
}
func TestReminderSchedulingConsentAndDeduplication(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mis := misclient.NewMockMISClient()
	dialer := &fakeDialer{}
	runner := New(s, mis, dialer, map[string]bool{}, false)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, models.ClinicLocation)
	v, err := mis.CreateVisit(ctx, models.VisitCreate{ClientID: 1, DoctorID: 1, ScheduledAt: "19.10.2026 10:00"})
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Schedule(ctx, now); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM reminders").Scan(&n)
	if n != 0 {
		t.Fatal("scheduled without consent")
	}
	if err = s.SaveContact(ctx, storage.Contact{ClientID: 1, Endpoint: "PJSIP/200", ReminderConsent: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = runner.Schedule(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM reminders").Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate/missing reminder: %d", n)
	}
	for i := 0; i < 2; i++ {
		if err = runner.dispatch(ctx, 0, now); err != nil {
			t.Fatal(err)
		}
	}
	if dialer.calls != 1 {
		t.Fatal("duplicate originate")
	}
	if err = runner.Outcome(ctx, v.ID, "CONFIRMED"); err != nil {
		t.Fatal(err)
	}
	if err = runner.dispatch(ctx, 0, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if dialer.calls != 1 {
		t.Fatal("confirmed visit was called again")
	}
}

func TestDelayedReminderNeverCallsAfterAppointment(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mis := misclient.NewMockMISClient()
	dialer := &fakeDialer{}
	runner := New(store, mis, dialer, map[string]bool{}, false)
	ctx := context.Background()
	_, err = mis.CreateVisit(ctx, models.VisitCreate{ClientID: 1, DoctorID: 1, ScheduledAt: "19.10.2026 09:00"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveContact(ctx, storage.Contact{ClientID: 1, Endpoint: "PJSIP/200", ReminderConsent: true}); err != nil {
		t.Fatal(err)
	}
	if err = runner.Schedule(ctx, time.Date(2026, 10, 16, 11, 0, 0, 0, models.ClinicLocation)); err != nil {
		t.Fatal(err)
	}
	if err = runner.dispatch(ctx, 0, time.Date(2026, 10, 19, 10, 0, 0, 0, models.ClinicLocation)); err != nil {
		t.Fatal(err)
	}
	if dialer.calls != 0 {
		t.Fatal("late reminder dialed after the visit")
	}
	var status string
	if err = store.DB.QueryRow("SELECT status FROM reminders").Scan(&status); err != nil || status != "STALE" {
		t.Fatalf("status=%s error=%v", status, err)
	}
}

func TestExplicitDialDoesNotClaimQueuedForCompletedReminder(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mis := misclient.NewMockMISClient()
	dialer := &fakeDialer{}
	runner := New(s, mis, dialer, nil, true)
	ctx := context.Background()
	now := time.Now()
	v, err := mis.CreateVisit(ctx, models.VisitCreate{ClientID: 1, DoctorID: 1, ScheduledAt: now.Add(48 * time.Hour).In(models.ClinicLocation).Format(models.DateTimeLayout)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContact(ctx, storage.Contact{ClientID: 1, Endpoint: "PJSIP/200", ReminderConsent: true}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Dial(ctx, v.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := runner.Dial(ctx, v.ID, now); err == nil {
		t.Fatal("active reminder falsely reported queued")
	}
	if err := runner.Outcome(ctx, v.ID, "CONFIRMED"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Dial(ctx, v.ID, now.Add(3*time.Hour)); err == nil {
		t.Fatal("confirmed reminder falsely reported queued")
	}
	if dialer.calls != 1 {
		t.Fatalf("unexpected repeated dial: %d", dialer.calls)
	}
}
