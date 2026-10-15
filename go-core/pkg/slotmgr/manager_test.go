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
			if _, err := sm.HoldSlot(slot.DoctorID, slot.StartTime, string(rune('a'+i)), time.Minute); err == nil {
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
	if _, err := sm.HoldSlot(slot.DoctorID, slot.StartTime, "caller", time.Minute); err != nil {
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
	if _, err = sm.HoldSlot(slot.DoctorID, slot.StartTime, "other", time.Minute); err == nil {
		t.Fatal("booking lost on restart")
	}
	if err = sm.Cancel(context.Background(), 5001); err != nil {
		t.Fatal(err)
	}
	if _, err = sm.HoldSlot(slot.DoctorID, slot.StartTime, "other", time.Minute); err != nil {
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
