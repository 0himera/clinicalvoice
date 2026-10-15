package misclient

import (
	"context"
	"database/sql"
	"errors"
	"maps"

	"github.com/himera/tochka-voice/go-core/pkg/models"
	"github.com/himera/tochka-voice/go-core/pkg/storage"
)

type mockSnapshot struct {
	Clients          map[int]models.Client
	Visits           map[int]models.Visit
	Escalations      map[int]models.Escalation
	NextClientID     int
	NextVisitID      int
	NextEscalationID int
}

func NewPersistentMock(ctx context.Context, store *storage.Store, doctors []models.Doctor) (*MockMISClient, error) {
	m := NewMockMISClient()
	m.store = store
	var data mockSnapshot
	if err := store.Get(ctx, "mock_mis", &data); err == nil {
		m.restore(data)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	m.doctors = map[int]models.Doctor{}
	for _, d := range doctors {
		m.doctors[d.ID] = d
	}
	return m, m.persist(ctx)
}
func (m *MockMISClient) snapshot() mockSnapshot {
	return mockSnapshot{Clients: maps.Clone(m.clients), Visits: maps.Clone(m.visits), Escalations: maps.Clone(m.escalations), NextClientID: m.nextClientID, NextVisitID: m.nextVisitID, NextEscalationID: m.nextEscalationID}
}
func (m *MockMISClient) restore(s mockSnapshot) {
	m.clients = s.Clients
	m.visits = s.Visits
	m.escalations = s.Escalations
	m.nextClientID = s.NextClientID
	m.nextVisitID = s.NextVisitID
	m.nextEscalationID = s.NextEscalationID
}
func (m *MockMISClient) persist(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	return m.store.Put(ctx, "mock_mis", m.snapshot())
}
func (m *MockMISClient) save(ctx context.Context, previous mockSnapshot) error {
	if err := m.persist(ctx); err != nil {
		m.restore(previous)
		return err
	}
	return nil
}
