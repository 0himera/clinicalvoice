package llmclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
					Confidence: 0.49, // entropy/margin in THX-01
					Probabilities: map[string]float64{
						"AFFIRM": 0.96,
						"DENY":   0.04,
					},
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

	client := NewDecisionClient(server.URL, "", true)
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
					Confidence: 0.45, // entropy/margin in THX-01
					Probabilities: map[string]float64{
						"OPTION_1": 0.05,
						"OPTION_2": 0.93,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, "", true)
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

	client := NewDecisionClient(server.URL, "", true)
	text, start, end, prob, err := client.DecideDoctorExcerpt(context.Background(), "запишите к терапевту Богдановой пожалуйста")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Богдановой" || start != 22 || end != 32 || prob < 0.9 {
		t.Fatalf("unexpected excerpt: text=%s, start=%d, end=%d, prob=%f", text, start, end, prob)
	}
}

func TestDecisionClient_Disabled(t *testing.T) {
	client := NewDecisionClient("", "", false)
	if client.IsEnabled() {
		t.Fatalf("client should be disabled")
	}
	_, _, _, err := client.DecideConfirmationAndAside(context.Background(), "да", "слот", "подтверждаете?")
	if err == nil {
		t.Fatalf("expected error on disabled client")
	}
}

func TestDecisionClient_AuthorizationHeader(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		resp := DecisionResponse{
			Model:   "THX-01",
			Answers: map[string]DecisionAnswer{},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, "secret-test-token-12345", true)
	_, _ = client.Decide(context.Background(), "state", map[string]QuestionSpec{})

	if receivedAuth != "Bearer secret-test-token-12345" {
		t.Fatalf("expected 'Bearer secret-test-token-12345', got %q", receivedAuth)
	}
}

func TestDecisionClient_Timeout(t *testing.T) {
	// Verify default timeout is 1500ms
	defaultClient := NewDecisionClient("", "", true)
	if defaultClient.httpClient.Timeout != 1500*time.Millisecond {
		t.Fatalf("expected default timeout 1500ms, got %v", defaultClient.httpClient.Timeout)
	}

	// Verify DECISION_TIMEOUT_MS env var
	t.Setenv("DECISION_TIMEOUT_MS", "50")
	envClient := NewDecisionClient("", "", true)
	if envClient.httpClient.Timeout != 50*time.Millisecond {
		t.Fatalf("expected env timeout 50ms, got %v", envClient.httpClient.Timeout)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewDecisionClient(server.URL, "", true)
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

	client := NewDecisionClient(server.URL, "", true)
	nlu, err := client.DecideNLU(context.Background(), "запишите к Богдановой", "меню")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nlu.Intent != "BOOKING" || nlu.DoctorName != "Богдановой" {
		t.Fatalf("unexpected nlu result: %+v", nlu)
	}
}
