package dialogue

import (
	"context"

	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func (f *FSMController) menu(ctx context.Context, s *models.CallSession, text string) StepResult {
	intent := ParseIntent(text)
	if s.CurrentState == StateFinal && Confirmation(text) == IntentDeny && !isTaskIntent(intent) {
		return StepResult{NextState: StateDone, BotSpeech: "Всего доброго, будьте здоровы!", ShouldHangup: true, Outcome: "COMPLETED"}
	}
	requested := f.extractDoctor(text)
	specialty := f.extractSpecialty(text)
	doctorSchedule := intent == IntentSchedule && scheduleQuestion(normalized(text)) && (requested != nil || f.extractSpecialty(text) != "")
	if answer, ok := FAQ(text); ok && !doctorSchedule && intent != IntentHomeVisit {
		s.FailCount = 0
		return say(StateFinal, answer+" Могу ещё чем-то помочь?")
	}
	if intent == IntentSchedule {
		if scheduleQuestion(normalized(text)) {
			return f.schedule(ctx, s, text)
		}
		intent = IntentUnknown
	}
	if intent == IntentDirectory {
		return f.directory(s, specialty, false)
	}
	var nlu *llmclient.ExtractedNLU
	semanticReview := needsSemanticReview(text, intent)
	if semanticReview || (intent == IntentUnknown && specialty == "" && requested == nil) {
		nlu = f.understand(ctx, text, s.CurrentState)
		if result, ok := f.nluInformation(ctx, s, nlu); ok {
			return result
		}
		if nlu != nil {
			intent = Intent(nlu.Intent)
			specialty = f.nluSpecialty(nlu.Specialty)
			if nlu.DoctorName != "" {
				requested = f.extractDoctor(nlu.DoctorName)
			}
			if intent == IntentSchedule {
				return f.schedule(ctx, s, text+" "+specialty+" "+nlu.DoctorName)
			}
		}
		if semanticReview && (nlu == nil || !isTaskIntent(intent)) {
			return f.retry(ctx, s, "Вы хотите оформить запись, перенос или отмену, либо только уточнить порядок действий?")
		}
	}
	if s.ThirdParty && !s.Guardian && (intent == IntentCheckStatus || intent == IntentCancel || intent == IntentReschedule) {
		return f.transfer(ctx, s, "101", "R-07: сведения о другом пациенте, полномочия не проверены", "Сведения о чужих записях сообщает сотрудник после проверки полномочий. Соединяю с регистратором.")
	}
	if intent != IntentBooking && intent != IntentCancel && intent != IntentReschedule && intent != IntentCheckStatus && intent != IntentHomeVisit {
		if specialty != "" || requested != nil {
			intent = IntentBooking
		} else {
			return f.retry(ctx, s, "Могу записать к врачу, перенести или отменить запись, подсказать график. Что нужно?")
		}
	}
	if familyRequest(text) && s.Identified {
		if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
			return f.systemFailure(ctx, s)
		}
		resetIdentity(s)
		s.ThirdParty = true
	}
	// A task switch abandons a tentative selection, never a confirmed MIS visit.
	if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
		return f.systemFailure(ctx, s)
	}
	s.FailCount = 0
	s.MedicalBoundaryGiven = false
	s.Options = nil
	s.SelectedSlot = nil
	s.VisitID = 0
	s.SlotHeldKey = ""
	s.Preference = text
	s.Specialty = specialty
	s.RequestedDoctorID = 0
	if requested != nil {
		s.Specialty = requested.Specialty
		s.RequestedDoctorID = requested.ID
	}
	switch intent {
	case IntentCancel:
		s.CurrentScenario = models.ScenarioCancel
	case IntentReschedule:
		s.CurrentScenario = models.ScenarioReschedule
	case IntentCheckStatus:
		s.CurrentScenario = models.ScenarioCheckStatus
	case IntentHomeVisit:
		s.CurrentScenario = models.ScenarioHomeVisit
	default:
		s.CurrentScenario = models.ScenarioBookingDirect
	}
	if s.CurrentScenario == models.ScenarioBookingDirect && s.Specialty != "" && !f.slotMgr.IsDirectBookingAllowed(s.Specialty) {
		s.CurrentScenario = models.ScenarioBookingReferral
		return say(StateReferral, "К этому специалисту требуется направление. Записать вас к терапевту для его получения?")
	}
	if s.Identified && s.Patient != nil {
		return f.afterIdentity(ctx, s)
	}
	if ExtractBirthDate(text) != "" || (nlu != nil && nlu.BirthDate != "") {
		return f.identity(ctx, s, text)
	}
	return say(StateIdentity, "Назовите, пожалуйста, фамилию, имя, отчество и дату рождения пациента.")
}
