package mcodeui

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

type testArchiveItem struct {
	name  string
	body  []byte
	isDir bool
}

func buildTestZip(t *testing.T, items []testArchiveItem) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, it := range items {
		if it.isDir {
			name := strings.TrimSuffix(it.name, "/") + "/"
			if _, err := zw.Create(name); err != nil {
				t.Fatalf("zip create dir %q: %v", it.name, err)
			}
			continue
		}
		w, err := zw.Create(it.name)
		if err != nil {
			t.Fatalf("zip create %q: %v", it.name, err)
		}
		if _, err := w.Write(it.body); err != nil {
			t.Fatalf("zip write %q: %v", it.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func buildTestTar(t *testing.T, items []testArchiveItem, gzipped bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var gw *gzip.Writer
	var tw *tar.Writer
	if gzipped {
		gw = gzip.NewWriter(&buf)
		tw = tar.NewWriter(gw)
	} else {
		tw = tar.NewWriter(&buf)
	}
	for _, it := range items {
		if it.isDir {
			hdr := &tar.Header{
				Name:     strings.TrimSuffix(it.name, "/") + "/",
				Mode:     0o755,
				Typeflag: tar.TypeDir,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatalf("tar write dir header %q: %v", it.name, err)
			}
			continue
		}
		hdr := &tar.Header{
			Name:     it.name,
			Mode:     0o644,
			Size:     int64(len(it.body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar write header %q: %v", it.name, err)
		}
		if _, err := tw.Write(it.body); err != nil {
			t.Fatalf("tar write body %q: %v", it.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if gw != nil {
		if err := gw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
	}
	return buf.Bytes()
}

func TestAttachArchiveIsArchiveNameAndBaseName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"bundle.zip", true},
		{"bundle.ZIP", true},
		{"bundle.tar", true},
		{"bundle.TAR", true},
		{"bundle.tar.gz", true},
		{"bundle.TAR.GZ", true},
		{"bundle.tgz", true},
		{"bundle.TGZ", true},
		{"path/to/archive.Tar.Gz", true},
		{"notes.txt", false},
		{"file.gz", false},
		{"file.rar", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isArchiveName(tc.name); got != tc.want {
			t.Fatalf("isArchiveName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}

	baseCases := []struct {
		in   string
		want string
	}{
		{"a/b/c.txt", "c.txt"},
		{"c.txt", "c.txt"},
		{`a\b\c.txt`, "c.txt"},
		{"../evil", ""},
		{"/abs", ""},
		{"", ""},
	}
	for _, tc := range baseCases {
		if got := archiveEntryBaseName(tc.in); got != tc.want {
			t.Fatalf("archiveEntryBaseName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAttachArchiveListSortedAndRead(t *testing.T) {
	items := []testArchiveItem{
		{name: "z/last.txt", body: []byte("last")},
		{name: "a/dir/", isDir: true},
		{name: "./a/first.txt", body: []byte("first content")},
		{name: "m.txt", body: []byte("mid")},
	}

	formats := []struct {
		name string
		data []byte
	}{
		{"sample.zip", buildTestZip(t, items)},
		{"sample.tar", buildTestTar(t, items, false)},
		{"sample.tar.gz", buildTestTar(t, items, true)},
		{"sample.tgz", buildTestTar(t, items, true)},
	}

	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			r := bytes.NewReader(f.data)
			entries, truncated, err := listArchiveEntries(f.name, r, int64(len(f.data)))
			if err != nil {
				t.Fatalf("listArchiveEntries: %v", err)
			}
			if truncated {
				t.Fatalf("expected truncated=false, got true")
			}
			wantPaths := []string{"a/dir", "a/first.txt", "m.txt", "z/last.txt"}
			if len(entries) != len(wantPaths) {
				t.Fatalf("got %d entries, want %d: %+v", len(entries), len(wantPaths), entries)
			}
			for i, wp := range wantPaths {
				if entries[i].Path != wp {
					t.Fatalf("entry[%d].Path = %q, want %q", i, entries[i].Path, wp)
				}
			}
			if !entries[0].IsDir {
				t.Fatalf("expected a/dir IsDir=true")
			}
			if entries[1].IsDir || entries[1].Size != int64(len("first content")) {
				t.Fatalf("unexpected entry[1]: %+v", entries[1])
			}

			got, err := readArchiveEntry(f.name, r, int64(len(f.data)), "a/first.txt", 1024)
			if err != nil {
				t.Fatalf("readArchiveEntry: %v", err)
			}
			if string(got) != "first content" {
				t.Fatalf("readArchiveEntry = %q, want %q", string(got), "first content")
			}
			if _, err := readArchiveEntry(f.name, r, int64(len(f.data)), "a/dir", 1024); err == nil {
				t.Fatalf("expected error reading directory entry")
			}
			if _, err := readArchiveEntry(f.name, r, int64(len(f.data)), "missing.txt", 1024); err == nil {
				t.Fatalf("expected error reading missing entry")
			}
		})
	}
}

func TestAttachArchiveListTruncation(t *testing.T) {
	items := make([]testArchiveItem, archiveListLimit+5)
	for i := range items {
		items[i] = testArchiveItem{
			name: fmt.Sprintf("f_%04d.txt", len(items)-i),
			body: []byte("x"),
		}
	}

	formats := []struct {
		name string
		data []byte
	}{
		{"many.zip", buildTestZip(t, items)},
		{"many.tar", buildTestTar(t, items, false)},
		{"many.tar.gz", buildTestTar(t, items, true)},
	}

	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			r := bytes.NewReader(f.data)
			entries, truncated, err := listArchiveEntries(f.name, r, int64(len(f.data)))
			if err != nil {
				t.Fatalf("listArchiveEntries: %v", err)
			}
			if !truncated {
				t.Fatalf("expected truncated=true")
			}
			if len(entries) != archiveListLimit {
				t.Fatalf("got %d entries, want %d", len(entries), archiveListLimit)
			}
			for i := 1; i < len(entries); i++ {
				if entries[i-1].Path > entries[i].Path {
					t.Fatalf("entries not sorted at %d: %q > %q", i, entries[i-1].Path, entries[i].Path)
				}
			}
		})
	}
}

func TestAttachArchiveMaxBytesAndSpoofedZipSize(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 256)
	items := []testArchiveItem{{name: "big.txt", body: payload}}

	formats := []struct {
		name string
		data []byte
	}{
		{"limit.zip", buildTestZip(t, items)},
		{"limit.tar", buildTestTar(t, items, false)},
		{"limit.tar.gz", buildTestTar(t, items, true)},
	}

	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			r := bytes.NewReader(f.data)
			got, err := readArchiveEntry(f.name, r, int64(len(f.data)), "big.txt", int64(len(payload)))
			if err != nil {
				t.Fatalf("exact maxBytes read failed: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("payload mismatch")
			}
			if _, err := readArchiveEntry(f.name, r, int64(len(f.data)), "big.txt", int64(len(payload)-1)); err == nil {
				t.Fatalf("expected error when entry exceeds maxBytes")
			}
		})
	}

	// 伪造 zip 中央目录头里的 UncompressedSize 为 4，验证读取时仍按实际解压流限制拦截。
	bombPayload := bytes.Repeat([]byte("B"), 2048)
	rawZip := buildTestZip(t, []testArchiveItem{{name: "bomb.txt", body: bombPayload}})
	cdSig := []byte{0x50, 0x4b, 0x01, 0x02}
	cdIdx := bytes.Index(rawZip, cdSig)
	if cdIdx < 0 || cdIdx+28 > len(rawZip) {
		t.Fatalf("central directory signature not found")
	}
	if gotSize := binary.LittleEndian.Uint32(rawZip[cdIdx+24 : cdIdx+28]); gotSize != uint32(len(bombPayload)) {
		t.Fatalf("unexpected header size %d, want %d", gotSize, len(bombPayload))
	}
	binary.LittleEndian.PutUint32(rawZip[cdIdx+24:cdIdx+28], 4)

	r := bytes.NewReader(rawZip)
	entries, _, err := listArchiveEntries("bomb.zip", r, int64(len(rawZip)))
	if err != nil {
		t.Fatalf("listArchiveEntries(bomb.zip): %v", err)
	}
	if len(entries) != 1 || entries[0].Size != 4 {
		t.Fatalf("expected spoofed header size=4, got %+v", entries)
	}
	if _, err := readArchiveEntry("bomb.zip", r, int64(len(rawZip)), "bomb.txt", 64); err == nil {
		t.Fatalf("expected error when actual uncompressed stream exceeds maxBytes")
	}
}

func TestAttachArchiveUntrustedPathsSkippedAndRejected(t *testing.T) {
	items := []testArchiveItem{
		{name: "../evil", body: []byte("bad1")},
		{name: "/abs", body: []byte("bad2")},
		{name: "sub/../../escape.txt", body: []byte("bad3")},
		{name: `..\win_evil`, body: []byte("bad4")},
		{name: `\win_abs`, body: []byte("bad5")},
		{name: "safe/ok.txt", body: []byte("good")},
	}

	formats := []struct {
		name string
		data []byte
	}{
		{"evil.zip", buildTestZip(t, items)},
		{"evil.tar", buildTestTar(t, items, false)},
		{"evil.tar.gz", buildTestTar(t, items, true)},
	}

	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			r := bytes.NewReader(f.data)
			entries, truncated, err := listArchiveEntries(f.name, r, int64(len(f.data)))
			if err != nil {
				t.Fatalf("listArchiveEntries: %v", err)
			}
			if truncated {
				t.Fatalf("expected truncated=false")
			}
			if len(entries) != 1 || entries[0].Path != "safe/ok.txt" {
				t.Fatalf("expected only safe/ok.txt, got %+v", entries)
			}

			for _, badPath := range []string{"../evil", "/abs", "sub/../../escape.txt", `..\win_evil`, `\win_abs`} {
				if _, err := readArchiveEntry(f.name, r, int64(len(f.data)), badPath, 1024); err == nil {
					t.Fatalf("expected error reading untrusted path %q", badPath)
				}
			}
		})
	}
}

func TestAttachArchiveInvalidNameOrCorruptData(t *testing.T) {
	validZip := buildTestZip(t, []testArchiveItem{{name: "a.txt", body: []byte("hi")}})
	r := bytes.NewReader(validZip)
	if _, _, err := listArchiveEntries("notes.txt", r, int64(len(validZip))); err == nil {
		t.Fatalf("expected error for non-archive name in listArchiveEntries")
	}
	if _, err := readArchiveEntry("notes.txt", r, int64(len(validZip)), "a.txt", 1024); err == nil {
		t.Fatalf("expected error for non-archive name in readArchiveEntry")
	}

	corrupt := []byte("definitely not an archive payload")
	cr := bytes.NewReader(corrupt)
	for _, name := range []string{"bad.zip", "bad.tar", "bad.tar.gz", "bad.tgz"} {
		if _, _, err := listArchiveEntries(name, cr, int64(len(corrupt))); err == nil {
			t.Fatalf("expected error listing corrupt %s", name)
		}
		if _, err := readArchiveEntry(name, cr, int64(len(corrupt)), "a.txt", 1024); err == nil {
			t.Fatalf("expected error reading corrupt %s", name)
		}
	}
}
