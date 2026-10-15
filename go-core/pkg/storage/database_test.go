package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseOwnershipAndNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Open(path); err == nil {
		_ = second.Close()
		t.Fatal("second owner was allowed")
	}
	if err = store.Bind(context.Background(), "mock"); err != nil {
		t.Fatal(err)
	}
	if err = store.Bind(context.Background(), "stand:another-team"); err == nil {
		t.Fatal("mock data reused by another MIS team")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("database permissions are not private")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Bind(context.Background(), "mock"); err != nil {
		t.Fatal(err)
	}
}
