package llmclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema == nil || !request.ResponseFormat.JSONSchema.Strict || request.ReasoningEffort != "minimal" || request.MaxTokens != defaultMaxTokens {
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
				FinishReason string `json:"finish_reason"`
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
				FinishReason string `json:"finish_reason"`
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

func TestOllamaDisablesThinkingAndBoundsResponse(t *testing.T) {
	for _, budget := range []int{0, 256, 384} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				expected := budget
				if expected == 0 {
					expected = defaultMaxTokens
				}
				if request.ReasoningEffort != "none" || request.MaxTokens != expected {
					t.Errorf("unbounded or thinking-enabled NLU: effort=%q max_tokens=%d", request.ReasoningEffort, request.MaxTokens)
				}
				if request.ResponseFormat == nil || request.ResponseFormat.JSONSchema == nil || !request.ResponseFormat.JSONSchema.Strict {
					t.Error("missing strict NLU schema")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"intent":"DIRECTORY_INFO"}`}, "finish_reason": "stop"}}})
			}))
			defer server.Close()
			client := NewUniversalLLMClient(Config{Provider: "ollama", BaseURL: server.URL, Model: "gemma4:e4b", MaxTokens: budget})
			nlu, err := client.ExtractSlots(context.Background(), "К кому можно обратиться?", "STATE_MAIN_MENU")
			if err != nil || nlu.Intent != "DIRECTORY_INFO" {
				t.Fatalf("unexpected NLU result: %v, %v", nlu, err)
			}
		})
	}
}

func TestTruncatedNLUIsRejectedEvenWhenJSONIsValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"intent":"BOOKING","specialty":"терапевт"}`}, "finish_reason": "length"}}})
	}))
	defer server.Close()
	client := NewUniversalLLMClient(Config{Provider: "ollama", BaseURL: server.URL, Model: "gemma4:e4b"})
	if _, err := client.ExtractSlots(context.Background(), "Запишите меня", "STATE_MAIN_MENU"); err == nil || !strings.Contains(err.Error(), "token limit") {
		t.Fatalf("truncated generation accepted: %v", err)
	}
}

func TestLoadOllamaConfigAndResponseBudget(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "ollama")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("LLM_TIMEOUT_SEC", "12")
	for _, test := range []struct {
		raw      string
		expected int
		valid    bool
	}{
		{"", 256, true}, {"192", 192, true}, {"256", 256, true}, {"0", -1, false}, {"-1", -1, false}, {"oops", -1, false}, {"4097", 4097, false},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Setenv("LLM_MAX_TOKENS", test.raw)
			cfg := LoadConfigFromEnv()
			if cfg.Provider != "ollama" || cfg.Model != "gemma4:e4b" || cfg.BaseURL != "http://127.0.0.1:11434/v1" || cfg.Timeout != 12*time.Second || cfg.MaxTokens != test.expected {
				t.Errorf("incorrect local NLU settings: provider=%q model=%q base=%q timeout=%v max_tokens=%d", cfg.Provider, cfg.Model, cfg.BaseURL, cfg.Timeout, cfg.MaxTokens)
			}
			if err := NewUniversalLLMClient(cfg).Validate(); (err == nil) != test.valid {
				t.Errorf("unexpected validation: %v", err)
			}
		})
	}
}

type capturingRemoteTransport struct {
	calls int
	body  string
}

func (c *capturingRemoteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls++
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	c.body = string(body)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"intent\":\"DIRECTORY_INFO\"}"},"finish_reason":"stop"}]}`))}, nil
}

func TestRemoteClientRejectsPrivateFirstUtteranceBeforeHTTP(t *testing.T) {
	transport := &capturingRemoteTransport{}
	client := NewUniversalLLMClient(Config{Provider: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", APIKey: "fixture", Model: "fixture", AllowRemote: true})
	client.httpClient.Transport = transport
	for _, speech := range []string{"Запишите Иванова Ивана Ивановича", "иванов иван иванович двенадцатого марта", "Ли Вэй", "родился 12.03.1985"} {
		if _, err := client.ExtractSlots(context.Background(), speech, "STATE_MAIN_MENU"); err == nil {
			t.Fatalf("PII sent: %s", speech)
		}
	}
	if transport.calls != 0 {
		t.Fatal("private first utterance reached HTTP")
	}
	if _, err := client.ExtractSlots(context.Background(), "К кому вообще можно у вас обратиться?", "STATE_MAIN_MENU"); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || strings.Contains(transport.body, "Иванов") {
		t.Fatal("invalid remote payload")
	}
}
