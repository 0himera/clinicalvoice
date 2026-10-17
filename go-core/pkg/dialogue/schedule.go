package dialogue

import (
	"context"
	"fmt"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func (f *FSMController) schedule(ctx context.Context, s *models.CallSession, text string) StepResult {
	q := SlotPreference(text)
	q.Specialty = f.extractSpecialty(text)
	if requested := f.extractDoctor(text); requested != nil {
		q.DoctorID = requested.ID
		q.Specialty = requested.Specialty
	}
	if q.DoctorID == 0 && q.Specialty == "" {
		question := text
		if containsAny(normalized(text), "поликлиник", "в целом", "общий режим") {
			question = "режим работы"
		}
		if answer, ok := FAQ(question); ok {
			s.FailCount = 0
			return say(StateFinal, answer+" Могу ещё чем-то помочь?")
		}
		if s.CurrentState == StateSchedule {
			nlu := f.understand(ctx, text, StateSchedule)
			if result, ok := f.nluInformation(ctx, s, nlu); ok {
				return result
			}
			if nlu != nil {
				q.Specialty = f.nluSpecialty(nlu.Specialty)
				if d := f.extractDoctor(nlu.DoctorName); nlu.DoctorName != "" && d != nil {
					q.DoctorID = d.ID
					q.Specialty = d.Specialty
				}
			}
		}
	}
	if q.DoctorID == 0 && q.Specialty == "" {
		prompt := "Расписание какого врача или специальности вас интересует? Можно также уточнить часы работы поликлиники."
		if s.CurrentState == StateSchedule {
			return f.retry(ctx, s, prompt)
		}
		s.FailCount = 0
		s.CurrentScenario = models.ScenarioScheduleInfo
		return say(StateSchedule, prompt)
	}
	slots, err := f.slotMgr.Available(ctx, q)
	if err != nil {
		return f.systemFailure(ctx, s)
	}
	s.FailCount = 0
	if len(slots) == 0 {
		return say(StateFinal, "Подходящего свободного времени нет. Скажите «оператор», чтобы уточнить расписание.")
	}
	s.Specialty = q.Specialty
	s.RequestedDoctorID = q.DoctorID
	s.Preference = text
	s.CurrentScenario = models.ScenarioBookingDirect
	return say(StateOfferBooking, fmt.Sprintf("Ближайшее свободное время: %s, %s. Записать вас?", slots[0].Doctor, FormatDateRussian(slots[0].StartTime)))
}
