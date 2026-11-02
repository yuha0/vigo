package tv

import (
	"context"
	"maps"
	"slices"
	"sync"
)

// Store is what the service persists. internal/db implements it on
// PostgreSQL; MemoryStore is for tests and tools.
type Store interface {
	// Config returns the runtime overrides; zero fields are unset.
	Config(ctx context.Context) (Config, error)
	SetConfig(ctx context.Context, c Config) error

	// Token returns the stored TV token, or "" when there is none.
	Token(ctx context.Context) (string, error)
	// SetToken replaces the token. It is never deleted automatically.
	SetToken(ctx context.Context, token, clientName string) error
	// TokenVerified records that the TV accepted the token.
	TokenVerified(ctx context.Context) error
	// TokenRejected records that the TV refused the token.
	TokenRejected(ctx context.Context) error

	// Selection returns the persisted selection record, or nil.
	Selection(ctx context.Context) ([]byte, error)
	// SetSelection replaces it; nil deletes it.
	SetSelection(ctx context.Context, record []byte) error

	// Uploads returns the upload tally, content id to bytes.
	Uploads(ctx context.Context) (map[string]int64, error)
	SetUpload(ctx context.Context, contentID string, bytes int64) error
	DeleteUpload(ctx context.Context, contentID string) error

	// ObserveModel records a REST model string. The first model ever seen
	// is acknowledged; any later new model is not, until an operator
	// acknowledges it.
	ObserveModel(ctx context.Context, model string) (ModelObservation, error)
}

type ModelObservation struct {
	Acknowledged bool
	// Previous is the most recently seen other model, if any.
	Previous string
}

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu            sync.Mutex
	config        Config
	token         string
	clientName    string
	tokenVerified bool
	tokenRejected bool
	selection     []byte
	uploads       map[string]int64
	models        []string
	acked         map[string]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{uploads: map[string]int64{}, acked: map[string]bool{}}
}

func (m *MemoryStore) Config(context.Context) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config, nil
}

func (m *MemoryStore) SetConfig(_ context.Context, c Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = c
	return nil
}

func (m *MemoryStore) Token(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token, nil
}

func (m *MemoryStore) SetToken(_ context.Context, token, clientName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token, m.clientName = token, clientName
	m.tokenVerified, m.tokenRejected = false, false
	return nil
}

func (m *MemoryStore) TokenVerified(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenVerified, m.tokenRejected = true, false
	return nil
}

func (m *MemoryStore) TokenRejected(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenRejected = true
	return nil
}

// TokenState reports the verification flags, for tests.
func (m *MemoryStore) TokenState() (verified, rejected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokenVerified, m.tokenRejected
}

func (m *MemoryStore) Selection(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.selection), nil
}

func (m *MemoryStore) SetSelection(_ context.Context, record []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selection = slices.Clone(record)
	return nil
}

func (m *MemoryStore) Uploads(context.Context) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.uploads), nil
}

func (m *MemoryStore) SetUpload(_ context.Context, id string, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uploads[id] = n
	return nil
}

func (m *MemoryStore) DeleteUpload(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploads, id)
	return nil
}

func (m *MemoryStore) ObserveModel(_ context.Context, model string) (ModelObservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.models) == 0 {
		m.acked[model] = true
	}
	m.models = slices.DeleteFunc(m.models, func(s string) bool { return s == model })
	m.models = append(m.models, model)
	obs := ModelObservation{Acknowledged: m.acked[model]}
	if n := len(m.models); n > 1 {
		obs.Previous = m.models[n-2]
	}
	return obs, nil
}

// AcknowledgeModels marks every model seen so far as acknowledged.
func (m *MemoryStore) AcknowledgeModels() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, model := range m.models {
		m.acked[model] = true
	}
}
