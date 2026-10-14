package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config конфигурация для любого LLM провайдера
type Config struct {
	Provider string        `json:"provider"`  // "gemini", "openai", "groq", "ollama", "deepseek", "custom"
	BaseURL  string        `json:"base_url"`  // URL эндпоинта
	APIKey   string        `json:"api_key"`   // API ключ
	Model    string        `json:"model"`     // Имя модели
	Timeout  time.Duration `json:"timeout"`   // Таймаут запроса (по умолчанию 3 сек)
}

// LoadConfigFromEnv загружает настройки из переменных окружения
func LoadConfigFromEnv() Config {
	provider := strings.ToLower(os.Getenv("LLM_PROVIDER"))
	if provider == "" {
		provider = "gemini"
	}

	apiKey := os.Getenv("LLM_API_KEY")
	baseURL := os.Getenv("LLM_BASE_URL")
	model := os.Getenv("LLM_MODEL")

	timeoutSec := 3
	if tStr := os.Getenv("LLM_TIMEOUT_SEC"); tStr != "" {
		if t, err := strconv.Atoi(tStr); err == nil && t > 0 {
			timeoutSec = t
		}
	}

	// Пресеты для популярных провайдеров
	switch provider {
	case "gemini":
		if baseURL == "" {
			baseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		if model == "" {
			model = "gemini-2.5-flash"
		}
	case "openai":
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		if model == "" {
			model = "gpt-4o-mini"
		}
	case "groq":
		if baseURL == "" {
			baseURL = "https://api.groq.com/openai/v1"
		}
		if model == "" {
			model = "llama-3.1-8b-instant" // сверхбыстрый отклик < 200 мс
		}
	case "deepseek":
		if baseURL == "" {
			baseURL = "https://api.deepseek.com/v1"
		}
		if model == "" {
			model = "deepseek-chat"
		}
	case "ollama":
		if baseURL == "" {
			baseURL = "http://localhost:11434/v1"
		}
		if model == "" {
			model = "qwen2.5:7b"
		}
	case "vllm":
		if baseURL == "" {
			baseURL = "http://localhost:8000/v1"
		}
		if model == "" {
			model = "default"
		}
	}

	return Config{
		Provider: provider,
		BaseURL:  strings.TrimRight(baseURL, "/"),
		APIKey:   apiKey,
		Model:    model,
		Timeout:  time.Duration(timeoutSec) * time.Second,
	}
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model          string        `json:"model"`
	Messages       []ChatMessage `json:"messages"`
	Temperature    float32       `json:"temperature"`
	ResponseFormat *RespFormat   `json:"response_format,omitempty"`
}

type RespFormat struct {
	Type string `json:"type"` // "json_object"
}

type ChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ExtractedNLU структурированный результат извлечения слотов из реплики
type ExtractedNLU struct {
	Intent            string `json:"intent"`             // BOOKING, RESCHEDULE, CANCEL, CHECK_STATUS, SCHEDULE_INFO, AFFIRM, DENY, UNKNOWN
	Specialty         string `json:"specialty"`          // терапевт, педиатр, лор, офтальмолог, кардиолог и т.д.
	LastName          string `json:"last_name"`          // Иванов
	FirstName         string `json:"first_name"`         // Иван
	MiddleName        string `json:"middle_name"`        // Иванович
	BirthDate         string `json:"birth_date"`         // ДД.ММ.ГГГГ
	TargetTimePref    string `json:"target_time_pref"`   // "утро", "после обеда", "в пятницу"
	IsEmergency       bool   `json:"is_emergency"`       // признак острой боли / угрозы жизни
	OperatorRequested bool   `json:"operator_requested"` // просит живого человека
}

type UniversalLLMClient struct {
	config     Config
	httpClient *http.Client
}

func NewUniversalLLMClient(cfg Config) *UniversalLLMClient {
	return &UniversalLLMClient{
		config: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (c *UniversalLLMClient) IsEnabled() bool {
	// Для локальных Ollama/vLLM ключ не обязателен
	if c.config.Provider == "ollama" || c.config.Provider == "vllm" {
		return c.config.BaseURL != ""
	}
	return c.config.APIKey != "" && c.config.BaseURL != ""
}

// Chat выполняет базовый чат-запрос к любому OpenAI-совместимому API
func (c *UniversalLLMClient) Chat(ctx context.Context, messages []ChatMessage, jsonMode bool) (string, error) {
	if !c.IsEnabled() {
		return "", fmt.Errorf("LLM клиент не настроен (отсутствует API ключ или BaseURL)")
	}

	reqBody := ChatRequest{
		Model:       c.config.Model,
		Messages:    messages,
		Temperature: 0.1, // минимальная температура для стабильности
	}
	if jsonMode {
		reqBody.ResponseFormat = &RespFormat{Type: "json_object"}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := c.config.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	tStart := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		log.Printf("[LLM-ERROR] Ошибка запроса к %s (%s): %v\n", c.config.Provider, c.config.Model, err)
		return "", fmt.Errorf("ошибка запроса к %s (%s): %w", c.config.Provider, c.config.Model, err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[LLM-ERROR] HTTP %d: %s\n", resp.StatusCode, string(respData))
		return "", fmt.Errorf("LLM API вернул ошибку HTTP %d: %s", resp.StatusCode, string(respData))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respData, &chatResp); err != nil {
		return "", fmt.Errorf("ошибка парсинга ответа LLM: %w", err)
	}

	if chatResp.Error != nil && chatResp.Error.Message != "" {
		return "", fmt.Errorf("LLM ошибка: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("пустой ответ choices от LLM")
	}

	resContent := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	log.Printf("[LLM-OK] %s (%s) ответил за %v | %d символов\n", c.config.Provider, c.config.Model, time.Since(tStart), len(resContent))
	return resContent, nil
}

// ExtractSlots извлекает намерения и сущности из живой речи пациента
func (c *UniversalLLMClient) ExtractSlots(ctx context.Context, userSpeech string, currentState string) (*ExtractedNLU, error) {
	systemPrompt := `Ты — специализированный NLU-экстрактор для голосового ассистента поликлиники № 7.
Твоя задача — извлечь данные из реплики пациента и вернуть СТРОГО JSON следующего формата:
{
  "intent": "BOOKING" | "RESCHEDULE" | "CANCEL" | "CHECK_STATUS" | "SCHEDULE_INFO" | "AFFIRM" | "DENY" | "UNKNOWN",
  "specialty": "терапевт" | "педиатр" | "оториноларинголог" | "офтальмолог" | "хирург" | "акушер-гинеколог" | "дерматовенеролог" | "травматолог-ортопед" | "невролог" | "кардиолог" | "гастроэнтеролог" | "уролог" | "эндокринолог" | "",
  "last_name": "",
  "first_name": "",
  "middle_name": "",
  "birth_date": "ДД.ММ.ГГГГ" (если названа),
  "target_time_pref": "утро" | "вечер" | "пятница" | "",
  "is_emergency": false (true если есть острая боль, инфаркт, задыхается, травма),
  "operator_requested": false (true если просит позвать человека/оператора)
}
Отвечай ТОЛЬКО валидным JSON без markdown-кавычек.`

	userMsg := fmt.Sprintf("Текущее состояние диалога: %s\nРеплика пациента: \"%s\"", currentState, userSpeech)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}

	content, err := c.Chat(ctx, messages, true)
	if err != nil {
		return nil, err
	}

	// Очищаем от возможных markdown обрамлений
	cleaned := strings.TrimPrefix(content, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var nlu ExtractedNLU
	if err := json.Unmarshal([]byte(cleaned), &nlu); err != nil {
		return nil, fmt.Errorf("ошибка десериализации JSON NLU: %w (ответ был: %s)", err, cleaned)
	}

	return &nlu, nil
}

// AnswerFAQ генерирует лаконичный ответ на частый вопрос по базе регламента
func (c *UniversalLLMClient) AnswerFAQ(ctx context.Context, question string) (string, error) {
	systemPrompt := `Ты — голосовой ассистент поликлиники № 7. 
Отвечай на вопрос пациента по регламенту поликлиники:
- Поликлиника работает: будни 7:30–20:00, сб 9:00–15:00 (дежурные терапевт и педиатр), вс выходной.
- Забор крови (процедурный): будни 7:30–10:30, сб 9:00–11:00.
- Адрес: ул. Центральная, д. 7.
- Что взять на прием: паспорт и полис ОМС.
- Документы для прикрепления: паспорт, полис ОМС, СНИЛС (в регистратуру или через Госуслуги).
Правило: ответ должен быть коротким, вежливым, НЕ БОЛЕЕ 20 СЛОВ.`

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: question},
	}

	ans, err := c.Chat(ctx, messages, false)
	if err != nil {
		return "", err
	}
	return ans, nil
}
