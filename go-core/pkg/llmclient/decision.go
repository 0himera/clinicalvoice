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
			Question: "Is the patient reporting an acute, real physical heart attack, severe vital choking or medical emergency right now, excluding negations and emotional metaphors?",
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
		"dialogue_step":       "confirm_booking",
		"last_prompt":         lastPrompt,
		"held_slot":           heldSlotDesc,
		"patient_utterance":   utterance,
	}
	questions := map[string]QuestionSpec{
		"confirmation": {
			Type:     TypeChoice,
			Question: "Does the patient confirm the proposed appointment slot?",
			Criteria: map[string]string{
				"AFFIRM":      "Yes, patient agrees and confirms appointment",
				"DENY":        "No, patient rejects appointment or cancels",
				"CHANGE_SLOT": "Patient asks for another date, day or time",
				"UNCERTAIN":   "Patient is hesitant, unsure or asks questions without confirming",
			},
		},
		"aside_intent": {
			Type:     TypeChoice,
			Question: "Did the patient ask an additional FAQ or informational question?",
			Criteria: map[string]string{
				"NONE":          "No additional question",
				"FAQ_LOCATION":  "Question about cabinet, room number, floor, or clinic address",
				"FAQ_ANALYSIS":  "Question about blood tests, preparation or fluorography",
				"FAQ_DOCUMENTS": "Question about required documents (passport, insurance policy)",
				"FAQ_PRICE":     "Question about prices, costs or paid services",
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
		"dialogue_step":      "choose_slot",
		"option_1":           opt1Desc,
		"option_2":           opt2Desc,
		"patient_preference": utterance,
	}
	questions := map[string]QuestionSpec{
		"slot_choice": {
			Type:     TypeChoice,
			Question: "Which of the two offered appointment slots does the patient want?",
			Criteria: map[string]string{
				"OPTION_1":     fmt.Sprintf("Option 1: %s", opt1Desc),
				"OPTION_2":     fmt.Sprintf("Option 2: %s", opt2Desc),
				"DECLINE_BOTH": "Patient declines both offered options or asks for a different time",
				"UNCERTAIN":    "Unclear or unrelated response",
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
		"dialogue_step":     "find_doctor",
		"patient_utterance": utterance,
	}
	questions := map[string]QuestionSpec{
		"doctor_name": {
			Type:     TypeExcerpt,
			Question: "Surname of the doctor the patient wants to visit",
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
		"dialogue_step":     state,
		"patient_utterance": text,
	}
	questions := map[string]QuestionSpec{
		"intent": {
			Type:     TypeChoice,
			Question: "What is the primary intent of the patient?",
			Criteria: map[string]string{
				"BOOKING":        "Booking an appointment with a doctor",
				"CANCEL":         "Cancelling an existing appointment",
				"RESCHEDULE":     "Rescheduling an existing appointment to another date or time",
				"CHECK_STATUS":   "Checking details or date of an existing appointment",
				"HOME_VISIT":     "Calling a doctor for a home visit",
				"DIRECTORY_INFO": "Asking about the directory of doctors and specialties",
				"FAQ_INFO":       "General FAQ (address, tests, documents, fluorography, prices)",
				"OPERATOR":       "Requesting to speak with a human operator or registrar",
				"UNKNOWN":        "Unclear request or unrelated to medical clinic services",
			},
		},
		"faq_topic": {
			Type:     TypeChoice,
			Question: "Topic of the FAQ question if applicable",
			Criteria: map[string]string{
				"none":         "Not an FAQ topic",
				"address":      "Clinic address, building, floor or how to get there",
				"documents":    "Required documents, insurance policy, passport",
				"blood_test":   "Blood test, laboratory, treatment room",
				"fluorography": "Fluorography, X-ray",
				"prices":       "Prices, costs or commercial services",
			},
		},
		"doctor_name": {
			Type:     TypeExcerpt,
			Question: "Doctor surname if mentioned",
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
