package dialogue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

type StepResult struct {
	BotSpeech      string
	NextState      string
	ShouldHangup   bool
	TransferTarget string // "101", "102", "103", "104", "105"
	LLMUsed        bool   // true если ответ или извлечение слотов выполнялись через LLM
}

type FSMController struct {
	slotMgr   *slotmgr.SlotManager
	misClient misclient.MISClient
	llmClient *llmclient.UniversalLLMClient
}

func NewFSMController(sm *slotmgr.SlotManager, mc misclient.MISClient) *FSMController {
	return &FSMController{
		slotMgr:   sm,
		misClient: mc,
	}
}

func (f *FSMController) SetLLMClient(llm *llmclient.UniversalLLMClient) {
	f.llmClient = llm
}

// InitialGreeting возвращает стандартное приветствие по регламенту п. 3.3
func (f *FSMController) InitialGreeting() StepResult {
	return StepResult{
		BotSpeech: "Здравствуйте! Регистратура поликлиники номер семь, виртуальный помощник. Разговор записывается. Чем могу помочь?",
		NextState: "STATE_MAIN_MENU",
	}
}

// ProcessInput главный шаг конечного автомата
func (f *FSMController) ProcessInput(ctx context.Context, session *models.CallSession, userInput string) StepResult {
	userInput = strings.TrimSpace(userInput)

	// Обработка тишины
	if userInput == "" {
		session.FailCount++
		if session.FailCount >= 3 {
			return StepResult{
				BotSpeech:      "Вас не слышно. Соединяю с сотрудником регистратуры.",
				NextState:      "STATE_TRANSFER",
				TransferTarget: "101",
			}
		}
		return StepResult{
			BotSpeech: "Вы меня слышите? Чем я могу помочь?",
			NextState: session.CurrentState,
		}
	}

	// Сброс счётчика непониманий при наличии ответа
	defer func() {
		if session.FailCount > 0 {
			session.FailCount = 0
		}
	}()

	switch session.CurrentState {
	case "STATE_MAIN_MENU", "":
		return f.handleMainMenu(ctx, session, userInput)

	case "STATE_AWAIT_IDENTITY":
		return f.handleAwaitIdentity(ctx, session, userInput)

	case "STATE_AWAIT_SPECIALTY":
		return f.handleAwaitSpecialty(ctx, session, userInput)

	case "STATE_CHOOSE_SLOT":
		return f.handleChooseSlot(ctx, session, userInput)

	case "STATE_CONFIRM_BOOKING":
		return f.handleConfirmBooking(ctx, session, userInput)

	case "STATE_CONFIRM_CANCEL":
		return f.handleConfirmCancel(ctx, session, userInput)

	case "STATE_FINAL":
		intent := ParseIntent(userInput)
		if intent == IntentDeny || containsAny(strings.ToLower(userInput), "нет", "спасибо", "всего доброго", "до свидания") {
			return StepResult{
				BotSpeech:    "Всего доброго, будьте здоровы!",
				NextState:    "STATE_DONE",
				ShouldHangup: true,
			}
		}
		// Новый вопрос пациента
		session.CurrentState = "STATE_MAIN_MENU"
		return f.handleMainMenu(ctx, session, userInput)

	default:
		return StepResult{
			BotSpeech: "Чем могу помочь? Записать к врачу, перенести запись или подсказать график работы?",
			NextState: "STATE_MAIN_MENU",
		}
	}
}

func (f *FSMController) handleMainMenu(ctx context.Context, session *models.CallSession, text string) StepResult {
	intent := ParseIntent(text)

	// Если intent неизвестен или неполный, пробуем LLM NLU (с таймаутом 1.5 сек)
	if intent == IntentUnknown && f.llmClient != nil && f.llmClient.IsEnabled() {
		llmCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		nlu, err := f.llmClient.ExtractSlots(llmCtx, text, session.CurrentState)
		cancel()
		if err == nil && nlu != nil {
			if nlu.OperatorRequested {
				return StepResult{
					BotSpeech:      "Перевожу вас на сотрудника регистратуры. Пожалуйста, оставайтесь на линии.",
					NextState:      "STATE_TRANSFER",
					TransferTarget: "101",
				}
			}
			if nlu.IsEmergency {
				return StepResult{
					BotSpeech:      "При экстренном состоянии срочно звоните 103 или 112. Перевожу в кабинет неотложной помощи.",
					NextState:      "STATE_TRANSFER",
					TransferTarget: "103",
				}
			}
			if nlu.Specialty != "" {
				session.Slots["specialty"] = nlu.Specialty
			}
			if nlu.LastName != "" {
				session.Slots["last_name"] = nlu.LastName
			}
			if nlu.FirstName != "" {
				session.Slots["first_name"] = nlu.FirstName
			}
			if nlu.BirthDate != "" {
				session.Slots["birth_date"] = nlu.BirthDate
			}

			switch nlu.Intent {
			case "BOOKING":
				intent = IntentBooking
			case "CANCEL":
				intent = IntentCancel
			case "RESCHEDULE":
				intent = IntentReschedule
			case "CHECK_STATUS":
				intent = IntentCheckStatus
			case "SCHEDULE_INFO":
				// Если вопрос содержит конкретику (кровь, анализ, процедурный и т.д.), вызываем AnswerFAQ через LLM
				if f.llmClient != nil && f.llmClient.IsEnabled() && containsAny(strings.ToLower(text), "кров", "анализ", "процедурн", "укол", "прививк", "врач", "когда") {
					faqCtx, cancelFaq := context.WithTimeout(ctx, 2*time.Second)
					ans, err := f.llmClient.AnswerFAQ(faqCtx, text)
					cancelFaq()
					if err == nil && ans != "" {
						session.CurrentScenario = models.ScenarioScheduleInfo
						session.CurrentState = "STATE_FINAL"
						return StepResult{
							BotSpeech: ans + " Могу ещё чем-то помочь?",
							NextState: "STATE_FINAL",
							LLMUsed:   true,
						}
					}
				}
				intent = IntentSchedule
			}
		}
	}

	switch intent {
	case IntentSchedule:
		// Если это специфический вопрос про забор крови или анализы
		if f.llmClient != nil && f.llmClient.IsEnabled() && containsAny(strings.ToLower(text), "кров", "анализ", "процедурн") {
			faqCtx, cancelFaq := context.WithTimeout(ctx, 3500*time.Millisecond)
			ans, err := f.llmClient.AnswerFAQ(faqCtx, text)
			cancelFaq()
			if err == nil && ans != "" {
				session.CurrentScenario = models.ScenarioScheduleInfo
				session.CurrentState = "STATE_FINAL"
				return StepResult{
					BotSpeech: ans + " Могу ещё чем-то помочь?",
					NextState: "STATE_FINAL",
					LLMUsed:   true,
				}
			}
		}

		// Сценарий С-07: График работы поликлиники по умолчанию
		session.CurrentScenario = models.ScenarioScheduleInfo
		session.CurrentState = "STATE_FINAL"
		return StepResult{
			BotSpeech: "Поликлиника работает по будням с семи тридцати до двадцати, в субботу с девяти до пятнадцати. Могу ещё чем-то помочь?",
			NextState: "STATE_FINAL",
		}

	case IntentBooking:
		session.CurrentScenario = models.ScenarioBookingDirect
		spec := ExtractSpecialty(text)
		if spec == "" {
			if s, ok := session.Slots["specialty"].(string); ok && s != "" {
				spec = s
			}
		} else {
			session.Slots["specialty"] = spec
		}

		// Если специальность названа, но требует направления (С-02)
		if spec != "" && !f.slotMgr.IsDirectBookingAllowed(spec) {
			session.CurrentScenario = models.ScenarioBookingReferral
			session.CurrentState = "STATE_FINAL"
			return StepResult{
				BotSpeech: fmt.Sprintf("К %s запись возможна по направлению терапевта. Записать вас к терапевту для получения направления?", spec),
				NextState: "STATE_AWAIT_SPECIALTY",
			}
		}

		// Проверяем, возможно пациент сразу назвал свои данные
		bDate := ExtractBirthDate(text)
		lName, fName, _ := ExtractFIO(text)
		if bDate != "" && lName != "" && fName != "" {
			session.CurrentState = "STATE_AWAIT_IDENTITY"
			return f.handleAwaitIdentity(ctx, session, text)
		}

		// По регламенту идентификация: Фамилия, имя, отчество и ДР
		session.CurrentState = "STATE_AWAIT_IDENTITY"
		return StepResult{
			BotSpeech: "Хорошо. Назовите, пожалуйста, фамилию, имя, отчество и дату рождения.",
			NextState: "STATE_AWAIT_IDENTITY",
		}

	case IntentCancel, IntentReschedule, IntentCheckStatus:
		session.CurrentScenario = models.ScenarioCheckStatus
		if intent == IntentCancel {
			session.CurrentScenario = models.ScenarioCancel
		} else if intent == IntentReschedule {
			session.CurrentScenario = models.ScenarioReschedule
		}
		session.CurrentState = "STATE_AWAIT_IDENTITY"
		return StepResult{
			BotSpeech: "Чтобы найти вашу запись, назовите фамилию, имя, отчество и дату рождения.",
			NextState: "STATE_AWAIT_IDENTITY",
		}

	default:
		spec := ExtractSpecialty(text)
		if spec == "" {
			if s, ok := session.Slots["specialty"].(string); ok && s != "" {
				spec = s
			}
		}
		if spec != "" {
			session.Slots["specialty"] = spec
			session.CurrentState = "STATE_AWAIT_IDENTITY"
			return StepResult{
				BotSpeech: fmt.Sprintf("Запись к врачу: %s. Назовите фамилию, имя и дату рождения.", spec),
				NextState: "STATE_AWAIT_IDENTITY",
			}
		}

		// Если LLM доступен, пробуем ответить на общий вопрос / FAQ
		if f.llmClient != nil && f.llmClient.IsEnabled() {
			faqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			ans, err := f.llmClient.AnswerFAQ(faqCtx, text)
			cancel()
			if err == nil && ans != "" {
				session.CurrentState = "STATE_FINAL"
				return StepResult{
					BotSpeech: ans + " Могу ещё чем-то помочь?",
					NextState: "STATE_FINAL",
				}
			}
		}

		session.FailCount++
		if session.FailCount >= 2 {
			return StepResult{
				BotSpeech:      "Соединяю с сотрудником регистратуры, он поможет сориентироваться.",
				NextState:      "STATE_TRANSFER",
				TransferTarget: "101",
			}
		}
		return StepResult{
			BotSpeech: "Я могу записать вас к врачу, перенести или отменить запись, либо подсказать режим работы. Что вас интересует?",
			NextState: "STATE_MAIN_MENU",
		}
	}
}

func (f *FSMController) handleAwaitIdentity(ctx context.Context, session *models.CallSession, text string) StepResult {
	norm := strings.ToLower(text)
	if containsAny(norm, "до свидания", "всего доброго", "пока", "спасибо, до свидания", "спасибо до свидания") ||
		(containsAny(norm, "спасибо", "отмена", "не надо", "не нужно") && !containsAny(norm, "запис", "врач", "терапевт")) {
		return StepResult{
			BotSpeech:    "Всего доброго, до свидания!",
			NextState:    "STATE_DONE",
			ShouldHangup: true,
		}
	}

	birthDate := ExtractBirthDate(text)
	last, first, middle := ExtractFIO(text)

	// Fallback на LLM для сложной живой речи
	if (birthDate == "" || last == "" || first == "") && f.llmClient != nil && f.llmClient.IsEnabled() {
		llmCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		nlu, err := f.llmClient.ExtractSlots(llmCtx, text, session.CurrentState)
		cancel()
		if err == nil && nlu != nil {
			if birthDate == "" && nlu.BirthDate != "" {
				birthDate = nlu.BirthDate
			}
			if last == "" && nlu.LastName != "" {
				last = nlu.LastName
			}
			if first == "" && nlu.FirstName != "" {
				first = nlu.FirstName
			}
			if middle == "" && nlu.MiddleName != "" {
				middle = nlu.MiddleName
			}
		}
	}

	if birthDate != "" {
		session.Slots["birth_date"] = birthDate
	}
	if last != "" {
		session.Slots["last_name"] = last
	}
	if first != "" {
		session.Slots["first_name"] = first
	}
	if middle != "" {
		session.Slots["middle_name"] = middle
	}

	// Проверяем, собраны ли обязательные поля
	lName, _ := session.Slots["last_name"].(string)
	fName, _ := session.Slots["first_name"].(string)
	bDate, _ := session.Slots["birth_date"].(string)

	if lName == "" || fName == "" || bDate == "" {
		return StepResult{
			BotSpeech: "Пожалуйста, уточните вашу фамилию, имя и дату рождения.",
			NextState: "STATE_AWAIT_IDENTITY",
		}
	}

	// Поиск или регистрация пациента в МИС
	clients, err := f.misClient.FindClients(ctx, lName, fName, bDate)
	if err == nil && len(clients) > 0 {
		session.Patient = &clients[0]
	} else {
		// Создаем карточку пациента
		c, err := f.misClient.CreateClient(ctx, models.ClientCreate{
			LastName:  lName,
			FirstName: fName,
			BirthDate: bDate,
		})
		if err == nil {
			session.Patient = c
		}
	}

	// Ветвление в зависимости от сценария
	switch session.CurrentScenario {
	case models.ScenarioCancel, models.ScenarioCheckStatus, models.ScenarioReschedule:
		return f.handleExistingVisitsLookup(ctx, session)

	default:
		// Проверяем выбранную специальность
		spec, _ := session.Slots["specialty"].(string)
		if spec == "" {
			session.CurrentState = "STATE_AWAIT_SPECIALTY"
			return StepResult{
				BotSpeech: fmt.Sprintf("Спасибо, %s. К какому врачу или специальности хотите записаться?", fName),
				NextState: "STATE_AWAIT_SPECIALTY",
			}
		}
		return f.presentAvailableSlots(session, spec)
	}
}

func (f *FSMController) handleAwaitSpecialty(ctx context.Context, session *models.CallSession, text string) StepResult {
	spec := ExtractSpecialty(text)
	if spec == "" {
		return StepResult{
			BotSpeech: "Назовите специальность врача, например: терапевт, хирург или окулист.",
			NextState: "STATE_AWAIT_SPECIALTY",
		}
	}

	session.Slots["specialty"] = spec
	if !f.slotMgr.IsDirectBookingAllowed(spec) {
		session.CurrentScenario = models.ScenarioBookingReferral
		session.CurrentState = "STATE_FINAL"
		return StepResult{
			BotSpeech: fmt.Sprintf("К %s запись ведётся по направлению терапевта. Записать вас к терапевту?", spec),
			NextState: "STATE_AWAIT_SPECIALTY",
		}
	}

	return f.presentAvailableSlots(session, spec)
}

func (f *FSMController) presentAvailableSlots(session *models.CallSession, specialty string) StepResult {
	slots := f.slotMgr.FindAvailableSlots(specialty, 0, 2)
	if len(slots) == 0 {
		// С-11: Лист ожидания при отсутствии слотов
		session.CurrentState = "STATE_FINAL"
		return StepResult{
			BotSpeech: "Свободного времени на ближайшие дни нет. Я могу записать вас в лист ожидания и позвонить, когда появится время.",
			NextState: "STATE_FINAL",
		}
	}

	// Запоминаем предложенные слоты
	session.Slots["slot_1"] = slots[0]
	speech := fmt.Sprintf("Ближайшее время: %s", FormatDateRussian(slots[0].StartTime))
	if len(slots) > 1 {
		session.Slots["slot_2"] = slots[1]
		speech += fmt.Sprintf(" или %s. Что удобнее?", FormatDateRussian(slots[1].StartTime))
	} else {
		speech += ". Записать вас?"
	}

	session.CurrentState = "STATE_CHOOSE_SLOT"
	return StepResult{
		BotSpeech: speech,
		NextState: "STATE_CHOOSE_SLOT",
	}
}

func (f *FSMController) handleChooseSlot(ctx context.Context, session *models.CallSession, text string) StepResult {
	norm := strings.ToLower(text)
	var chosen models.Slot

	slot1, ok1 := session.Slots["slot_1"].(models.Slot)
	slot2, ok2 := session.Slots["slot_2"].(models.Slot)

	if ok2 && (containsAny(norm, "втор", "позже", "другое", "второй") || strings.Contains(norm, slot2.StartTime.Format("15:04"))) {
		chosen = slot2
	} else if ok1 {
		chosen = slot1
	} else {
		return StepResult{
			BotSpeech: "Пожалуйста, выберите первый или второй вариант времени.",
			NextState: "STATE_CHOOSE_SLOT",
		}
	}

	// Ставим распределённую блокировку на выбранный слот на 2 минуты
	holdKey, err := f.slotMgr.HoldSlot(chosen.DoctorID, chosen.StartTime, session.CallID, 2*time.Minute)
	if err != nil {
		// Слот перехватили
		spec, _ := session.Slots["specialty"].(string)
		return f.presentAvailableSlots(session, spec)
	}

	session.SelectedSlot = &chosen
	session.SlotHeldKey = holdKey
	session.CurrentState = "STATE_CONFIRM_BOOKING"

	return StepResult{
		BotSpeech: fmt.Sprintf("Записываю: %s %s, %s, кабинет %s. Верно?", chosen.Specialty, chosen.Doctor, FormatDateRussian(chosen.StartTime), chosen.Room),
		NextState: "STATE_CONFIRM_BOOKING",
	}
}

func (f *FSMController) handleConfirmBooking(ctx context.Context, session *models.CallSession, text string) StepResult {
	intent := ParseIntent(text)

	if intent == IntentDeny || containsAny(strings.ToLower(text), "нет", "неверно", "отмена") {
		// Освобождаем блокировку
		if session.SlotHeldKey != "" {
			f.slotMgr.ReleaseSlot(session.SlotHeldKey, session.CallID)
			session.SlotHeldKey = ""
		}
		spec, _ := session.Slots["specialty"].(string)
		return f.presentAvailableSlots(session, spec)
	}

	// Подтверждение! Совершаем действие в МИС
	if session.SelectedSlot == nil || session.Patient == nil {
		return StepResult{
			BotSpeech: "Произошла ошибка сессии. Соединяю с регистратором.",
			NextState: "STATE_TRANSFER",
			TransferTarget: "101",
		}
	}

	// 1. Фиксируем слот в локальном менеджере
	f.slotMgr.CommitBooking(session.SelectedSlot.DoctorID, session.SelectedSlot.StartTime)

	// 2. Создаем запись в МИС через Rate-Limiter
	timeStr := session.SelectedSlot.StartTime.Format("02.01.2006 15:04")
	_, err := f.misClient.CreateVisit(ctx, models.VisitCreate{
		ClientID:    session.Patient.ID,
		DoctorID:    session.SelectedSlot.DoctorID,
		ScheduledAt: timeStr,
	})

	if err != nil {
		return StepResult{
			BotSpeech:      "Извините, система временно не отвечает. Соединяю с регистратором.",
			NextState:      "STATE_TRANSFER",
			TransferTarget: "101",
		}
	}

	session.CurrentState = "STATE_FINAL"
	return StepResult{
		BotSpeech: "Готово, вы записаны. SMS с деталями придёт на ваш номер. Возьмите паспорт и полис. Могу ещё чем-то помочь?",
		NextState: "STATE_FINAL",
	}
}

func (f *FSMController) handleExistingVisitsLookup(ctx context.Context, session *models.CallSession) StepResult {
	if session.Patient == nil {
		return StepResult{
			BotSpeech: "Не удалось найти вашу карточку. Соединяю с регистратором.",
			NextState: "STATE_TRANSFER",
			TransferTarget: "101",
		}
	}

	visits, err := f.misClient.ListVisits(ctx, session.Patient.ID, "Запланирована")
	if err != nil || len(visits) == 0 {
		return StepResult{
			BotSpeech: "У вас нет активных запланированных записей. Желаете записаться на приём?",
			NextState: "STATE_MAIN_MENU",
		}
	}

	session.ActiveVisits = visits
	v := visits[0]

	switch session.CurrentScenario {
	case models.ScenarioCancel:
		session.CurrentState = "STATE_CONFIRM_CANCEL"
		return StepResult{
			BotSpeech: fmt.Sprintf("Найдена запись: %s %s на %s. Вы хотите отменить её?", v.Doctor.Specialty, v.Doctor.FullName, v.ScheduledAt),
			NextState: "STATE_CONFIRM_CANCEL",
		}

	case models.ScenarioCheckStatus:
		session.CurrentState = "STATE_FINAL"
		return StepResult{
			BotSpeech: fmt.Sprintf("Вы записаны: %s %s на %s, кабинет %s. Могу ещё помочь?", v.Doctor.Specialty, v.Doctor.FullName, v.ScheduledAt, v.Doctor.Room),
			NextState: "STATE_FINAL",
		}

	case models.ScenarioReschedule:
		session.Slots["specialty"] = v.Doctor.Specialty
		session.Slots["reschedule_visit_id"] = v.ID
		return f.presentAvailableSlots(session, v.Doctor.Specialty)

	default:
		return StepResult{
			BotSpeech: "Чем могу помочь?",
			NextState: "STATE_MAIN_MENU",
		}
	}
}

func (f *FSMController) handleConfirmCancel(ctx context.Context, session *models.CallSession, text string) StepResult {
	intent := ParseIntent(text)
	if intent == IntentAffirm || containsAny(strings.ToLower(text), "да", "отменяйте", "верно") {
		if len(session.ActiveVisits) > 0 {
			v := session.ActiveVisits[0]
			cancelStatus := "Отменена"
			_, _ = f.misClient.UpdateVisit(ctx, v.ID, models.VisitUpdate{
				Status: &cancelStatus,
			})
		}
		session.CurrentState = "STATE_FINAL"
		return StepResult{
			BotSpeech: "Ваша запись отменена. Слот освобождён. Могу ещё чем-то помочь?",
			NextState: "STATE_FINAL",
		}
	}

	session.CurrentState = "STATE_FINAL"
	return StepResult{
		BotSpeech: "Хорошо, запись сохранена. Могу ещё чем-то помочь?",
		NextState: "STATE_FINAL",
	}
}
