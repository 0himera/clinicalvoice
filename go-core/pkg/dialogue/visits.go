package dialogue

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func (f *FSMController) existing(ctx context.Context, s *models.CallSession) StepResult {
	visits, err := f.misClient.ListVisits(ctx, s.Patient.ID, models.VisitPlanned)
	if err != nil {
		return f.systemFailure(ctx, s)
	}
	sort.Slice(visits, func(i, j int) bool {
		a, _ := models.ParseDateTime(visits[i].ScheduledAt)
		b, _ := models.ParseDateTime(visits[j].ScheduledAt)
		if a.Equal(b) {
			return visits[i].ID < visits[j].ID
		}
		return a.Before(b)
	})
	s.ActiveVisits = visits
	s.FailCount = 0
	if len(visits) == 0 {
		return say(StateFinal, "У вас нет запланированных записей. Могу помочь записаться к врачу.")
	}
	if len(visits) > 1 {
		var parts []string
		for i, v := range visits {
			if i == 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%d: %s, %s", i+1, v.Doctor.Specialty, v.ScheduledAt))
		}
		return say(StateChooseVisit, "Найдены записи: "+strings.Join(parts, "; ")+". Какую выбрать?")
	}
	return f.selectedVisit(ctx, s, visits[0])
}
func (f *FSMController) chooseVisit(ctx context.Context, s *models.CallSession, text string) StepResult {
	i := optionIndex(text, min(3, len(s.ActiveVisits)))
	if i < 0 || i >= len(s.ActiveVisits) {
		return f.retry(ctx, s, "Выберите запись: первую, вторую или третью.")
	}
	s.FailCount = 0
	return f.selectedVisit(ctx, s, s.ActiveVisits[i])
}
func (f *FSMController) selectedVisit(ctx context.Context, s *models.CallSession, v models.Visit) StepResult {
	s.VisitID = v.ID
	switch s.CurrentScenario {
	case models.ScenarioCancel:
		return say(StateConfirmCancel, fmt.Sprintf("Запись: %s, %s. Подтверждаете отмену?", v.Doctor.FullName, v.ScheduledAt))
	case models.ScenarioReschedule:
		if !v.Doctor.IsActive {
			return f.transfer(ctx, s, "101", "C-03: врач деактивирован", "Врач недоступен. Соединяю с регистратором для подбора другого врача.")
		}
		s.Specialty = v.Doctor.Specialty
		if s.RequestedDoctorID == 0 {
			s.RequestedDoctorID = v.Doctor.ID
		}
		return f.offer(ctx, s)
	default:
		return say(StateFinal, fmt.Sprintf("Вы записаны: %s, %s. Могу ещё помочь?", v.Doctor.FullName, v.ScheduledAt))
	}
}
func (f *FSMController) confirmCancel(ctx context.Context, s *models.CallSession, text string) StepResult {
	if ParseIntent(text) == IntentReschedule {
		s.CurrentScenario = models.ScenarioReschedule
		if len(s.ActiveVisits) > 0 {
			return f.selectedVisit(ctx, s, s.ActiveVisits[0])
		}
		return f.menu(ctx, s, text)
	}
	if Confirmation(text) == IntentAffirm && isTaskIntent(ParseIntent(text)) && ParseIntent(text) != IntentCancel {
		return f.retry(ctx, s, "Сейчас подтверждаем отмену существующей записи. Отменить её? Скажите да или нет.")
	}
	if Confirmation(text) == IntentDeny {
		s.FailCount = 0
		return say(StateFinal, "Запись сохранена. Могу ещё помочь?")
	}
	if Confirmation(text) != IntentAffirm {
		return f.retry(ctx, s, "Подтверждаете отмену? Скажите да или нет.")
	}
	status := models.VisitCancelled
	if _, err := f.misClient.UpdateVisit(ctx, s.VisitID, models.VisitUpdate{Status: &status}); err != nil {
		return f.systemFailure(ctx, s)
	}
	if err := f.slotMgr.Cancel(ctx, s.VisitID); err != nil {
		return f.systemFailure(ctx, s)
	}
	s.FailCount = 0
	return StepResult{
		NextState:   StateFinal,
		BotSpeech:   "Ваша запись отменена. Могу ещё помочь?",
		Outcome:     "CANCELLED",
		Source:      "mis",
		Confidence:  1.0,
		Intent:      string(models.ScenarioCancel),
		Explanation: fmt.Sprintf("Запись #%d успешно отменена в МИС", s.VisitID),
	}
}
func (f *FSMController) StartReminder(ctx context.Context, s *models.CallSession, id int) StepResult {
	v, err := f.misClient.GetVisit(ctx, id)
	if err != nil || v.Status != models.VisitPlanned {
		return f.systemFailure(ctx, s)
	}
	c, err := f.misClient.GetClient(ctx, v.ClientID)
	if err != nil {
		return f.systemFailure(ctx, s)
	}
	s.Patient = c
	s.ActiveVisits = []models.Visit{*v}
	s.OutboundVisitID = id
	s.VisitID = id
	s.CurrentScenario = models.ScenarioOutboundRemind
	s.CurrentState = StateReminderIdentity
	result := StepResult{
		NextState:   StateReminderIdentity,
		BotSpeech:   fmt.Sprintf("Здравствуйте! Поликлиника номер семь, виртуальный помощник. %s %s, это вы?", c.FirstName, c.MiddleName),
		Source:      "campaign",
		Confidence:  1.0,
		Intent:      "OUTBOUND_REMINDER",
		Explanation: fmt.Sprintf("Исходящее напоминание о записи #%d", id),
	}
	s.PendingPrompt = result.BotSpeech
	return result
}
func (f *FSMController) reminder(ctx context.Context, s *models.CallSession, text string) StepResult {
	intent := ParseIntent(text)
	if intent == IntentReschedule {
		s.CurrentScenario = models.ScenarioReschedule
		return f.selectedVisit(ctx, s, s.ActiveVisits[0])
	}
	if Confirmation(text) == IntentDeny || intent == IntentCancel {
		s.CurrentScenario = models.ScenarioCancel
		return say(StateConfirmCancel, "Могу перенести запись. Для переноса скажите «перенести», для отмены подтвердите: отменить запись?")
	}
	if Confirmation(text) != IntentAffirm {
		return f.retry(ctx, s, "Подтвердить запись, перенести или отменить её?")
	}
	unlock, lockErr := f.patientLocks.acquire(ctx, s.Patient.ID)
	if lockErr != nil {
		return f.systemFailure(ctx, s)
	}
	defer unlock()
	v, err := f.misClient.GetVisit(ctx, s.OutboundVisitID)
	if err != nil || v.Status != models.VisitPlanned {
		return f.systemFailure(ctx, s)
	}
	c, err := f.misClient.GetClient(ctx, s.Patient.ID)
	if err != nil {
		return f.systemFailure(ctx, s)
	}
	marker := fmt.Sprintf("Подтверждена запись #%d %s", v.ID, v.ScheduledAt)
	comment := c.Comment
	if !strings.Contains(comment, marker) {
		if comment != "" {
			comment += "\n"
		}
		comment += marker
	}
	if len([]rune(comment)) > 1000 {
		return f.transfer(ctx, s, "101", "C-10: комментарий клиента заполнен", "Соединяю с регистратором для сохранения подтверждения.")
	}
	if _, err = f.misClient.UpdateClient(ctx, c.ID, models.ClientUpdate{Comment: &comment}); err != nil {
		return f.systemFailure(ctx, s)
	}
	return StepResult{
		NextState:    StateDone,
		BotSpeech:    "Спасибо, запись подтверждена. Возьмите паспорт и полис. Всего доброго!",
		ShouldHangup: true,
		Outcome:      "CONFIRMED",
		Source:       "mis",
		Confidence:   1.0,
		Intent:       "CONFIRM_REMINDER",
		Explanation:  fmt.Sprintf("Запись #%d подтверждена пациентом и отмечена в МИС", v.ID),
	}
}
