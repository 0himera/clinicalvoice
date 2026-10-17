package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DecisionType represents the type of question evaluated by the decision model.
type DecisionType string

const (
	TypeChoice  DecisionType = "choice"
	TypeNoul    DecisionType = "noul"
	TypeExcerpt DecisionType = "excerpt"
	TypeNumber  DecisionType = "number"
)

// QuestionSpec defines a typed question evaluated by the System-1 model.
type QuestionSpec struct {
	Type     DecisionType      `json:"type"`
	Question string            `json:"question"`
	Criteria map[string]string `json:"criteria,omitempty"`
}

// DecisionRequest holds the dialogue context and typed questions.
type DecisionRequest struct {
	State     any                     `json:"state"`
	Questions map[string]QuestionSpec `json:"questions"`
}

// DecisionAnswer is the structured evaluation of a single typed question.
type DecisionAnswer struct {
	Type          DecisionType       `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Noul          float64            `json:"noul,omitempty"`        // Calibrated P(true) for boolean questions
	Text          string             `json:"text,omitempty"`        // Span text for excerpt
	Start         int                `json:"start,omitempty"`       // Start byte/char index
	End           int                `json:"end,omitempty"`         // End byte/char index
	Probability   float64            `json:"probability,omitempty"` // For excerpt
	Value         float64            `json:"value,omitempty"`       // For number
}

// DecisionResponse is returned by /v1/decide.
type DecisionResponse struct {
	Model     string                    `json:"model"`
	Answers   map[string]DecisionAnswer `json:"answers"`
	LatencyMS float64                   `json:"latency_ms"`
}

// DecisionClient interacts with the local System-1 decision sidecar (e.g. THX-01 / mmBERT).
type DecisionClient struct {
	endpoint   string
	enabled    bool
	httpClient *http.Client
}

// NewDecisionClient initializes the DecisionClient with a strict timeout (250ms).
func NewDecisionClient(endpoint string, enabled bool) *DecisionClient {
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8000/v1/decide"
	}
	return &DecisionClient{
		endpoint: endpoint,
		enabled:  enabled,
		httpClient: &http.Client{
			Timeout: 250 * time.Millisecond,
		},
	}
}

// IsEnabled reports whether the decision client is active.
func (c *DecisionClient) IsEnabled() bool {
	return c != nil && c.enabled && c.endpoint != ""
}

// Decide performs a raw POST /v1/decide request.
func (c *DecisionClient) Decide(ctx context.Context, state any, questions map[string]QuestionSpec) (*DecisionResponse, error) {
	if !c.IsEnabled() {
		return nil, fmt.Errorf("decision client is disabled")
	}
	reqBody := DecisionRequest{
		State:     state,
		Questions: questions,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("decision endpoint returned HTTP %d", resp.StatusCode)
	}

	var dResp DecisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&dResp); err != nil {
		return nil, err
	}
	return &dResp, nil
}

// DecideVitalThreat evaluates whether an utterance expresses a genuine medical emergency or a false alarm
// (e.g. emotional metaphor "сердце кровью обливается" or negation "я не задыхаюсь").
// In accordance with fail-closed safety, if the decision model is unavailable or errors, it returns isThreat=true.
func (c *DecisionClient) DecideVitalThreat(ctx context.Context, utterance string) (isThreat bool, pTrue float64, err error) {
	if !c.IsEnabled() {
		// Fail-closed: assume threat if client disabled
		return true, 1.0, nil
	}
	state := map[string]string{
		"шаг_диалога":     "проверка_безопасности",
		"реплика_пациента": utterance,
	}
	questions := map[string]QuestionSpec{
		"vital_threat": {
			Type:     TypeNoul,
			Question: "Сообщает ли пациент о непосредственном физическом приступе боли в сердце, инфаркте или удушье прямо сейчас, исключая эмоциональные метафоры и отрицания?",
		},
	}
	resp, err := c.Decide(ctx, state, questions)
	if err != nil {
		// Fail-closed: treat as real emergency if model fails or times out
		return true, 1.0, err
	}
	ans, ok := resp.Answers["vital_threat"]
	if !ok {
		return true, 1.0, nil
	}
	pTrue = ans.Noul
	// Calibrated threshold: if P(true) < 0.15, it is a proven false alarm / metaphor
	if pTrue < 0.15 {
		return false, pTrue, nil
	}
	return true, pTrue, nil
}

// DecideConfirmationAndAside resolves composite utterances in confirmation states:
// evaluates whether the patient confirms the held slot, and whether they asked an aside FAQ question.
func (c *DecisionClient) DecideConfirmationAndAside(ctx context.Context, utterance, heldSlotDesc, lastPrompt string) (confirmChoice, asideChoice string, confirmConf float64, err error) {
	if !c.IsEnabled() {
		return "", "", 0, fmt.Errorf("decision client is disabled")
	}
	state := map[string]string{
		"шаг_диалога":           "подтверждение_записи",
		"последний_вопрос_бота": lastPrompt,
		"предложенный_талон":    heldSlotDesc,
		"ответ_пациента":        utterance,
	}
	questions := map[string]QuestionSpec{
		"confirmation": {
			Type:     TypeChoice,
			Question: "Подтверждает ли пациент предложенную запись на приём?",
			Criteria: map[string]string{
				"AFFIRM":      "Согласие подтвердить текущий талон, готовность прийти",
				"DENY":        "Отказ от предложенного талона или отмена",
				"CHANGE_SLOT": "Просьба подобрать другое время или день",
				"UNCERTAIN":   "Неуверенность, встречный вопрос или сомнение",
			},
		},
		"aside_intent": {
			Type:     TypeChoice,
			Question: "Содержит ли реплика дополнительный справочный вопрос?",
			Criteria: map[string]string{
				"NONE":          "Дополнительного справочного вопроса нет",
				"FAQ_LOCATION":  "Вопрос о кабинете, этаже, адресе поликлиники или как добраться",
				"FAQ_ANALYSIS":  "Вопрос об анализах, флюорографии или подготовке к приёму",
				"FAQ_DOCUMENTS": "Вопрос о необходимых документах (паспорт, полис)",
				"FAQ_PRICE":     "Вопрос о стоимости, ценах или платных услугах",
			},
		},
	}

	resp, err := c.Decide(ctx, state, questions)
	if err != nil {
		return "", "", 0, err
	}
	confAns := resp.Answers["confirmation"]
	asideAns := resp.Answers["aside_intent"]
	return confAns.Choice, asideAns.Choice, confAns.Confidence, nil
}

// DecideSlotChoice resolves semantic preference when two options are presented
// (e.g. "тот который попозже", "второй вариант", "лучше после обеда").
func (c *DecisionClient) DecideSlotChoice(ctx context.Context, utterance, opt1Desc, opt2Desc string) (chosenIndex int, conf float64, err error) {
	if !c.IsEnabled() {
		return -1, 0, fmt.Errorf("decision client is disabled")
	}
	state := map[string]string{
		"шаг_диалога":           "выбор_слота",
		"вариант_1":             opt1Desc,
		"вариант_2":             opt2Desc,
		"предпочтение_пациента": utterance,
	}
	questions := map[string]QuestionSpec{
		"slot_choice": {
			Type:     TypeChoice,
			Question: "Какой из двух предложенных вариантов приёма выбирает пациент?",
			Criteria: map[string]string{
				"OPTION_1":     fmt.Sprintf("Первый вариант: %s", opt1Desc),
				"OPTION_2":     fmt.Sprintf("Второй вариант: %s", opt2Desc),
				"DECLINE_BOTH": "Отказ от обоих предложенных вариантов или просьба другого времени",
				"UNCERTAIN":    "Непонятно или не относится к выбору",
			},
		},
	}

	resp, err := c.Decide(ctx, state, questions)
	if err != nil {
		return -1, 0, err
	}
	ans := resp.Answers["slot_choice"]
	switch ans.Choice {
	case "OPTION_1":
		return 0, ans.Confidence, nil
	case "OPTION_2":
		return 1, ans.Confidence, nil
	default:
		return -1, ans.Confidence, nil
	}
}

// DecideDoctorExcerpt extracts mentioned doctor name substring along with start and end character offsets.
func (c *DecisionClient) DecideDoctorExcerpt(ctx context.Context, utterance string) (text string, start, end int, conf float64, err error) {
	if !c.IsEnabled() {
		return "", -1, -1, 0, fmt.Errorf("decision client is disabled")
	}
	state := map[string]string{
		"шаг_диалога":      "поиск_врача",
		"реплика_пациента": utterance,
	}
	questions := map[string]QuestionSpec{
		"doctor_name": {
			Type:     TypeExcerpt,
			Question: "Фамилия или специальность врача, к которому обращается пациент",
		},
	}
	resp, err := c.Decide(ctx, state, questions)
	if err != nil {
		return "", -1, -1, 0, err
	}
	ans := resp.Answers["doctor_name"]
	return ans.Text, ans.Start, ans.End, ans.Probability, nil
}

// DecideNLU evaluates user intent, doctor name excerpt, and FAQ topic in a single forward pass.
func (c *DecisionClient) DecideNLU(ctx context.Context, text, state string) (*ExtractedNLU, error) {
	if !c.IsEnabled() {
		return nil, fmt.Errorf("decision client is disabled")
	}
	dState := map[string]string{
		"шаг_диалога":      state,
		"реплика_пациента": text,
	}
	questions := map[string]QuestionSpec{
		"intent": {
			Type:     TypeChoice,
			Question: "Основное намерение пациента",
			Criteria: map[string]string{
				"BOOKING":        "Запись на приём к врачу или выбор специалиста",
				"CANCEL":         "Отмена существующей записи на приём",
				"RESCHEDULE":     "Перенос существующей записи на другую дату или время",
				"CHECK_STATUS":   "Проверка деталей, даты или времени существующей записи",
				"HOME_VISIT":     "Вызов врача на дом",
				"DIRECTORY_INFO": "Справочный вопрос о списке врачей и специальностей",
				"FAQ_INFO":       "Справочный вопрос (адрес, анализы, документы, флюорография, цены)",
				"OPERATOR":       "Просьба соединить с оператором или регистратором",
				"UNKNOWN":        "Непонятный запрос или не относится к медицинским услугам",
			},
		},
		"faq_topic": {
			Type:     TypeChoice,
			Question: "Тема справочного вопроса",
			Criteria: map[string]string{
				"none":         "Не относится к FAQ",
				"address":      "Адрес поликлиники, этаж, как добраться",
				"documents":    "Документы для приёма, полис ОМС, паспорт, прикрепление",
				"blood_test":   "Сдача анализов, забор крови, процедурный кабинет",
				"fluorography": "Флюорография или рентген",
				"prices":       "Стоимость услуг или платный приём",
			},
		},
		"doctor_name": {
			Type:     TypeExcerpt,
			Question: "Фамилия врача, если названа",
		},
	}

	resp, err := c.Decide(ctx, dState, questions)
	if err != nil {
		return nil, err
	}

	nlu := &ExtractedNLU{
		Intent: "UNKNOWN",
	}

	if intentAns, ok := resp.Answers["intent"]; ok {
		if intentAns.Confidence >= 0.50 && intentAns.Choice != "UNKNOWN" {
			nlu.Intent = intentAns.Choice
			if intentAns.Choice == "OPERATOR" {
				nlu.OperatorRequested = true
			}
		}
	}

	if faqAns, ok := resp.Answers["faq_topic"]; ok {
		if faqAns.Choice != "" && faqAns.Choice != "none" {
			nlu.FAQTopic = faqAns.Choice
		}
	}

	if docAns, ok := resp.Answers["doctor_name"]; ok {
		if docAns.Text != "" && docAns.Probability >= 0.50 {
			nlu.DoctorName = docAns.Text
		}
	}

	return nlu, nil
}
