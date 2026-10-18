package slotmgr

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/schedule"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

func fixture(t *testing.T, path string) (*SlotManager, *storage.Store, models.Slot) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := models.DemoDoctors()[0]
	slot := models.Slot{DoctorID: d.ID, Doctor: d.FullName, Specialty: d.Specialty, StartTime: time.Now().Add(48 * time.Hour).Truncate(time.Minute), Duration: 20}
	return New(db, schedule.Snapshot{Doctors: []models.Doctor{d}, Entries: []schedule.Entry{{Free: true, Slot: slot}}}), db, slot
}
func TestConcurrentHoldHasExactlyOneWinner(t *testing.T) {
	sm, db, slot := fixture(t, ":memory:")
	defer db.Close()
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := sm.HoldSlot(context.Background(), slot.DoctorID, slot.StartTime, string(rune('a'+i)), time.Minute); err == nil {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d callers acquired one slot", won.Load())
	}
}
func TestClaimsSurviveRestartAndExpiredHoldsCannotCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slots.db")
	sm, db, slot := fixture(t, path)
	if _, err := sm.HoldSlot(context.Background(), slot.DoctorID, slot.StartTime, "caller", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := sm.MarkPending(context.Background(), slot, "caller"); err != nil {
		t.Fatal(err)
	}
	if err := sm.Resolve(context.Background(), slot, "caller", 5001, false); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sm = New(db, schedule.Snapshot{Doctors: models.DemoDoctors(), Entries: []schedule.Entry{{Free: true, Slot: slot}}})
	if _, err = sm.HoldSlot(context.Background(), slot.DoctorID, slot.StartTime, "other", time.Minute); err == nil {
		t.Fatal("booking lost on restart")
	}
	if err = sm.Cancel(context.Background(), 5001); err != nil {
		t.Fatal(err)
	}
	if _, err = sm.HoldSlot(context.Background(), slot.DoctorID, slot.StartTime, "other", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec("UPDATE slot_claims SET expires_at=0")
	if err != nil {
		t.Fatal(err)
	}
	if err = sm.MarkPending(context.Background(), slot, "other"); err != ErrUnavailable {
		t.Fatalf("expired lease committed: %v", err)
	}
}
func TestDirectBookingRules(t *testing.T) {
	sm, db, _ := fixture(t, ":memory:")
	defer db.Close()
	for _, s := range []string{"терапевт", "педиатр", "хирург", "офтальмолог", "оториноларинголог", "акушер-гинеколог", "дерматовенеролог"} {
		if !sm.IsDirectBookingAllowed(s) {
			t.Errorf("direct booking rejected for %s", s)
		}
	}
	for _, s := range []string{"кардиолог", "невролог", "эндокринолог", "гастроэнтеролог", "уролог"} {
		if sm.IsDirectBookingAllowed(s) {
			t.Errorf("referral bypass for %s", s)
		}
	}
}

func TestPurgeExpired(t *testing.T) {
	sm, db, slot := fixture(t, ":memory:")
	defer db.Close()
	if _, err := sm.HoldSlot(context.Background(), slot.DoctorID, slot.StartTime, "caller1", time.Minute); err != nil {
		t.Fatal(err)
	}
	// Expire the hold
	if _, err := db.WriteDB().Exec("UPDATE slot_claims SET expires_at=?", time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := sm.PurgeExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM slot_claims WHERE status='held'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expected 0 held slots after purge, got %d, err: %v", count, err)
	}
}

func TestExpiredPendingRemainsBlockedAndReconcilesAgainstMIS(t *testing.T) {
	sm, db, slot := fixture(t, ":memory:")
	defer db.Close()
	ctx := context.Background()
	if _, err := sm.HoldSlot(ctx, slot.DoctorID, slot.StartTime, "call", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := sm.MarkPendingVisit(ctx, slot, "call", 42); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec("UPDATE slot_claims SET expires_at=0"); err != nil {
		t.Fatal(err)
	}
	if err := sm.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	claims, err := sm.ReviewClaims(ctx)
	if err != nil || len(claims) != 1 || claims[0].Status != "uncertain" || claims[0].VisitID != 42 {
		t.Fatalf("lost unknown outcome: %+v %v", claims, err)
	}
	if _, err := sm.HoldSlot(ctx, slot.DoctorID, slot.StartTime, "other", time.Minute); err == nil {
		t.Fatal("unknown outcome freed by timer")
	}
	if err := sm.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if claims, _ = sm.ReviewClaims(ctx); len(claims) != 1 {
		t.Fatal("absence in snapshot freed an uncertain claim")
	}
	visit := models.Visit{ID: 42, Doctor: models.Doctor{ID: slot.DoctorID}, ScheduledAt: slot.StartTime.In(models.ClinicLocation).Format(models.DateTimeLayout), Status: models.VisitPlanned}
	if err := sm.Reconcile(ctx, []models.Visit{visit}); err != nil {
		t.Fatal(err)
	}
	if claims, _ = sm.ReviewClaims(ctx); len(claims) != 0 {
		t.Fatal("confirmed visit failed reconciliation")
	}
	var status string
	if err := db.DB.QueryRow("SELECT status FROM slot_claims WHERE visit_id=42").Scan(&status); err != nil || status != "booked" {
		t.Fatalf("%s %v", status, err)
	}
}

func TestRestartReleasesUncommittedHoldsButPreservesUnknownWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	sm, db, slot := fixture(t, path)
	ctx := context.Background()
	if _, err := sm.HoldSlot(ctx, slot.DoctorID, slot.StartTime, "dead-call", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM slot_claims").Scan(&n); err != nil || n != 0 {
		t.Fatalf("dead owner hold survived restart: %d %v", n, err)
	}
}

func TestReconciliationDoesNotEraseConcurrentBookings(t *testing.T) {
	sm, db, slot := fixture(t, ":memory:")
	defer db.Close()
	ctx := context.Background()
	if _, err := sm.HoldSlot(ctx, slot.DoctorID, slot.StartTime, "call", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := sm.MarkPending(ctx, slot, "call"); err != nil {
		t.Fatal(err)
	}
	if err := sm.Resolve(ctx, slot, "call", 99, false); err != nil {
		t.Fatal(err)
	}
	if err := sm.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.HoldSlot(ctx, slot.DoctorID, slot.StartTime, "other", time.Minute); err == nil {
		t.Fatal("stale reconciliation snapshot freed a newly committed booking")
	}
}
