package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mcode/internal/fsutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

// Store 将会话条目存储在指定目录。
type Store struct{ dir string }

// 锁也覆盖不同 Store 实例对同一文件的访问。
var storeMu sync.Mutex

var (
	zstdEncOnce sync.Once
	zstdEncoder *zstd.Encoder
	zstdEncErr  error

	zstdDecOnce sync.Once
	zstdDecoder *zstd.Decoder
	zstdDecErr  error
)

func getZstdEncoder() (*zstd.Encoder, error) {
	zstdEncOnce.Do(func() {
		zstdEncoder, zstdEncErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
			zstd.WithEncoderConcurrency(1),
		)
	})
	return zstdEncoder, zstdEncErr
}

func getZstdDecoder() (*zstd.Decoder, error) {
	zstdDecOnce.Do(func() {
		zstdDecoder, zstdDecErr = zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1),
		)
	})
	return zstdDecoder, zstdDecErr
}

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
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		zstPath := path + ".zst"
		compressed, zerr := os.ReadFile(zstPath)
		if zerr == nil {
			dec, derr := getZstdDecoder()
			if derr != nil {
				return fmt.Errorf("init zstd decoder: %w", derr)
			}
			plain, derr := dec.DecodeAll(compressed, nil)
			if derr != nil {
				return fmt.Errorf("decompress session %q: %w", id, derr)
			}
			if werr := fsutil.WriteFileAtomic(path, plain, 0600); werr != nil {
				return werr
			}
			if rerr := os.Remove(zstPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				return rerr
			}
		} else if !errors.Is(zerr, os.ErrNotExist) {
			return zerr
		}
	} else if statErr != nil {
		return statErr
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

// Rewrite 原子替换会话中的所有条目。
func (s *Store) Rewrite(id string, entries ...any) error {
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
	f, err := os.CreateTemp(s.dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := fsutil.Rename(f.Name(), path); err != nil {
		return err
	}
	if err := os.Remove(path + ".zst"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open session %q: %w", id, err)
		}
		compressed, zerr := os.ReadFile(path + ".zst")
		if zerr != nil {
			if errors.Is(zerr, os.ErrNotExist) {
				return nil, fmt.Errorf("open session %q: %w", id, err)
			}
			return nil, fmt.Errorf("open compressed session %q: %w", id, zerr)
		}
		dec, derr := getZstdDecoder()
		if derr != nil {
			return nil, fmt.Errorf("init zstd decoder: %w", derr)
		}
		plain, derr := dec.DecodeAll(compressed, nil)
		if derr != nil {
			return nil, fmt.Errorf("decompress session %q: %w", id, derr)
		}
		return parseSessionLines(id, bytes.NewReader(plain))
	}
	defer f.Close()
	return parseSessionLines(id, f)
}

func parseSessionLines(id string, r io.Reader) ([]json.RawMessage, error) {
	reader := bufio.NewReader(r)
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
	storeMu.Lock()
	defer storeMu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return true
	}
	_, err = os.Stat(path + ".zst")
	return err == nil
}

// CompressIdle 将目录第一层中 mtime 早于 now-olderThan 的 .jsonl 无损压缩为 .jsonl.zst。
// 跳过符号链接和目录；每个文件在 storeMu 下完成压缩、原子写与解压校验后再删除原 .jsonl，
// 任何一步失败都保留原 .jsonl、清理半成品并记录后继续处理下一个。
func (s *Store) CompressIdle(olderThan time.Duration) (files int, savedBytes int64, err error) {
	if s == nil || s.dir == "" {
		return 0, 0, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	cutoff := time.Now().Add(-olderThan)
	var firstErr error
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 || e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") || len(name) <= len(".jsonl") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			if errors.Is(ierr, os.ErrNotExist) {
				continue
			}
			log.Printf("session: 读取会话文件 %s 信息失败: %v", name, ierr)
			if firstErr == nil {
				firstErr = ierr
			}
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}

		srcPath := filepath.Join(s.dir, name)
		saved, done, perErr := s.compressOneFile(srcPath, cutoff)
		if perErr != nil {
			log.Printf("session: 压缩会话文件 %s 失败: %v", srcPath, perErr)
			if firstErr == nil {
				firstErr = perErr
			}
			continue
		}
		if done {
			files++
			savedBytes += saved
		}
	}
	return files, savedBytes, firstErr
}

func (s *Store) compressOneFile(srcPath string, cutoff time.Time) (saved int64, compressed bool, err error) {
	storeMu.Lock()
	defer storeMu.Unlock()

	info, err := os.Lstat(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
		return 0, false, nil
	}
	if !info.ModTime().Before(cutoff) {
		return 0, false, nil
	}

	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return 0, false, err
	}
	enc, err := getZstdEncoder()
	if err != nil {
		return 0, false, fmt.Errorf("init zstd encoder: %w", err)
	}
	zstData := enc.EncodeAll(raw, nil)

	dstPath := srcPath + ".zst"
	if err := fsutil.WriteFileAtomic(dstPath, zstData, 0600); err != nil {
		return 0, false, err
	}

	written, err := os.ReadFile(dstPath)
	if err != nil {
		_ = os.Remove(dstPath)
		return 0, false, err
	}
	dec, err := getZstdDecoder()
	if err != nil {
		_ = os.Remove(dstPath)
		return 0, false, fmt.Errorf("init zstd decoder: %w", err)
	}
	decoded, err := dec.DecodeAll(written, nil)
	if err != nil {
		_ = os.Remove(dstPath)
		return 0, false, fmt.Errorf("verify zstd %s: %w", dstPath, err)
	}
	if !bytes.Equal(raw, decoded) {
		_ = os.Remove(dstPath)
		return 0, false, fmt.Errorf("verify zstd %s: content mismatch", dstPath)
	}

	if err := os.Remove(srcPath); err != nil {
		_ = os.Remove(dstPath)
		return 0, false, err
	}
	return int64(len(raw)) - int64(len(written)), true, nil
}
