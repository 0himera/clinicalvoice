package dialogue

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func TestSafetyAndHumanRequestsInterruptConfirmationAndOutboundStates(t *testing.T) {
	states := []string{StateMenu, StateIdentity, StateSpecialty, StateSchedule, StateGuardian, StateReferral, StateChooseSlot, StateConfirmBooking, StateChooseVisit, StateConfirmCancel, StateHome, StateConfirmHome, StateReminderIdentity, StateReminder, StateReminderConsent, StateLabStatus, StateFinal}
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
	for _, query := range []string{"Когда записана Иванова Ольга, 12.03.1985?", "Во сколько записан Иванов Иван?", "Когда записана Chen Wei?", "Когда у Смирновой Ольги запись?", "На когда записана Смирнова Ольга?", "Во сколько у Смирновой приём?", "Запись Смирновой Ольги когда?", "Отмените запись моей жены", "Отмените её запись"} {
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

func TestSelfStatementIsNotMistakenForThirdParty(t *testing.T) {
	r := newRig(t)
	result := r.turn("Я записана к врачу, хочу перенести")
	if result.TransferTarget != "" || result.NextState != StateIdentity {
		t.Fatalf("self statement was treated as a third-party request: %+v", result)
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

func TestPersonalLaboratoryStatusUsesNormativeEstimateWithoutValues(t *testing.T) {
	r := newRig(t)
	result := r.turn("Готовы ли мои анализы?")
	if result.TransferTarget != "" || result.NextState != StateLabStatus || !strings.Contains(strings.ToLower(result.BotSpeech), "какой анализ") {
		t.Fatalf("personal lab status did not ask for details: %+v", result)
	}
	result = r.turn("Общий анализ крови сдавал вчера")
	if result.TransferTarget != "" || result.NextState != StateFinal || !strings.Contains(result.BotSpeech, "По нормативу") || !strings.Contains(result.BotSpeech, "Значения результатов я не вижу") {
		t.Fatalf("normative lab status missing: %+v", result)
	}
	for _, value := range []string{"ммоль", "г/л", "хорош", "плох"} {
		if strings.Contains(strings.ToLower(result.BotSpeech), value) {
			t.Fatalf("lab values or interpretation leaked: %s in %q", value, result.BotSpeech)
		}
	}
	r = newRig(t)
	result = r.turn("Когда будет готов анализ крови?")
	if result.TransferTarget != "" || !strings.Contains(result.BotSpeech, "рабочий день") {
		t.Fatalf("general turnaround FAQ was lost: %+v", result)
	}
}

func TestLabStatusUnknownDetailsReachRegistrar(t *testing.T) {
	r := newRig(t)
	r.turn("Готовы ли мои анализы?")
	result := r.turn("Не знаю")
	if result.TransferTarget != "101" || result.ReasonCode != "R-08" {
		t.Fatalf("unknown lab details did not reach a human: %+v", result)
	}
}

func TestThirdPartyLabQueryIsRefused(t *testing.T) {
	for _, query := range []string{"Готовы ли анализы Смирновой Ольги?", "Пришли результаты моей жены?", "Смирнова сдавала анализы, готовы?"} {
		t.Run(query, func(t *testing.T) {
			r := newRig(t)
			result := r.turn(query)
			if result.TransferTarget != "101" || result.ReasonCode != "R-07" {
				t.Fatalf("third-party lab query was not refused: %+v", result)
			}
		})
	}
}

func TestLabValuesQuestionStaysMedicalBoundary(t *testing.T) {
	r := newRig(t)
	result := r.turn("Мои результаты хорошие?")
	if result.NextState == StateLabStatus || !strings.Contains(result.BotSpeech, "не могу") {
		t.Fatalf("lab values were treated as a status query: %+v", result)
	}
}

func TestLabCollectedDateParsing(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, models.ClinicLocation)
	cases := map[string]string{
		"сегодня":         "21.10.2026",
		"вчера":           "20.10.2026",
		"позавчера":       "19.10.2026",
		"3 дня назад":     "18.10.2026",
		"20.10.2026":      "20.10.2026",
		"20 октября":      "20.10.2026",
		"20 октября 2026": "20.10.2026",
		"31.02.2026":      "",
		"завтра":          "",
		"20 октября 2030": "",
	}
	for phrase, want := range cases {
		got := ""
		if parsed, ok := extractLabCollectedDate(phrase, now); ok {
			got = parsed.Format("02.01.2006")
		}
		if got != want {
			t.Errorf("date %q => %q, want %q", phrase, got, want)
		}
	}
}

func TestWorkingDaysSkipWeekendsAndHolidays(t *testing.T) {
	f := &FSMController{holidays: map[string]bool{"2026-10-23": true}}
	friday := time.Date(2026, 10, 23, 0, 0, 0, 0, models.ClinicLocation)
	if got := f.addWorkingDays(friday, 1); got.Format("02.01.2006") != "26.10.2026" {
		t.Fatalf("holiday/weekend skipped incorrectly: %s", got.Format("02.01.2006"))
	}
	thursday := time.Date(2026, 10, 22, 0, 0, 0, 0, models.ClinicLocation)
	if got := f.addWorkingDays(thursday, 1); got.Format("02.01.2006") != "26.10.2026" {
		t.Fatalf("holiday was not skipped: %s", got.Format("02.01.2006"))
	}
}

func TestShortVisitQueryIdentifiesBeforeReturningDetails(t *testing.T) {
	for _, query := range []string{"Во сколько ко врачу?", "Когда у меня запись?"} {
		r := newRig(t)
		r.ready()
		r.turn("да")
		visit := r.visits(t)[0]
		r.call = &models.CallSession{CallID: "status", CurrentState: StateMenu}
		result := r.turn(query)
		if result.NextState != StateIdentity || strings.Contains(result.BotSpeech, visit.ScheduledAt) {
			t.Fatalf("visit lookup lost identity boundary for %q: %+v", query, result)
		}
	}
	r := newRig(t)
	r.ready()
	r.turn("да")
	visit := r.visits(t)[0]
	r.call = &models.CallSession{CallID: "status", CurrentState: StateMenu}
	result := r.turn("Во сколько ко врачу?")
	result = r.turn("Иванов Иван 12.03.1985")
	if !strings.Contains(result.BotSpeech, visit.ScheduledAt) || !strings.Contains(result.BotSpeech, visit.Doctor.FullName) {
		t.Fatalf("identified visit missing details: %+v", result)
	}
}
