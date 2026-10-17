package dialogue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

func (f *FSMController) offer(ctx context.Context, s *models.CallSession) StepResult {
	if s.Patient == nil || !s.Identified {
		return say(StateIdentity, "Назовите фамилию, имя и дату рождения пациента.")
	}
	if age, err := models.AgeOn(s.Patient.BirthDate, time.Now()); err == nil && age < 18 {
		candidate := models.PediatricSpecialty(s.Specialty)
		for _, d := range f.slotMgr.ListDoctors() {
			if models.SpecialtyKey(d.Specialty) == models.SpecialtyKey(candidate) {
				s.Specialty = d.Specialty
				break
			}
		}
	}
	if s.CurrentScenario != models.ScenarioReschedule && !f.slotMgr.IsDirectBookingAllowed(s.Specialty) {
		return say(StateReferral, "К этому специалисту требуется направление. Записать вас к терапевту для его получения?")
	}
	if s.CurrentScenario != models.ScenarioReschedule {
		visits, err := f.misClient.ListVisits(ctx, s.Patient.ID, models.VisitPlanned)
		if err != nil {
			return f.systemFailure(ctx, s)
		}
		for _, v := range visits {
			if models.SpecialtyKey(v.Doctor.Specialty) == models.SpecialtyKey(s.Specialty) {
				return say(StateFinal, "У вас уже есть запись к врачу этой специальности. Могу проверить, перенести или отменить её.")
			}
		}
	}
	q := SlotPreference(s.Preference)
	q.Specialty = s.Specialty
	q.BirthDate = s.Patient.BirthDate
	q.DoctorID = s.RequestedDoctorID
	options, err := f.slotMgr.Available(ctx, q)
	if err != nil {
		return f.systemFailure(ctx, s)
	}
	if len(options) == 0 {
		return f.transfer(ctx, s, "101", "C-11: нет подходящих свободных слотов, требуется лист ожидания", "Подходящего свободного времени нет. Передаю регистратору запрос на лист ожидания.")
	}
	s.Options = options
	s.FailCount = 0
	speech := fmt.Sprintf("Первый вариант: %s, %s", options[0].Doctor, FormatDateRussian(options[0].StartTime))
	if len(options) > 1 {
		speech += fmt.Sprintf(". Второй: %s, %s. Какой удобнее?", options[1].Doctor, FormatDateRussian(options[1].StartTime))
	} else {
		speech += ". Выбрать это время?"
	}
	return say(StateChooseSlot, speech)
}
func optionIndex(text string, n int) int {
	s := normalized(text)
	if informationQuestion(s) || Confirmation(text) == IntentDeny || containsAny(s, "не перв", "не втор", "не трет", "не выбира") || word(s, "или") {
		return -1
	}
	if n == 1 && Confirmation(text) == IntentAffirm {
		return 0
	}
	index := -1
	tokens := wordsRE.FindAllString(s, -1)
	_, _, _, hasClock := requestedClock(text)
	for pos, token := range tokens {
		i, found := slotOrdinals[token]
		if !found {
			if value, valid := numberToken(token); valid && value >= 1 && value <= n {
				markedOption := pos > 0 && (tokens[pos-1] == "вариант" || tokens[pos-1] == "номер")
				clockContext := hasClock && !markedOption || pos > 0 && (tokens[pos-1] == "в" || tokens[pos-1] == "на") || pos+1 < len(tokens) && strings.HasPrefix(tokens[pos+1], "час")
				if !clockContext {
					i, found = value-1, true
				}
			}
		}
		if found && i < n {
			if index >= 0 && index != i {
				return -1
			}
			index = i
		}
	}
	return index
}
func (f *FSMController) chooseSlot(ctx context.Context, s *models.CallSession, text string) StepResult {
	if informationQuestion(normalized(text)) {
		return f.aside(s, "Для выбора назовите номер варианта или удобный день и время.")
	}
	if len(clockRE.FindAllStringSubmatch(text, -1)) > 1 || word(normalized(text), "или") {
		result := f.aside(s, "Назовите один вариант: первый или второй, либо одно удобное время.")
		result.DiagnosticCode = "slot_choice_ambiguous"
		return result
	}
	index := optionIndex(text, len(s.Options))
	h, minute, precise, hasClock := requestedClock(text)
	if index < 0 && hasClock && (Confirmation(text) == IntentDeny || word(normalized(text), "не")) {
		s.FailCount = 0
		result := say(StateChooseSlot, "Назовите другое удобное время или номер подходящего варианта.")
		result.DiagnosticCode = "slot_time_declined"
		return result
	}
	if hasClock {
		matches := []int{}
		for i, o := range s.Options {
			if o.StartTime.Hour() == h && (!precise || o.StartTime.Minute() == minute) {
				matches = append(matches, i)
			}
		}
		if index >= 0 {
			consistent := false
			for _, candidate := range matches {
				consistent = consistent || candidate == index
			}
			if !consistent {
				result := f.aside(s, "Номер варианта и названное время не совпадают. Выберите первый или второй вариант.")
				result.DiagnosticCode = "slot_choice_conflict"
				return result
			}
		} else if len(matches) == 1 {
			index = matches[0]
		} else if len(matches) > 1 {
			result := f.aside(s, "Под это время подходят оба предложенных варианта. Скажите «первый» или «второй».")
			result.DiagnosticCode = "slot_time_ambiguous"
			return result
		} else {
			s.Preference = text
			return f.offer(ctx, s)
		}
	}
	if index < 0 || index >= len(s.Options) {
		q := SlotPreference(text)
		if q.MinHour > 0 || q.MaxHour > 0 || q.Weekday != nil || q.ExcludedWeekday != nil || !q.After.IsZero() {
			s.Preference = text
			return f.offer(ctx, s)
		}
		if Confirmation(text) == IntentDeny {
			return say(StateChooseSlot, "Назовите удобный день или время: например, завтра после обеда.")
		}
		if len(s.Options) >= 2 && f.decisionClient != nil && f.decisionClient.IsEnabled() {
			opt1 := fmt.Sprintf("%s, %s", s.Options[0].Doctor, FormatDateRussian(s.Options[0].StartTime))
			opt2 := fmt.Sprintf("%s, %s", s.Options[1].Doctor, FormatDateRussian(s.Options[1].StartTime))
			chosenIdx, conf, err := f.decisionClient.DecideSlotChoice(ctx, text, opt1, opt2)
			if err == nil {
				if conf >= 0.88 {
					if chosenIdx >= 0 && chosenIdx < len(s.Options) {
						index = chosenIdx
					} else {
						s.Preference = text
						return f.offer(ctx, s)
					}
				} else if conf >= 0.60 {
					if !s.SoftClarified {
						s.SoftClarified = true
						return StepResult{
							NextState:  StateChooseSlot,
							BotSpeech:  "Уточните, какой вариант вам больше подходит: первый или второй?",
							keepPrompt: true,
						}
					}
				}
			}
		}
		if index < 0 || index >= len(s.Options) {
			result := f.retry(ctx, s, "Выберите первый или второй вариант, либо назовите удобный день и время.")
			result.DiagnosticCode = "slot_choice_unrecognized"
			return result
		}
	}
	chosen := s.Options[index]
	key, err := f.slotMgr.HoldSlot(chosen.DoctorID, chosen.StartTime, s.CallID, 2*time.Minute)
	if errors.Is(err, slotmgr.ErrUnavailable) {
		return f.offer(ctx, s)
	}
	if err != nil {
		return f.systemFailure(ctx, s)
	}
	s.SelectedSlot = &chosen
	s.SlotHeldKey = key
	s.FailCount = 0
	return say(StateConfirmBooking, fmt.Sprintf("%s, %s, кабинет %s. Подтверждаете запись?", chosen.Doctor, FormatDateRussian(chosen.StartTime), chosen.Room))
}
func (f *FSMController) confirmBooking(ctx context.Context, s *models.CallSession, text string) StepResult {
	intent := ParseIntent(text)
	if Confirmation(text) == IntentAffirm && isTaskIntent(intent) && intent != IntentBooking && !(s.CurrentScenario == models.ScenarioReschedule && intent == IntentReschedule) {
		return f.retry(ctx, s, "Сейчас подтверждаем выбранного врача и время. Подтверждаете запись? Скажите да или нет.")
	}
	confirmed := false
	var asideSpeech string
	switch Confirmation(text) {
	case IntentDeny:
		if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
			return f.systemFailure(ctx, s)
		}
		s.SelectedSlot = nil
		return f.offer(ctx, s)
	case IntentAffirm:
		confirmed = true
	default:
		if f.decisionClient != nil && f.decisionClient.IsEnabled() {
			slotDesc := ""
			if s.SelectedSlot != nil {
				slotDesc = fmt.Sprintf("%s, %s", s.SelectedSlot.Doctor, FormatDateRussian(s.SelectedSlot.StartTime))
			}
			confirmChoice, asideChoice, conf, err := f.decisionClient.DecideConfirmationAndAside(ctx, text, slotDesc, s.PendingPrompt)
			if err == nil {
				if conf >= 0.88 {
					switch confirmChoice {
					case "AFFIRM":
						confirmed = true
						switch asideChoice {
						case "FAQ_LOCATION":
							floor := "втором"
							if s.SelectedSlot != nil && len(s.SelectedSlot.Room) > 0 {
								switch s.SelectedSlot.Room[0] {
								case '1':
									floor = "первом"
								case '2':
									floor = "втором"
								case '3':
									floor = "третьем"
								case '4':
									floor = "четвёртом"
								case '5':
									floor = "пятом"
								}
							}
							asideSpeech = fmt.Sprintf("Кабинет находится на %s этаже поликлиники.", floor)
						case "FAQ_ANALYSIS":
							asideSpeech = "Результаты анализов прикрепит врач на приёме."
						case "FAQ_DOCUMENTS":
							asideSpeech = "Возьмите с собой паспорт и полис ОМС."
						case "FAQ_PRICE":
							asideSpeech = "Приём по полису ОМС в нашей поликлинике бесплатный."
						}
					case "DENY", "CHANGE_SLOT":
						if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
							return f.systemFailure(ctx, s)
						}
						s.SelectedSlot = nil
						return f.offer(ctx, s)
					}
				} else if conf >= 0.60 {
					if !s.SoftClarified {
						s.SoftClarified = true
						return StepResult{
							NextState:  StateConfirmBooking,
							BotSpeech:  "Уточните, пожалуйста: вы подтверждаете запись на это время? Скажите да или нет.",
							keepPrompt: true,
						}
					}
				}
			}
		}
		if !confirmed {
			return f.retry(ctx, s, "Подтверждаете выбранного врача и время? Скажите да или нет.")
		}
	}
	if s.SelectedSlot == nil || s.Patient == nil {
		return f.systemFailure(ctx, s)
	}
	unlock, lockErr := f.patientLocks.acquire(ctx, s.Patient.ID)
	if lockErr != nil {
		return f.systemFailure(ctx, s)
	}
	defer unlock()
	if s.CurrentScenario != models.ScenarioReschedule {
		visits, err := f.misClient.ListVisits(ctx, s.Patient.ID, models.VisitPlanned)
		if err != nil {
			return f.systemFailure(ctx, s)
		}
		for _, v := range visits {
			if models.SpecialtyKey(v.Doctor.Specialty) == models.SpecialtyKey(s.Specialty) {
				if err = f.slotMgr.Release(ctx, s.CallID); err != nil {
					return f.systemFailure(ctx, s)
				}
				s.SelectedSlot = nil
				return say(StateFinal, "У вас уже есть запись к врачу этой специальности. Могу перенести или отменить её.")
			}
		}
	}
	slot := *s.SelectedSlot
	if err := f.slotMgr.MarkPending(ctx, slot, s.CallID); err != nil {
		if errors.Is(err, slotmgr.ErrUnavailable) {
			return f.offer(ctx, s)
		}
		return f.systemFailure(ctx, s)
	}
	var visit *models.Visit
	var err error
	at := slot.StartTime.In(models.ClinicLocation).Format(models.DateTimeLayout)
	if s.CurrentScenario == models.ScenarioReschedule {
		visit, err = f.misClient.UpdateVisit(ctx, s.VisitID, models.VisitUpdate{DoctorID: &slot.DoctorID, ScheduledAt: &at})
	} else {
		visit, err = f.misClient.CreateVisit(ctx, models.VisitCreate{ClientID: s.Patient.ID, DoctorID: slot.DoctorID, ScheduledAt: at})
	}
	if err != nil {
		if resolveErr := f.slotMgr.Resolve(context.WithoutCancel(ctx), slot, s.CallID, 0, misclient.Ambiguous(err)); resolveErr != nil {
			return f.systemFailure(ctx, s)
		}
		return f.systemFailure(ctx, s)
	}
	if err = f.slotMgr.Resolve(context.WithoutCancel(ctx), slot, s.CallID, visit.ID, false); err != nil {
		return f.systemFailure(ctx, s)
	}
	s.SelectedSlot = nil
	s.SlotHeldKey = ""
	s.FailCount = 0
	outcome := "BOOKED"
	speech := "Готово, вы записаны. Возьмите паспорт и полис. Могу ещё помочь?"
	if s.CurrentScenario == models.ScenarioReschedule {
		outcome = "RESCHEDULED"
		speech = "Готово, запись перенесена. Прежнее время освобождено. Могу ещё помочь?"
	}
	if asideSpeech != "" {
		speech = asideSpeech + " " + speech
	}
	state := StateFinal
	if f.saveContact != nil && s.CallerNumber != "" && s.OutboundVisitID == 0 {
		state = StateReminderConsent
		speech += " Можно напомнить о приёме звонком на этот номер?"
	}
	return StepResult{NextState: state, BotSpeech: speech, Outcome: outcome}
}
