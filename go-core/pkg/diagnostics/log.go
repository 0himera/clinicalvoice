// Package diagnostics contains opt-in, private local dialogue traces.
package diagnostics

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// File bounds a debug log to two files, each at most five MiB.
type File struct {
	mu    sync.Mutex
	file  *os.File
	path  string
	limit int64
}

func Open(directory, name string) (*File, error) {
	if name == "" || filepath.Base(name) != name {
		return nil, errors.New("invalid log name")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("debug directory must be a real directory")
	}
	if err = os.Chmod(directory, 0700); err != nil {
		return nil, err
	}
	f := &File{path: filepath.Join(directory, name+".jsonl"), limit: 5 << 20}
	if err = f.open(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) open() error {
	if info, err := os.Lstat(f.path); err == nil && !info.Mode().IsRegular() {
		return errors.New("debug log must be a regular file")
	}
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if err = file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	f.file = file
	return nil
}

func (f *File) Write(data []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return 0, os.ErrClosed
	}
	if int64(len(data)) > f.limit {
		return 0, io.ErrShortWrite
	}
	info, err := f.file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size()+int64(len(data)) > f.limit {
		if err = f.file.Close(); err != nil {
			return 0, err
		}
		f.file = nil
		if err = os.Rename(f.path, f.path+".1"); err != nil {
			return 0, err
		}
		if err = f.open(); err != nil {
			return 0, err
		}
	}
	return f.file.Write(data)
}

func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}
