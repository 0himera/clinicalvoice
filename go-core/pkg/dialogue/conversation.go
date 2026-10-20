package dialogue

import (
	"context"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

// Conversation acts are handled before field extraction and confirmation. They
// never count as failed identity attempts and cannot authorize a MIS mutation.
func (f *FSMController) conversation(ctx context.Context, s *models.CallSession, text string) (StepResult, bool) {
	norm := normalized(text)
	if result, handled := f.correctInput(ctx, s, norm); handled {
		return result, true
	}
	if containsAny(norm, "начнем сначала", "начать заново", "начнем заново", "в главное меню", "вернуться в меню") {
		if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
			return f.systemFailure(ctx, s), true
		}
		resetIdentity(s)
		s.ThirdParty, s.FailCount = false, 0
		s.Specialty, s.Preference, s.SlotHeldKey = "", "", ""
		s.VisitID, s.HomeRequest, s.PendingPrompt = 0, "", ""
		return f.InitialGreeting(), true
	}
	if repeatRequest(norm) {
		s.FailCount = 0
		return StepResult{NextState: s.CurrentState, BotSpeech: pendingPrompt(s), keepPrompt: true}, true
	}
	if purposeQuestion(norm) {
		return f.aside(s, purposeExplanation(s)), true
	}
	if s.Identified && (s.CurrentState == StateConfirmBooking || s.CurrentState == StateConfirmCancel || s.CurrentState == StateReminder) && containsAny(norm, "какой кабинет", "в каком кабинете", "во сколько", "когда прием", "когда приём", "какому врачу", "какой врач") {
		s.FailCount = 0
		return StepResult{NextState: s.CurrentState, BotSpeech: pendingPrompt(s), keepPrompt: true}, true
	}
	if s.CurrentState == StateIdentity && containsAny(norm, "не хочу называть", "не буду называть", "не хочу сообщать", "не буду сообщать", "не дам данные") {
		return f.aside(s, "Для поиска карточки нужны фамилия, имя и дата рождения. Без них я могу ответить на общие вопросы. Для оформления приёма можно обратиться к сотруднику."), true
	}
	if s.CurrentState == StateIdentity && containsAny(norm, "не знаю", "не помню") && containsAny(norm, "фамил", "дату", "дата", "рождения", "данные") {
		return f.aside(s, "Без этих данных я не смогу найти нужную карточку. Можно уточнить данные или обратиться к сотруднику."), true
	}
	if answer := actionHelp(norm); answer != "" {
		return f.aside(s, answer), true
	}
	if (s.CurrentState == StateHome || s.CurrentState == StateConfirmHome) && containsAny(norm, "отмените вызов", "отменить вызов", "отмените заявку", "не надо вызывать", "не нужно вызывать", "вызов не нужен") {
		s.HomeRequest, s.FailCount = "", 0
		return say(StateFinal, "Заявку на вызов не передаю. Могу ещё помочь?"), true
	}
	if needsDoctorGuidance(norm) && f.extractSpecialty(text) == "" && f.extractDoctor(text) == nil {
		return f.primaryCareOffer(ctx, s, text, true), true
	}
	if len(requestedTasks(text)) > 1 {
		return f.aside(s, "Вижу несколько задач. Какую выполнить первой: записаться, перенести или отменить запись?"), true
	}
	if isMedicalQuestion(norm) {
		return StepResult{}, false
	}
	// Literal address/reason collection is not a FAQ. Questions about clinic facts
	// elsewhere may interrupt a task without replacing its fields or held slot.
	compositeConfirm := f.decisionClient != nil && f.decisionClient.IsEnabled() &&
		(s.CurrentState == StateConfirmBooking || s.CurrentState == StateConfirmCancel) &&
		containsAny(norm, "да", "подтвержда", "соглас", "давайте", "хорошо", "верно", "подходит", "конечно", "удобн")
	if s.CurrentState != "" && s.CurrentState != StateMenu && s.CurrentState != StateFinal && s.CurrentState != StateDirectory &&
		!compositeConfirm && informationQuestion(norm) && !isTaskIntent(ParseIntent(text)) {
		doctorSchedule := scheduleQuestion(norm) && (f.extractSpecialty(text) != "" || f.extractDoctor(text) != nil)
		if answer, ok := FAQ(text); ok && !doctorSchedule {
			return f.aside(s, answer), true
		}
		if ParseIntent(text) == IntentDirectory {
			result := f.directory(s, f.extractSpecialty(text), false)
			return f.aside(s, result.BotSpeech), true
		}
	}
	return StepResult{}, false
}

func repeatRequest(s string) bool {
	command := hasWord(s, "повторите", "повтори") && !containsAny(s, "анализ", "исследован", "обследован", "процедур")
	return command || containsAny(s, "не расслышал", "не расслышала", "не услышал", "не услышала", "что вы спросили", "что ты спросил", "скажите еще раз") || s == "еще раз" || s == "еще раз?"
}

func purposeQuestion(s string) bool {
	bare := strings.Trim(s, " .,!?—-")
	if bare == "зачем" || bare == "почему" || bare == "для чего" || bare == "что это значит" || bare == "можно уточнить" {
		return true
	}
	return containsAny(s, "зачем", "для чего", "почему") && containsAny(s, "спрашива", "уточня", "называ", "сообща", "дата рождения", "дату рождения", "мои данные", "фамили", "направление", "подтвержда")
}

func informationQuestion(s string) bool {
	return strings.Contains(s, "?") || containsAny(s, "подскажите", "расскажите", "перечислите", "узнать") || hasWord(s, "что", "чем", "как", "где", "когда", "куда", "сколько", "какой", "какая", "какие", "какого", "какую", "каких", "зачем", "почему")
}

func hasWord(s string, values ...string) bool {
	for _, value := range values {
		if word(s, value) {
			return true
		}
	}
	return false
}

func needsDoctorGuidance(s string) bool {
	return (strings.Contains(s, "к кому") && containsAny(s, "мне", "меня", "идти", "запис", "болит", "ребенк")) || containsAny(s, "мне кому", "к какому врачу", "к какому доктору", "какой врач мне", "какой специалист мне", "какого врача выбрать", "какого специалиста выбрать", "не знаю к кому", "не знаю к какому")
}

func actionHelp(s string) string {
	if !containsAny(s, "как отменить", "как мне отменить", "как перенести", "как мне перенести", "как записаться", "как мне записаться") {
		return ""
	}
	return "Я могу оформить запись, перенос или отмену. Сначала уточню цель и данные пациента, затем предложу варианты. Изменения выполняю только после вашего явного подтверждения."
}

func isTaskIntent(intent Intent) bool {
	return intent == IntentBooking || intent == IntentCancel || intent == IntentReschedule || intent == IntentCheckStatus || intent == IntentHomeVisit
}

func canSwitchTask(state string) bool {
	switch state {
	case StateIdentity, StateSpecialty, StateSchedule, StateChooseSlot, StateChooseVisit, StateReferral, StateOfferBooking, StateLabStatus:
		return true
	}
	return false
}

func pendingPrompt(s *models.CallSession) string {
	if s.PendingPrompt != "" {
		return s.PendingPrompt
	}
	switch s.CurrentState {
	case StateIdentity:
		return identityPrompt(s)
	case StateConfirmBooking:
		return "Подтверждаете выбранного врача и время? Скажите да или нет."
	case StateConfirmCancel:
		return "Подтверждаете отмену? Скажите да или нет."
	case StateSpecialty:
		return "К какому врачу хотите записаться?"
	case StateHome:
		return "Назовите полный адрес и причину вызова."
	case StateReminderIdentity:
		return "Вы тот человек, которому звонит поликлиника? Скажите да или нет."
	case StateLabStatus:
		return "Какой анализ и когда вы сдавали?"
	default:
		return "Чем могу помочь?"
	}
}

func purposeExplanation(s *models.CallSession) string {
	switch s.CurrentState {
	case StateIdentity:
		return "Фамилия, имя и дата рождения нужны, чтобы найти карточку и не перепутать пациентов."
	case StateGuardian:
		return "Ребёнка записывает законный представитель. Проверяю, что вы можете оформить запись от его имени."
	case StateReferral:
		return "По регламенту для этого специалиста требуется направление. Его оформляет врач на приёме."
	case StateConfirmBooking:
		return "Я уточняю согласие на выбранного врача и время. Без явного подтверждения запись не оформляю."
	case StateConfirmCancel:
		return "Отмена освободит выбранное время. До вашего явного подтверждения запись сохраняется."
	case StateOfferBooking:
		return "Это предложение оформить приём. Если согласитесь, я уточню данные пациента и подберу свободное время. Врач проведёт осмотр и при необходимости направит к специалисту."
	case StateHome, StateConfirmHome:
		return "Адрес и причина нужны сотруднику для рассмотрения вызова. Решение о визите принимает медицинский сотрудник."
	case StateReminderConsent:
		return "Спрашиваю ваше согласие на звонок с напоминанием. Можно отказаться, запись на приём сохранится."
	case StateReminderIdentity:
		return "Сначала проверяю, что говорю с нужным человеком, чтобы не раскрыть чужие сведения."
	default:
		return "Я помогаю оформить запись, перенос или отмену и отвечаю на вопросы о поликлинике. Медицинскую консультацию проводит врач."
	}
}

func activeTask(state string) bool {
	return state != "" && state != StateMenu && state != StateFinal && state != StateDirectory
}

func (f *FSMController) aside(s *models.CallSession, answer string) StepResult {
	s.FailCount = 0
	state := s.CurrentState
	if state == "" {
		state = StateMenu
	}
	if activeTask(state) {
		answer += " " + pendingPrompt(s)
	}
	return StepResult{
		NextState:   state,
		BotSpeech:   answer,
		keepPrompt:  true,
		Source:      "faq",
		Confidence:  1.0,
		Intent:      "FAQ",
		Explanation: "Ответ на справочный или попутный вопрос пациента",
	}
}

// This is the approved primary-care script, independent of the reported symptom.
// It does not infer a diagnosis or select a specialist from a medical complaint.
func (f *FSMController) primaryCareOffer(ctx context.Context, s *models.CallSession, text string, guidance bool) StepResult {
	if !guidance && s.MedicalBoundaryGiven {
		return f.transfer(ctx, s, "101", "R-04: повторный запрос медицинской консультации", "Медицинскую консультацию проводит врач. Соединяю с регистратором, чтобы помочь попасть на приём.")
	}
	if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
		return f.systemFailure(ctx, s)
	}
	newPatient := familyRequest(text)
	backToSelf := s.ThirdParty && !newPatient && hasWord(text, "меня", "мне", "себя")
	if (newPatient && s.Identified) || backToSelf {
		resetIdentity(s)
	}
	if newPatient || backToSelf {
		s.ThirdParty = newPatient
	}
	s.Specialty, s.RequestedDoctorID = "терапевт", 0
	child := hasWord(text, "ребенок", "ребенку", "ребенка")
	if s.Identified && s.Patient != nil {
		if age, err := models.AgeOn(s.Patient.BirthDate, time.Now()); err == nil {
			child = age < 18
		}
	}
	speech := "Я не могу давать медицинские советы или оценивать результаты. Это может сделать врач. Записать вас к терапевту?"
	if guidance {
		speech = "Начните с участкового терапевта: он осмотрит и при необходимости направит к нужному специалисту. Записать вас?"
	}
	if child {
		s.Specialty = "педиатр"
		speech = "Для ребёнка начните с педиатра: он осмотрит и при необходимости направит к нужному специалисту. Оформить запись?"
	}
	s.CurrentScenario = models.ScenarioBookingDirect
	s.SelectedSlot, s.Options, s.SlotHeldKey = nil, nil, ""
	s.FailCount = 0
	s.MedicalBoundaryGiven = !guidance
	return say(StateOfferBooking, speech)
}
