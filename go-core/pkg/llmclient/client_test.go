package llmclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGeminiRequiresExplicitRemoteOptInAndOfficialHTTPS(t *testing.T) {
	for _, test := range []struct {
		url            string
		allow, enabled bool
	}{
		{"https://generativelanguage.googleapis.com/v1beta/openai", false, false},
		{"https://generativelanguage.googleapis.com/v1beta/openai", true, true},
		{"http://generativelanguage.googleapis.com/v1beta/openai", true, false},
		{"https://generativelanguage.googleapis.com.evil.invalid/v1beta/openai", true, false},
		{"https://generativelanguage.googleapis.com/v1beta/openai?key=secret", true, false},
	} {
		client := NewUniversalLLMClient(Config{Provider: "gemini", BaseURL: test.url, APIKey: "test", Model: "gemini-3.5-flash-lite", AllowRemote: test.allow})
		if client.IsEnabled() != test.enabled {
			t.Errorf("incorrect Gemini endpoint policy: %s", test.url)
		}
	}
}

func TestGeminiUsesSchemaAndRejectsGeneratedSpeech(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema == nil || !request.ResponseFormat.JSONSchema.Strict || request.ReasoningEffort != "minimal" {
			t.Errorf("unconstrained Gemini request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"intent":"FAQ_INFO","bot_speech":"invented medical advice"}`}}}})
	}))
	defer server.Close()
	client := NewUniversalLLMClient(Config{Provider: "gemini", BaseURL: server.URL, APIKey: "test", Model: "fixture"})
	if _, err := client.ExtractSlots(context.Background(), "Как попасть внутрь?", "STATE_MAIN_MENU"); err == nil {
		t.Fatal("free generated speech bypassed NLU schema")
	}
}

func TestNLURedirectDoesNotSendKeyToAnotherHost(t *testing.T) {
	visited := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visited = true; _, _ = io.WriteString(w, "unexpected") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := NewUniversalLLMClient(Config{Provider: "custom", BaseURL: redirect.URL, APIKey: "private-test-key", Model: "fixture"})
	_, err := client.ExtractSlots(context.Background(), "тест", "STATE_MAIN_MENU")
	if err == nil || visited || strings.Contains(err.Error(), "private-test-key") {
		t.Fatal("redirect leaked NLU credentials")
	}
}

func TestUniversalLLMClient_Disabled(t *testing.T) {
	cfg := Config{
		Provider: "custom",
		BaseURL:  "https://example.invalid/v1",
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
