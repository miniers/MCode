package noderuntime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeArchiveEntry struct {
	name     string
	content  []byte
	mode     int64
	typeflag byte
	linkname string
}

func buildFakeTarGz(t *testing.T, entries []fakeArchiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			if tf == tar.TypeDir {
				mode = 0o755
			} else {
				mode = 0o644
			}
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     mode,
			Size:     int64(len(e.content)),
			Typeflag: tf,
			Linkname: e.linkname,
		}
		if tf == tar.TypeDir || tf == tar.TypeSymlink {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%s): %v", e.name, err)
		}
		if tf == tar.TypeReg && len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("Write(%s): %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzw.Close: %v", err)
	}
	return buf.Bytes()
}

func buildFakeZip(t *testing.T, entries []fakeArchiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{
			Name:   e.name,
			Method: zip.Deflate,
		}
		mode := os.FileMode(e.mode)
		if mode == 0 {
			mode = 0o644
		}
		if e.typeflag == tar.TypeDir {
			if !strings.HasSuffix(hdr.Name, "/") {
				hdr.Name += "/"
			}
			hdr.SetMode(os.ModeDir | 0o755)
		} else if e.typeflag == tar.TypeSymlink {
			hdr.SetMode(os.ModeSymlink | 0o777)
		} else {
			hdr.SetMode(mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("CreateHeader(%s): %v", e.name, err)
		}
		body := e.content
		if e.typeflag == tar.TypeSymlink {
			body = []byte(e.linkname)
		}
		if len(body) > 0 {
			if _, err := w.Write(body); err != nil {
				t.Fatalf("zip Write(%s): %v", e.name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}
	return buf.Bytes()
}

func fakeNodeArchiveForCurrentPlatform(t *testing.T, version string) (pkgName string, archiveBytes []byte) {
	t.Helper()
	var err error
	var isZip bool
	pkgName, isZip, err = archiveName(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("当前平台不支持 Node 官方归档测试: %v", err)
	}
	topDir := strings.TrimSuffix(strings.TrimSuffix(pkgName, ".tar.gz"), ".zip")
	if isZip {
		// Windows 无法直接用 shell 脚本充当 node.exe；该分支仅在非 Windows 验证 zip 打包格式时使用。
		entries := []fakeArchiveEntry{
			{name: topDir + "/", typeflag: tar.TypeDir},
			{name: topDir + "/node.exe", content: []byte("fake"), mode: 0o755},
		}
		return pkgName, buildFakeZip(t, entries)
	}
	script := []byte("#!/bin/sh\necho \"" + version + "\"\n")
	entries := []fakeArchiveEntry{
		{name: topDir + "/", typeflag: tar.TypeDir},
		{name: topDir + "/bin/", typeflag: tar.TypeDir},
		{name: topDir + "/bin/node", content: script, mode: 0o755},
		{name: topDir + "/LICENSE", content: []byte("MIT\n"), mode: 0o644},
	}
	return pkgName, buildFakeTarGz(t, entries)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func setupMockDistServer(t *testing.T, indexJSON, version, pkgName string, archive []byte, shaSum string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(indexJSON))
		case "/" + version + "/SHASUMS256.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintf(w, "%s  %s\n", shaSum, pkgName)
		case "/" + version + "/" + pkgName:
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func overrideDistBase(t *testing.T, newBase string) {
	t.Helper()
	old := base
	base = newBase
	t.Cleanup(func() { base = old })
}

func overrideLookPath(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	old := lookPath
	lookPath = fn
	t.Cleanup(func() { lookPath = old })
}

func overrideEnviron(t *testing.T, fn func() []string) {
	t.Helper()
	old := environ
	environ = fn
	t.Cleanup(func() { environ = old })
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s 存在残留文件: %v", dir, names)
	}
}

func TestDownloadSuccessAndDetect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell 脚本假 node 仅在非 Windows 平台运行")
	}

	const version = "v22.20.0"
	pkgName, archive := fakeNodeArchiveForCurrentPlatform(t, version)
	indexJSON := `[
		{"version": "v23.1.0", "lts": false},
		{"version": "v22.20.0", "lts": "Jod"},
		{"version": "v20.18.0", "lts": "Iron"}
	]`
	srv := setupMockDistServer(t, indexJSON, version, pkgName, archive, sha256Hex(archive))
	overrideDistBase(t, srv.URL)
	overrideEnviron(t, os.Environ)

	runtimesDir := t.TempDir()
	var lastDone, lastTotal int64
	var calls int
	st, err := Download(context.Background(), runtimesDir, func(done, total int64) {
		calls++
		lastDone = done
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("Download 失败: %v", err)
	}
	if calls == 0 || lastTotal != int64(len(archive)) || lastDone != lastTotal {
		t.Fatalf("progress 异常: calls=%d done=%d total=%d expect=%d", calls, lastDone, lastTotal, len(archive))
	}

	wantBin := filepath.Join(runtimesDir, "node", version, "bin", "node")
	if st.Path != wantBin || st.Version != "22.20.0" || !st.Managed || !st.OK {
		t.Fatalf("Download 返回 Status 异常: %+v", st)
	}
	if got := BinDir(st); got != filepath.Dir(wantBin) {
		t.Fatalf("BinDir = %q, want %q", got, filepath.Dir(wantBin))
	}

	// 验证顶层目录已剥离，且可执行权限位保留。
	info, err := os.Stat(wantBin)
	if err != nil {
		t.Fatalf("Stat(%s): %v", wantBin, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("node 可执行权限丢失: %v", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(runtimesDir, "node", version, "LICENSE")); err != nil {
		t.Fatalf("期望顶层目录下的 LICENSE 就位: %v", err)
	}

	// 即使系统 PATH 找不到 node，Detect 也必须优先返回 mcode 下载的版本。
	overrideLookPath(t, func(string) (string, error) {
		return "", errors.New("not found")
	})
	detected := Detect(context.Background(), runtimesDir)
	if detected != st {
		t.Fatalf("Detect = %+v, want %+v", detected, st)
	}

	// 目标已存在时直接返回其 Status，不重复拉取归档。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.json" {
			_, _ = w.Write([]byte(indexJSON))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv2.Close)
	overrideDistBase(t, srv2.URL)
	cached, err := Download(context.Background(), runtimesDir, nil)
	if err != nil {
		t.Fatalf("目标已存在时不应再次下载归档: %v", err)
	}
	if cached != st {
		t.Fatalf("目标已存在返回 %+v, want %+v", cached, st)
	}
}

func TestDownloadSHA256MismatchLeavesNoResidue(t *testing.T) {
	const version = "v22.20.0"
	pkgName, archive := fakeNodeArchiveForCurrentPlatform(t, version)
	indexJSON := `[{"version": "v22.20.0", "lts": "Jod"}]`
	badSHA := strings.Repeat("0", 64)
	srv := setupMockDistServer(t, indexJSON, version, pkgName, archive, badSHA)
	overrideDistBase(t, srv.URL)

	runtimesDir := t.TempDir()
	_, err := Download(context.Background(), runtimesDir, nil)
	if err == nil {
		t.Fatal("sha256 不匹配时必须返回错误")
	}
	assertDirEmpty(t, runtimesDir)
}

func TestDownloadRejectsPathTraversalAndSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("仅在支持 tar.gz 平台执行完整下载路径穿越测试")
	}

	const version = "v22.20.0"
	pkgName, _, err := archiveName(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("跳过: %v", err)
	}
	topDir := strings.TrimSuffix(pkgName, ".tar.gz")
	indexJSON := `[{"version": "v22.20.0", "lts": "Jod"}]`

	cases := []struct {
		name    string
		entries []fakeArchiveEntry
	}{
		{
			name: "dotdot_entry",
			entries: []fakeArchiveEntry{
				{name: topDir + "/bin/node", content: []byte("#!/bin/sh\necho v22.20.0\n"), mode: 0o755},
				{name: "../evil", content: []byte("pwn"), mode: 0o644},
			},
		},
		{
			name: "nested_dotdot_entry",
			entries: []fakeArchiveEntry{
				{name: topDir + "/../../evil", content: []byte("pwn"), mode: 0o644},
			},
		},
		{
			name: "absolute_entry",
			entries: []fakeArchiveEntry{
				{name: "/etc/evil", content: []byte("pwn"), mode: 0o644},
			},
		},
		{
			name: "symlink_escape",
			entries: []fakeArchiveEntry{
				{name: topDir + "/bin/evil", typeflag: tar.TypeSymlink, linkname: "../../outside"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := buildFakeTarGz(t, tc.entries)
			srv := setupMockDistServer(t, indexJSON, version, pkgName, archive, sha256Hex(archive))
			overrideDistBase(t, srv.URL)

			runtimesDir := t.TempDir()
			if _, err := Download(context.Background(), runtimesDir, nil); err == nil {
				t.Fatalf("用例 %s 必须拒绝恶意归档", tc.name)
			}
			assertDirEmpty(t, runtimesDir)
		})
	}
}

func TestExtractZipRejectsPathTraversalAndPreservesPermissions(t *testing.T) {
	badZip := filepath.Join(t.TempDir(), "bad.zip")
	data := buildFakeZip(t, []fakeArchiveEntry{
		{name: "../evil", content: []byte("pwn"), mode: 0o644},
	})
	if err := os.WriteFile(badZip, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := extractZip(badZip, dst); err == nil {
		t.Fatal("extractZip 必须拒绝 ../evil")
	}

	goodZip := filepath.Join(t.TempDir(), "good.zip")
	goodData := buildFakeZip(t, []fakeArchiveEntry{
		{name: "node-v22.20.0-win-x64/", typeflag: tar.TypeDir},
		{name: "node-v22.20.0-win-x64/node.exe", content: []byte("exe"), mode: 0o755},
	})
	if err := os.WriteFile(goodZip, goodData, 0o600); err != nil {
		t.Fatal(err)
	}
	dstGood := t.TempDir()
	if err := extractZip(goodZip, dstGood); err != nil {
		t.Fatalf("extractZip 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstGood, "node.exe")); err != nil {
		t.Fatalf("剥离顶层目录后缺少 node.exe: %v", err)
	}
}

func TestDetectSystemNodeBelowMinVersionAndHighestManaged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell 脚本假 node 仅在非 Windows 平台运行")
	}

	overrideEnviron(t, os.Environ)

	// 系统 node 版本低于 MinVersion (22.18.0) -> OK=false, Managed=false。
	sysDir := t.TempDir()
	sysNode := filepath.Join(sysDir, "node")
	if err := os.WriteFile(sysNode, []byte("#!/bin/sh\necho \"v20.11.1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	overrideLookPath(t, func(name string) (string, error) {
		if name == "node" {
			return sysNode, nil
		}
		return "", errors.New("unexpected")
	})

	emptyRuntimes := t.TempDir()
	st := Detect(context.Background(), emptyRuntimes)
	if st.Path != sysNode || st.Version != "20.11.1" || st.Managed || st.OK {
		t.Fatalf("低版本系统 node Detect = %+v", st)
	}

	// 多个托管版本时选择版本号最高的。
	runtimesDir := t.TempDir()
	for _, item := range []struct {
		dir string
		out string
	}{
		{"v22.18.0", "v22.18.0"},
		{"v22.20.0", "v22.20.0"},
	} {
		bin := filepath.Join(runtimesDir, "node", item.dir, "bin", "node")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \""+item.out+"\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	best := Detect(context.Background(), runtimesDir)
	wantPath := filepath.Join(runtimesDir, "node", "v22.20.0", "bin", "node")
	if best.Path != wantPath || best.Version != "22.20.0" || !best.Managed || !best.OK {
		t.Fatalf("多版本托管 node Detect = %+v, want Path=%s", best, wantPath)
	}
}

func TestArchiveNameUnsupportedPlatform(t *testing.T) {
	if _, _, err := archiveName("v22.20.0", "freebsd", "amd64"); err == nil {
		t.Fatal("不支持的平台必须返回错误")
	}
	if _, _, err := archiveName("v22.20.0", "darwin", "386"); err == nil {
		t.Fatal("不支持的架构必须返回错误")
	}
}
