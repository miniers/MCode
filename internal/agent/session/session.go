package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

// Store 将会话条目存储在指定目录。
type Store struct{ dir string }

// 锁也覆盖不同 Store 实例对同一文件的访问。
var storeMu sync.Mutex

// NewStore 创建会话存储。
func NewStore(dir string) *Store { return &Store{dir: dir} }

// NewID 生成新的会话 ID。
func NewID() string { return uuid.NewString() }

func (s *Store) path(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", fmt.Errorf("invalid session id %q", id)
		}
	}
	return filepath.Join(s.dir, id+".jsonl"), nil
}

// Append 向会话追加 JSON 条目。
func (s *Store) Append(id string, entries ...any) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	var data []byte
	for _, entry := range entries {
		b, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshal session entry: %w", err)
		}
		data = append(data, b...)
		data = append(data, '\n')
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if info.Size() > 0 && len(data) > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			f.Close()
			return err
		}
		if last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// Load 读取会话，并忽略末尾不完整的条目。
func (s *Store) Load(id string) ([]json.RawMessage, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session %q: %w", id, err)
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var entries []json.RawMessage
	sawInvalid := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if len(bytes.TrimSpace(line)) > 0 {
			if json.Valid(line) {
				entries = append(entries, json.RawMessage(line))
			} else {
				// 跳过损坏的行，保留会话中其余有效记录。
				sawInvalid = true
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if len(entries) == 0 && sawInvalid {
		return nil, fmt.Errorf("invalid JSON in session %q", id)
	}
	return entries, nil
}

// Exists 判断会话文件是否存在。
func (s *Store) Exists(id string) bool {
	path, err := s.path(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
