package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// StandingStore persists responsibilities. A Standing saves after every
// change — a note, a self-wake, a pause — so a restart loses at most the
// wake that was in flight.
type StandingStore interface {
	Load(ctx context.Context) ([]Responsibility, error)
	Save(ctx context.Context, r Responsibility) error
	Delete(ctx context.Context, id string) error
}

// MemoryStandingStore keeps responsibilities for the life of the process.
type MemoryStandingStore struct {
	mu    sync.Mutex
	items map[string]Responsibility
}

func NewMemoryStandingStore() *MemoryStandingStore {
	return &MemoryStandingStore{items: map[string]Responsibility{}}
}

func (m *MemoryStandingStore) Load(context.Context) ([]Responsibility, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Responsibility, 0, len(m.items))
	for _, r := range m.items {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemoryStandingStore) Save(_ context.Context, r Responsibility) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[r.ID] = r
	return nil
}

func (m *MemoryStandingStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

// SQLiteStandingStore keeps responsibilities in the database a Service
// already owns, one JSON row each. Writes are serialised for the reason
// SQLitePlanStore's are.
type SQLiteStandingStore struct {
	db      *sql.DB
	writeMu sync.Mutex
}

func NewSQLiteStandingStore(db *sql.DB) (*SQLiteStandingStore, error) {
	if db == nil {
		return nil, fmt.Errorf("agent: NewSQLiteStandingStore needs a database handle")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS standing_responsibilities (
			id         TEXT PRIMARY KEY,
			body       TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`); err != nil {
		return nil, fmt.Errorf("agent: create standing_responsibilities: %w", err)
	}
	return &SQLiteStandingStore{db: db}, nil
}

func (s *SQLiteStandingStore) Load(ctx context.Context) ([]Responsibility, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM standing_responsibilities ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Responsibility
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var r Responsibility
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			return nil, fmt.Errorf("agent: responsibility row is not JSON: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStandingStore) Save(ctx context.Context, r Responsibility) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO standing_responsibilities (id, body, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		r.ID, string(body), time.Now().UTC())
	return err
}

func (s *SQLiteStandingStore) Delete(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM standing_responsibilities WHERE id = ?`, id)
	return err
}
