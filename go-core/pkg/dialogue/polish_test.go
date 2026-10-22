package dialogue

import (
	"context"
	"strings"
	"testing"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func TestUncertainASRCannotAuthorizeMutation(t *testing.T) {
	r := newRig(t)
	r.ready()
	result := r.fsm.ProcessRecognizedInput(context.Background(), r.call, "да", true)
	if result.NextState != StateConfirmBooking || !strings.Contains(result.BotSpeech, "Не уверен") || len(r.visits(t)) != 0 {
		t.Fatalf("uncertain consent used: %+v", result)
	}
	if result = r.turn("да"); result.Outcome != "BOOKED" || len(r.visits(t)) != 1 {
		t.Fatalf("clear correction not accepted: %+v", result)
	}
}

func TestUncertainASRStillHonorsSafetyAndHuman(t *testing.T) {
	for phrase, target := range map[string]string{"хочу умереть": "104", "оператор": "101"} {
		r := newRig(t)
		r.ready()
		result := r.fsm.ProcessRecognizedInput(context.Background(), r.call, phrase, true)
		if result.TransferTarget != target || len(r.visits(t)) != 0 {
			t.Fatalf("quality hint bypassed gate: %+v", result)
		}
	}
	r := newRig(t)
	r.ready()
	var result StepResult
	for i := 0; i < 3; i++ {
		result = r.fsm.ProcessRecognizedInput(context.Background(), r.call, "да", true)
	}
	if result.TransferTarget != "101" || len(r.visits(t)) != 0 {
		t.Fatalf("uncertainty has no bounded fallback: %+v", result)
	}
}

func TestCorrectionReleasesHoldAndRequiresFreshConfirmation(t *testing.T) {
	for _, phrase := range []string{"исправить дату рождения", "исправить имя", "исправить данные"} {
		t.Run(phrase, func(t *testing.T) {
			r := newRig(t)
			r.ready()
			result := r.turn(phrase)
			if result.NextState != StateIdentity || r.call.Patient != nil || r.call.Identified || len(r.visits(t)) != 0 {
				t.Fatalf("correction kept authorization: %+v", result)
			}
			var held int
			if err := r.store.ReadDB().QueryRow("SELECT count(*) FROM slot_claims WHERE status='held' AND owner=?", r.call.CallID).Scan(&held); err != nil || held != 0 {
				t.Fatalf("old hold retained: %d %v", held, err)
			}
			if result = r.turn("Иванов Иван Иванович 12.03.1985"); result.NextState != StateChooseSlot {
				t.Fatalf("could not correct: %+v", result)
			}
			r.turn("первый")
			if len(r.visits(t)) != 0 {
				t.Fatal("correction implicitly confirmed booking")
			}
			if result = r.turn("да"); result.Outcome != "BOOKED" || len(r.visits(t)) != 1 {
				t.Fatalf("corrected booking failed: %+v", result)
			}
		})
	}
}

func TestOutboundCorrectionDoesNotChangeRecipient(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	before := r.visits(t)[0]
	r.call.CurrentState = StateReminder
	r.call.OutboundVisitID = before.ID
	result := r.turn("исправить имя")
	if result.TransferTarget != "101" || len(r.visits(t)) != 1 || r.call.Patient.ID != before.ClientID {
		t.Fatalf("outbound recipient changed: %+v", result)
	}
}

func TestTimeCorrectionRequiresConfirmingNewSlot(t *testing.T) {
	r := newRig(t)
	r.ready()
	old := r.call.SelectedSlot.StartTime
	if result := r.turn("исправить время"); result.NextState != StateChooseSlot || r.call.SelectedSlot != nil {
		t.Fatalf("time correction retained choice: %+v", result)
	}
	r.turn("второй")
	if r.call.SelectedSlot == nil || r.call.SelectedSlot.StartTime.Equal(old) || len(r.visits(t)) != 0 {
		t.Fatal("time correction did not select an uncommitted different slot")
	}
	selected := r.call.SelectedSlot.StartTime
	if result := r.turn("да"); result.Outcome != "BOOKED" {
		t.Fatalf("new slot not confirmed: %+v", result)
	}
	if visits := r.visits(t); len(visits) != 1 || visits[0].ScheduledAt != selected.In(models.ClinicLocation).Format("02.01.2006 15:04") {
		t.Fatalf("wrong time booked: %+v", visits)
	}
}

func TestCorrectionHelpDoesNotDiscardConfirmedFieldsOrHold(t *testing.T) {
	r := newRig(t)
	r.ready()
	selected := r.call.SelectedSlot
	result := r.turn("Как исправить данные?")
	if result.NextState != StateConfirmBooking || r.call.SelectedSlot != selected || !r.call.Identified || len(r.visits(t)) != 0 {
		t.Fatalf("help changed the pending operation: %+v", result)
	}
}

func TestIdentitySkipsPoliteDiscourseMarkers(t *testing.T) {
	for _, prefix := range []string{"Хорошо. ", "Да, ", "Конечно, ", "Ладно, ", "Понятно. "} {
		t.Run(prefix, func(t *testing.T) {
			r := newRig(t)
			r.turn("Запишите к терапевту")
			r.turn("А зачем вам мои персональные данные?")
			result := r.turn(prefix + "Иванов Иван Иванович 12 марта 1985")
			if result.NextState != StateChooseSlot || !r.call.Identified || r.call.Patient == nil {
				t.Fatalf("discourse marker changed identity parsing: %+v", result)
			}
		})
	}
}

func TestInitialIdentityPromptOmitsTechnicalCorrectionHint(t *testing.T) {
	r := newRig(t)
	result := r.turn("Запишите к терапевту")
	if result.NextState != StateIdentity || strings.Contains(result.BotSpeech, "исправить данные") {
		t.Fatalf("initial identity prompt exposes internal correction command: %+v", result)
	}
}
