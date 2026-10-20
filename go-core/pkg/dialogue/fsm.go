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
	StateLabStatus        = "STATE_LAB_STATUS"
	StateFinal            = "STATE_FINAL"
	StateTransfer         = "STATE_TRANSFER"
	StateDone             = "STATE_DONE"
)

type StepResult struct {
	BotSpeech        string  `json:"bot_speech"`
	NextState        string  `json:"state"`
	ShouldHangup     bool    `json:"should_hangup"`
	TransferTarget   string  `json:"transfer_target,omitempty"`
	LLMUsed          bool    `json:"llm_used"`
	Outcome          string  `json:"outcome,omitempty"`
	EscalationQueued bool    `json:"escalation_queued,omitempty"`
	ReasonCode       string  `json:"reason_code,omitempty"`
	DiagnosticCode   string  `json:"-"`
	Source           string  `json:"source,omitempty"`
	Confidence       float64 `json:"confidence,omitempty"`
	Intent           string  `json:"intent,omitempty"`
	Explanation      string  `json:"explanation,omitempty"`
	keepPrompt       bool
}

type EscalateFunc func(context.Context, int, string) error
type SaveContactFunc func(context.Context, int, string, bool) error
type FSMController struct {
	slotMgr        *slotmgr.SlotManager
	misClient      misclient.MISClient
	llmClient      *llmclient.UniversalLLMClient
	decisionClient *llmclient.DecisionClient
	gate           *safety.FastGate
	escalate       EscalateFunc
	saveContact    SaveContactFunc
	patientLocks   patientLocks
	holidays       map[string]bool
}

func NewFSMController(sm *slotmgr.SlotManager, mc misclient.MISClient) *FSMController {
	return &FSMController{slotMgr: sm, misClient: mc, gate: safety.NewFastGate()}
}
func (f *FSMController) SetLLMClient(c *llmclient.UniversalLLMClient) { f.llmClient = c }
func (f *FSMController) SetDecisionClient(c *llmclient.DecisionClient) {
	f.decisionClient = c
}
func (f *FSMController) Gate() *safety.FastGate             { return f.gate }
func (f *FSMController) SetEscalator(fn EscalateFunc)       { f.escalate = fn }
func (f *FSMController) SetContactSaver(fn SaveContactFunc) { f.saveContact = fn }
func (f *FSMController) SetHolidays(holidays map[string]bool) {
	f.holidays = holidays
}
func (f *FSMController) InitialGreeting() StepResult {
	return StepResult{
		NextState:   StateMenu,
		BotSpeech:   "Здравствуйте! Регистратура поликлиники номер семь, виртуальный помощник. Чем могу помочь?",
		Source:      "rule_fsm",
		Confidence:  1.0,
		Intent:      "GREETING",
		Explanation: "Начальное приветствие регистратуры",
	}
}
func say(state, speech string) StepResult {
	return StepResult{
		NextState:   state,
		BotSpeech:   speech,
		Source:      "rule_fsm",
		Confidence:  1.0,
		Explanation: fmt.Sprintf("Диалоговый переход в состояние %s", state),
	}
}

func (f *FSMController) transfer(ctx context.Context, s *models.CallSession, target, reason, speech string) StepResult {
	r := StepResult{
		NextState:      StateTransfer,
		BotSpeech:      speech,
		TransferTarget: target,
		Outcome:        "TRANSFERRED",
		Source:         "safety_gate",
		Confidence:     1.0,
		Explanation:    "Перевод вызова на оператора: " + reason,
	}
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
		actionCode := r.ReasonCode
		if actionCode == "" {
			actionCode, _, _ = strings.Cut(reason, ":")
		}
		contextText := actionCode
		if s.CallID != "" {
			contextText += "; звонок: " + s.CallID
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

type recognitionUncertainKey struct{}

// ProcessRecognizedInput keeps ASR quality separate from NLU confidence.
// The safety gate still receives the original utterance before any retry.
func (f *FSMController) ProcessRecognizedInput(ctx context.Context, s *models.CallSession, input string, uncertain bool) StepResult {
	return f.ProcessInput(context.WithValue(ctx, recognitionUncertainKey{}, uncertain), s, input)
}

// ProcessInput is the sole owner of state transitions. Callers serialize turns per call.
func (f *FSMController) ProcessInput(ctx context.Context, s *models.CallSession, input string) (result StepResult) {
	understanding := &nluTurn{}
	ctx = context.WithValue(ctx, nluContextKey{}, understanding)
	defer func() {
		if s.CurrentState != result.NextState {
			s.SoftClarified = false
		}
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

		// Guarantee non-empty BotSpeech for caller to avoid dead air
		if result.BotSpeech == "" && !result.ShouldHangup {
			if s.PendingPrompt != "" {
				result.BotSpeech = s.PendingPrompt
			} else {
				result.BotSpeech = "Чем могу помочь?"
			}
		}

		// Ensure response metadata fields are always populated
		if result.Source == "" {
			if result.LLMUsed {
				result.Source = "nlu"
			} else {
				result.Source = "rule_fsm"
			}
		}
		if result.Confidence == 0 {
			result.Confidence = 1.0
		}
		if result.Intent == "" {
			if s.NLUIntent != "" {
				result.Intent = s.NLUIntent
			} else {
				parsed := ParseIntent(input)
				if parsed != "" && parsed != IntentUnknown {
					result.Intent = string(parsed)
				} else {
					result.Intent = "UNKNOWN"
				}
			}
		}
		if result.Explanation == "" {
			if result.TransferTarget != "" {
				result.Explanation = fmt.Sprintf("Перевод на оператора %s (причина: %s)", result.TransferTarget, result.ReasonCode)
			} else if result.Outcome != "" {
				result.Explanation = fmt.Sprintf("Завершение шага со статусом %s", result.Outcome)
			} else {
				result.Explanation = fmt.Sprintf("Переход в состояние %s (сценарий: %s)", result.NextState, s.CurrentScenario)
			}
		}
	}()
	text := strings.TrimSpace(input)
	norm := normalized(text)
	if s.CurrentState == StateDone {
		return StepResult{NextState: StateDone, ShouldHangup: true}
	}
	if gate := f.gate.CheckWithContext(ctx, text); gate.Level != models.FlagNone {
		target := gate.Extension()
		if s.CurrentState == StateTransfer && s.TransferTarget == target {
			return StepResult{NextState: StateTransfer, TransferTarget: target, BotSpeech: gate.BotResponse}
		}
		return f.transfer(ctx, s, target, gate.ActionCode+": "+string(gate.Level), gate.BotResponse)
	}
	if s.CurrentState == StateTransfer {
		return StepResult{NextState: StateTransfer, TransferTarget: s.TransferTarget, BotSpeech: "Соединяю с сотрудником. Оставайтесь на линии."}
	}

	if uncertain, _ := ctx.Value(recognitionUncertainKey{}).(bool); uncertain && text != "" {
		result := f.retry(ctx, s, "Не уверен, что расслышал. Повторите, пожалуйста.")
		result.DiagnosticCode = "asr_uncertain"
		if result.TransferTarget == "" {
			result.Source = "asr_quality"
			result.Explanation = "Локальный ASR запросил повтор реплики; действие не выполнено"
		}
		return result
	}

	if text == "" {
		s.FailCount++
		if s.FailCount >= 2 {
			_ = f.slotMgr.Release(ctx, s.CallID)
			return StepResult{NextState: StateDone, BotSpeech: "Вас не слышно. Перезвоните, пожалуйста.", ShouldHangup: true, Outcome: "SILENCE"}
		}
		return StepResult{NextState: s.CurrentState, BotSpeech: "Вы меня слышите?", keepPrompt: true}
	}
	if recordingObjection(norm) {
		return f.aside(s, "Хорошо, запись разговора отключена, аудио не сохраняется. Продолжим.")
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
	if thirdPartyNamedVisit(text) || (thirdPartyRecordRequest(text) && (recordRequest(ParseIntent(text)) || containsAny(norm, "когда у ", "во сколько у ", "на когда у "))) {
		s.ThirdParty = true
		return f.transfer(ctx, s, "101", "R-07: сведения о другом пациенте, полномочия не проверены", "Сведения о чужих записях сообщает сотрудник после проверки полномочий. Соединяю с регистратором.")
	}
	if personalLabStatus(norm) || (s.CurrentState == StateLabStatus && labDetail(text)) {
		return f.labStatus(ctx, s, text)
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
	if familyRequest(text) || containsAny(norm, "мою жену", "моего мужа", "моя жена", "мой муж", "моя мама", "мама записана", "за другого", "свою дочь", "своего сына") {
		s.ThirdParty = true
	}
	if s.ThirdParty && !s.Guardian && (s.CurrentScenario == models.ScenarioCheckStatus || s.CurrentScenario == models.ScenarioCancel || s.CurrentScenario == models.ScenarioReschedule || ParseIntent(text) == IntentCheckStatus || ParseIntent(text) == IntentCancel || ParseIntent(text) == IntentReschedule) {
		return f.transfer(ctx, s, "101", "R-07: сведения о другом пациенте, полномочия не проверены", "Сведения о чужих записях сообщает сотрудник после проверки полномочий. Соединяю с регистратором.")
	}
	identityContinuation := s.CurrentState == StateIdentity && intent == IntentBooking && ExtractBirthDate(text) != "" && f.extractSpecialty(text) == "" && f.extractDoctor(text) == nil
	slotChoiceInFlight := s.CurrentState == StateChooseSlot && (intent == IntentBooking || (s.CurrentScenario == models.ScenarioReschedule && intent == IntentReschedule) || containsAny(norm, "вариант", "попозже", "пораньше", "перв", "втор"))
	if canSwitchTask(s.CurrentState) && isTaskIntent(intent) && !identityContinuation && !slotChoiceInFlight {
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
		isGuardianAffirm := Confirmation(text) == IntentAffirm || (containsAny(norm, "мама", "папа", "родитель", "представитель", "опекун", "мать", "отец") && !word(norm, "не"))
		if !isGuardianAffirm {
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
		if answeringMachine(norm) {
			return StepResult{NextState: StateDone, BotSpeech: "Попросите пациента перезвонить в регистратуру поликлиники номер семь. Всего доброго!", ShouldHangup: true, Outcome: "WRONG_PERSON"}
		}
		if Confirmation(text) == IntentDeny || containsAny(norm, "другой человек", "ошиблись", "не я") {
			return StepResult{NextState: StateDone, BotSpeech: "Попросите пациента связаться с регистратурой поликлиники номер семь. Всего доброго!", ShouldHangup: true, Outcome: "WRONG_PERSON"}
		}
		isIdentityAffirm := Confirmation(text) == IntentAffirm || containsAny(norm, "это я", "слушаю", "у аппарата", "так точно") || word(norm, "я")
		if !isIdentityAffirm {
			return f.retry(ctx, s, "Это вы? Скажите да или нет.")
		}
		s.Identified = true
		s.FailCount = 0
		v := s.ActiveVisits[0]
		return say(StateReminder, fmt.Sprintf("Напоминаем о записи: %s, %s. Сможете подойти?", v.Doctor.FullName, v.ScheduledAt))
	case StateReminder:
		return f.reminder(ctx, s, text)
	case StateLabStatus:
		return f.labStatus(ctx, s, text)
	default:
		return f.systemFailure(ctx, s)
	}
}

func recordingObjection(s string) bool {
	if containsAny(s, "против записи", "не хочу записи") && !containsAny(s, "к врачу", "на прием", "на приём", "к доктору", "на консультацию") {
		return true
	}
	if containsAny(s, "против того чтобы записывали", "без записи разговора", "без аудиозаписи", "отключите запись", "отключите аудиозапись", "не сохраняйте аудио", "не сохраняйте разговор", "не надо записывать разговор", "не нужно записывать разговор", "не записывайте разговор", "не записывайте наш разговор", "не записывайте звонок", "не записывайте аудио") {
		return true
	}
	return containsAny(s, "не записывайте", "не записывать", "не надо записывать", "не нужно записывать") && containsAny(s, "разговор", "аудио", "звонок")
}

func answeringMachine(text string) bool {
	return containsAny(text, "оставьте сообщение", "оставить сообщение", "после сигнала", "после звукового сигнала", "голосовая почта", "это автоответчик", "абонент недоступен", "абонент временно недоступен")
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
