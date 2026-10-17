package dialogue

import (
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/schedule"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

func timedRig(t *testing.T, clocks ...string) *rig {
	t.Helper()
	r := newRig(t)
	doc := models.DemoDoctors()[0]
	day := time.Now().In(models.ClinicLocation).AddDate(0, 0, 2)
	var entries []schedule.Entry
	for _, clock := range clocks {
		parsed, err := time.Parse("15:04", clock)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), parsed.Hour(), parsed.Minute(), 0, 0, models.ClinicLocation)
		entries = append(entries, schedule.Entry{Free: true, Slot: models.Slot{DoctorID: doc.ID, Doctor: doc.FullName, Specialty: doc.Specialty, Room: doc.Room, StartTime: at, Duration: 30}})
	}
	r.slots = slotmgr.New(r.store, schedule.Snapshot{Doctors: []models.Doctor{doc}, Entries: entries})
	r.fsm = NewFSMController(r.slots, r.mis)
	r.turn("Запишите к терапевту")
	if result := r.turn("Иванов Иван Иванович 12.03.1985"); result.NextState != StateChooseSlot {
		t.Fatalf("prepare slot choice: %+v", result)
	}
	return r
}

func TestSpokenSlotChoiceCreatesOnlyExplicitlyConfirmedVisit(t *testing.T) {
	for _, phrase := range []string{"второй", "Второй.", "второе время", "2", "вариант два", "в 15", "на 15 часов", "в пятнадцать", "в пятнадцать часов", "15:00", "в 15 часов 00 минут"} {
		t.Run(phrase, func(t *testing.T) {
			r := timedRig(t, "14:00", "15:00")
			wanted := r.call.Options[1].StartTime
			result := r.turn(phrase)
			if result.NextState != StateConfirmBooking || r.call.SelectedSlot == nil || !r.call.SelectedSlot.StartTime.Equal(wanted) || len(r.visits(t)) != 0 {
				t.Fatalf("selection %q failed or mutated MIS: %+v", phrase, result)
			}
			if confirmed := r.turn("да"); confirmed.Outcome != "BOOKED" {
				t.Fatalf("confirm: %+v", confirmed)
			}
			visits := r.visits(t)
			if len(visits) != 1 || visits[0].ScheduledAt != wanted.Format(models.DateTimeLayout) {
				t.Fatalf("wrong booked visit: %+v", visits)
			}
		})
	}
}

func TestHourOnlyChoiceDisambiguatesWithoutR05(t *testing.T) {
	r := timedRig(t, "14:00", "14:30")
	for i := 0; i < 4; i++ {
		result := r.turn("в 14")
		if result.NextState != StateChooseSlot || r.call.FailCount != 0 || result.DiagnosticCode != "slot_time_ambiguous" || r.call.SelectedSlot != nil || len(r.visits(t)) != 0 {
			t.Fatalf("ambiguous hour punished or selected: %+v", result)
		}
	}
	if result := r.turn("второй"); result.NextState != StateConfirmBooking || r.call.SelectedSlot.StartTime.Minute() != 30 {
		t.Fatalf("second failed after hour clarification: %+v", result)
	}
	if result := r.turn("да"); result.Outcome != "BOOKED" || len(r.visits(t)) != 1 {
		t.Fatalf("confirmation failed: %+v", result)
	}
}

func TestSlotTimeSelectionCannotOverrideNegationOrConflict(t *testing.T) {
	for _, phrase := range []string{"не в 14:00", "не на 14", "первый в 15:00", "в 14 или в 15", "в четырнадцать и в пятнадцать"} {
		t.Run(phrase, func(t *testing.T) {
			r := timedRig(t, "14:00", "15:00")
			result := r.turn(phrase)
			if result.NextState != StateChooseSlot || r.call.SelectedSlot != nil || len(r.visits(t)) != 0 {
				t.Fatalf("ambiguous time held a slot: %+v", result)
			}
		})
	}
	if got := optionIndex("во вторник", 2); got != -1 {
		t.Fatalf("weekday mistaken for second option: %d", got)
	}
}

func TestSpokenClockExtraction(t *testing.T) {
	for _, test := range []struct {
		text         string
		hour, minute int
		precise      bool
	}{
		{"в 14", 14, 0, false}, {"в четырнадцать", 14, 0, false}, {"в девять тридцать", 9, 30, true},
		{"на 14:30", 14, 30, true}, {"в два часа дня", 14, 0, false}, {"в 21 час 45 минут", 21, 45, true},
	} {
		hour, minute, precise, ok := requestedClock(test.text)
		if !ok || hour != test.hour || minute != test.minute || precise != test.precise {
			t.Errorf("%q: %d:%d precise=%v ok=%v", test.text, hour, minute, precise, ok)
		}
	}
	for _, text := range []string{"второй", "во вторник", "на 17 октября", "на 17.10.2026", "в 25:61", "в девять и в десять"} {
		if _, _, _, ok := requestedClock(text); ok {
			t.Errorf("non-clock accepted: %q", text)
		}
	}
}
