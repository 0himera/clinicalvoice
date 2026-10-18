package dialogue

import (
	"context"
	"strings"
	"testing"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func TestSafetyAndHumanRequestsInterruptConfirmationAndOutboundStates(t *testing.T) {
	states := []string{StateMenu, StateIdentity, StateSpecialty, StateSchedule, StateGuardian, StateReferral, StateChooseSlot, StateConfirmBooking, StateChooseVisit, StateConfirmCancel, StateHome, StateConfirmHome, StateReminderIdentity, StateReminder, StateReminderConsent, StateFinal}
	for _, state := range states {
		for phrase, target := range map[string]string{"хочу умереть": "104", "соедините": "101", "сильно болит зуб, отёк": "102"} {
			t.Run(state+"/"+phrase, func(t *testing.T) {
				r := newRig(t)
				r.ready()
				r.call.CurrentState = state
				res := r.turn(phrase)
				if res.TransferTarget != target || res.NextState != StateTransfer || len(r.visits(t)) != 0 {
					t.Fatalf("interruption lost or visit created: %+v", res)
				}
				if target == "104" && !strings.Contains(res.BotSpeech, "сто двенадцать") {
					t.Fatal("missing emergency warning")
				}
			})
		}
	}
}

func TestNamedThirdPartyCannotReadOrCancelVisit(t *testing.T) {
	for _, query := range []string{"Когда записана Иванова Ольга, 12.03.1985?", "Во сколько записан Иванов Иван?", "Когда записана Chen Wei?", "Отмените запись моей жены", "Отмените её запись"} {
		t.Run(query, func(t *testing.T) {
			r := newRig(t)
			r.ready()
			r.turn("да")
			visit := r.visits(t)[0]
			r.call = &models.CallSession{CallID: "third-party", CurrentState: StateMenu}
			res := r.turn(query)
			if res.TransferTarget != "101" || strings.Contains(res.BotSpeech, visit.ScheduledAt) || strings.Contains(res.BotSpeech, visit.Doctor.FullName) {
				t.Fatalf("unverified subject exposed visit: %+v", res)
			}
			r.turn("Иванов Иван Иванович 12.03.1985")
			r.turn("да")
			if got := r.visits(t); len(got) != 1 || got[0].ID != visit.ID {
				t.Fatalf("third party changed visit: %+v", got)
			}
		})
	}
}

func TestVisitPronounAfterIdentificationDoesNotChangePatient(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	before := r.visits(t)[0]
	if result := r.turn("Тогда перенесите её"); result.NextState != StateChooseSlot {
		t.Fatalf("visit pronoun misread as another patient: %+v", result)
	}
	r.turn("первый")
	result := r.turn("да")
	after := r.visits(t)
	if result.Outcome != "RESCHEDULED" || len(after) != 1 || after[0].ID != before.ID || after[0].ScheduledAt == before.ScheduledAt {
		t.Fatalf("identified visit not rescheduled in place: %+v %+v", result, after)
	}
}

func TestRecordAuthorizationIsCheckedAtReadAndMutation(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	r.call.ThirdParty = true
	r.call.Guardian = true // Adult consent is not provided by the MIS contract.
	if res := r.fsm.existing(context.Background(), r.call); res.TransferTarget != "101" {
		t.Fatalf("adult guardian flag bypassed read permission: %+v", res)
	}
	r.call.CurrentState = StateConfirmCancel
	if res := r.turn("да"); res.TransferTarget != "101" || len(r.visits(t)) != 1 {
		t.Fatalf("unverified cancellation changed a visit: %+v", res)
	}
}

func TestUnknownPatientIsRecheckedThenTransferredWithoutRegistration(t *testing.T) {
	for _, task := range []string{"Запишите к терапевту", "Когда я записан?", "Вызовите врача на дом"} {
		t.Run(task, func(t *testing.T) {
			r := newRig(t)
			r.turn(task)
			first := r.turn("Несуществов Несуществ 01.01.1971")
			if first.NextState != StateIdentity || !strings.Contains(first.BotSpeech, "не найдена") {
				t.Fatalf("unknown identity did not request correction: %+v", first)
			}
			second := r.turn("Несуществов Несуществ 01.01.1971")
			if second.TransferTarget != "101" || second.ReasonCode != "R-07" {
				t.Fatalf("repeated miss did not reach registrar: %+v", second)
			}
			cards, err := r.mis.FindClients(context.Background(), "Несуществов", "Несуществ", "01.01.1971")
			if err != nil || len(cards) != 0 || len(r.visits(t)) != 0 {
				t.Fatalf("unknown patient silently registered/booked: %+v %v", cards, err)
			}
		})
	}
}

func TestPatientCanCorrectBirthDateAfterLookupMiss(t *testing.T) {
	r := newRig(t)
	r.turn("Запишите к терапевту")
	r.turn("Иванов Иван 12.03.1986")
	result := r.turn("12.03.1985")
	if result.NextState != StateChooseSlot || !r.call.Identified || r.call.Patient.ID != 1 || r.call.IdentityMisses != 0 {
		t.Fatalf("corrected identity could not continue: %+v", result)
	}
}

func TestAnsweringMachineCannotReceiveReminderDetails(t *testing.T) {
	r := newRig(t)
	r.call.CurrentState = StateReminderIdentity
	r.call.Patient = &models.Client{ID: 1, FirstName: "Иван", MiddleName: "Иванович"}
	result := r.turn("Здравствуйте, оставьте сообщение после сигнала")
	if !result.ShouldHangup || result.Outcome != "WRONG_PERSON" || strings.Contains(result.BotSpeech, "Иван") || !strings.Contains(result.BotSpeech, "регистратур") {
		t.Fatalf("answering machine received an unsafe reminder: %+v", result)
	}
}

func TestPersonalLaboratoryStatusIsNotInvented(t *testing.T) {
	r := newRig(t)
	result := r.turn("Готовы ли мои анализы?")
	if result.TransferTarget != "101" || result.ReasonCode != "R-08" || !strings.Contains(result.BotSpeech, "недоступен") {
		t.Fatalf("personal status did not reach a human: %+v", result)
	}
	r = newRig(t)
	result = r.turn("Когда будет готов анализ крови?")
	if result.TransferTarget != "" || !strings.Contains(result.BotSpeech, "рабочий день") {
		t.Fatalf("general turnaround FAQ was lost: %+v", result)
	}
}

func TestShortVisitQueryIdentifiesBeforeReturningDetails(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	visit := r.visits(t)[0]
	r.call = &models.CallSession{CallID: "status", CurrentState: StateMenu}
	result := r.turn("Во сколько ко врачу?")
	if result.NextState != StateIdentity || strings.Contains(result.BotSpeech, visit.ScheduledAt) {
		t.Fatalf("visit lookup lost identity boundary: %+v", result)
	}
	result = r.turn("Иванов Иван 12.03.1985")
	if !strings.Contains(result.BotSpeech, visit.ScheduledAt) || !strings.Contains(result.BotSpeech, visit.Doctor.FullName) {
		t.Fatalf("identified visit missing details: %+v", result)
	}
}
