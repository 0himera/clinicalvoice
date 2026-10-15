package misclient

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

type MockMISClient struct {
	store            *storage.Store
	escalations      map[int]models.Escalation
	nextEscalationID int
	mu               sync.RWMutex
	doctors          map[int]models.Doctor
	clients          map[int]models.Client
	visits           map[int]models.Visit
	nextClientID     int
	nextVisitID      int
}

func NewMockMISClient() *MockMISClient {
	mock := &MockMISClient{
		escalations:  make(map[int]models.Escalation),
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
	for _, d := range models.DemoDoctors() {
		m.doctors[d.ID] = d
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res := make([]models.Doctor, 0, len(m.doctors))
	for _, d := range m.doctors {
		res = append(res, d)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	return res, nil
}

func (m *MockMISClient) CreateClient(ctx context.Context, req models.ClientCreate) (*models.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	previous := m.snapshot()
	if req.LastName == "" || req.FirstName == "" {
		return nil, &APIError{StatusCode: 400}
	}
	if _, err := time.Parse("02.01.2006", req.BirthDate); err != nil {
		return nil, &APIError{StatusCode: 400}
	}

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
		CreatedAt:  time.Now().In(models.ClinicLocation).Format(models.DateTimeLayout),
	}
	m.clients[client.ID] = client
	if err := m.save(ctx, previous); err != nil {
		return nil, err
	}
	return &client, nil
}

func (m *MockMISClient) FindClients(ctx context.Context, lastName, firstName, birthDate string) ([]models.Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := []models.Client{}
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
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (m *MockMISClient) GetClient(ctx context.Context, id int) (*models.Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, ok := m.clients[id]
	if !ok {
		return nil, &APIError{StatusCode: 404}
	}
	return &c, nil
}

func (m *MockMISClient) CreateVisit(ctx context.Context, req models.VisitCreate) (*models.Visit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	previous := m.snapshot()
	if _, ok := m.clients[req.ClientID]; !ok {
		return nil, &APIError{StatusCode: 400}
	}
	if _, err := models.ParseDateTime(req.ScheduledAt); err != nil {
		return nil, &APIError{StatusCode: 400}
	}

	doc, ok := m.doctors[req.DoctorID]
	if !ok || !doc.IsActive {
		return nil, &APIError{StatusCode: 400}
	}

	m.nextVisitID++
	nowStr := time.Now().In(models.ClinicLocation).Format(models.DateTimeLayout)
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
	if err := m.save(ctx, previous); err != nil {
		return nil, err
	}
	return &visit, nil
}

func (m *MockMISClient) ListVisits(ctx context.Context, clientID int, status string) ([]models.Visit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := []models.Visit{}
	for _, v := range m.visits {
		if clientID > 0 && v.ClientID != clientID {
			continue
		}
		if status != "" && v.Status != status {
			continue
		}
		result = append(result, m.currentVisit(v))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (m *MockMISClient) GetVisit(ctx context.Context, id int) (*models.Visit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v, ok := m.visits[id]
	if !ok {
		return nil, &APIError{StatusCode: 404}
	}
	v = m.currentVisit(v)
	return &v, nil
}

func (m *MockMISClient) UpdateVisit(ctx context.Context, id int, req models.VisitUpdate) (*models.Visit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	previous := m.snapshot()

	v, ok := m.visits[id]
	if !ok {
		return nil, &APIError{StatusCode: 404}
	}
	v = m.currentVisit(v)
	changed := (req.DoctorID != nil && *req.DoctorID != v.Doctor.ID) || (req.ScheduledAt != nil && *req.ScheduledAt != v.ScheduledAt) || (req.Status != nil && *req.Status != v.Status)
	if !changed {
		v = m.currentVisit(v)
		return &v, nil
	}
	if v.Status != models.VisitPlanned {
		return nil, &APIError{StatusCode: 409}
	}
	if !v.Doctor.IsActive && (req.Status == nil || *req.Status != models.VisitCancelled || req.DoctorID != nil || req.ScheduledAt != nil) {
		return nil, &APIError{StatusCode: 409}
	}
	if req.Status != nil && *req.Status != models.VisitPlanned && *req.Status != models.VisitCancelled && *req.Status != models.VisitCompleted {
		return nil, &APIError{StatusCode: 400}
	}
	if req.ScheduledAt != nil {
		if _, err := models.ParseDateTime(*req.ScheduledAt); err != nil {
			return nil, &APIError{StatusCode: 400}
		}
	}

	if req.DoctorID != nil {
		doc, docOk := m.doctors[*req.DoctorID]
		if !docOk || !doc.IsActive {
			return nil, &APIError{StatusCode: 400}
		}
		v.Doctor = doc
	}
	if req.ScheduledAt != nil {
		v.ScheduledAt = *req.ScheduledAt
	}
	if req.Status != nil {
		v.Status = *req.Status
	}
	v.UpdatedAt = time.Now().In(models.ClinicLocation).Format(models.DateTimeLayout)
	m.visits[v.ID] = v
	if err := m.save(ctx, previous); err != nil {
		return nil, err
	}
	v = m.currentVisit(v)
	return &v, nil
}

func stringsEqual(a, b string) bool {
	return bytes.EqualFold([]byte(a), []byte(b))
}

func (m *MockMISClient) UpdateClient(ctx context.Context, id int, req models.ClientUpdate) (*models.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	previous := m.snapshot()
	c, ok := m.clients[id]
	if !ok {
		return nil, &APIError{StatusCode: 404}
	}
	if req.MiddleName != nil {
		c.MiddleName = *req.MiddleName
	}
	if req.Comment != nil {
		c.Comment = *req.Comment
	}
	m.clients[id] = c
	if err := m.save(ctx, previous); err != nil {
		return nil, err
	}
	return &c, nil
}

func (m *MockMISClient) CreateEscalation(ctx context.Context, req models.EscalationCreate) (*models.Escalation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	previous := m.snapshot()
	if _, ok := m.clients[req.ClientID]; !ok {
		return nil, &APIError{StatusCode: 400}
	}
	if len([]rune(req.Description)) == 0 || len([]rune(req.Description)) > 1000 {
		return nil, &APIError{StatusCode: 400}
	}
	m.nextEscalationID++
	e := models.Escalation{ID: m.nextEscalationID, ClientID: req.ClientID, Description: req.Description, CreatedAt: time.Now().In(models.ClinicLocation).Format(models.DateTimeLayout)}
	m.escalations[e.ID] = e
	if err := m.save(ctx, previous); err != nil {
		return nil, err
	}
	return &e, nil
}

func (m *MockMISClient) currentVisit(v models.Visit) models.Visit {
	if d, ok := m.doctors[v.Doctor.ID]; ok {
		v.Doctor = d
	} else {
		v.Doctor.IsActive = false
	}
	return v
}
