package llmclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUniversalLLMClient_Disabled(t *testing.T) {
	cfg := Config{
		Provider: "gemini",
		BaseURL:  "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKey:   "", // empty key
	}
	client := NewUniversalLLMClient(cfg)
	if client.IsEnabled() {
		t.Fatalf("Expected client to be disabled with empty API key")
	}

	_, err := client.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "привет"}}, false)
	if err == nil {
		t.Fatalf("Expected error when calling disabled client")
	}
}

func TestUniversalLLMClient_MockChat(t *testing.T) {
	// Создаем мок HTTP сервер, имитирующий OpenAI-совместимый API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("Unexpected auth header: %s", auth)
		}

		resp := ChatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{
					Message: struct {
						Content string `json:"content"`
					}{
						Content: "Здравствуйте! Чем помочь?",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{
		Provider: "custom",
		BaseURL:  server.URL,
		APIKey:   "test-key",
		Model:    "test-model",
		Timeout:  2 * time.Second,
	}

	client := NewUniversalLLMClient(cfg)
	if !client.IsEnabled() {
		t.Fatalf("Expected client to be enabled")
	}

	ans, err := client.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "Здравствуйте"}}, false)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if ans != "Здравствуйте! Чем помочь?" {
		t.Errorf("Unexpected response content: %s", ans)
	}
}

func TestUniversalLLMClient_ExtractSlotsMock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mockNLU := ExtractedNLU{
			Intent:         "BOOKING",
			Specialty:      "терапевт",
			LastName:       "Иванов",
			FirstName:      "Иван",
			MiddleName:     "Иванович",
			BirthDate:      "15.04.1985",
			TargetTimePref: "утро",
		}
		jsonBytes, _ := json.Marshal(mockNLU)

		resp := ChatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{
					Message: struct {
						Content string `json:"content"`
					}{
						Content: string(jsonBytes),
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{
		Provider: "custom",
		BaseURL:  server.URL,
		APIKey:   "test-key",
		Model:    "test-model",
		Timeout:  2 * time.Second,
	}

	client := NewUniversalLLMClient(cfg)
	nlu, err := client.ExtractSlots(context.Background(), "Запишите меня к терапевту, Иванов Иван Иванович, 15.04.1985 на утро", "STATE_MAIN_MENU")
	if err != nil {
		t.Fatalf("ExtractSlots failed: %v", err)
	}

	if nlu.Intent != "BOOKING" || nlu.Specialty != "терапевт" || nlu.LastName != "Иванов" || nlu.BirthDate != "15.04.1985" {
		t.Errorf("Unexpected extracted NLU: %+v", nlu)
	}
}
