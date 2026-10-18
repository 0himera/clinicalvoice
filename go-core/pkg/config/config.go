package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Address              string
	APIToken             string
	Database             string
	Schedule             string
	MISToken             string
	MISURL               string
	AMIAddress           string
	AMIUser              string
	AMISecret            string
	DemoCalls            bool
	DebugDialogue        bool
	DebugLogDir          string
	TechnicalLogDir      string
	Transcripts          bool
	TranscriptDir        string
	TranscriptKey        string
	TranscriptHours      int
	AIAPIToken           string
	DecisionModelURL     string
	DecisionModelEnabled bool
}

// LoadDotEnv accepts literal KEY=VALUE lines. Existing process variables win;
// shell substitution and executable configuration are deliberately unsupported.
func LoadDotEnv(filename string) error {
	f, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" {
			return fmt.Errorf("invalid environment line")
		}
		for _, r := range k {
			if !(r == '_' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return fmt.Errorf("invalid environment key")
			}
		}
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		if _, exists := os.LookupEnv(k); !exists {
			if err = os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	return s.Err()
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func Load() (Config, error) {
	c := Config{Address: net.JoinHostPort(env("CORE_HOST", "127.0.0.1"), env("PORT", "8080")), APIToken: os.Getenv("CORE_API_TOKEN"), Database: env("DATABASE_PATH", "var/tochka.db"), Schedule: os.Getenv("SCHEDULE_PATH"), MISToken: os.Getenv("MIS_API_TOKEN"), MISURL: env("MIS_API_URL", "https://crm-med-mock.dbi-testing.ru/api/v1"), AMIAddress: env("ASTERISK_AMI_ADDRESS", "127.0.0.1:5038"), AMIUser: env("ASTERISK_AMI_USER", "tochka"), AMISecret: os.Getenv("ASTERISK_AMI_SECRET"), DemoCalls: os.Getenv("ALLOW_DEMO_CALLS_OUTSIDE_WINDOW") == "true"}
	c.DebugDialogue = os.Getenv("DEBUG_DIALOGUE") == "true"
	c.DebugLogDir = env("DEBUG_LOG_DIR", "var/debug")
	c.TechnicalLogDir = env("TECHNICAL_LOG_DIR", "var/log")
	c.Transcripts = os.Getenv("TRANSCRIPT_LOGGING") == "true"
	c.TranscriptDir = env("TRANSCRIPT_LOG_DIR", "var/transcripts")
	c.TranscriptKey = env("TRANSCRIPT_KEY_PATH", "var/secrets/transcripts.key")
	c.AIAPIToken = os.Getenv("AI_API_TOKEN")
	c.DecisionModelURL = env("DECISION_MODEL_URL", "http://127.0.0.1:8000/v1/decide")
	c.DecisionModelEnabled = os.Getenv("DECISION_MODEL_ENABLED") != "false"
	c.TranscriptHours = 24
	if raw := os.Getenv("TRANSCRIPT_RETENTION_HOURS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 24 {
			return c, errors.New("TRANSCRIPT_RETENTION_HOURS must be between 1 and 24")
		}
		c.TranscriptHours = value
	}
	if c.Transcripts && c.DebugDialogue {
		return c, errors.New("choose encrypted transcripts or local plaintext debugging, not both")
	}
	if c.DebugDialogue && c.MISToken != "" {
		return c, errors.New("dialogue transcript debugging is available only with the local mock")
	}
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil {
		return c, err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return c, fmt.Errorf("PORT must be between 1 and 65535")
	}
	if strings.TrimSpace(c.APIToken) == "" {
		return c, errors.New("CORE_API_TOKEN is required")
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) && len(c.APIToken) < 24 {
		return c, fmt.Errorf("CORE_API_TOKEN of at least 24 characters is required for a non-loopback bind")
	}
	u, err := url.Parse(c.MISURL)
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return c, fmt.Errorf("MIS_API_URL must be HTTPS")
	}
	if c.DemoCalls && c.MISToken != "" {
		return c, fmt.Errorf("demo calling-window override is forbidden with MIS_API_TOKEN")
	}
	if c.Schedule == "" {
		candidates := []string{"data/*.xlsx", "../data/*.xlsx", "../../data/*.xlsx"}
		var paths []string
		for _, pattern := range candidates {
			p, e := filepath.Glob(pattern)
			if e == nil && len(p) > 0 {
				paths = p
				break
			}
		}
		if len(paths) == 0 {
			return c, fmt.Errorf("set SCHEDULE_PATH to the organizer's workbook (checked data/*.xlsx, ../data/*.xlsx)")
		}
		if len(paths) != 1 {
			return c, errors.New("multiple workbooks found; set SCHEDULE_PATH explicitly")
		}
		c.Schedule = paths[0]
	}
	if c.DecisionModelURL != "" {
		du, err := url.Parse(c.DecisionModelURL)
		if err != nil || du.Host == "" {
			return c, fmt.Errorf("invalid DECISION_MODEL_URL")
		}
		host := du.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return c, fmt.Errorf("DECISION_MODEL_URL host must be a loopback address (127.0.0.1 or localhost)")
		}
	}
	return c, nil
}
