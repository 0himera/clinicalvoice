package dialogue

import (
	"context"
	"strings"
	"testing"

	"github.com/himera/tochka-voice/go-core/pkg/misclient"
	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/slotmgr"
)

func TestFSMScenarioC01Booking(t *testing.T) {
	sm := slotmgr.NewSlotManager()
	mc := misclient.NewMockMISClient()
	fsm := NewFSMController(sm, mc)
	ctx := context.Background()

	session := &models.CallSession{
		CallID: "test-call-1",
		Slots:  make(map[string]interface{}),
	}

	// 1. Initial greeting
	initRes := fsm.InitialGreeting()
	if !strings.Contains(initRes.BotSpeech, "виртуальный помощник") {
		t.Fatalf("Unexpected greeting: %s", initRes.BotSpeech)
	}
	session.CurrentState = initRes.NextState

	// 2. Patient: "Запишите к терапевту"
	res2 := fsm.ProcessInput(ctx, session, "Запишите меня к терапевту")
	if res2.NextState != "STATE_AWAIT_IDENTITY" {
		t.Fatalf("Expected STATE_AWAIT_IDENTITY, got %s (speech: %s)", res2.NextState, res2.BotSpeech)
	}
	session.CurrentState = res2.NextState

	// 3. Patient: "Смирнов Алексей Викторович 14.05.1985"
	res3 := fsm.ProcessInput(ctx, session, "Смирнов Алексей Викторович 14.05.1985")
	if res3.NextState != "STATE_CHOOSE_SLOT" {
		t.Fatalf("Expected STATE_CHOOSE_SLOT, got %s (speech: %s)", res3.NextState, res3.BotSpeech)
	}
	session.CurrentState = res3.NextState

	// 4. Patient chooses first slot: "Давайте первый"
	res4 := fsm.ProcessInput(ctx, session, "Давайте первый вариант")
	if res4.NextState != "STATE_CONFIRM_BOOKING" {
		t.Fatalf("Expected STATE_CONFIRM_BOOKING, got %s (speech: %s)", res4.NextState, res4.BotSpeech)
	}
	session.CurrentState = res4.NextState

	// 5. Patient confirms: "Да, верно"
	res5 := fsm.ProcessInput(ctx, session, "Да, верно")
	if res5.NextState != "STATE_FINAL" {
		t.Fatalf("Expected STATE_FINAL, got %s (speech: %s)", res5.NextState, res5.BotSpeech)
	}
	if !strings.Contains(res5.BotSpeech, "вы записаны") {
		t.Fatalf("Expected booking confirmation speech, got: %s", res5.BotSpeech)
	}
	session.CurrentState = res5.NextState

	// 6. Patient ends call: "Нет, спасибо"
	res6 := fsm.ProcessInput(ctx, session, "Нет, спасибо")
	if !res6.ShouldHangup {
		t.Fatalf("Expected hangup signal")
	}
}

func TestFSMScenarioC07ScheduleInfo(t *testing.T) {
	sm := slotmgr.NewSlotManager()
	mc := misclient.NewMockMISClient()
	fsm := NewFSMController(sm, mc)
	ctx := context.Background()

	session := &models.CallSession{
		CallID: "test-call-2",
		Slots:  make(map[string]interface{}),
	}

	res := fsm.ProcessInput(ctx, session, "Какой у вас график работы?")
	if !strings.Contains(res.BotSpeech, "семи тридцати") {
		t.Fatalf("Expected schedule speech, got: %s", res.BotSpeech)
	}
}

func TestFSMScenarioC02ReferralCheck(t *testing.T) {
	sm := slotmgr.NewSlotManager()
	mc := misclient.NewMockMISClient()
	fsm := NewFSMController(sm, mc)
	ctx := context.Background()

	session := &models.CallSession{
		CallID: "test-call-3",
		Slots:  make(map[string]interface{}),
	}

	res := fsm.ProcessInput(ctx, session, "Хочу записаться к кардиологу")
	if !strings.Contains(res.BotSpeech, "по направлению терапевта") {
		t.Fatalf("Expected referral explanation for cardiologist, got: %s", res.BotSpeech)
	}
}
