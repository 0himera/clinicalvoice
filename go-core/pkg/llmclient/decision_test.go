package llmclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDecisionClient_VitalThreatFailClosed(t *testing.T) {
	// Case 1: Server returns HTTP 500 error -> Must fail closed (isThreat = true)
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server500.Close()

	client := NewDecisionClient(server500.URL, true)
	isThreat, _, err := client.DecideVitalThreat(context.Background(), "болит в груди")
	if err == nil {
		t.Fatalf("expected error on 500")
	}
	if !isThreat {
		t.Fatalf("expected fail-closed isThreat=true on server error")
	}

	// Case 2: Server returns high P(threat) -> isThreat = true
	serverThreat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DecisionResponse{
			Model: "THX-01",
			Answers: map[string]DecisionAnswer{
				"vital_threat": {
					Type: TypeNoul,
					Noul: 0.98,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer serverThreat.Close()

	clientThreat := NewDecisionClient(serverThreat.URL, true)
	isThreat, p, err := clientThreat.DecideVitalThreat(context.Background(), "давит в груди и трудно дышать")
	if err != nil || !isThreat || p < 0.9 {
		t.Fatalf("expected real threat: isThreat=%v, p=%v, err=%v", isThreat, p, err)
	}

	// Case 3: Metaphor / Negation where P(threat) < 0.15 -> isThreat = false
	serverMetaphor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DecisionResponse{
			Model: "THX-01",
			Answers: map[string]DecisionAnswer{
				"vital_threat": {
					Type: TypeNoul,
					Noul: 0.02,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer serverMetaphor.Close()

	clientMetaphor := NewDecisionClient(serverMetaphor.URL, true)
	isThreat, p, err = clientMetaphor.DecideVitalThreat(context.Background(), "сердце кровью обливается")
	if err != nil || isThreat || p >= 0.15 {
		t.Fatalf("expected false alarm filtered: isThreat=%v, p=%v, err=%v", isThreat, p, err)
	}
}

func TestDecisionClient_ConfirmationAndAside(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DecisionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		resp := DecisionResponse{
			Model: "THX-01",
			Answers: map[string]DecisionAnswer{
				"confirmation": {
					Type:       TypeChoice,
					Choice:     "AFFIRM",
					Confidence: 0.96,
				},
				"aside_intent": {
					Type:   TypeChoice,
					Choice: "FAQ_ANALYSIS",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, true)
	confirm, aside, conf, err := client.DecideConfirmationAndAside(context.Background(), "Да, подтверждаю, а флюорографию брать?", "Иванов, 12 мая 10:00", "Подтверждаете?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirm != "AFFIRM" || aside != "FAQ_ANALYSIS" || conf < 0.90 {
		t.Fatalf("unexpected result: confirm=%s, aside=%s, conf=%f", confirm, aside, conf)
	}
}

func TestDecisionClient_SlotChoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DecisionResponse{
			Model: "THX-01",
			Answers: map[string]DecisionAnswer{
				"slot_choice": {
					Type:       TypeChoice,
					Choice:     "OPTION_2",
					Confidence: 0.93,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, true)
	idx, conf, err := client.DecideSlotChoice(context.Background(), "давайте тот что попозже", "10:00", "15:30")
	if err != nil || idx != 1 || conf < 0.90 {
		t.Fatalf("unexpected slot choice: idx=%d, conf=%f, err=%v", idx, conf, err)
	}
}

func TestDecisionClient_DoctorExcerpt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DecisionResponse{
			Model: "THX-01",
			Answers: map[string]DecisionAnswer{
				"doctor_name": {
					Type:        TypeExcerpt,
					Text:        "Богдановой",
					Start:       22,
					End:         32,
					Probability: 0.94,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, true)
	text, start, end, prob, err := client.DecideDoctorExcerpt(context.Background(), "запишите к терапевту Богдановой пожалуйста")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Богдановой" || start != 22 || end != 32 || prob < 0.9 {
		t.Fatalf("unexpected excerpt: text=%s, start=%d, end=%d, prob=%f", text, start, end, prob)
	}
}

func TestDecisionClient_Disabled(t *testing.T) {
	client := NewDecisionClient("", false)
	if client.IsEnabled() {
		t.Fatalf("client should be disabled")
	}
	_, _, _, err := client.DecideConfirmationAndAside(context.Background(), "да", "слот", "подтверждаете?")
	if err == nil {
		t.Fatalf("expected error on disabled client")
	}
}

func TestDecisionClient_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, true)
	// Timeout is 250ms, server sleeps 300ms
	_, err := client.Decide(context.Background(), "state", map[string]QuestionSpec{})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
}

func TestDecisionClient_DecideNLU(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DecisionResponse{
			Model: "THX-01",
			Answers: map[string]DecisionAnswer{
				"intent": {
					Type:       TypeChoice,
					Choice:     "BOOKING",
					Confidence: 0.95,
				},
				"doctor_name": {
					Type:        TypeExcerpt,
					Text:        "Богдановой",
					Probability: 0.92,
				},
				"faq_topic": {
					Type:   TypeChoice,
					Choice: "none",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, true)
	nlu, err := client.DecideNLU(context.Background(), "запишите к Богдановой", "меню")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nlu.Intent != "BOOKING" || nlu.DoctorName != "Богдановой" {
		t.Fatalf("unexpected nlu result: %+v", nlu)
	}
}

