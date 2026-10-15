package dialogue

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/schedule"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

type rig struct {
	store *storage.Store
	slots *slotmgr.SlotManager
	mis   *misclient.MockMISClient
	fsm   *FSMController
	call  *models.CallSession
}

func TestConcurrentCallsCannotCreateTwoVisitsForSameSpecialty(t *testing.T) {
	r := newRig(t)
	r.ready()
	first := r.call
	r.call = &models.CallSession{CallID: "second", CurrentState: StateMenu}
	r.ready()
	second := r.call
	var wg sync.WaitGroup
	wg.Add(2)
	for _, call := range []*models.CallSession{first, second} {
		go func(call *models.CallSession) {
			defer wg.Done()
			r.fsm.ProcessInput(context.Background(), call, "да")
		}(call)
	}
	wg.Wait()
	if visits := r.visits(t); len(visits) != 1 {
		t.Fatalf("concurrent duplicate specialty visits: %+v", visits)
	}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d := models.DemoDoctors()[0]
	start := time.Now().In(models.ClinicLocation).Add(48 * time.Hour).Truncate(time.Hour)
	entries := []schedule.Entry{}
	for i := 0; i < 4; i++ {
		entries = append(entries, schedule.Entry{Free: true, Slot: models.Slot{DoctorID: d.ID, Doctor: d.FullName, Specialty: d.Specialty, Room: d.Room, StartTime: start.Add(time.Duration(i) * 30 * time.Minute), Duration: 30}})
	}
	slots := slotmgr.New(store, schedule.Snapshot{Doctors: []models.Doctor{d}, Entries: entries})
	mis := misclient.NewMockMISClient()
	return &rig{store: store, slots: slots, mis: mis, fsm: NewFSMController(slots, mis), call: &models.CallSession{CallID: t.Name(), CurrentState: StateMenu}}
}
func (r *rig) turn(text string) StepResult {
	return r.fsm.ProcessInput(context.Background(), r.call, text)
}
func (r *rig) ready() {
	r.turn("Запишите к терапевту")
	r.turn("Иванов Иван Иванович 12 марта 1985")
	r.turn("первый")
}
func (r *rig) visits(t *testing.T) []models.Visit {
	t.Helper()
	v, e := r.mis.ListVisits(context.Background(), 1, models.VisitPlanned)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestExplicitConfirmationRequired(t *testing.T) {
	for _, phrase := range []string{"Повторите время", "я не понял", "можно уточнить?"} {
		t.Run(phrase, func(t *testing.T) {
			r := newRig(t)
			r.ready()
			res := r.turn(phrase)
			if len(r.visits(t)) != 0 || res.NextState != StateConfirmBooking {
				t.Fatalf("ambiguous phrase created booking: %+v", res)
			}
		})
	}
}
func TestReschedulePreservesVisitAndFreesOldSlot(t *testing.T) {
	r := newRig(t)
	r.ready()
	oldSlot := *r.call.SelectedSlot
	if out := r.turn("да"); out.Outcome != "BOOKED" {
		t.Fatalf("booking failed: %+v", out)
	}
	old := r.visits(t)[0]
	r.turn("Перенесите запись")
	r.turn("первый")
	out := r.turn("да")
	visits := r.visits(t)
	if len(visits) != 1 || visits[0].ID != old.ID || visits[0].ScheduledAt == old.ScheduledAt || out.Outcome != "RESCHEDULED" {
		t.Fatalf("reschedule duplicated/lost visit: %+v %+v", visits, out)
	}
	if _, err := r.slots.HoldSlot(oldSlot.DoctorID, oldSlot.StartTime, "another", time.Minute); err != nil {
		t.Fatalf("old slot remained occupied: %v", err)
	}
}
func TestCancelFreesSlotAndRequiresAffirmation(t *testing.T) {
	r := newRig(t)
	r.ready()
	slot := *r.call.SelectedSlot
	r.turn("да")
	r.turn("Отмените запись")
	if res := r.turn("повторите"); res.NextState != StateConfirmCancel || len(r.visits(t)) != 1 {
		t.Fatalf("ambiguous cancellation: %+v", res)
	}
	if res := r.turn("да"); res.Outcome != "CANCELLED" || len(r.visits(t)) != 0 {
		t.Fatalf("cancel failed: %+v", res)
	}
	if _, err := r.slots.HoldSlot(slot.DoctorID, slot.StartTime, "new", time.Minute); err != nil {
		t.Fatalf("cancelled slot not released: %v", err)
	}
}

type failingUpdate struct{ misclient.MISClient }

func (failingUpdate) UpdateVisit(context.Context, int, models.VisitUpdate) (*models.Visit, error) {
	return nil, &misclient.APIError{StatusCode: 503}
}
func TestCancelFailureIsNotReportedAsSuccess(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	r.turn("Отмените запись")
	r.fsm.misClient = failingUpdate{r.mis}
	res := r.turn("да")
	if res.NextState != StateTransfer || res.Outcome == "CANCELLED" || len(r.visits(t)) != 1 {
		t.Fatalf("false cancellation success: %+v", res)
	}
}
func TestUnknownUtterancesEscalate(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 2; i++ {
		if r.turn("абракадабра").NextState == StateTransfer {
			t.Fatal("transferred before third failure")
		}
	}
	if r.turn("абракадабра").NextState != StateTransfer {
		t.Fatal("third failure did not transfer")
	}
}
func TestEmergencyInterruptsEveryState(t *testing.T) {
	for _, state := range []string{StateMenu, StateIdentity, StateChooseSlot, StateConfirmBooking, StateConfirmCancel, StateHome, StateReminder} {
		t.Run(state, func(t *testing.T) {
			r := newRig(t)
			r.call.CurrentState = state
			res := r.turn("Не могу дышать")
			if res.TransferTarget != "104" || !strings.Contains(res.BotSpeech, "сто три") || res.ShouldHangup {
				t.Fatalf("emergency was not routed: %+v", res)
			}
		})
	}
}
func TestOperatorAndMedicalBoundaries(t *testing.T) {
	for _, phrase := range []string{"Что мне выпить от головной боли?", "Какой у меня холестерин в последнем анализе?"} {
		r := newRig(t)
		res := r.turn(phrase)
		if !strings.Contains(res.BotSpeech, "не могу") || len(r.visits(t)) != 0 {
			t.Fatalf("medical boundary: %+v", res)
		}
	}
	r := newRig(t)
	r.ready()
	if res := r.turn("соедините с человеком"); res.TransferTarget != "101" {
		t.Fatalf("operator not available mid-booking: %+v", res)
	}
}
func TestRecordingObjectionDoesNotBecomeBooking(t *testing.T) {
	r := newRig(t)
	res := r.turn("Я против записи разговора")
	if res.NextState != StateMenu || !strings.Contains(res.BotSpeech, "не сохраняется") {
		t.Fatalf("recording objection misrouted: %+v", res)
	}
}

func TestMedicalOfferCanActuallyStartBooking(t *testing.T) {
	r := newRig(t)
	if res := r.turn("Что мне выпить от головной боли?"); res.NextState != StateOfferBooking {
		t.Fatalf("missing actionable offer: %+v", res)
	}
	if res := r.turn("да"); res.NextState != StateIdentity {
		t.Fatalf("offer acceptance lost: %+v", res)
	}
}
func TestDoctorNameIsRespected(t *testing.T) {
	r := newRig(t)
	r.turn("Запишите меня к Васильевой")
	res := r.turn("Иванов Иван Иванович 12.03.1985")
	if res.NextState != StateChooseSlot || r.call.RequestedDoctorID != 1 {
		t.Fatalf("doctor selection lost: %+v", res)
	}
	for _, s := range r.call.Options {
		if s.DoctorID != 1 {
			t.Fatal("offered a different doctor")
		}
	}
}

func TestDoctorScheduleDoesNotBecomeGeneralClinicHours(t *testing.T) {
	r := newRig(t)
	res := r.turn("Какой режим работы у терапевта Васильевой?")
	if res.NextState != StateOfferBooking || !strings.Contains(res.BotSpeech, "Васильева") {
		t.Fatalf("doctor schedule lost: %+v", res)
	}
}
func TestReferralCannotBeBypassedByAnotherEntryPoint(t *testing.T) {
	r := newRig(t)
	patient, err := r.mis.GetClient(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	r.call.Patient = patient
	r.call.Identified = true
	r.call.Specialty = "кардиолог"
	r.call.CurrentScenario = models.ScenarioBookingDirect
	if res := r.fsm.offer(context.Background(), r.call); res.NextState != StateReferral {
		t.Fatalf("referral bypass: %+v", res)
	}
}
func TestChildNeedsGuardianBeforeIdentityLookup(t *testing.T) {
	r := newRig(t)
	r.turn("Запишите к педиатру")
	res := r.turn("Иванов Иван Иванович 12.03.2018")
	if res.NextState != StateGuardian || r.call.Patient != nil {
		t.Fatalf("guardian was not checked: %+v", res)
	}
	if res = r.turn("нет"); res.NextState != StateTransfer {
		t.Fatal("child booking proceeded without representative")
	}
}

func TestSecondPatientDoesNotReuseFirstPatientsIdentity(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	res := r.turn("Запишите мою дочь к педиатру")
	if res.NextState != StateIdentity || r.call.Patient != nil || r.call.Identified || r.call.BirthDate != "" {
		t.Fatalf("first patient's identity leaked to family booking: %+v", res)
	}
	res = r.turn("Запишите меня к хирургу")
	if r.call.ThirdParty || res.NextState != StateIdentity {
		t.Fatalf("return to caller identity failed: %+v", res)
	}
}
func TestThirdPartyStatusDoesNotDiscloseVisits(t *testing.T) {
	r := newRig(t)
	res := r.turn("На когда записана моя жена Иванова?")
	if res.NextState != StateTransfer {
		t.Fatalf("third-party disclosure: %+v", res)
	}
}
func TestReminderDenialIsNeverConfirmed(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	v := r.visits(t)[0]
	r.call = &models.CallSession{CallID: "outbound"}
	r.fsm.StartReminder(context.Background(), r.call, v.ID)
	r.turn("да")
	res := r.turn("не смогу прийти")
	if res.NextState != StateConfirmCancel || res.Outcome == "CONFIRMED" {
		t.Fatalf("denial confirmed: %+v", res)
	}
	c, err := r.mis.GetClient(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Comment, "Подтверждена") {
		t.Fatal("denial changed client comment")
	}
}
func TestReminderConfirmsInMISComment(t *testing.T) {
	r := newRig(t)
	r.ready()
	r.turn("да")
	v := r.visits(t)[0]
	r.call = &models.CallSession{CallID: "outbound"}
	r.fsm.StartReminder(context.Background(), r.call, v.ID)
	r.turn("да")
	res := r.turn("приду")
	c, err := r.mis.GetClient(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "CONFIRMED" || !strings.Contains(c.Comment, "Подтверждена запись #") {
		t.Fatalf("confirmation was not persisted: %+v", res)
	}
}
func TestReferralContinuesWithoutStateMismatch(t *testing.T) {
	r := newRig(t)
	res := r.turn("Запишите к кардиологу")
	if r.call.CurrentState != res.NextState || res.NextState != StateReferral {
		t.Fatal("state mismatch")
	}
	if res = r.turn("да"); res.NextState != StateIdentity || r.call.Specialty != "терапевт" {
		t.Fatalf("referral acceptance broken: %+v", res)
	}
}

func TestRussianNamesAndDates(t *testing.T) {
	for _, phrase := range []string{
		"Рябова Мария Сергеевна 15 апреля 1985 года",
		"Можно записать мою дочь Рябова Мария Сергеевна 15 апреля 2018 года",
	} {
		last, first, middle := ExtractFIO(phrase)
		if last != "Рябова" || first != "Мария" || middle != "Сергеевна" {
			t.Fatalf("name corrupted in %q: %s %s %s", phrase, last, first, middle)
		}
	}
	for input, want := range map[string]string{"15 апреля 1985": "15.04.1985", "29.02.2024": "29.02.2024", "31.02.1985": "", "12 03 85": "12.03.1985", "15 апреля 3000": ""} {
		if got := ExtractBirthDate(input); got != want {
			t.Errorf("%q => %q, want %q", input, got, want)
		}
	}
	for _, phrase := range []string{"не подходит", "не буду", "не смогу", "нет", "не подтверждаю"} {
		if Confirmation(phrase) != IntentDeny {
			t.Errorf("negation lost: %q", phrase)
		}
	}
	for input, want := range map[string]string{"двенадцатое марта семьдесят восьмого": "12.03.1978", "пятнадцатого апреля тысяча девятьсот восемьдесят пятого года": "15.04.1985", "тридцать первого февраля тысяча девятьсот восьмидесятого": ""} {
		if got := ExtractBirthDate(input); got != want {
			t.Errorf("spoken date %q => %q, want %q", input, got, want)
		}
	}
	if Confirmation("да, не отменяйте") != IntentDeny {
		t.Fatal("negated cancellation became affirmative")
	}
}

func TestScheduleRequestAsksForDoctorWithoutCountingMisunderstanding(t *testing.T) {
	for _, phrase := range []string{"Хочу узнать расписание", "Подскажите график"} {
		t.Run(phrase, func(t *testing.T) {
			r := newRig(t)
			result := r.turn(phrase)
			if result.NextState != StateSchedule || r.call.FailCount != 0 {
				t.Fatalf("recognized schedule request rejected: %+v, failures %d", result, r.call.FailCount)
			}
			result = r.turn("Терапевта")
			if result.NextState != StateOfferBooking || r.call.FailCount != 0 || len(r.visits(t)) != 0 {
				t.Fatalf("doctor clarification did not return schedule: %+v", result)
			}
		})
	}
}

func TestScheduleClarificationCanAnswerClinicHoursOrSwitchToBooking(t *testing.T) {
	r := newRig(t)
	r.turn("Хочу узнать расписание")
	result := r.turn("Поликлиники")
	if result.NextState != StateFinal || !strings.Contains(result.BotSpeech, "семи тридцати") || r.call.FailCount != 0 {
		t.Fatalf("clinic hours clarification rejected: %+v", result)
	}
	r.turn("Хочу узнать расписание")
	result = r.turn("Запишите к терапевту")
	if result.NextState != StateIdentity {
		t.Fatalf("could not switch from schedule to booking: %+v", result)
	}
}

func TestClinicHoursQuestionVariantsAndDoctorScope(t *testing.T) {
	for _, phrase := range []string{
		"В какие дни вы работаете?", "В какие дни выработаете все?",
		"Какой у вас режим работы?", "Когда открываетесь?", "Во сколько закрываетесь?",
		"Подскажите часы работы", "По каким дням вы работаете?",
	} {
		r := newRig(t)
		result := r.turn(phrase)
		if result.NextState != StateFinal || !strings.Contains(result.BotSpeech, "семи тридцати") || r.call.FailCount != 0 {
			t.Errorf("clinic hours not understood for %q: %+v", phrase, result)
		}
	}
	r := newRig(t)
	result := r.turn("В какие дни работает Васильева?")
	if result.NextState != StateOfferBooking || !strings.Contains(result.BotSpeech, "Васильева") || strings.Contains(result.BotSpeech, "Поликлиника работает") {
		t.Fatalf("doctor question answered with clinic hours: %+v", result)
	}
}

func TestLabelledBirthDatesAreExplicitValidatedAndOrderIndependent(t *testing.T) {
	for _, phrase := range []string{
		"Иванов, Иван Ивановича, 2005 год, 5 месяц, 3 день.",
		"день 3, месяц 5, год 2005", "год 2005, месяц 5, день 3",
		"3 день 5 месяц 2005 год", "две тысячи пятого года пятый месяц третий день",
		"Добрый день, Иванов Иван, 2005 год 5 месяц 3 день",
	} {
		if got := ExtractBirthDate(phrase); got != "03.05.2005" {
			t.Errorf("labelled date %q => %q", phrase, got)
		}
	}
	for _, phrase := range []string{"2005 год 2 месяц 31 день", "2005 год 5 месяц", "2005 год 5 месяц 3 день 2006 год"} {
		if got := ExtractBirthDate(phrase); got != "" {
			t.Errorf("invalid date accepted in %q: %q", phrase, got)
		}
	}
}

func TestIdentityKeepsKnownNameAndAsksOnlyForMissingBirthDate(t *testing.T) {
	r := newRig(t)
	r.turn("Записать к врачу")
	result := r.turn("Иванов Иван Иванович 2005 год")
	if result.NextState != StateIdentity || r.call.FailCount != 0 || strings.Contains(result.BotSpeech, "фамили") || r.call.LastName != "Иванов" {
		t.Fatalf("partial identity was discarded or counted as a failure: %+v", result)
	}
	r.turn("День месяц года")
	if r.call.LastName != "Иванов" || r.call.FirstName != "Иван" {
		t.Fatal("date clarification overwrote patient's name")
	}
	result = r.turn("2005 год 5 месяц 3 день")
	if result.NextState != StateSpecialty || r.call.Patient == nil || r.call.Patient.BirthDate != "03.05.2005" || r.call.Patient.LastName != "Иванов" {
		t.Fatalf("completed identity rejected: %+v, patient %+v", result, r.call.Patient)
	}
}
