package diagnostics

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotationPermissionsAndClosedWriter(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "debug")
	f, err := Open(directory, "core")
	if err != nil {
		t.Fatal(err)
	}
	f.limit = 100
	defer f.Close()
	for i := 0; i < 3; i++ {
		if _, err = f.Write(bytes.Repeat([]byte("x"), 60)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{f.path, f.path + ".1"} {
		info, err := os.Stat(path)
		if err != nil || info.Size() > 100 || info.Mode().Perm() != 0600 {
			t.Fatalf("invalid private rotated log: %v %v", info, err)
		}
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("debug directory permissions: %v %v", info, err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("late event")); err == nil {
		t.Fatal("closed trace accepted a write")
	}
}

func TestRefusesSymlinkLog(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "unrelated")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "core.jsonl")); err != nil {
		t.Fatal(err)
	}
	if f, err := Open(directory, "core"); err == nil {
		_ = f.Close()
		t.Fatal("followed a debug log symlink")
	}
}
