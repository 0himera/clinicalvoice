// Local operator utility: plaintext is produced only by an explicit read command.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/himera/tochka-voice/go-core/pkg/config"
	"github.com/himera/tochka-voice/go-core/pkg/diagnostics"
)

func run() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	keyDefault := os.Getenv("TRANSCRIPT_KEY_PATH")
	if keyDefault == "" {
		keyDefault = "var/secrets/transcripts.key"
	}
	dirDefault := os.Getenv("TRANSCRIPT_LOG_DIR")
	if dirDefault == "" {
		dirDefault = "var/transcripts"
	}
	key := flag.String("key", keyDefault, "path to local 32-byte key")
	dir := flag.String("dir", dirDefault, "encrypted transcript directory")
	initKey := flag.Bool("init-key", false, "create a new key; refuses to overwrite")
	prune := flag.Bool("prune", false, "delete expired transcript files without printing plaintext")
	call := flag.String("call", "", "filter by call UUID")
	last := flag.Int("last", 50, "maximum number of decrypted events to print")
	retentionHours := 24
	if raw := os.Getenv("TRANSCRIPT_RETENTION_HOURS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 24 {
			return errors.New("invalid transcript retention")
		}
		retentionHours = value
	}
	flag.Parse()
	if *initKey {
		if err := diagnostics.CreateKey(*key); err != nil {
			return err
		}
		fmt.Println("Private transcript key created.")
		return nil
	}
	if *prune {
		writer, err := diagnostics.OpenEncrypted(*dir, *key, time.Duration(retentionHours)*time.Hour)
		if err != nil {
			return err
		}
		return writer.Close()
	}
	if *last < 1 || *last > 10000 {
		return errors.New("last must be between 1 and 10000")
	}
	aead, err := diagnostics.LoadCipher(*key)
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(*dir, "transcript-*.jsonl*"))
	if err != nil {
		return err
	}
	sort.Slice(paths, func(i, j int) bool {
		if paths[i]+".1" == paths[j] {
			return false
		}
		if paths[j]+".1" == paths[i] {
			return true
		}
		return paths[i] < paths[j]
	})
	var records [][]byte
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("transcript must be a private regular file")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			plaintext, err := diagnostics.Decrypt(aead, scanner.Bytes(), time.Now())
			if errors.Is(err, diagnostics.ErrExpired) {
				continue
			}
			if err != nil {
				file.Close()
				return err
			}
			if *call != "" {
				var event struct {
					CallID string `json:"call_id"`
				}
				if json.Unmarshal(plaintext, &event) != nil || event.CallID != *call {
					continue
				}
			}
			records = append(records, bytes.TrimSpace(plaintext))
			if len(records) > *last {
				records = records[1:]
			}
		}
		err = scanner.Err()
		file.Close()
		if err != nil {
			return err
		}
	}
	for _, record := range records {
		fmt.Println(string(record))
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "transcripts:", err)
		os.Exit(1)
	}
}
