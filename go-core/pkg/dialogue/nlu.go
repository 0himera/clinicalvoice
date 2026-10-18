package dialogue

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/safety"
)

type nluContextKey struct{}
type nluTurn struct {
	attempted bool
	used      bool
	intent    string
	latency   int64
	failure   string
	result    *llmclient.ExtractedNLU
}

// understand makes at most one bounded NLU request per turn. It never executes
func isDecisionConfident(nlu *llmclient.ExtractedNLU) bool {
	return nlu != nil && (nlu.Intent != "UNKNOWN" || nlu.DoctorName != "" || nlu.Specialty != "" || nlu.FAQTopic != "" || nlu.IsEmergency || nlu.OperatorRequested)
}

func isLLMConfident(nlu *llmclient.ExtractedNLU) bool {
	return nlu != nil && (nlu.Intent != "UNKNOWN" || nlu.Specialty != "" || nlu.BirthDate != "" || nlu.LastName != "" || nlu.OperatorRequested || nlu.IsEmergency)
}

// understand makes at most one bounded NLU request per turn. It never executes
// model instructions or accepts generated speech as an answer to the patient.
func (f *FSMController) understand(ctx context.Context, text, state string) *llmclient.ExtractedNLU {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	hasDecision := f.decisionClient != nil && f.decisionClient.IsEnabled()
	hasLLM := f.llmClient != nil && f.llmClient.IsEnabled()
	// SEC-01 (152-FZ): In PII collection and confirmation states, strictly forbid remote LLM/models
	if state == StateIdentity || state == StateGuardian || state == StateConfirmCancel {
		if f.llmClient != nil && !f.llmClient.IsLocal() {
			hasLLM = false
		}
		if f.decisionClient != nil && !f.decisionClient.IsLocal() {
			hasDecision = false
		}
	}
	if !hasDecision && !hasLLM {
		return nil
	}
	turn, _ := ctx.Value(nluContextKey{}).(*nluTurn)
	if turn == nil {
		turn = &nluTurn{}
	}
	if turn.attempted {
		return turn.result
	}
	turn.attempted = true
	started := time.Now()

	// 1. If only Decision Model is enabled:
	if hasDecision && !hasLLM {
		nlu, err := f.decisionClient.DecideNLU(ctx, text, state)
		turn.latency = time.Since(started).Milliseconds()
		if err == nil && isDecisionConfident(nlu) {
			turn.result, turn.intent = nlu, nlu.Intent
			turn.used = true
			return nlu
		}
		return nil
	}

	// 2. If only LLM is enabled:
	if !hasDecision && hasLLM {
		nlu, err := f.llmClient.ExtractSlots(ctx, text, state)
		turn.latency = time.Since(started).Milliseconds()
		if err != nil {
			turn.failure = "provider_error"
			if ctx.Err() != nil || strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "Timeout") {
				turn.failure = "timeout"
			}
			return nil
		}
		turn.result, turn.intent = nlu, nlu.Intent
		turn.used = isLLMConfident(nlu)
		return nlu
	}

	// Race usable results, not merely HTTP completion. An early provider error
	// (including a privacy rejection) or UNKNOWN must not cancel the local model.
	type result struct {
		nlu      *llmclient.ExtractedNLU
		err      error
		decision bool
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, 2)
	go func() { nlu, err := f.decisionClient.DecideNLU(work, text, state); results <- result{nlu, err, true} }()
	go func() { nlu, err := f.llmClient.ExtractSlots(work, text, state); results <- result{nlu, err, false} }()
	defer func() { turn.latency = time.Since(started).Milliseconds() }()
	for remaining := 2; remaining > 0; remaining-- {
		select {
		case res := <-results:
			if res.err != nil {
				turn.failure = "provider_error"
				continue
			}
			confident := isLLMConfident(res.nlu)
			if res.decision {
				confident = isDecisionConfident(res.nlu)
			}
			if confident {
				turn.result, turn.intent, turn.used = res.nlu, res.nlu.Intent, true
				turn.failure = ""
				return res.nlu
			}
		case <-ctx.Done():
			turn.failure = "timeout"
			return nil
		}
	}
	return nil
}

func (f *FSMController) nluSpecialty(value string) string {
	key := models.SpecialtyKey(value)
	for _, doctor := range f.slotMgr.ListDoctors() {
		if key != "" && models.SpecialtyKey(doctor.Specialty) == key {
			return doctor.Specialty
		}
	}
	return ""
}

func (f *FSMController) nluInformation(ctx context.Context, s *models.CallSession, nlu *llmclient.ExtractedNLU) (StepResult, bool) {
	if nlu == nil {
		return StepResult{}, false
	}
	if nlu.IsEmergency {
		return f.transfer(ctx, s, safety.ExtensionEmergency, safety.ActionEmergency+": угроза жизни в смысловом разборе", safety.SpeechEmergency), true
	}
	if nlu.OperatorRequested || nlu.Intent == "OPERATOR" {
		return f.transfer(ctx, s, safety.ExtensionOperator, safety.ActionOperatorReq+": просьба о сотруднике в смысловом разборе", safety.SpeechOperator), true
	}
	if nlu.Intent == "CLARIFY" {
		return f.aside(s, purposeExplanation(s)), true
	}
	if nlu.Intent == "REPEAT" {
		s.FailCount = 0
		return StepResult{NextState: s.CurrentState, BotSpeech: pendingPrompt(s), keepPrompt: true}, true
	}
	if nlu.Intent == "CHOOSE_SPECIALIST" {
		return f.primaryCareOffer(ctx, s, "", true), true
	}
	if nlu.Intent == "MEDICAL_QUESTION" {
		return f.primaryCareOffer(ctx, s, "", false), true
	}
	preserve := activeTask(s.CurrentState)
	if nlu.Intent == "DIRECTORY_INFO" {
		result := f.directory(s, f.nluSpecialty(nlu.Specialty), false)
		if preserve {
			return f.aside(s, result.BotSpeech), true
		}
		return result, true
	}
	if nlu.Intent != "FAQ_INFO" {
		return StepResult{}, false
	}
	questions := map[string]string{"hours": "режим работы", "address": "адрес", "documents": "документы", "attachment": "прикрепление", "blood_draw": "забор крови", "lab_results": "когда готовы анализы"}
	question, ok := questions[nlu.FAQTopic]
	if !ok {
		return StepResult{}, false
	}
	if nlu.FAQTopic == "lab_results" {
		tests := map[string]string{"cbc": "общий анализ крови", "urine": "мочи", "biochemistry": "биохимия", "hormones": "гормоны", "pcr": "ПЦР", "culture": "посев", "cytology": "цитология", "histology": "гистология"}
		question += " " + tests[nlu.LabTest]
	}
	answer, ok := FAQ(question)
	if !ok {
		return StepResult{}, false
	}
	s.FailCount = 0
	if preserve {
		return f.aside(s, answer), true
	}
	return say(StateFinal, answer+" Могу ещё чем-то помочь?"), true
}

func (f *FSMController) directory(s *models.CallSession, specialty string, next bool) StepResult {
	if !next || specialty != s.DirectoryFilter {
		s.DirectoryPage = 0
	}
	if next {
		s.DirectoryPage++
	}
	s.DirectoryFilter = specialty
	profiles := map[string]string{}
	var names []string
	count := 0
	for _, doctor := range f.slotMgr.ListDoctors() {
		if !doctor.IsActive {
			continue
		}
		count++
		profiles[models.SpecialtyKey(doctor.Specialty)] = doctor.Specialty
		if specialty != "" && models.SpecialtyKey(doctor.Specialty) == models.SpecialtyKey(specialty) {
			names = append(names, doctor.FullName)
		}
	}
	intro := ""
	if specialty == "" {
		for _, value := range profiles {
			names = append(names, value)
		}
		intro = fmt.Sprintf("В расписании %d врачей по %d профилям. Специальности: ", count, len(profiles))
	} else {
		intro = "По профилю «" + specialty + "» принимают: "
	}
	sort.Strings(names)
	if specialty == "" {
		priority := map[string]int{"терапевт": 1, "педиатр": 2, "хирург": 3, "оториноларинголог": 4, "офтальмолог": 5, "акушер-гинеколог": 6}
		rank := func(name string) int {
			if value := priority[models.SpecialtyKey(name)]; value != 0 {
				return value
			}
			return 100
		}
		sort.SliceStable(names, func(i, j int) bool { return rank(names[i]) < rank(names[j]) })
	}
	s.FailCount = 0
	if len(names) == 0 {
		return say(StateFinal, "В действующем расписании подходящих врачей нет. Могу соединить с регистратором.")
	}
	start := s.DirectoryPage * 6
	if start >= len(names) {
		s.DirectoryPage = 0
		start = 0
	}
	end := start + 6
	if end > len(names) {
		end = len(names)
	}
	speech := intro + strings.Join(names[start:end], ", ") + "."
	if end < len(names) {
		speech += " Для продолжения скажите «дальше»."
	}
	speech += " Назовите специальность или врача, к которому хотите записаться."
	return say(StateDirectory, speech)
}
