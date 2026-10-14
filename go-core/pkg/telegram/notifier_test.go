package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotifier_UnconfiguredMock(t *testing.T) {
	n := NewNotifier(Config{})
	if n.IsConfigured() {
		t.Fatalf("Expected notifier to not be configured")
	}

	err := n.NotifyCategoryA(context.Background(), "call-1", "+79991234567", "давит в груди")
	if err != nil {
		t.Fatalf("Mock notification should not fail, got: %v", err)
	}
}

func TestNotifier_ConfiguredSend(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	n := NewNotifier(Config{
		BotToken: "test-token",
		ChatID:   "12345678",
	})
	// Подменяем baseURL через кастомный RoundTripper для теста
	n.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})

	err := n.NotifyCategoryA(context.Background(), "call-123", "+79991112233", "сильная острая боль")
	if err != nil {
		t.Fatalf("NotifyCategoryA failed: %v", err)
	}

	if receivedBody == "" {
		t.Fatalf("Server did not receive telegram payload")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
