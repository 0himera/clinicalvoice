package dialogue

import (
	"strings"
	"testing"
)

func TestGreetingAndSchedule(t *testing.T) {
	r := newRig(t)
	if !strings.Contains(r.fsm.InitialGreeting().BotSpeech, "виртуальный помощник") {
		t.Fatal("assistant identity missing")
	}
	if res := r.turn("Какой у вас график работы?"); !strings.Contains(res.BotSpeech, "семи тридцати") {
		t.Fatalf("schedule missing: %+v", res)
	}
}
