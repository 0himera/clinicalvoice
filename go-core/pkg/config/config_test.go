package config

import "testing"

func TestDialogueDebugRequiresExplicitMockMode(t *testing.T) {
	t.Setenv("CORE_HOST", "127.0.0.1")
	t.Setenv("PORT", "8080")
	t.Setenv("MIS_API_URL", "https://example.test/api/v1")
	t.Setenv("SCHEDULE_PATH", "schedule.xlsx")
	t.Setenv("ALLOW_DEMO_CALLS_OUTSIDE_WINDOW", "false")
	t.Setenv("DEBUG_DIALOGUE", "false")
	t.Setenv("MIS_API_TOKEN", "")
	config, err := Load()
	if err != nil || config.DebugDialogue {
		t.Fatalf("debug must be off by default: %v", err)
	}
	t.Setenv("DEBUG_DIALOGUE", "true")
	config, err = Load()
	if err != nil || !config.DebugDialogue {
		t.Fatalf("explicit mock debug failed: %v", err)
	}
	t.Setenv("MIS_API_TOKEN", "stand-token")
	if _, err = Load(); err == nil {
		t.Fatal("transcripts allowed on the external MIS")
	}
}
