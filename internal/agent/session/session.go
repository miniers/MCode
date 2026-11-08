package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

type Store struct { dir string }

// 锁也覆盖不同 Store 实例对同一文件的访问。
var storeMu sync.Mutex

func NewStore(dir string) *Store { return &Store{dir: dir} }
func NewID() string { return uuid.NewString() }

func (s *Store) path(id string) (string, error) {
	if id == "" { return "", fmt.Errorf("invalid session id %q", id) }
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", fmt.Errorf("invalid session id %q", id)
		}
	}
	return filepath.Join(s.dir, id+".jsonl"), nil
}

func (s *Store) Append(id string, entries ...any) error {
	path, err := s.path(id)
	if err != nil { return err }
	var data []byte
	for _, entry := range entries {
		b, err := json.Marshal(entry)
		if err != nil { return fmt.Errorf("marshal session entry: %w", err) }
		data = append(data, b...)
		data = append(data, '\n')
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil { return err }
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil { return err }
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil { return writeErr }
	return closeErr
}

func (s *Store) Load(id string) ([]json.RawMessage, error) {
	path, err := s.path(id)
	if err != nil { return nil, err }
	storeMu.Lock()
	defer storeMu.Unlock()
	f, err := os.Open(path)
	if err != nil { return nil, fmt.Errorf("open session %q: %w", id, err) }
	defer f.Close()
	reader := bufio.NewReader(f)
	var entries []json.RawMessage
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF { return nil, readErr }
		if len(line) > 0 {
			if !json.Valid(line) {
				if readErr == io.EOF { break }
				// 末行即使带换行符，也可能是在崩溃时留下的无效 JSON。
				if _, err := reader.Peek(1); err == io.EOF { break } else if err != nil { return nil, err }
				return nil, fmt.Errorf("invalid JSON in session %q", id)
			}
			entries = append(entries, json.RawMessage(line))
		}
		if readErr == io.EOF { break }
	}
	return entries, nil
}

func (s *Store) Exists(id string) bool {
	path, err := s.path(id)
	if err != nil { return false }
	_, err = os.Stat(path)
	return err == nil
}
