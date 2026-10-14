package misclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"golang.org/x/time/rate"
)

type MISClient interface {
	ListDoctors(ctx context.Context) ([]models.Doctor, error)
	CreateClient(ctx context.Context, req models.ClientCreate) (*models.Client, error)
	FindClients(ctx context.Context, lastName, firstName, birthDate string) ([]models.Client, error)
	GetClient(ctx context.Context, id int) (*models.Client, error)
	CreateVisit(ctx context.Context, req models.VisitCreate) (*models.Visit, error)
	ListVisits(ctx context.Context, clientID int, status string) ([]models.Visit, error)
	GetVisit(ctx context.Context, id int) (*models.Visit, error)
	UpdateVisit(ctx context.Context, id int, req models.VisitUpdate) (*models.Visit, error)
}

// -----------------------------------------------------------------------------
// Реальный HTTP-клиент к тестовому стенду https://crm-med-mock.dbi-testing.ru
// -----------------------------------------------------------------------------
type RealMISClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	limiter    *rate.Limiter // Строго не более 10 req/min
}

func NewRealMISClient(baseURL, token string) *RealMISClient {
	if baseURL == "" {
		baseURL = "https://crm-med-mock.dbi-testing.ru/api/v1"
	}
	// 10 запросов в 60 секунд = 1 запрос каждые 6 секунд, burst 1
	limiter := rate.NewLimiter(rate.Every(6*time.Second), 1)

	return &RealMISClient{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		limiter: limiter,
	}
}

func (c *RealMISClient) doRequest(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	// Соблюдаем жесткий лимит 10 req/min
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		return fmt.Errorf("HTTP 429 Too Many Requests (Retry-After: %s)", retryAfter)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("MIS API Error HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *RealMISClient) ListDoctors(ctx context.Context) ([]models.Doctor, error) {
	var doctors []models.Doctor
	err := c.doRequest(ctx, http.MethodGet, "/doctors", nil, &doctors)
	return doctors, err
}

func (c *RealMISClient) CreateClient(ctx context.Context, req models.ClientCreate) (*models.Client, error) {
	var client models.Client
	err := c.doRequest(ctx, http.MethodPost, "/clients", req, &client)
	return &client, err
}

func (c *RealMISClient) FindClients(ctx context.Context, lastName, firstName, birthDate string) ([]models.Client, error) {
	params := url.Values{}
	if lastName != "" {
		params.Set("last_name", lastName)
	}
	if firstName != "" {
		params.Set("first_name", firstName)
	}
	if birthDate != "" {
		params.Set("birth_date", birthDate)
	}
	path := "/clients?" + params.Encode()
	var clients []models.Client
	err := c.doRequest(ctx, http.MethodGet, path, nil, &clients)
	return clients, err
}

func (c *RealMISClient) GetClient(ctx context.Context, id int) (*models.Client, error) {
	var client models.Client
	err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/clients/%d", id), nil, &client)
	return &client, err
}

func (c *RealMISClient) CreateVisit(ctx context.Context, req models.VisitCreate) (*models.Visit, error) {
	var visit models.Visit
	err := c.doRequest(ctx, http.MethodPost, "/visits", req, &visit)
	return &visit, err
}

func (c *RealMISClient) ListVisits(ctx context.Context, clientID int, status string) ([]models.Visit, error) {
	params := url.Values{}
	if clientID > 0 {
		params.Set("client_id", strconv.Itoa(clientID))
	}
	if status != "" {
		params.Set("status", status)
	}
	path := "/visits?" + params.Encode()
	var visits []models.Visit
	err := c.doRequest(ctx, http.MethodGet, path, nil, &visits)
	return visits, err
}

func (c *RealMISClient) GetVisit(ctx context.Context, id int) (*models.Visit, error) {
	var visit models.Visit
	err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/visits/%d", id), nil, &visit)
	return &visit, err
}

func (c *RealMISClient) UpdateVisit(ctx context.Context, id int, req models.VisitUpdate) (*models.Visit, error) {
	var visit models.Visit
	err := c.doRequest(ctx, http.MethodPatch, fmt.Sprintf("/visits/%d", id), req, &visit)
	return &visit, err
}

// -----------------------------------------------------------------------------
// Внутренний Mock-сервер МИС для автономных тестов без действующего токена
// -----------------------------------------------------------------------------
type MockMISClient struct {
	mu           sync.RWMutex
	doctors      map[int]models.Doctor
	clients      map[int]models.Client
	visits       map[int]models.Visit
	nextClientID int
	nextVisitID  int
}

func NewMockMISClient() *MockMISClient {
	mock := &MockMISClient{
		doctors:      make(map[int]models.Doctor),
		clients:      make(map[int]models.Client),
		visits:       make(map[int]models.Visit),
		nextClientID: 100,
		nextVisitID:  5000,
	}
	mock.seedData()
	return mock
}

func (m *MockMISClient) seedData() {
	// Добавляем всех врачей поликлиники (1..25)
	specs := map[int]struct {
		name string
		spec string
	}{
		1:  {"Васильева Татьяна Геннадьевна", "терапевт"},
		2:  {"Кузнецов Андрей Павлович", "терапевт"},
		3:  {"Орлова Марина Сергеевна", "терапевт"},
		4:  {"Смирнова Елена Викторовна", "терапевт"},
		5:  {"Белова Ольга Николаевна", "педиатр"},
		6:  {"Громов Илья Александрович", "педиатр"},
		7:  {"Попова Дарья Андреевна", "педиатр"},
		8:  {"Волков Артём Сергеевич", "оториноларинголог"},
		9:  {"Медведева Алина Игоревна", "оториноларинголог"},
		10: {"Никитина Ирина Владимировна", "оториноларинголог"},
		11: {"Егоров Максим Андреевич", "офтальмолог"},
		12: {"Морозова Светлана Петровна", "офтальмолог"},
		13: {"Тихонова Екатерина Олеговна", "дерматовенеролог"},
		14: {"Ковалёва Юлия Дмитриевна", "акушер-гинеколог"},
		15: {"Семёнова Виктория Алексеевна", "акушер-гинеколог"},
		16: {"Макаров Павел Сергеевич", "хирург"},
		17: {"Зайцев Константин Ильич", "травматолог-ортопед"},
		18: {"Степанов Григорий Львович", "травматолог-ортопед"},
		19: {"Захаров Дмитрий Олегович", "невролог"},
		20: {"Фёдорова Наталья Юрьевна", "невролог"},
		21: {"Киселёв Алексей Романович", "кардиолог"},
		22: {"Лебедев Сергей Игоревич", "кардиолог"},
		23: {"Павлов Роман Евгеньевич", "гастроэнтеролог"},
		24: {"Андреев Николай Васильевич", "уролог"},
		25: {"Соколова Анна Михайловна", "эндокринолог"},
	}

	for id, info := range specs {
		m.doctors[id] = models.Doctor{
			ID:        id,
			FullName:  info.name,
			Specialty: info.spec,
			IsActive:  true,
		}
	}

	// Тестовый клиент
	m.clients[1] = models.Client{
		ID:        1,
		LastName:  "Иванов",
		FirstName: "Иван",
		BirthDate: "12.03.1985",
	}
}

func (m *MockMISClient) ListDoctors(ctx context.Context) ([]models.Doctor, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]models.Doctor, 0, len(m.doctors))
	for _, d := range m.doctors {
		res = append(res, d)
	}
	return res, nil
}

func (m *MockMISClient) CreateClient(ctx context.Context, req models.ClientCreate) (*models.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Проверка на дубликат по фамилии, имени и ДР
	for _, c := range m.clients {
		if stringsEqual(c.LastName, req.LastName) && stringsEqual(c.FirstName, req.FirstName) && c.BirthDate == req.BirthDate {
			return &c, nil
		}
	}

	m.nextClientID++
	middle := ""
	if req.MiddleName != nil {
		middle = *req.MiddleName
	}
	comment := ""
	if req.Comment != nil {
		comment = *req.Comment
	}

	client := models.Client{
		ID:         m.nextClientID,
		LastName:   req.LastName,
		FirstName:  req.FirstName,
		MiddleName: middle,
		BirthDate:  req.BirthDate,
		Comment:    comment,
		CreatedAt:  time.Now().Format("02.01.2006 15:04"),
	}
	m.clients[client.ID] = client
	return &client, nil
}

func (m *MockMISClient) FindClients(ctx context.Context, lastName, firstName, birthDate string) ([]models.Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []models.Client
	for _, c := range m.clients {
		match := true
		if lastName != "" && !stringsEqual(c.LastName, lastName) {
			match = false
		}
		if firstName != "" && !stringsEqual(c.FirstName, firstName) {
			match = false
		}
		if birthDate != "" && c.BirthDate != birthDate {
			match = false
		}
		if match {
			result = append(result, c)
		}
	}
	return result, nil
}

func (m *MockMISClient) GetClient(ctx context.Context, id int) (*models.Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.clients[id]
	if !ok {
		return nil, fmt.Errorf("клиент с id %d не найден", id)
	}
	return &c, nil
}

func (m *MockMISClient) CreateVisit(ctx context.Context, req models.VisitCreate) (*models.Visit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.doctors[req.DoctorID]
	if !ok || !doc.IsActive {
		return nil, fmt.Errorf("врач не существует или деактивирован")
	}

	m.nextVisitID++
	nowStr := time.Now().Format("02.01.2006 15:04")
	visit := models.Visit{
		ID:          m.nextVisitID,
		ClientID:    req.ClientID,
		Doctor:      doc,
		ScheduledAt: req.ScheduledAt,
		Status:      "Запланирована",
		CreatedAt:   nowStr,
		UpdatedAt:   nowStr,
	}
	m.visits[visit.ID] = visit
	return &visit, nil
}

func (m *MockMISClient) ListVisits(ctx context.Context, clientID int, status string) ([]models.Visit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []models.Visit
	for _, v := range m.visits {
		if clientID > 0 && v.ClientID != clientID {
			continue
		}
		if status != "" && v.Status != status {
			continue
		}
		result = append(result, v)
	}
	return result, nil
}

func (m *MockMISClient) GetVisit(ctx context.Context, id int) (*models.Visit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.visits[id]
	if !ok {
		return nil, fmt.Errorf("запись с id %d не найдена", id)
	}
	return &v, nil
}

func (m *MockMISClient) UpdateVisit(ctx context.Context, id int, req models.VisitUpdate) (*models.Visit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.visits[id]
	if !ok {
		return nil, fmt.Errorf("запись с id %d не найдена", id)
	}

	if req.DoctorID != nil {
		doc, docOk := m.doctors[*req.DoctorID]
		if !docOk || !doc.IsActive {
			return nil, fmt.Errorf("деактивированный врач")
		}
		v.Doctor = doc
	}
	if req.ScheduledAt != nil {
		v.ScheduledAt = *req.ScheduledAt
	}
	if req.Status != nil {
		v.Status = *req.Status
	}
	v.UpdatedAt = time.Now().Format("02.01.2006 15:04")
	m.visits[v.ID] = v
	return &v, nil
}

func stringsEqual(a, b string) bool {
	return bytes.EqualFold([]byte(a), []byte(b))
}
