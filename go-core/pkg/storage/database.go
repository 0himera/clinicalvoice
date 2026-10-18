// Package storage owns the durable local data absent from the MIS contract.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB      *sql.DB
	writeDB *sql.DB
	readDB  *sql.DB
	lock    *os.File
}

func (s *Store) WriteDB() *sql.DB {
	return s.writeDB
}

func (s *Store) ReadDB() *sql.DB {
	return s.readDB
}

func Open(path string) (result *Store, resultErr error) {
	var lock *os.File
	defer func() {
		if resultErr != nil && lock != nil {
			_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			_ = lock.Close()
		}
	}()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	if path != "" && path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			return nil, err
		}
		lock, err = os.OpenFile(abs+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return nil, fmt.Errorf("database is already owned by another core process: %w", err)
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		if err = os.Chmod(abs, 0600); err != nil {
			return nil, err
		}
		dsn = (&url.URL{Scheme: "file", Path: abs}).String() + "?"
	} else {
		dsn += "&"
	}
	basePragmas := "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	writeDSN := dsn + basePragmas + "&_txlock=immediate"
	readDSN := dsn + basePragmas + "&_pragma=query_only(1)"

	writeDB, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		return nil, err
	}
	writeDB.SetMaxOpenConns(1)

	s := &Store{DB: writeDB, writeDB: writeDB, lock: lock}
	if err = s.migrate(); err != nil {
		_ = writeDB.Close()
		return nil, err
	}

	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		_ = writeDB.Close()
		return nil, err
	}
	readDB.SetMaxOpenConns(10)
	s.readDB = readDB

	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.writeDB.Exec(`
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version);
CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS slot_claims (
 doctor_id INTEGER NOT NULL, starts_at TEXT NOT NULL, owner TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('held','pending','booked','uncertain','seed')),
 expires_at INTEGER NOT NULL DEFAULT 0, visit_id INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(doctor_id, starts_at));
CREATE INDEX IF NOT EXISTS claims_visit ON slot_claims(visit_id);
CREATE TABLE IF NOT EXISTS contacts (
 client_id INTEGER PRIMARY KEY, endpoint TEXT NOT NULL, reminder_consent INTEGER NOT NULL,
 updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS reminders (
 visit_id INTEGER NOT NULL, scheduled_at TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING',
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0,
 lease_until INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(visit_id, scheduled_at));
CREATE TABLE IF NOT EXISTS events (
 id INTEGER PRIMARY KEY, kind TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS escalations (
 id TEXT PRIMARY KEY, client_id INTEGER NOT NULL, description TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'PENDING', mis_id INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0, lease_until INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
`)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	_, _ = s.writeDB.Exec("ALTER TABLE escalations ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0")

	var version int
	if err = s.writeDB.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return fmt.Errorf("unsupported database schema %d", version)
	}
	if _, err = s.writeDB.Exec("UPDATE slot_claims SET status='uncertain',expires_at=0 WHERE status='pending'"); err != nil {
		return err
	}
	if _, err = s.writeDB.Exec("DELETE FROM slot_claims WHERE status='held'"); err != nil {
		return err
	}
	return nil
}

func (s *Store) Close() error {
	var err error
	if s.readDB != nil {
		err = errors.Join(err, s.readDB.Close())
	}
	if s.writeDB != nil {
		err = errors.Join(err, s.writeDB.Close())
	}
	if s.lock != nil {
		err = errors.Join(err, unix.Flock(int(s.lock.Fd()), unix.LOCK_UN), s.lock.Close())
		s.lock = nil
	}
	return err
}

// Bind prevents accidental reuse of mock contacts or another team's client IDs
// after changing the MIS token. Namespace is a non-reversible token fingerprint.
func (s *Store) Bind(ctx context.Context, namespace string) error {
	var previous string
	err := s.Get(ctx, "namespace", &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return s.Put(ctx, "namespace", namespace)
	}
	if err != nil {
		return err
	}
	if previous != namespace {
		return errors.New("database belongs to another MIS mode/team; choose another DATABASE_PATH")
	}
	return nil
}

func (s *Store) Put(ctx context.Context, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.writeDB.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, b)
	return err
}

func (s *Store) Get(ctx context.Context, key string, out any) error {
	var data []byte
	if err := s.readDB.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&data); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

type Contact struct {
	ClientID        int    `json:"client_id"`
	Endpoint        string `json:"endpoint"`
	ReminderConsent bool   `json:"reminder_consent"`
}

func (s *Store) SaveContact(ctx context.Context, c Contact) error {
	if c.ClientID <= 0 || c.Endpoint == "" {
		return fmt.Errorf("client and endpoint are required")
	}
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO contacts VALUES(?,?,?,?) ON CONFLICT(client_id)
 DO UPDATE SET endpoint=excluded.endpoint, reminder_consent=excluded.reminder_consent, updated_at=excluded.updated_at`,
		c.ClientID, c.Endpoint, c.ReminderConsent, time.Now().Unix())
	return err
}

func (s *Store) Contact(ctx context.Context, id int) (Contact, error) {
	c := Contact{ClientID: id}
	err := s.readDB.QueryRowContext(ctx, "SELECT endpoint,reminder_consent FROM contacts WHERE client_id=?", id).Scan(&c.Endpoint, &c.ReminderConsent)
	return c, err
}

func (s *Store) GetContact(ctx context.Context, id int) (Contact, error) {
	return s.Contact(ctx, id)
}

// Event stores aggregate outcomes only, never utterances, names or caller numbers.
func (s *Store) Event(ctx context.Context, kind string) error {
	_, err := s.writeDB.ExecContext(ctx, "INSERT INTO events(kind,created_at) VALUES(?,?)", kind, time.Now().Unix())
	return err
}

func (s *Store) Stats(ctx context.Context) (map[string]int, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT kind,COUNT(*) FROM events GROUP BY kind")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err = rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		result[key] = n
	}
	return result, rows.Err()
}
