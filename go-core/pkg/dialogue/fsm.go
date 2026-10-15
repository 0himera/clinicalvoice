package dialogue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/safety"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

const (
	StateMenu             = "STATE_MAIN_MENU"
	StateIdentity         = "STATE_AWAIT_IDENTITY"
	StateGuardian         = "STATE_GUARDIAN"
	StateSpecialty        = "STATE_AWAIT_SPECIALTY"
	StateSchedule         = "STATE_AWAIT_SCHEDULE"
	StateDirectory        = "STATE_DIRECTORY"
	StateReferral         = "STATE_REFERRAL"
	StateOfferBooking     = "STATE_OFFER_BOOKING"
	StateChooseSlot       = "STATE_CHOOSE_SLOT"
	StateConfirmBooking   = "STATE_CONFIRM_BOOKING"
	StateChooseVisit      = "STATE_CHOOSE_VISIT"
	StateConfirmCancel    = "STATE_CONFIRM_CANCEL"
	StateHome             = "STATE_HOME_REQUEST"
	StateConfirmHome      = "STATE_CONFIRM_HOME"
	StateReminderConsent  = "STATE_REMINDER_CONSENT"
	StateReminderIdentity = "STATE_REMINDER_IDENTITY"
	StateReminder         = "STATE_REMINDER"
	StateFinal            = "STATE_FINAL"
	StateTransfer         = "STATE_TRANSFER"
	StateDone             = "STATE_DONE"
)

type StepResult struct {
	BotSpeech        string `json:"bot_speech"`
	NextState        string `json:"state"`
	ShouldHangup     bool   `json:"should_hangup"`
	TransferTarget   string `json:"transfer_target,omitempty"`
	LLMUsed          bool   `json:"llm_used"`
	Outcome          string `json:"outcome,omitempty"`
	EscalationQueued bool   `json:"escalation_queued,omitempty"`
	ReasonCode       string `json:"reason_code,omitempty"`
	keepPrompt       bool
}

type EscalateFunc func(context.Context, int, string) error
type SaveContactFunc func(context.Context, int, string, bool) error
type FSMController struct {
	slotMgr      *slotmgr.SlotManager
	misClient    misclient.MISClient
	llmClient    *llmclient.UniversalLLMClient
	gate         *safety.FastGate
	escalate     EscalateFunc
	saveContact  SaveContactFunc
	patientLocks patientLocks
}

func NewFSMController(sm *slotmgr.SlotManager, mc misclient.MISClient) *FSMController {
	return &FSMController{slotMgr: sm, misClient: mc, gate: safety.NewFastGate()}
}
func (f *FSMController) SetLLMClient(c *llmclient.UniversalLLMClient) { f.llmClient = c }
func (f *FSMController) SetEscalator(fn EscalateFunc)                 { f.escalate = fn }
func (f *FSMController) SetContactSaver(fn SaveContactFunc)           { f.saveContact = fn }
func (f *FSMController) InitialGreeting() StepResult {
	return say(StateMenu, "Здравствуйте! Регистратура поликлиники номер семь, виртуальный помощник. Чем могу помочь?")
}
func say(state, speech string) StepResult { return StepResult{NextState: state, BotSpeech: speech} }

func (f *FSMController) transfer(ctx context.Context, s *models.CallSession, target, reason, speech string) StepResult {
	r := StepResult{NextState: StateTransfer, BotSpeech: speech, TransferTarget: target, Outcome: "TRANSFERRED"}
	r.ReasonCode, _, _ = strings.Cut(reason, ":")
	s.TransferTarget = target
	if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
		r.Outcome = "TRANSFER_STORAGE_ERROR"
	}
	if f.escalate != nil {
		id := 0
		if s.Patient != nil {
			id = s.Patient.ID
		}
		contextText := reason + "; сценарий: " + string(s.CurrentScenario) + "; специальность: " + s.Specialty
		if s.CallerNumber != "" {
			contextText += "; контакт (ANI): " + s.CallerNumber
		}
		if err := f.escalate(ctx, id, contextText); err == nil {
			r.EscalationQueued = true
		} else {
			r.Outcome = "ESCALATION_STORAGE_ERROR"
			r.BotSpeech += " Не удалось сохранить заявку. Сообщите сотруднику цель обращения."
		}
	}
	return r
}
func (f *FSMController) systemFailure(ctx context.Context, s *models.CallSession) StepResult {
	return f.transfer(ctx, s, "101", "R-06: ошибка обращения к МИС или локальному хранилищу", "Система временно недоступна. Соединяю с регистратором для проверки результата.")
}

// ProcessInput is the sole owner of state transitions. Callers serialize turns per call.
func (f *FSMController) ProcessInput(ctx context.Context, s *models.CallSession, input string) (result StepResult) {
	understanding := &nluTurn{}
	ctx = context.WithValue(ctx, nluContextKey{}, understanding)
	defer func() {
		s.CurrentState = result.NextState
		if !result.keepPrompt && result.BotSpeech != "" {
			s.PendingPrompt = result.BotSpeech
		}
		s.LastActivityAt = time.Now()
		result.LLMUsed = understanding.used
		s.NLUAttempted = understanding.attempted
		s.NLUIntent = understanding.intent
		s.NLULatencyMS = understanding.latency
		s.NLUError = understanding.failure
	}()
	text := strings.TrimSpace(input)
	norm := normalized(text)
	if s.CurrentState == StateDone {
		return StepResult{NextState: StateDone, ShouldHangup: true}
	}
	if gate := f.gate.Check(text); gate.Level != models.FlagNone {
		target := gate.Extension()
		if s.CurrentState == StateTransfer && s.TransferTarget == target {
			return StepResult{NextState: StateTransfer, TransferTarget: target, BotSpeech: gate.BotResponse}
		}
		trigger := []rune(gate.MatchedText)
		if len(trigger) > 200 {
			trigger = trigger[:200]
		}
		return f.transfer(ctx, s, target, gate.ActionCode+": "+string(gate.Level)+"; триггер: "+string(trigger), gate.BotResponse)
	}
	if s.CurrentState == StateTransfer {
		return StepResult{NextState: StateTransfer, TransferTarget: s.TransferTarget, BotSpeech: "Соединяю с сотрудником. Оставайтесь на линии."}
	}

	if text == "" {
		s.FailCount++
		if s.FailCount >= 2 {
			_ = f.slotMgr.Release(ctx, s.CallID)
			return StepResult{NextState: StateDone, BotSpeech: "Вас не слышно. Перезвоните, пожалуйста.", ShouldHangup: true, Outcome: "SILENCE"}
		}
		return StepResult{NextState: s.CurrentState, BotSpeech: "Вы меня слышите?", keepPrompt: true}
	}
	if containsAny(norm, "не записывайте разговор", "против записи разговора", "без записи разговора", "отключите запись") {
		return f.aside(s, "Аудиозапись не сохраняется. Продолжим.")
	}
	if containsAny(norm, "вы робот", "ты робот", "вы человек") {
		return f.aside(s, "Да, я виртуальный помощник регистратуры. Могу соединить с сотрудником.")
	}
	if containsAny(norm, "до свидания", "всего доброго") || norm == "пока" {
		if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
			return f.systemFailure(ctx, s)
		}
		return StepResult{NextState: StateDone, BotSpeech: "Всего доброго, будьте здоровы!", ShouldHangup: true, Outcome: "COMPLETED"}
	}
	if result, handled := f.conversation(ctx, s, text); handled {
		return result
	}
	if isMedicalQuestion(norm) {
		return f.primaryCareOffer(ctx, s, text, false)
	}
	intent := ParseIntent(text)
	newSubject := familyRequest(text) && (intent == IntentBooking || intent == IntentHomeVisit)
	backToSelf := s.ThirdParty && (word(text, "меня") || containsAny(norm, "моя запись", "мою запись", "когда я запис")) && (intent == IntentBooking || intent == IntentCancel || intent == IntentReschedule || intent == IntentCheckStatus)
	if newSubject || backToSelf {
		if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
			return f.systemFailure(ctx, s)
		}
		resetIdentity(s)
		s.ThirdParty = newSubject
	}
	if familyRequest(text) || containsAny(norm, "мою жену", "моего мужа", "моя жена", "мой муж", "моя мама", "мама записана", "на когда записана", "на когда записан", "за другого", "свою дочь", "своего сына") {
		s.ThirdParty = true
	}
	if s.ThirdParty && !s.Guardian && (s.CurrentScenario == models.ScenarioCheckStatus || s.CurrentScenario == models.ScenarioCancel || s.CurrentScenario == models.ScenarioReschedule || ParseIntent(text) == IntentCheckStatus || ParseIntent(text) == IntentCancel || ParseIntent(text) == IntentReschedule) {
		return f.transfer(ctx, s, "101", "R-07: сведения о другом пациенте, полномочия не проверены", "Сведения о чужих записях сообщает сотрудник после проверки полномочий. Соединяю с регистратором.")
	}
	identityContinuation := s.CurrentState == StateIdentity && intent == IntentBooking && ExtractBirthDate(text) != "" && f.extractSpecialty(text) == "" && f.extractDoctor(text) == nil
	if canSwitchTask(s.CurrentState) && isTaskIntent(intent) && !identityContinuation {
		return f.menu(ctx, s, text)
	}
	switch s.CurrentState {
	case "", StateMenu, StateFinal:
		return f.menu(ctx, s, text)
	case StateDirectory:
		_, faq := FAQ(text)
		if containsAny(norm, "дальше", "продолж", "остальные", "еще") && !faq && !isTaskIntent(intent) && f.extractSpecialty(text) == "" && f.extractDoctor(text) == nil {
			return f.directory(s, s.DirectoryFilter, true)
		}
		return f.menu(ctx, s, text)
	case StateIdentity:
		return f.identity(ctx, s, text)
	case StateGuardian:
		if Confirmation(text) == IntentDeny {
			return f.transfer(ctx, s, "101", "C-12: требуется законный представитель ребёнка", "Запись ребёнка оформляет законный представитель. Соединяю с регистратором.")
		}
		if Confirmation(text) != IntentAffirm {
			return f.retry(ctx, s, "Вы законный представитель ребёнка? Скажите да или нет.")
		}
		s.Guardian = true
		s.FailCount = 0
		return f.identity(ctx, s, "")
	case StateSpecialty:
		return f.specialty(ctx, s, text)
	case StateSchedule:
		switch ParseIntent(text) {
		case IntentBooking, IntentCancel, IntentReschedule, IntentCheckStatus, IntentHomeVisit:
			return f.menu(ctx, s, text)
		default:
			return f.schedule(ctx, s, text)
		}
	case StateReferral:
		if containsAny(norm, "есть направление", "уже направление", "бумажное направление") {
			return f.transfer(ctx, s, "101", "C-02: требуется проверка направления", "Соединяю с регистратором для проверки направления.")
		}
		if Confirmation(text) == IntentAffirm {
			s.Specialty = "терапевт"
			s.RequestedDoctorID = 0
			s.CurrentScenario = models.ScenarioBookingDirect
			s.FailCount = 0
			if s.Identified {
				return f.offer(ctx, s)
			}
			return say(StateIdentity, "Назовите фамилию, имя и дату рождения пациента.")
		}
		if Confirmation(text) == IntentDeny {
			return f.transfer(ctx, s, "101", "C-02: требуется проверка направления", "Соединяю с регистратором для проверки направления.")
		}
		return f.retry(ctx, s, "Записать вас к терапевту для получения направления?")
	case StateOfferBooking:
		if Confirmation(text) == IntentDeny {
			return say(StateFinal, "Хорошо. Могу ещё чем-то помочь?")
		}
		if Confirmation(text) != IntentAffirm {
			return f.retry(ctx, s, "Записать вас на приём? Скажите да или нет.")
		}
		s.FailCount = 0
		if s.Identified {
			return f.afterIdentity(ctx, s)
		}
		return say(StateIdentity, "Назовите фамилию, имя и дату рождения пациента.")
	case StateChooseSlot:
		return f.chooseSlot(ctx, s, text)
	case StateConfirmBooking:
		return f.confirmBooking(ctx, s, text)
	case StateChooseVisit:
		return f.chooseVisit(ctx, s, text)
	case StateConfirmCancel:
		return f.confirmCancel(ctx, s, text)
	case StateHome:
		if len([]rune(text)) < 12 || len([]rune(text)) > 700 {
			return f.retry(ctx, s, "Назовите полный адрес и причину вызова своими словами.")
		}
		s.HomeRequest = text
		s.FailCount = 0
		return say(StateConfirmHome, "Передать адрес и причину вызова регистратору? Решение о визите принимает медицинский сотрудник.")
	case StateConfirmHome:
		if Confirmation(text) == IntentDeny {
			s.HomeRequest = ""
			return say(StateHome, "Назовите исправленный адрес и причину вызова.")
		}
		if Confirmation(text) != IntentAffirm {
			return f.retry(ctx, s, "Передать заявку регистратору? Скажите да или нет.")
		}
		if f.escalate == nil {
			return f.systemFailure(ctx, s)
		}
		if err := f.escalate(ctx, s.Patient.ID, "C-06: вызов врача на дом. Адрес и причина: "+s.HomeRequest); err != nil {
			return f.systemFailure(ctx, s)
		}
		return StepResult{NextState: StateFinal, BotSpeech: "Заявка сохранена для отправки регистратору. Медицинский сотрудник подтвердит возможность визита.", EscalationQueued: true, Outcome: "HOME_REQUESTED"}
	case StateReminderConsent:
		answer := Confirmation(text)
		if answer == IntentUnknown {
			return f.retry(ctx, s, "Можно напомнить о приёме звонком на этот номер? Скажите да или нет.")
		}
		if f.saveContact == nil || s.Patient == nil {
			return f.systemFailure(ctx, s)
		}
		if err := f.saveContact(ctx, s.Patient.ID, s.CallerNumber, answer == IntentAffirm); err != nil {
			return f.systemFailure(ctx, s)
		}
		s.FailCount = 0
		return say(StateFinal, "Предпочтение сохранено. Могу ещё помочь?")
	case StateReminderIdentity:
		if Confirmation(text) == IntentDeny || containsAny(norm, "другой человек", "ошиблись", "не я") {
			return StepResult{NextState: StateDone, BotSpeech: "Попросите пациента связаться с регистратурой поликлиники номер семь. Всего доброго!", ShouldHangup: true, Outcome: "WRONG_PERSON"}
		}
		if Confirmation(text) != IntentAffirm {
			return f.retry(ctx, s, "Это вы? Скажите да или нет.")
		}
		s.Identified = true
		s.FailCount = 0
		v := s.ActiveVisits[0]
		return say(StateReminder, fmt.Sprintf("Напоминаем о записи: %s, %s. Сможете подойти?", v.Doctor.FullName, v.ScheduledAt))
	case StateReminder:
		return f.reminder(ctx, s, text)
	default:
		return f.systemFailure(ctx, s)
	}
}

func (f *FSMController) retry(ctx context.Context, s *models.CallSession, prompt string) StepResult {
	s.FailCount++
	if s.FailCount >= 3 {
		return f.transfer(ctx, s, "101", "R-05: три непонимания подряд", "Соединяю с сотрудником, он поможет с вашим вопросом.")
	}
	if s.FailCount == 2 {
		prompt += " Если удобнее, скажите «оператор»."
	}
	state := s.CurrentState
	if state == "" {
		state = StateMenu
	}
	return StepResult{NextState: state, BotSpeech: prompt, keepPrompt: s.PendingPrompt != ""}
}
