package dialogue

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
)

func familyRequest(text string) bool {
	for _, relation := range []string{"ребенка", "ребенку", "дочь", "дочку", "дочери", "сына", "сыну", "маму", "папу", "мужа", "жену", "сестру", "брата", "бабушку", "дедушку", "другого"} {
		if word(text, relation) {
			return true
		}
	}
	return false
}

func resetIdentity(s *models.CallSession) {
	s.Patient = nil
	s.Identified = false
	s.Guardian = false
	s.LastName = ""
	s.FirstName = ""
	s.MiddleName = ""
	s.BirthDate = ""
	s.ActiveVisits = nil
	s.SelectedSlot = nil
	s.Options = nil
	s.RequestedDoctorID = 0
	s.CurrentScenario = models.ScenarioNone
	s.CurrentState = StateMenu
	s.PendingPrompt = ""
	s.MedicalBoundaryGiven = false
}

func (f *FSMController) identity(ctx context.Context, s *models.CallSession, text string) StepResult {
	oldLast, oldFirst, oldMiddle, oldBirth := s.LastName, s.FirstName, s.MiddleName, s.BirthDate
	last, first, middle := ExtractFIO(text)
	birth := ExtractBirthDate(text)
	missingName := (last == "" || first == "") && (s.LastName == "" || s.FirstName == "")
	missingBirth := birth == "" && s.BirthDate == ""
	understanding, _ := ctx.Value(nluContextKey{}).(*nluTurn)
	if missingName || missingBirth || (understanding != nil && understanding.result != nil) {
		// Provider privacy is enforced in understand and in the remote client.
		// A disabled/remote LLM must not disable the local Decision model.
		nlu := f.understand(ctx, text, StateIdentity)
		if result, ok := f.nluInformation(ctx, s, nlu); ok {
			return result
		}
		if nlu != nil {
			if nlu.LastName != "" && word(text, normalized(nlu.LastName)) {
				last = nlu.LastName
			}
			if nlu.FirstName != "" && word(text, normalized(nlu.FirstName)) {
				first = nlu.FirstName
			}
			if nlu.MiddleName != "" && word(text, normalized(nlu.MiddleName)) {
				middle = nlu.MiddleName
			}
			if birth == "" {
				birth = ExtractBirthDate(nlu.BirthDate)
			}
		}
	}
	if last != "" && first != "" {
		s.LastName = last
		s.FirstName = first
		s.MiddleName = middle
	}
	if birth != "" {
		s.BirthDate = birth
	}
	if s.LastName == "" || s.FirstName == "" || s.BirthDate == "" {
		prompt := identityPrompt(s)
		if oldLast != s.LastName || oldFirst != s.FirstName || oldMiddle != s.MiddleName || oldBirth != s.BirthDate {
			s.FailCount = 0
			return say(StateIdentity, prompt)
		}
		return f.retry(ctx, s, prompt)
	}
	if age, err := models.AgeOn(s.BirthDate, time.Now()); err != nil {
		return f.retry(ctx, s, "Уточните дату рождения.")
	} else if age < 15 && !s.Guardian {
		return say(StateGuardian, "Вы звоните как законный представитель ребёнка?")
	}
	clients, err := f.misClient.FindClients(ctx, s.LastName, s.FirstName, s.BirthDate)
	if err != nil {
		return f.systemFailure(ctx, s)
	}
	if len(clients) > 1 {
		return f.transfer(ctx, s, "101", "R-07: найдено несколько карточек пациента", "Нужна дополнительная проверка карточки. Соединяю с регистратором.")
	}
	if len(clients) == 0 {
		if s.CurrentScenario != models.ScenarioBookingDirect && s.CurrentScenario != models.ScenarioHomeVisit {
			return say(StateFinal, "Карточка с этими данными не найдена. Проверьте данные или попросите оператора.")
		}
		req := models.ClientCreate{LastName: s.LastName, FirstName: s.FirstName, BirthDate: s.BirthDate}
		if s.MiddleName != "" {
			req.MiddleName = &s.MiddleName
		}
		client, err := f.misClient.CreateClient(ctx, req)
		if err != nil {
			return f.systemFailure(ctx, s)
		}
		s.Patient = client
	} else {
		s.Patient = &clients[0]
	}
	s.Identified = true
	s.FailCount = 0
	return f.afterIdentity(ctx, s)
}

func identityPrompt(s *models.CallSession) string {
	if s.LastName != "" && s.FirstName != "" {
		return "Назовите полную дату рождения: число, месяц и год."
	}
	if s.BirthDate != "" {
		return "Назовите фамилию, имя и отчество пациента."
	}
	return "Назовите фамилию, имя и полную дату рождения: число, месяц и год."
}

func (f *FSMController) afterIdentity(ctx context.Context, s *models.CallSession) StepResult {
	if s.CurrentScenario == models.ScenarioHomeVisit {
		return say(StateHome, "Назовите полный адрес: улицу, дом, квартиру и причину вызова своими словами.")
	}
	if s.CurrentScenario == models.ScenarioCancel || s.CurrentScenario == models.ScenarioReschedule || s.CurrentScenario == models.ScenarioCheckStatus {
		return f.existing(ctx, s)
	}
	if s.Specialty == "" {
		return say(StateSpecialty, "К какому врачу или по какой специальности хотите записаться?")
	}
	return f.offer(ctx, s)
}
func (f *FSMController) extractSpecialty(text string) string {
	// Prefer the longest official specialty, then familiar aliases.
	docs := f.slotMgr.ListDoctors()
	sort.SliceStable(docs, func(i, j int) bool { return len(docs[i].Specialty) > len(docs[j].Specialty) })
	for _, d := range docs {
		if strings.Contains(normalized(text), normalized(d.Specialty)) {
			return d.Specialty
		}
	}
	return ExtractSpecialty(text)
}

func (f *FSMController) extractDoctor(text string) *models.Doctor {
	s := normalized(text)
	for _, marker := range []string{" я ", "меня зовут", "фамилия", "пациент"} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i]
		}
	}
	var matches []models.Doctor
	for _, d := range f.slotMgr.ListDoctors() {
		parts := strings.Fields(normalized(d.FullName))
		if len(parts) == 0 {
			continue
		}
		stem := parts[0]
		for _, suffix := range []string{"ая", "яя", "а", "я", "ь"} {
			if strings.HasSuffix(stem, suffix) && len([]rune(stem)) > 5 {
				stem = strings.TrimSuffix(stem, suffix)
				break
			}
		}
		for _, token := range wordsRE.FindAllString(s, -1) {
			if token == parts[0] || (len([]rune(stem)) >= 4 && strings.HasPrefix(token, stem)) {
				matches = append(matches, d)
				break
			}
		}
	}
	if len(matches) == 1 {
		return &matches[0]
	}
	for _, d := range matches {
		parts := strings.Fields(normalized(d.FullName))
		if len(parts) > 1 && word(s, parts[1]) {
			copy := d
			return &copy
		}
	}
	return nil
}
func (f *FSMController) specialty(ctx context.Context, s *models.CallSession, text string) StepResult {
	spec := f.extractSpecialty(text)
	if d := f.extractDoctor(text); d != nil {
		spec = d.Specialty
		s.RequestedDoctorID = d.ID
	}
	if spec == "" {
		nlu := f.understand(ctx, text, StateSpecialty)
		if result, ok := f.nluInformation(ctx, s, nlu); ok {
			return result
		}
		if nlu != nil {
			spec = f.nluSpecialty(nlu.Specialty)
			if d := f.extractDoctor(nlu.DoctorName); nlu.DoctorName != "" && d != nil {
				spec = d.Specialty
				s.RequestedDoctorID = d.ID
			}
		}
	}
	if spec == "" {
		return f.retry(ctx, s, "Назовите специальность врача, например терапевт или хирург.")
	}
	s.Specialty = spec
	s.FailCount = 0
	if !f.slotMgr.IsDirectBookingAllowed(spec) {
		return say(StateReferral, "К этому специалисту требуется направление. Записать вас к терапевту для его получения?")
	}
	return f.offer(ctx, s)
}
