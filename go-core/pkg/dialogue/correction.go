package dialogue

import (
	"context"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

// Corrections discard uncommitted choices. They never edit a MIS card or visit.
func (f *FSMController) correctInput(ctx context.Context, s *models.CallSession, text string) (StepResult, bool) {
	if containsAny(text, "как исправить", "как изменить данные") {
		return f.aside(s, "Скажите «исправить имя», «исправить дату рождения» или «исправить время». Затем назовите верные данные."), true
	}
	name := containsAny(text, "исправить имя", "исправьте имя", "исправить фамилию", "ошибка в фамилии", "неверное имя", "исправить фио")
	birth := containsAny(text, "исправить дату рождения", "исправьте дату рождения", "ошибка в дате рождения", "дата рождения неверная")
	all := containsAny(text, "исправить данные", "исправьте данные", "данные неверные")
	clock := containsAny(text, "исправить время", "изменить время", "другое время") && s.CurrentState == StateConfirmBooking
	if !name && !birth && !all && !clock {
		return StepResult{}, false
	}
	if s.OutboundVisitID > 0 {
		return f.transfer(ctx, s, "101", "R-07: исправление данных исходящего звонка", "Соединяю с регистратором для проверки данных. Запись сохранена."), true
	}
	if err := f.slotMgr.Release(ctx, s.CallID); err != nil {
		return f.systemFailure(ctx, s), true
	}
	s.SelectedSlot, s.SlotHeldKey = nil, ""
	s.FailCount = 0
	if clock {
		return say(StateChooseSlot, "Назовите удобный день и время или выберите другой вариант."), true
	}
	s.Patient, s.Identified, s.Guardian = nil, false, false
	s.IdentityMisses, s.VisitID = 0, 0
	s.ActiveVisits, s.Options = nil, nil
	s.HomeRequest = ""
	if name || all {
		s.LastName, s.FirstName, s.MiddleName = "", "", ""
	}
	if birth || all {
		s.BirthDate = ""
	}
	if s.CurrentScenario == models.ScenarioNone {
		s.CurrentScenario = models.ScenarioBookingDirect
	}
	prompt := identityPrompt(s)
	if name && !all && s.BirthDate != "" {
		prompt = "Назовите фамилию, имя и отчество заново. Дата рождения сохранена."
	}
	return say(StateIdentity, prompt), true
}
