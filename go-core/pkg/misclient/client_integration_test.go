package misclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

func TestDoctorsPaginationAndCache(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing authentication")
		}
		if r.URL.Query().Get("limit") != "50" {
			t.Error("missing page size")
		}
		n := 50
		if r.URL.Query().Get("offset") == "50" {
			n = 1
		}
		_ = json.NewEncoder(w).Encode(make([]models.Doctor, n))
	}))
	defer server.Close()
	c := NewRealMISClient(server.URL, "token")
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	d, err := c.ListDoctors(context.Background())
	if err != nil || len(d) != 51 {
		t.Fatalf("pagination: %d %v", len(d), err)
	}
	_, _ = c.ListDoctors(context.Background())
	if requests.Load() != 2 {
		t.Fatalf("cache missed: %d requests", requests.Load())
	}
}
func Test429AndMutationsAreNotBlindlyRetried(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(429)
	}))
	defer s.Close()
	c := NewRealMISClient(s.URL, "token")
	c.limiter = rate.NewLimiter(rate.Inf, 1)
	_, err := c.CreateVisit(context.Background(), models.VisitCreate{})
	var api *APIError
	if !errors.As(err, &api) || api.RetryAfter != 12*time.Second || requests.Load() != 1 {
		t.Fatalf("429 policy: %v requests=%d", err, requests.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.GetVisit(ctx, 1); err == nil {
		t.Fatal("cancelled request was sent")
	}
}
func TestMockPersistenceAndTerminalStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewPersistentMock(context.Background(), store, models.DemoDoctors())
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.CreateVisit(context.Background(), models.VisitCreate{ClientID: 1, DoctorID: 1, ScheduledAt: "20.10.2026 10:00"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := models.VisitCancelled
	if _, err = m.UpdateVisit(context.Background(), v.ID, models.VisitUpdate{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	at := "21.10.2026 10:00"
	if _, err = m.UpdateVisit(context.Background(), v.ID, models.VisitUpdate{ScheduledAt: &at}); err == nil {
		t.Fatal("terminal visit was changed")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m, err = NewPersistentMock(context.Background(), store, models.DemoDoctors())
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.GetVisit(context.Background(), v.ID)
	if err != nil || got.Status != cancelled {
		t.Fatalf("lost persistent visit: %+v %v", got, err)
	}
}

func TestMockDeactivatedDoctorAllowsOnlyCancellation(t *testing.T) {
	m := NewMockMISClient()
	ctx := context.Background()
	v, err := m.CreateVisit(ctx, models.VisitCreate{ClientID: 1, DoctorID: 1, ScheduledAt: "20.10.2026 10:00"})
	if err != nil {
		t.Fatal(err)
	}
	d := m.doctors[1]
	d.IsActive = false
	m.doctors[1] = d
	at := "21.10.2026 10:00"
	if _, err = m.UpdateVisit(ctx, v.ID, models.VisitUpdate{ScheduledAt: &at}); err == nil {
		t.Fatal("inactive doctor accepted a reschedule")
	}
	status := models.VisitCancelled
	if _, err = m.UpdateVisit(ctx, v.ID, models.VisitUpdate{Status: &status}); err != nil {
		t.Fatal(err)
	}
}
