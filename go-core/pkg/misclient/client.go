package misclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/himera/tochka-voice/go-core/pkg/diagnostics"
	"github.com/himera/tochka-voice/go-core/pkg/models"
)

type MISClient interface {
	ListDoctors(context.Context) ([]models.Doctor, error)
	CreateClient(context.Context, models.ClientCreate) (*models.Client, error)
	FindClients(context.Context, string, string, string) ([]models.Client, error)
	GetClient(context.Context, int) (*models.Client, error)
	UpdateClient(context.Context, int, models.ClientUpdate) (*models.Client, error)
	CreateVisit(context.Context, models.VisitCreate) (*models.Visit, error)
	ListVisits(context.Context, int, string) ([]models.Visit, error)
	GetVisit(context.Context, int) (*models.Visit, error)
	UpdateVisit(context.Context, int, models.VisitUpdate) (*models.Visit, error)
	CreateEscalation(context.Context, models.EscalationCreate) (*models.Escalation, error)
}

type APIError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("MIS returned HTTP %d", e.StatusCode) }

var ErrNotSent = errors.New("request not sent to MIS")

// Ambiguous reports whether a failed mutation may already have reached the MIS.
// Such mutations must be reconciled, never blindly retried.
func Ambiguous(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotSent) {
		return false
	}
	var api *APIError
	if errors.As(err, &api) {
		return api.StatusCode >= 500
	}
	return true
}

type RealMISClient struct {
	baseURL      string
	token        string
	httpClient   *http.Client
	limiter      *rate.Limiter
	mu           sync.Mutex
	blockedUntil time.Time
	doctorsMu    sync.Mutex
	doctors      []models.Doctor
	doctorsUntil time.Time
	events       *slog.Logger
	foreground   atomic.Int64
}

func NewRealMISClient(baseURL, token string) *RealMISClient {
	if baseURL == "" {
		baseURL = "https://crm-med-mock.dbi-testing.ru/api/v1"
	}
	var limiter *rate.Limiter
	if token != "" {
		limiter = rate.NewLimiter(rate.Every(6*time.Second), 1)
	}
	return &RealMISClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		events:     slog.Default(),
		httpClient: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		limiter:    limiter,
	}
}

type backgroundKey struct{}

// Background marks maintenance reads/writes as lower priority than live turns.
// All callers still share the official 10 RPM quota.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

func (c *RealMISClient) waitQuota(ctx context.Context) error {
	if c.limiter == nil {
		return nil
	}
	if background, _ := ctx.Value(backgroundKey{}).(bool); !background {
		c.foreground.Add(1)
		defer c.foreground.Add(-1)
		return c.limiter.Wait(ctx)
	}
	// Background work never reserves future tokens ahead of interactive calls.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.foreground.Load() == 0 && c.limiter.Allow() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *RealMISClient) doRequest(ctx context.Context, method, path string, body, out any) (resultErr error) {
	started := time.Now()
	var requestStarted time.Time
	status := 0
	phase := "limiter"
	defer func() {
		if c.events == nil {
			return
		}
		elapsed, queued := time.Since(started), time.Since(started)
		if !requestStarted.IsZero() {
			queued = requestStarted.Sub(started)
		}
		issue := ""
		if resultErr != nil {
			issue = phase
		}
		resource := strings.SplitN(path, "?", 2)[0]
		parts := strings.Split(strings.Trim(resource, "/"), "/")
		if len(parts) > 0 && (parts[0] == "clients" || parts[0] == "visits" || parts[0] == "doctors" || parts[0] == "escalations") {
			resource = "/" + parts[0]
			if len(parts) > 1 {
				resource += "/{id}"
			}
		} else {
			resource = "unknown"
		}
		c.events.Info("mis_request", "call_id", diagnostics.CallID(ctx), "method", method, "resource", resource, "status", status,
			"queue_ms", queued.Milliseconds(), "request_ms", (elapsed - queued).Milliseconds(), "latency_ms", elapsed.Milliseconds(), "error_kind", issue)
	}()
	for {
		if err := c.waitQuota(ctx); err != nil {
			return fmt.Errorf("%w: %w", ErrNotSent, err)
		}
		// Recheck after waiting: other requests may have received HTTP 429.
		c.mu.Lock()
		wait := time.Until(c.blockedUntil)
		c.mu.Unlock()
		if wait <= 0 {
			break
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %w", ErrNotSent, ctx.Err())
		case <-timer.C:
		}
		// Acquire quota again so requests held by Retry-After cannot burst.
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrNotSent, ctx.Err())
	}
	requestStarted = time.Now()
	phase = "encode"
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNotSent, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	phase = "transport"
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// net/http's url.Error includes patient search parameters. Preserve the
		// transport cause for errors.Is/As without retaining that URL in logs.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("MIS transport: %w", err)
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	phase = "http"
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		e := &APIError{StatusCode: resp.StatusCode}
		if resp.StatusCode == http.StatusTooManyRequests {
			e.RetryAfter = retryDelay(resp.Header.Get("Retry-After"), time.Now())
			c.mu.Lock()
			c.blockedUntil = time.Now().Add(e.RetryAfter)
			c.mu.Unlock()
		}
		return e
	}
	if out == nil {
		return nil
	}
	phase = "decode"
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode MIS response: %w", err)
	}
	return nil
}

func retryDelay(header string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return time.Minute
}

func list[T any](ctx context.Context, c *RealMISClient, path string, params url.Values) ([]T, error) {
	all := []T{}
	for offset := 0; offset < 100000; offset += 50 {
		params.Set("limit", "50")
		params.Set("offset", strconv.Itoa(offset))
		var page []T
		if err := c.doRequest(ctx, http.MethodGet, path+"?"+params.Encode(), nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 50 {
			return all, nil
		}
	}
	return nil, errors.New("MIS pagination limit exceeded")
}

func (c *RealMISClient) ListDoctors(ctx context.Context) ([]models.Doctor, error) {
	c.doctorsMu.Lock()
	defer c.doctorsMu.Unlock()
	if time.Now().Before(c.doctorsUntil) {
		return append([]models.Doctor(nil), c.doctors...), nil
	}
	docs, err := list[models.Doctor](ctx, c, "/doctors", url.Values{})
	if err != nil {
		return nil, err
	}
	c.doctors = docs
	c.doctorsUntil = time.Now().Add(10 * time.Minute)
	return append([]models.Doctor(nil), docs...), nil
}
func (c *RealMISClient) CreateClient(ctx context.Context, req models.ClientCreate) (*models.Client, error) {
	var out models.Client
	if err := c.doRequest(ctx, http.MethodPost, "/clients", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *RealMISClient) FindClients(ctx context.Context, last, first, birth string) ([]models.Client, error) {
	return list[models.Client](ctx, c, "/clients", url.Values{"last_name": {last}, "first_name": {first}, "birth_date": {birth}})
}
func (c *RealMISClient) GetClient(ctx context.Context, id int) (*models.Client, error) {
	var out models.Client
	if err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/clients/%d", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *RealMISClient) UpdateClient(ctx context.Context, id int, req models.ClientUpdate) (*models.Client, error) {
	var out models.Client
	if err := c.doRequest(ctx, http.MethodPatch, fmt.Sprintf("/clients/%d", id), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *RealMISClient) CreateVisit(ctx context.Context, req models.VisitCreate) (*models.Visit, error) {
	var out models.Visit
	if err := c.doRequest(ctx, http.MethodPost, "/visits", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *RealMISClient) ListVisits(ctx context.Context, id int, status string) ([]models.Visit, error) {
	params := url.Values{}
	if id > 0 {
		params.Set("client_id", strconv.Itoa(id))
	}
	if status != "" {
		params.Set("status", status)
	}
	return list[models.Visit](ctx, c, "/visits", params)
}
func (c *RealMISClient) GetVisit(ctx context.Context, id int) (*models.Visit, error) {
	var out models.Visit
	if err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/visits/%d", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *RealMISClient) UpdateVisit(ctx context.Context, id int, req models.VisitUpdate) (*models.Visit, error) {
	var out models.Visit
	if err := c.doRequest(ctx, http.MethodPatch, fmt.Sprintf("/visits/%d", id), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *RealMISClient) CreateEscalation(ctx context.Context, req models.EscalationCreate) (*models.Escalation, error) {
	var out models.Escalation
	if err := c.doRequest(ctx, http.MethodPost, "/escalations", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
