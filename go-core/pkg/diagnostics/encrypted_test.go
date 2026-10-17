package diagnostics

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncryptedTranscriptsAuthenticateExpireAndPrune(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "secrets", "key")
	if err := CreateKey(key); err != nil {
		t.Fatal(err)
	}
	if err := CreateKey(key); err == nil {
		t.Fatal("overwrote key")
	}
	w, err := OpenEncrypted(filepath.Join(root, "logs"), key, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	now := time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	plaintext := []byte("{\"text\":\"Иванов Иван 12.03.1985\"}\n")
	if _, err = w.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	path := w.file.path
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("Иванов")) || bytes.Contains(encoded, []byte("1985")) {
		t.Fatal("plaintext leaked")
	}
	decoded, err := Decrypt(w.aead, bytes.TrimSpace(encoded), now)
	if err != nil || !bytes.Equal(decoded, plaintext) {
		t.Fatalf("roundtrip: %q %v", decoded, err)
	}
	tampered := bytes.Replace(encoded, []byte("2026-10-17"), []byte("2026-10-18"), 1)
	if _, err = Decrypt(w.aead, bytes.TrimSpace(tampered), now); err == nil {
		t.Fatal("unauthenticated expiry accepted")
	}
	now = now.Add(24 * time.Hour)
	if _, err = Decrypt(w.aead, bytes.TrimSpace(encoded), now); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired record exposed: %v", err)
	}
	if err = w.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired ciphertext not deleted: %v", err)
	}
	info, err := os.Stat(key)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions: %v %v", info, err)
	}
}

func TestEncryptedTranscriptRejectsPublicKeyAndColocatedKey(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "secrets", "key")
	if err := CreateKey(key); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEncrypted(filepath.Dir(key), key, time.Hour); err == nil {
		t.Fatal("key can be exported with logs")
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCipher(key); err == nil {
		t.Fatal("accepted readable key")
	}
}
