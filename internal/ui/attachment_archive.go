package mcodeui

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strings"
)

const archiveListLimit = 2000

type archiveEntry struct {
	Path  string
	Size  int64
	IsDir bool
}

type archiveKind uint8

const (
	archiveKindUnknown archiveKind = iota
	archiveKindZip
	archiveKindTar
	archiveKindTarGz
)

func detectArchiveKind(name string) archiveKind {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return archiveKindTarGz
	case strings.HasSuffix(lower, ".zip"):
		return archiveKindZip
	case strings.HasSuffix(lower, ".tar"):
		return archiveKindTar
	default:
		return archiveKindUnknown
	}
}

func isArchiveName(name string) bool {
	return detectArchiveKind(name) != archiveKindUnknown
}

func sanitizeArchiveEntryPath(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", false
	}
	s := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(s, "/") {
		return "", false
	}
	clean := path.Clean(s)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

func archiveEntryBaseName(p string) string {
	clean, ok := sanitizeArchiveEntryPath(p)
	if !ok {
		return ""
	}
	return path.Base(clean)
}

func openTarReader(kind archiveKind, r io.ReaderAt, size int64) (*tar.Reader, func() error, error) {
	sr := io.NewSectionReader(r, 0, size)
	if kind == archiveKindTarGz {
		gz, err := gzip.NewReader(sr)
		if err != nil {
			return nil, nil, err
		}
		return tar.NewReader(gz), gz.Close, nil
	}
	return tar.NewReader(sr), func() error { return nil }, nil
}

func listArchiveEntries(name string, r io.ReaderAt, size int64) ([]archiveEntry, bool, error) {
	if r == nil {
		return nil, false, errors.New("archive reader is nil")
	}
	if size <= 0 {
		return nil, false, errors.New("archive size must be positive")
	}
	kind := detectArchiveKind(name)
	if kind == archiveKindUnknown {
		return nil, false, fmt.Errorf("unsupported archive format: %s", name)
	}

	var entries []archiveEntry
	truncated := false

	switch kind {
	case archiveKindZip:
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return nil, false, err
		}
		for _, f := range zr.File {
			p, ok := sanitizeArchiveEntryPath(f.Name)
			if !ok {
				continue
			}
			if len(entries) >= archiveListLimit {
				truncated = true
				break
			}
			isDir := f.FileInfo().IsDir() || strings.HasSuffix(strings.ReplaceAll(f.Name, "\\", "/"), "/")
			entrySize := int64(math.MaxInt64)
			if f.UncompressedSize64 <= math.MaxInt64 {
				entrySize = int64(f.UncompressedSize64)
			}
			entries = append(entries, archiveEntry{
				Path:  p,
				Size:  entrySize,
				IsDir: isDir,
			})
		}
	case archiveKindTar, archiveKindTarGz:
		tr, closeFn, err := openTarReader(kind, r, size)
		if err != nil {
			return nil, false, err
		}
		defer closeFn()
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, false, err
			}
			if hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
				continue
			}
			p, ok := sanitizeArchiveEntryPath(hdr.Name)
			if !ok {
				continue
			}
			if len(entries) >= archiveListLimit {
				truncated = true
				break
			}
			entries = append(entries, archiveEntry{
				Path:  p,
				Size:  hdr.Size,
				IsDir: hdr.Typeflag == tar.TypeDir,
			})
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, truncated, nil
}

func readArchiveEntry(name string, r io.ReaderAt, size int64, entryPath string, maxBytes int64) ([]byte, error) {
	if r == nil {
		return nil, errors.New("archive reader is nil")
	}
	if size <= 0 {
		return nil, errors.New("archive size must be positive")
	}
	if maxBytes < 0 {
		return nil, errors.New("maxBytes must be non-negative")
	}
	target, ok := sanitizeArchiveEntryPath(entryPath)
	if !ok {
		return nil, fmt.Errorf("invalid archive entry path: %q", entryPath)
	}
	kind := detectArchiveKind(name)
	if kind == archiveKindUnknown {
		return nil, fmt.Errorf("unsupported archive format: %s", name)
	}

	switch kind {
	case archiveKindZip:
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			p, ok := sanitizeArchiveEntryPath(f.Name)
			if !ok || p != target {
				continue
			}
			if f.FileInfo().IsDir() || strings.HasSuffix(strings.ReplaceAll(f.Name, "\\", "/"), "/") {
				return nil, fmt.Errorf("archive entry is a directory: %s", target)
			}
			if f.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("archive entry is not a regular file: %s", target)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, readErr := readArchiveLimited(rc, maxBytes)
			closeErr := rc.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			return data, nil
		}
		return nil, fmt.Errorf("%w: %s", os.ErrNotExist, target)

	case archiveKindTar, archiveKindTarGz:
		tr, closeFn, err := openTarReader(kind, r, size)
		if err != nil {
			return nil, err
		}
		defer closeFn()
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				return nil, fmt.Errorf("%w: %s", os.ErrNotExist, target)
			}
			if err != nil {
				return nil, err
			}
			p, ok := sanitizeArchiveEntryPath(hdr.Name)
			if !ok || p != target {
				continue
			}
			if hdr.Typeflag == tar.TypeDir {
				return nil, fmt.Errorf("archive entry is a directory: %s", target)
			}
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
				return nil, fmt.Errorf("archive entry is not a regular file: %s", target)
			}
			return readArchiveLimited(tr, maxBytes)
		}
	}

	return nil, fmt.Errorf("unsupported archive format: %s", name)
}

func readArchiveLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	limit := maxBytes + 1
	if maxBytes == math.MaxInt64 {
		limit = math.MaxInt64
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("archive entry exceeds max size (%d bytes)", maxBytes)
	}
	return data, nil
}
