package dialogue

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/schedule"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

func TestObservedCallCanRecoverAndCompleteBooking(t *testing.T) {
	r := newRig(t)
	for _, step := range []struct{ text, state, fragment string }{
		{"Какие врачи у вас есть?", StateDirectory, "терапевт"},
		{"У меня иногда болит голова мне кому записать.", StateOfferBooking, "Начните с участкового терапевта"},
		{"Да", StateIdentity, "фамилию"},
		{"Зачем?", StateIdentity, "не перепутать пациентов"},
		{"Зачем?", StateIdentity, "не перепутать пациентов"},
		{"Иванов Иван Иванович", StateIdentity, "полную дату рождения"},
		{"Что вы спросили?", StateIdentity, "полную дату рождения"},
		{"12 марта 1985", StateChooseSlot, "Первый вариант"},
		{"Первый", StateConfirmBooking, "Подтверждаете запись"},
		{"Да, зачем подтверждать?", StateConfirmBooking, "Без явного подтверждения"},
		{"Да, но можно позже", StateConfirmBooking, "Подтверждаете"},
		{"Повторите время", StateConfirmBooking, "кабинет"},
	} {
		result := r.turn(step.text)
		if result.NextState != step.state || !strings.Contains(result.BotSpeech, step.fragment) {
			t.Fatalf("%q: %+v; want %s containing %q", step.text, result, step.state, step.fragment)
		}
		if len(r.visits(t)) != 0 {
			t.Fatalf("%q wrote an unconfirmed visit", step.text)
		}
	}
	if result := r.turn("Да, подтверждаю"); result.Outcome != "BOOKED" || len(r.visits(t)) != 1 {
		t.Fatalf("explicit confirmation failed: %+v", result)
	}
}

func TestClarificationsPreserveFieldsAndDoNotEscalate(t *testing.T) {
	r := newRig(t)
	r.turn("Запишите к терапевту")
	r.turn("Иванов Иван Иванович")
	for _, text := range []string{"Зачем?", "Для чего вам дата рождения?", "Что вы спросили?", "А где вы находитесь?", "Какие документы взять?", "Почему вы это спрашиваете?"} {
		result := r.turn(text)
		if result.NextState != StateIdentity || r.call.FailCount != 0 || r.call.LastName != "Иванов" || r.call.FirstName != "Иван" || r.call.BirthDate != "" || !strings.Contains(result.BotSpeech, "полную дату рождения") {
			t.Fatalf("clarification %q corrupted identity: %+v; session %+v", text, result, r.call)
		}
	}
	if result := r.turn("12.03.1985"); result.NextState != StateChooseSlot {
		t.Fatalf("could not resume after clarifications: %+v", result)
	}
}

func TestFAQAndRepetitionDuringConfirmationKeepSlotAndQuestion(t *testing.T) {
	r := newRig(t)
	r.ready()
	slot := *r.call.SelectedSlot
	prompt := r.call.PendingPrompt
	count := withNLU(t, r, llmclient.ExtractedNLU{Intent: "AFFIRM"}, 200)
	for _, text := range []string{"Какие документы взять?", "Зачем?", "Повторите", "Какой у вас адрес?", "Повторите"} {
		result := r.turn(text)
		if result.NextState != StateConfirmBooking || r.call.SelectedSlot == nil || *r.call.SelectedSlot != slot || r.call.PendingPrompt != prompt || !strings.Contains(result.BotSpeech, prompt) || r.call.FailCount != 0 || len(r.visits(t)) != 0 {
			t.Fatalf("aside %q changed booking: %+v", text, result)
		}
	}
	if count.Load() != 0 {
		t.Fatal("confirmation side questions invoked semantic confirmation")
	}
	if result := r.turn("Да"); result.Outcome != "BOOKED" || len(r.visits(t)) != 1 {
		t.Fatalf("held slot could not be confirmed: %+v", result)
	}
}

func TestConfirmationNeverAcceptsQuestionsConditionsOrDifferentActions(t *testing.T) {
	for _, text := range []string{"Да, а какой кабинет?", "Да, когда нужно прийти?", "Да, если успею", "Хорошо, но я не уверен", "Да, отмените", "Да, перенесите", "Можно, да?", "Буду занят", "Смогу только позже", "Да, только в другой день", "Да, это не мой врач", "Давайте потом", "Да, я подумаю", "Хорошо, я проверю расписание", "Подходит второе время"} {
		t.Run(text, func(t *testing.T) {
			r := newRig(t)
			r.ready()
			result := r.turn(text)
			want := StateConfirmBooking
			if Confirmation(text) == IntentDeny {
				want = StateChooseSlot
			}
			if len(r.visits(t)) != 0 || result.NextState != want {
				t.Fatalf("non-consent created booking: %+v", result)
			}
		})
	}
}

func TestQuestionsAndNegationsCannotSelectOfferedSlot(t *testing.T) {
	for _, text := range []string{"не первый", "Первый или второй?", "Первый и второй", "А первый во сколько?"} {
		t.Run(text, func(t *testing.T) {
			r := newRig(t)
			r.turn("Запишите к терапевту")
			r.turn("Иванов Иван Иванович 12.03.1985")
			result := r.turn(text)
			if result.NextState != StateChooseSlot || r.call.SelectedSlot != nil || len(r.visits(t)) != 0 {
				t.Fatalf("ambiguous selection held a slot: %+v", result)
			}
		})
	}
	r := newRig(t)
	r.turn("Запишите к терапевту")
	r.turn("Иванов Иван Иванович 12.03.1985")
	result := r.turn("Это время " + r.call.Options[0].StartTime.Format("15:04") + "?")
	if r.call.SelectedSlot != nil || result.NextState != StateChooseSlot {
		t.Fatalf("question with exact clock selected a slot: %+v", result)
	}
}

func TestTaskSwitchDuringIdentityDoesNotMakeQuestionIntoName(t *testing.T) {
	r := newRig(t)
	r.turn("Запишите к терапевту")
	result := r.turn("Передумал, проверьте мою запись")
	if result.NextState != StateIdentity || r.call.CurrentScenario != models.ScenarioCheckStatus || r.call.LastName != "" || r.call.FirstName != "" {
		t.Fatalf("task switch treated as identity: %+v", result)
	}
	result = r.turn("Иванов Иван Иванович 12.03.1985")
	if result.NextState != StateFinal || !strings.Contains(result.BotSpeech, "нет запланированных") || len(r.visits(t)) != 0 {
		t.Fatalf("status request created a booking: %+v", result)
	}
}

func TestMultipleTasksRequireOrderingAndDoNotCancelVisit(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	visit := r.visits(t)[0]
	result := r.turn("Отмените запись и запишите к другому врачу")
	if result.Outcome != "" || len(r.visits(t)) != 1 || !strings.Contains(result.BotSpeech, "первой") {
		t.Fatalf("multiple tasks silently selected a mutation: %+v", result)
	}
	r.turn("Сначала отменить запись")
	result = r.turn("Повторите")
	if result.NextState != StateConfirmCancel || len(r.visits(t)) != 1 {
		t.Fatalf("cancellation lost confirmation: %+v", result)
	}
	if result = r.turn("Да, запишите"); result.Outcome != "" || len(r.visits(t)) != 1 {
		t.Fatalf("confirmation of different action cancelled visit: %+v", result)
	}
	if result = r.turn("Да, отмените"); result.Outcome != "CANCELLED" || len(r.visits(t)) != 0 {
		t.Fatalf("explicit cancellation failed: %+v", result)
	}
	stored, err := r.mis.GetVisit(context.Background(), visit.ID)
	if err != nil || stored.Status != models.VisitCancelled {
		t.Fatalf("cancellation not stored: %+v %v", stored, err)
	}
}

func TestRestartReleasesTentativeSlotWithoutCancellingBookedVisit(t *testing.T) {
	r := newRig(t)
	r.ready()
	slot := *r.call.SelectedSlot
	result := r.turn("Начнём сначала")
	if result.NextState != StateMenu || r.call.Patient != nil || r.call.SelectedSlot != nil || r.call.SlotHeldKey != "" || len(r.visits(t)) != 0 {
		t.Fatalf("restart retained tentative context: %+v", result)
	}
	if _, err := r.slots.HoldSlot(slot.DoctorID, slot.StartTime, "another-call", time.Minute); err != nil {
		t.Fatalf("restart did not release slot: %v", err)
	}
}

func TestDoctorGuidanceForChildUsesApprovedPediatricOffer(t *testing.T) {
	r := newRig(t)
	result := r.turn("У ребёнка болит голова, к кому записаться?")
	if result.NextState != StateOfferBooking || r.call.Specialty != "педиатр" || !strings.Contains(result.BotSpeech, "педиатра") || !r.call.ThirdParty || r.call.Patient != nil {
		t.Fatalf("child guidance did not use pediatric script: %+v", result)
	}
	if result = r.turn("Да"); result.NextState != StateIdentity {
		t.Fatalf("child offer acceptance failed: %+v", result)
	}
	if result = r.turn("Иванов Иван Иванович 12.03.2018"); result.NextState != StateGuardian {
		t.Fatalf("pediatric offer bypassed representative check: %+v", result)
	}
}

func TestSemanticReviewOfKeywordRequestFailsClosed(t *testing.T) {
	for _, status := range []int{200, 503} {
		r := newRig(t)
		count := withNLU(t, r, llmclient.ExtractedNLU{Intent: "UNKNOWN"}, status)
		result := r.turn("Нужно ли записаться к терапевту, если я только хочу узнать порядок?")
		if count.Load() != 1 || result.NextState != StateMenu || r.call.Patient != nil || r.call.CurrentScenario != "" || len(r.visits(t)) != 0 {
			t.Fatalf("uncertain keyword bypassed semantic review: %+v; requests %d", result, count.Load())
		}
	}
}

func TestNegatedActionDoesNotOverridePositiveTask(t *testing.T) {
	for text, want := range map[string]Intent{
		"Не отменяйте запись, перенесите её":       IntentReschedule,
		"Не переносите, отмените запись":           IntentCancel,
		"Не записывайте, отмените запись":          IntentCancel,
		"Не хочу отменять, перенесите":             IntentReschedule,
		"Мне не нужно сейчас переносить, отмените": IntentCancel,
	} {
		if got := ParseIntent(text); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
}

func TestDirectoryContinuationDoesNotConsumeQuestionAboutDocuments(t *testing.T) {
	r := newRig(t)
	r.turn("Какие врачи у вас есть?")
	result := r.turn("Какие ещё документы нужны?")
	if result.NextState != StateFinal || !strings.Contains(result.BotSpeech, "полис") || strings.Contains(result.BotSpeech, "профил") {
		t.Fatalf("FAQ became a directory page: %+v", result)
	}
}

func TestIdentityRefusalAndMissingDataNeverOverwriteKnownName(t *testing.T) {
	r := newRig(t)
	r.turn("Запишите к терапевту")
	r.turn("Иванов Иван Иванович")
	for _, text := range []string{"Не хочу сообщать дату рождения", "Я не помню дату рождения", "Не знаю, какая дата рождения"} {
		result := r.turn(text)
		if result.NextState != StateIdentity || r.call.LastName != "Иванов" || r.call.FirstName != "Иван" || r.call.BirthDate != "" || r.call.FailCount != 0 {
			t.Fatalf("refusal/unavailable data treated as name: %+v", result)
		}
	}
}

func TestGuidanceReturningToCallerDoesNotReuseChildIdentity(t *testing.T) {
	r := newRig(t)
	r.call.ThirdParty = true
	r.call.Identified = true
	r.call.Patient = &models.Client{ID: 99, BirthDate: "12.03.2018"}
	result := r.turn("А теперь мне, у меня болит голова, к кому записаться?")
	if result.NextState != StateOfferBooking || r.call.Specialty != "терапевт" || r.call.Patient != nil || r.call.Identified || r.call.ThirdParty {
		t.Fatalf("caller inherited child's identity: %+v", result)
	}
}

func TestRepeatedMedicalAdviceRequestEscalatesInsteadOfLooping(t *testing.T) {
	r := newRig(t)
	if result := r.turn("Что выпить от головной боли?"); result.NextState != StateOfferBooking {
		t.Fatalf("first medical question missing boundary: %+v", result)
	}
	result := r.turn("А всё-таки какую таблетку принять?")
	if result.NextState != StateTransfer || result.ReasonCode != "R-04" || result.TransferTarget != "101" || len(r.visits(t)) != 0 {
		t.Fatalf("insistence did not escalate: %+v", result)
	}
}

func TestSemanticClarificationAndGuidanceUseApprovedScripts(t *testing.T) {
	for _, test := range []struct{ intent, state, text, want string }{
		{"CLARIFY", StateIdentity, "Объясните назначение запрошенных сведений", StateIdentity},
		{"REPEAT", StateIdentity, "Продублируйте предыдущую просьбу", StateIdentity},
		{"CHOOSE_SPECIALIST", StateMenu, "Голова побаливает, не представляю, кто этим занимается", StateOfferBooking},
	} {
		t.Run(test.intent, func(t *testing.T) {
			r := newRig(t)
			r.call.CurrentState = test.state
			count := withNLU(t, r, llmclient.ExtractedNLU{Intent: test.intent}, 200)
			result := r.turn(test.text)
			if result.NextState != test.want || !result.LLMUsed || count.Load() != 1 || r.call.FailCount != 0 || r.call.Patient != nil || len(r.visits(t)) != 0 {
				t.Fatalf("semantic conversation act not handled: %+v; count %d", result, count.Load())
			}
		})
	}
}

func TestNegativeAndTimePreferencesSurviveIdentityAndFAQ(t *testing.T) {
	r := newRig(t)
	d := models.DemoDoctors()[0]
	day := time.Now().In(models.ClinicLocation).AddDate(0, 0, 2)
	var entries []schedule.Entry
	for i := 0; i < 7; i++ {
		date := day.AddDate(0, 0, i)
		for _, hour := range []int{9, 14, 16} {
			entries = append(entries, schedule.Entry{Free: true, Slot: models.Slot{DoctorID: d.ID, Doctor: d.FullName, Specialty: d.Specialty, Room: d.Room, StartTime: time.Date(date.Year(), date.Month(), date.Day(), hour, 0, 0, 0, models.ClinicLocation), Duration: 30}})
		}
	}
	r.slots = slotmgr.New(r.store, schedule.Snapshot{Doctors: []models.Doctor{d}, Entries: entries})
	r.fsm = NewFSMController(r.slots, r.mis)
	r.turn("Запишите к терапевту, но не в пятницу, после обеда")
	r.turn("Какие документы нужны?")
	result := r.turn("Иванов Иван Иванович 12.03.1985")
	if result.NextState != StateChooseSlot || len(r.call.Options) == 0 {
		t.Fatalf("preferences produced no options: %+v", result)
	}
	for _, slot := range r.call.Options {
		if slot.StartTime.Weekday() == time.Friday || slot.StartTime.Hour() < 13 {
			t.Fatalf("preference was lost: %+v", slot)
		}
	}
}

func TestSafetyInterruptsClarificationAndSideQuestions(t *testing.T) {
	for _, state := range []string{StateIdentity, StateConfirmBooking, StateConfirmCancel, StateHome, StateReminderIdentity, StateReminderConsent} {
		for _, text := range []string{"Зачем дата рождения, я не могу дышать", "Повторите и соедините с человеком"} {
			r := newRig(t)
			r.call.CurrentState = state
			result := r.turn(text)
			if result.NextState != StateTransfer || (result.TransferTarget != "104" && result.TransferTarget != "101") {
				t.Fatalf("side question bypassed safety in %s: %+v", state, result)
			}
		}
	}
}

func TestHomeAddressIsLiteralDataAndCancellationDoesNotSubmitRequest(t *testing.T) {
	r := newRig(t)
	queued := 0
	r.fsm.SetEscalator(func(context.Context, int, string) error { queued++; return nil })
	r.turn("Вызовите врача на дом")
	r.turn("Иванов Иван Иванович 12.03.1985")
	request := "Запишите адрес: Центральная, дом семь, квартира два. Не смогу прийти в поликлинику, болит нога."
	result := r.turn(request)
	if result.NextState != StateConfirmHome || r.call.HomeRequest != request || queued != 0 || len(r.visits(t)) != 0 {
		t.Fatalf("address became a task switch: %+v", result)
	}
	if result = r.turn("Какие документы нужны?"); result.NextState != StateConfirmHome || r.call.HomeRequest != request || queued != 0 {
		t.Fatalf("FAQ changed home request: %+v", result)
	}
	if result = r.turn("Отмените вызов"); result.NextState != StateFinal || r.call.HomeRequest != "" || queued != 0 {
		t.Fatalf("withdrawn home request was queued: %+v", result)
	}
}

func TestRepeatingMedicalProcedureIsNotARequestToRepeatBotPrompt(t *testing.T) {
	for _, text := range []string{"Мне нужно повторить анализ крови", "Повторите исследование", "Повтори анализ"} {
		if repeatRequest(normalized(text)) {
			t.Errorf("procedure request mistaken for repetition: %q", text)
		}
	}
}

func TestIdentityWithBookingPreambleKeepsChosenSpecialty(t *testing.T) {
	r := newRig(t)
	r.turn("Запишите к терапевту")
	result := r.turn("Запишите меня Иванов Иван Иванович 12.03.1985")
	if result.NextState != StateChooseSlot || r.call.Specialty != "терапевт" || r.call.Patient == nil || r.call.Patient.ID != 1 {
		t.Fatalf("identity continuation discarded specialty: %+v", result)
	}
}
