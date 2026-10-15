package dialogue

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/llmclient"
)

func withNLU(t *testing.T, r *rig, response llmclient.ExtractedNLU, status int) *atomic.Int32 {
	t.Helper()
	count := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		count.Add(1)
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		payload, _ := json.Marshal(response)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(payload)}}}})
	}))
	t.Cleanup(server.Close)
	r.fsm.SetLLMClient(llmclient.NewUniversalLLMClient(llmclient.Config{Provider: "custom", BaseURL: server.URL, APIKey: "fixture", Model: "fixture", Timeout: time.Second}))
	return count
}

func TestNLUFallbackRoutesValidatedIntentToRealHandlers(t *testing.T) {
	for _, test := range []struct {
		name, text, state, speech string
		nlu                       llmclient.ExtractedNLU
	}{
		{"directory", "Сориентируйте, к кому вообще можно обратиться?", StateDirectory, "терапевт", llmclient.ExtractedNLU{Intent: "DIRECTORY_INFO"}},
		{"address", "На какой улице вас искать?", StateFinal, "Центральная", llmclient.ExtractedNLU{Intent: "FAQ_INFO", FAQTopic: "address"}},
		{"booking", "Хочу консультацию у доктора по общему здоровью", StateIdentity, "фамилию", llmclient.ExtractedNLU{Intent: "BOOKING", Specialty: "терапевт"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newRig(t)
			count := withNLU(t, r, test.nlu, 200)
			result := r.turn(test.text)
			if !result.LLMUsed || result.NextState != test.state || !strings.Contains(result.BotSpeech, test.speech) || count.Load() != 1 || r.call.FailCount != 0 {
				t.Fatalf("NLU route failed: %+v, requests %d", result, count.Load())
			}
			if len(r.visits(t)) != 0 {
				t.Fatal("NLU classification created a visit")
			}
		})
	}
}

func TestNLUFallbackCannotAuthorizeConfirmationOrBypassSafety(t *testing.T) {
	r := newRig(t)
	count := withNLU(t, r, llmclient.ExtractedNLU{Intent: "AFFIRM"}, 200)
	r.ready()
	result := r.turn("Поступайте как знаете")
	if result.NextState != StateConfirmBooking || len(r.visits(t)) != 0 || count.Load() != 0 || result.LLMUsed {
		t.Fatal("model authorized a booking confirmation")
	}
	result = r.turn("Не могу дышать")
	if result.TransferTarget != "104" || count.Load() != 0 {
		t.Fatal("safety waited for NLU")
	}
}

func TestNLUInformationDuringIdentityKeepsPatientFieldsUntouched(t *testing.T) {
	r := newRig(t)
	withNLU(t, r, llmclient.ExtractedNLU{Intent: "FAQ_INFO", FAQTopic: "address"}, 200)
	r.turn("Запишите к терапевту")
	result := r.turn("Как удобнее добраться до вашей двери?")
	if result.NextState != StateIdentity || r.call.LastName != "" || r.call.FirstName != "" || r.call.Patient != nil || !strings.Contains(result.BotSpeech, "Центральная") {
		t.Fatalf("FAQ corrupted identity: %+v", result)
	}
}

func TestNLUFailureAsksForClarificationAndRejectsInventedSpecialty(t *testing.T) {
	r := newRig(t)
	withNLU(t, r, llmclient.ExtractedNLU{}, 503)
	result := r.turn("Непонятная необычная формулировка")
	if result.LLMUsed || r.call.FailCount != 1 || r.call.NLUError == "" || len(r.visits(t)) != 0 {
		t.Fatalf("provider error did not fail safely: %+v", result)
	}
	r = newRig(t)
	withNLU(t, r, llmclient.ExtractedNLU{Intent: "BOOKING", Specialty: "волшебник"}, 200)
	r.turn("Хочу необычную консультацию")
	result = r.turn("Иванов Иван Иванович 12.03.1985")
	if result.NextState != StateSpecialty || r.call.Specialty != "" {
		t.Fatalf("invented specialty accepted: %+v", result)
	}
}

func TestNLUThirdPartyLookupStillRequiresRepresentative(t *testing.T) {
	r := newRig(t)
	withNLU(t, r, llmclient.ExtractedNLU{Intent: "CHECK_STATUS", LastName: "Иванов", FirstName: "Иван", BirthDate: "12.03.1985"}, 200)
	r.call.ThirdParty = true
	result := r.turn("Назначенный ему день хочу уточнить")
	if result.NextState != StateTransfer || result.ReasonCode != "R-07" || r.call.Patient != nil {
		t.Fatalf("NLU bypassed third-party identity check: %+v", result)
	}
}

func TestOneNLURequestCarriesExplicitIdentityThroughBookingIntent(t *testing.T) {
	r := newRig(t)
	count := withNLU(t, r, llmclient.ExtractedNLU{Intent: "BOOKING", Specialty: "терапевт", LastName: "Иванов", FirstName: "Иван", MiddleName: "Иванович", BirthDate: "12.03.1985"}, 200)
	result := r.turn("Мне нужна консультация, Иванов Иван Иванович, рожден двенадцатого марта восемьдесят пятого")
	if result.NextState != StateChooseSlot || r.call.Patient == nil || r.call.Patient.ID != 1 || count.Load() != 1 || !result.LLMUsed {
		t.Fatalf("extracted identity lost or re-requested: %+v, count %d", result, count.Load())
	}
	if len(r.visits(t)) != 0 {
		t.Fatal("NLU extracted fields created an unconfirmed visit")
	}
}

func TestNLUCannotInventNamesNotPresentInSpeech(t *testing.T) {
	r := newRig(t)
	withNLU(t, r, llmclient.ExtractedNLU{Intent: "UNKNOWN", LastName: "Выдумкин", FirstName: "Несуществующий", BirthDate: "12.03.1985"}, 200)
	r.turn("Записать к врачу")
	result := r.turn("Родился двенадцатого марта восемьдесят пятого")
	if result.NextState != StateIdentity || r.call.Patient != nil || r.call.LastName != "" || r.call.FirstName != "" {
		t.Fatalf("hallucinated identity accepted: %+v", result)
	}
}
