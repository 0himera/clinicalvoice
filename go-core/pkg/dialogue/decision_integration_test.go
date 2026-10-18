package dialogue

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
)

func TestDecisionModel_CompositeConfirmationAndAside(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		resp := llmclient.DecisionResponse{
			Model: "THX-01",
			Answers: map[string]llmclient.DecisionAnswer{
				"confirmation": {
					Type:       llmclient.TypeChoice,
					Choice:     "AFFIRM",
					Confidence: 0.96,
				},
				"aside_intent": {
					Type:   llmclient.TypeChoice,
					Choice: "FAQ_LOCATION",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := newRig(t)
	dClient := llmclient.NewDecisionClient(server.URL, "", true)
	r.fsm.SetDecisionClient(dClient)

	// Advance to StateConfirmBooking
	r.ready()
	if r.call.CurrentState != StateConfirmBooking {
		t.Fatalf("expected StateConfirmBooking, got %s", r.call.CurrentState)
	}

	// Composite user utterance: confirmation + aside question.
	// Model answers aside and explicitly prompts for confirmation without booking yet.
	res := r.turn("Да, подтверждаю, а где кабинет находится?")
	if res.NextState != StateConfirmBooking {
		t.Fatalf("expected StateConfirmBooking, got next state %s", res.NextState)
	}
	if !strings.Contains(res.BotSpeech, "Кабинет находится на втором этаже") {
		t.Errorf("expected aside speech in bot response, got %s", res.BotSpeech)
	}
	if !strings.Contains(res.BotSpeech, "Подтверждаете выбранное время? Скажите да или нет.") {
		t.Errorf("expected explicit confirmation question in bot response, got %s", res.BotSpeech)
	}

	// Invariant: booking must NOT be confirmed before explicit user confirmation
	if len(r.visits(t)) != 0 {
		t.Fatalf("expected 0 confirmed visits before explicit consent, got %d", len(r.visits(t)))
	}

	// User explicitly confirms in the next turn
	res2 := r.turn("Да")
	if res2.NextState != StateFinal && res2.NextState != StateReminderConsent {
		t.Fatalf("expected booking success after explicit confirmation, got next state %s", res2.NextState)
	}
	if !strings.Contains(res2.BotSpeech, "Готово, вы записаны") {
		t.Errorf("expected booking confirmation speech, got %s", res2.BotSpeech)
	}

	visits := r.visits(t)
	if len(visits) != 1 {
		t.Fatalf("expected exactly 1 confirmed visit in MIS, got %d", len(visits))
	}
}

func TestDecisionModel_GrayZoneSoftClarify(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		resp := llmclient.DecisionResponse{
			Model: "THX-01",
			Answers: map[string]llmclient.DecisionAnswer{
				"confirmation": {
					Type:       llmclient.TypeChoice,
					Choice:     "AFFIRM",
					Confidence: 0.72, // Gray zone: 0.60 <= conf < 0.88
				},
				"aside_intent": {
					Type:   llmclient.TypeChoice,
					Choice: "NONE",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := newRig(t)
	dClient := llmclient.NewDecisionClient(server.URL, "", true)
	r.fsm.SetDecisionClient(dClient)

	// Advance to StateConfirmBooking
	r.ready()

	// Turn 1: user utterance in gray zone (confidence 0.72)
	res1 := r.turn("ну наверное да")
	if res1.NextState != StateConfirmBooking {
		t.Fatalf("expected to remain in StateConfirmBooking, got %s", res1.NextState)
	}
	if !strings.Contains(res1.BotSpeech, "Уточните, пожалуйста") {
		t.Errorf("expected soft clarify question, got %s", res1.BotSpeech)
	}
	if !r.call.SoftClarified {
		t.Fatalf("expected SoftClarified flag to be true")
	}
	if r.call.FailCount != 0 {
		t.Fatalf("expected FailCount to stay 0 on first soft clarify, got %d", r.call.FailCount)
	}

	// Turn 2: second consecutive turn in gray zone -> must increment FailCount
	res2 := r.turn("ну да вроде")
	if r.call.FailCount != 1 {
		t.Fatalf("expected FailCount to increment to 1 on second gray zone turn, got %d", r.call.FailCount)
	}
	if !strings.Contains(res2.BotSpeech, "Подтверждаете выбранного врача") {
		t.Errorf("expected standard retry prompt, got %s", res2.BotSpeech)
	}
}

func TestDecisionModel_SlotChoiceSemanticPreference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		resp := llmclient.DecisionResponse{
			Model: "THX-01",
			Answers: map[string]llmclient.DecisionAnswer{
				"slot_choice": {
					Type:       llmclient.TypeChoice,
					Choice:     "OPTION_2",
					Confidence: 0.94,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := newRig(t)
	dClient := llmclient.NewDecisionClient(server.URL, "", true)
	r.fsm.SetDecisionClient(dClient)

	// Advance to choose slot state
	r.turn("Запишите к терапевту")
	r.turn("Иванов Иван Иванович 12 марта 1985")
	if r.call.CurrentState != StateChooseSlot {
		t.Fatalf("expected StateChooseSlot, got %s", r.call.CurrentState)
	}
	if len(r.call.Options) < 2 {
		t.Fatalf("expected at least 2 slot options, got %d", len(r.call.Options))
	}
	opt2StartTime := r.call.Options[1].StartTime

	// User gives semantic relational preference not caught by basic regex
	res := r.turn("Давайте тот вариант, который попозже")
	if res.NextState != StateConfirmBooking {
		t.Fatalf("expected StateConfirmBooking, got %s: %s", res.NextState, res.BotSpeech)
	}
	if r.call.SelectedSlot == nil || !r.call.SelectedSlot.StartTime.Equal(opt2StartTime) {
		t.Fatalf("expected Option 2 selected (start=%v), got %v", opt2StartTime, r.call.SelectedSlot)
	}
}

func TestDecisionModel_FalseAlarmSafetyFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		resp := llmclient.DecisionResponse{
			Model: "THX-01",
			Answers: map[string]llmclient.DecisionAnswer{
				"vital_threat": {
					Type: llmclient.TypeNoul,
					Noul: 0.01, // P(true) < 0.15: Emotional metaphor filtered!
				},
				"intent": {
					Type:       llmclient.TypeChoice,
					Choice:     "BOOKING",
					Confidence: 0.90,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := newRig(t)
	dClient := llmclient.NewDecisionClient(server.URL, "", true)
	r.fsm.SetDecisionClient(dClient)

	// Phrase contains "сердце...бол", which triggers Category A in pure regex,
	// but Decision Model identifies it as an emotional metaphor (P=0.01)
	res := r.turn("У меня сердце кровью обливается, запишите к терапевту")
	if res.NextState == StateTransfer && res.TransferTarget == "104" {
		t.Fatalf("metaphor incorrectly transferred to emergency: %+v", res)
	}
}

func TestIdentityCanClarifyWithDecisionAndNoLLM(t *testing.T) {
	r := newRig(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		var body llmclient.DecisionRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, ok := body.Questions["intent"].Criteria["CLARIFY"]; !ok {
			t.Error("missing clarification intent")
		}
		_ = json.NewEncoder(w).Encode(llmclient.DecisionResponse{Answers: map[string]llmclient.DecisionAnswer{
			"intent": {Type: llmclient.TypeChoice, Choice: "CLARIFY", Confidence: 0.1, Probabilities: map[string]float64{"CLARIFY": 0.96}},
		}})
	}))
	defer server.Close()
	r.fsm.SetDecisionClient(llmclient.NewDecisionClient(server.URL, "fixture", true))
	r.turn("Запишите к терапевту")
	got := r.turn("Объясните назначение запрошенных сведений")
	if got.NextState != StateIdentity || !strings.Contains(got.BotSpeech, "не перепутать пациентов") || !got.LLMUsed || requests.Load() != 1 {
		t.Fatalf("local clarification lost: %+v calls=%d", got, requests.Load())
	}
	if r.call.LastName != "" || r.call.FirstName != "" || len(r.visits(t)) != 0 {
		t.Fatal("clarification became patient data or changed MIS")
	}
}
