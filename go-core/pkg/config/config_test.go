package config

import "testing"

func TestDialogueDebugRequiresExplicitMockMode(t *testing.T) {
	t.Setenv("CORE_HOST", "127.0.0.1")
	t.Setenv("PORT", "8080")
	t.Setenv("CORE_API_TOKEN", "test-token-at-least-24-chars-long")
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

func TestEncryptedTranscriptsAreExplicitAndBoundedOnStand(t *testing.T) {
	t.Setenv("CORE_HOST", "127.0.0.1")
	t.Setenv("PORT", "8080")
	t.Setenv("CORE_API_TOKEN", "test-token-at-least-24-chars-long")
	t.Setenv("MIS_API_URL", "https://example.test/api/v1")
	t.Setenv("SCHEDULE_PATH", "schedule.xlsx")
	t.Setenv("MIS_API_TOKEN", "stand-token")
	t.Setenv("DEBUG_DIALOGUE", "false")
	t.Setenv("ALLOW_DEMO_CALLS_OUTSIDE_WINDOW", "false")
	t.Setenv("TRANSCRIPT_LOGGING", "false")
	t.Setenv("TRANSCRIPT_RETENTION_HOURS", "24")
	cfg, err := Load()
	if err != nil || cfg.Transcripts {
		t.Fatalf("transcripts enabled without opt-in: %v", err)
	}
	t.Setenv("TRANSCRIPT_LOGGING", "true")
	cfg, err = Load()
	if err != nil || !cfg.Transcripts || cfg.TranscriptHours != 24 {
		t.Fatalf("explicit encrypted mode rejected: %v", err)
	}
	for _, hours := range []string{"0", "25", "invalid"} {
		t.Setenv("TRANSCRIPT_RETENTION_HOURS", hours)
		if _, err := Load(); err == nil {
			t.Fatalf("unbounded retention accepted: %q", hours)
		}
	}
}

func TestDecisionModelURLMustBeLoopback(t *testing.T) {
	t.Setenv("CORE_HOST", "127.0.0.1")
	t.Setenv("PORT", "8080")
	t.Setenv("CORE_API_TOKEN", "test-token-at-least-24-chars-long")
	t.Setenv("MIS_API_URL", "https://example.test/api/v1")
	t.Setenv("SCHEDULE_PATH", "schedule.xlsx")
	t.Setenv("ALLOW_DEMO_CALLS_OUTSIDE_WINDOW", "false")
	t.Setenv("DEBUG_DIALOGUE", "false")
	t.Setenv("MIS_API_TOKEN", "")

	for _, valid := range []string{
		"http://127.0.0.1:8000/v1/decide",
		"http://localhost:8000/v1/decide",
		"http://127.0.0.2:9000/v1/decide",
	} {
		t.Setenv("DECISION_MODEL_URL", valid)
		if _, err := Load(); err != nil {
			t.Fatalf("expected %s to be valid loopback URL: %v", valid, err)
		}
	}

	for _, invalid := range []string{
		"http://example.com/v1/decide",
		"http://192.168.1.100:8000/v1/decide",
		"http://10.0.0.1:8000/v1/decide",
		"not-a-url",
	} {
		t.Setenv("DECISION_MODEL_URL", invalid)
		if _, err := Load(); err == nil {
			t.Fatalf("expected %s to be rejected as non-loopback URL", invalid)
		}
	}
}

func TestCoreAPITokenRequired(t *testing.T) {
	t.Setenv("CORE_HOST", "127.0.0.1")
	t.Setenv("PORT", "8080")
	t.Setenv("MIS_API_URL", "https://example.test/api/v1")
	t.Setenv("SCHEDULE_PATH", "schedule.xlsx")
	t.Setenv("ALLOW_DEMO_CALLS_OUTSIDE_WINDOW", "false")
	t.Setenv("DEBUG_DIALOGUE", "false")
	t.Setenv("MIS_API_TOKEN", "")

	t.Setenv("CORE_API_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("empty CORE_API_TOKEN accepted")
	}

	t.Setenv("CORE_API_TOKEN", "valid-token")
	if _, err := Load(); err != nil {
		t.Fatalf("valid CORE_API_TOKEN rejected: %v", err)
	}
}

