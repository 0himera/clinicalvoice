package slotmgr

import (
	"testing"
	"time"
)

func TestDirectBookingRules(t *testing.T) {
	sm := NewSlotManager()

	// Прямая запись разрешена
	directSpecs := []string{"терапевт", "педиатр", "хирург", "офтальмолог", "лор", "акушер-гинеколог", "дерматовенеролог"}
	for _, spec := range directSpecs {
		if !sm.IsDirectBookingAllowed(spec) {
			t.Errorf("Expected direct booking allowed for %s", spec)
		}
	}

	// Только по направлению
	referralSpecs := []string{"кардиолог", "невролог", "эндокринолог", "гастроэнтеролог", "уролог"}
	for _, spec := range referralSpecs {
		if sm.IsDirectBookingAllowed(spec) {
			t.Errorf("Expected direct booking disallowed for %s", spec)
		}
	}
}

func TestSlotHoldAndRaceCondition(t *testing.T) {
	sm := NewSlotManager()
	now := time.Now().Add(2 * time.Hour)

	// Сессия A успешно блокирует слот
	key, err := sm.HoldSlot(1, now, "session_A", 1*time.Minute)
	if err != nil {
		t.Fatalf("Session A failed to hold slot: %v", err)
	}

	// Сессия B пытается заблокировать тот же слот -> Ошибка!
	_, errB := sm.HoldSlot(1, now, "session_B", 1*time.Minute)
	if errB == nil {
		t.Fatalf("Session B should NOT be able to hold the same slot!")
	}

	// Сессия A освобождает слот
	sm.ReleaseSlot(key, "session_A")

	// Теперь Сессия B может взять слот
	_, errB2 := sm.HoldSlot(1, now, "session_B", 1*time.Minute)
	if errB2 != nil {
		t.Fatalf("Session B should be able to hold slot after release: %v", errB2)
	}
}
