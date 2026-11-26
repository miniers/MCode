// Package noderuntime 负责探测和按需下载托管的 Node.js 运行时。
package noderuntime

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/cliupdate"
	"mcode/internal/providers/loginenv"
)

// MinVersion 是 open-artifact 要求的最低 Node.js 版本。
const MinVersion = "22.18.0"

const probeTimeout = 5 * time.Second

// 以下包级变量用于在单元测试中替换远端地址、环境探测与当前平台信息。
var (
	base          = "https://nodejs.org/dist"
	lookPath      = loginenv.LookPath
	environ       = loginenv.Environ
	currentGOOS   = runtime.GOOS
	currentGOARCH = runtime.GOARCH
	httpClient    = &http.Client{Timeout: 5 * time.Minute}
)

// Status 描述探测到的 Node.js 运行时状态。
type Status struct {
	Path    string
	Version string
	Managed bool
	OK      bool
}

// BinDir 返回 Node 可执行文件所在目录，供调用方放到 PATH 最前面。
func BinDir(s Status) string {
	if s.Path == "" {
		return ""
	}
	return filepath.Dir(s.Path)
}

// Detect 探测可用的 Node.js：优先查找 runtimesDir 下由 mcode 管理的最高版本，
// 找不到时回退到系统 PATH（经由 loginenv）。
func Detect(ctx context.Context, runtimesDir string) Status {
	if ctx == nil {
		ctx = context.Background()
	}
	if runtimesDir != "" {
		if st := detectManaged(ctx, runtimesDir); st.Path != "" {
			return st
		}
	}
	sysPath, err := lookPath("node")
	if err != nil || sysPath == "" {
		return Status{}
	}
	return probeNode(ctx, sysPath, false)
}

func detectManaged(ctx context.Context, runtimesDir string) Status {
	nodeRoot := filepath.Join(runtimesDir, "node")
	entries, err := os.ReadDir(nodeRoot)
	if err != nil {
		return Status{}
	}
	type candidate struct {
		version string
		binPath string
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		binPath := nodeBinaryPath(filepath.Join(nodeRoot, entry.Name()))
		info, err := os.Stat(binPath)
		if err != nil || info.IsDir() {
			continue
		}
		candidates = append(candidates, candidate{
			version: entry.Name(),
			binPath: binPath,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		cmp := cliupdate.CompareVersions(candidates[i].version, candidates[j].version)
		if cmp != 0 {
			return cmp > 0
		}
		return candidates[i].version > candidates[j].version
	})
	for _, c := range candidates {
		if st := probeNode(ctx, c.binPath, true); st.Path != "" {
			return st
		}
	}
	return Status{}
}

func nodeBinaryPath(versionDir string) string {
	if currentGOOS == "windows" {
		return filepath.Join(versionDir, "node.exe")
	}
	return filepath.Join(versionDir, "bin", "node")
}

func probeNode(ctx context.Context, binPath string, managed bool) Status {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := procattr.HideWindow(exec.CommandContext(probeCtx, binPath, "--version"))
	cmd.Env = environ()
	out, err := cmd.Output()
	if err != nil {
		return Status{}
	}
	ver := cliupdate.ParseVersion(string(out))
	if ver == "" {
		return Status{}
	}
	return Status{
		Path:    binPath,
		Version: ver,
		Managed: managed,
		OK:      cliupdate.CompareVersions(ver, MinVersion) >= 0,
	}
}

// Download 从 base 拉取最新 LTS 版本的 Node.js，校验 SHASUMS256 后解压到
// <runtimesDir>/node/<version>/。目标已存在时直接返回其 Status。
func Download(ctx context.Context, runtimesDir string, progress func(done, total int64)) (Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if runtimesDir == "" {
		return Status{}, errors.New("noderuntime: runtimesDir 不能为空")
	}

	version, err := fetchLatestLTS(ctx)
	if err != nil {
		return Status{}, err
	}
	pkgName, isZip, err := archiveName(version, currentGOOS, currentGOARCH)
	if err != nil {
		return Status{}, err
	}

	targetDir := filepath.Join(runtimesDir, "node", version)
	if _, err := os.Stat(targetDir); err == nil {
		st := probeNode(ctx, nodeBinaryPath(targetDir), true)
		if st.Path == "" {
			return Status{}, fmt.Errorf("noderuntime: 目标目录已存在但 Node 无效: %s", targetDir)
		}
		return st, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}

	expectedSHA, err := fetchSHA256(ctx, version, pkgName)
	if err != nil {
		return Status{}, err
	}

	if err := os.MkdirAll(runtimesDir, 0o755); err != nil {
		return Status{}, err
	}
	workDir, err := os.MkdirTemp(runtimesDir, ".node-tmp-*")
	if err != nil {
		return Status{}, err
	}
	defer os.RemoveAll(workDir)

	archivePath := filepath.Join(workDir, pkgName)
	pkgURL := strings.TrimRight(base, "/") + "/" + version + "/" + pkgName
	if err := downloadAndVerify(ctx, pkgURL, archivePath, expectedSHA, progress); err != nil {
		return Status{}, err
	}

	extractDir := filepath.Join(workDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return Status{}, err
	}
	if isZip {
		err = extractZip(archivePath, extractDir)
	} else {
		err = extractTarGz(archivePath, extractDir)
	}
	if err != nil {
		return Status{}, err
	}

	st := probeNode(ctx, nodeBinaryPath(extractDir), true)
	if st.Path == "" {
		return Status{}, errors.New("noderuntime: 解压后的 Node 无法执行")
	}

	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.Rename(extractDir, targetDir); err != nil {
		return Status{}, err
	}
	st.Path = nodeBinaryPath(targetDir)
	return st, nil
}

type indexEntry struct {
	Version string          `json:"version"`
	LTS     json.RawMessage `json:"lts"`
}

func fetchLatestLTS(ctx context.Context) (string, error) {
	url := strings.TrimRight(base, "/") + "/index.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("noderuntime: 请求 index.json 返回 %d", resp.StatusCode)
	}
	var entries []indexEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&entries); err != nil {
		return "", fmt.Errorf("noderuntime: 解析 index.json 失败: %w", err)
	}
	best := ""
	for _, entry := range entries {
		if !isLTSEntry(entry.LTS) {
			continue
		}
		ver := strings.TrimSpace(entry.Version)
		if cliupdate.ParseVersion(ver) == "" {
			continue
		}
		if best == "" || cliupdate.CompareVersions(ver, best) > 0 {
			best = ver
		}
	}
	if best == "" {
		return "", errors.New("noderuntime: index.json 中没有可用的 LTS 版本")
	}
	if !strings.HasPrefix(best, "v") {
		best = "v" + best
	}
	return best, nil
}

func isLTSEntry(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "false" && trimmed != "null"
}

func archiveName(version, goos, goarch string) (name string, isZip bool, err error) {
	var osPart, archPart, ext string
	switch goos {
	case "darwin":
		osPart = "darwin"
		ext = ".tar.gz"
	case "linux":
		osPart = "linux"
		ext = ".tar.gz"
	case "windows":
		osPart = "win"
		ext = ".zip"
		isZip = true
	default:
		return "", false, fmt.Errorf("noderuntime: 不支持的平台: %s/%s", goos, goarch)
	}

	switch goarch {
	case "amd64":
		archPart = "x64"
	case "arm64":
		archPart = "arm64"
	default:
		return "", false, fmt.Errorf("noderuntime: 不支持的平台: %s/%s", goos, goarch)
	}

	// nodejs.org 不提供 linux-arm64 以外的平台组合时这里只允许明确支持的 6 种。
	if goos == "linux" && archPart != "x64" && archPart != "arm64" {
		return "", false, fmt.Errorf("noderuntime: 不支持的平台: %s/%s", goos, goarch)
	}
	return fmt.Sprintf("node-%s-%s-%s%s", version, osPart, archPart, ext), isZip, nil
}

func fetchSHA256(ctx context.Context, version, pkgName string) (string, error) {
	url := strings.TrimRight(base, "/") + "/" + version + "/SHASUMS256.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("noderuntime: 请求 SHASUMS256.txt 返回 %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[1] == pkgName {
			sum := strings.ToLower(fields[0])
			if len(sum) != 64 {
				return "", fmt.Errorf("noderuntime: 无效的 sha256 校验值: %s", fields[0])
			}
			return sum, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("noderuntime: SHASUMS256.txt 中缺少 %s", pkgName)
}

type progressWriter struct {
	done  int64
	total int64
	cb    func(done, total int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.done += int64(n)
	if w.cb != nil {
		w.cb(w.done, w.total)
	}
	return n, nil
}

func downloadAndVerify(ctx context.Context, url, dstPath, expectedSHA string, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("noderuntime: 下载归档返回 %d", resp.StatusCode)
	}

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	hasher := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, cb: progress}
	if _, err := io.Copy(io.MultiWriter(out, hasher, pw), resp.Body); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if actualSHA != strings.ToLower(expectedSHA) {
		return fmt.Errorf("noderuntime: sha256 校验失败: got %s, want %s", actualSHA, expectedSHA)
	}
	return nil
}

func extractTarGz(archivePath, dstDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		isDir := hdr.Typeflag == tar.TypeDir
		rel, skip, err := sanitizeAndStripTop(hdr.Name, isDir)
		if err != nil {
			return err
		}
		if skip {
			continue
		}

		destPath, err := safeJoin(dstDir, rel)
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkParent(dstDir, destPath); err != nil {
			return err
		}

		mode := os.FileMode(hdr.Mode) & os.ModePerm
		switch hdr.Typeflag {
		case tar.TypeDir:
			if mode == 0 {
				mode = 0o755
			}
			// 解压过程中目录需保持可写可执行，以便创建子文件。
			if err := os.MkdirAll(destPath, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if mode == 0 {
				mode = 0o644
			}
			if err := writeExtractedFile(destPath, tr, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := validateSymlinkTarget(dstDir, destPath, hdr.Linkname); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(filepath.FromSlash(hdr.Linkname), destPath); err != nil {
				return err
			}
		case tar.TypeLink:
			linkRel, linkSkip, err := sanitizeAndStripTop(hdr.Linkname, false)
			if err != nil || linkSkip {
				return fmt.Errorf("noderuntime: 非法硬链接目标: %s", hdr.Linkname)
			}
			targetPath, err := safeJoin(dstDir, linkRel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return err
			}
			if err := os.Link(targetPath, destPath); err != nil {
				return err
			}
		default:
			return fmt.Errorf("noderuntime: 不支持的 tar 条目类型 %q: %s", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

func extractZip(archivePath, dstDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		isDir := f.FileInfo().IsDir()
		rel, skip, err := sanitizeAndStripTop(f.Name, isDir)
		if err != nil {
			return err
		}
		if skip {
			continue
		}

		destPath, err := safeJoin(dstDir, rel)
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkParent(dstDir, destPath); err != nil {
			return err
		}

		mode := f.Mode() & os.ModePerm
		if f.Mode()&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			linkBytes, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			_ = rc.Close()
			if readErr != nil {
				return readErr
			}
			linkTarget := string(linkBytes)
			if err := validateSymlinkTarget(dstDir, destPath, linkTarget); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(filepath.FromSlash(linkTarget), destPath); err != nil {
				return err
			}
			continue
		}

		if isDir {
			if mode == 0 {
				mode = 0o755
			}
			if err := os.MkdirAll(destPath, mode|0o700); err != nil {
				return err
			}
			continue
		}

		if mode == 0 {
			mode = 0o644
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		writeErr := writeExtractedFile(destPath, rc, mode)
		_ = rc.Close()
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

// sanitizeAndStripTop 校验归档条目路径并剥离顶层目录。
func sanitizeAndStripTop(raw string, isDir bool) (rel string, skip bool, err error) {
	if raw == "" {
		return "", false, errors.New("noderuntime: 归档包含空路径")
	}
	if strings.Contains(raw, "\\") || strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
		return "", false, fmt.Errorf("noderuntime: 拒绝非法归档路径: %s", raw)
	}
	if len(raw) >= 2 && raw[1] == ':' {
		return "", false, fmt.Errorf("noderuntime: 拒绝带盘符的归档路径: %s", raw)
	}

	trimmed := strings.TrimPrefix(raw, "./")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" || trimmed == "." {
		if isDir {
			return "", true, nil
		}
		return "", false, fmt.Errorf("noderuntime: 拒绝非法归档路径: %s", raw)
	}

	parts := strings.Split(trimmed, "/")
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return "", false, fmt.Errorf("noderuntime: 拒绝包含越界段的归档路径: %s", raw)
		}
	}

	if len(parts) == 1 {
		if isDir {
			return "", true, nil
		}
		return "", false, fmt.Errorf("noderuntime: 归档条目缺少顶层目录: %s", raw)
	}
	return strings.Join(parts[1:], "/"), false, nil
}

func safeJoin(dstDir, slashRel string) (string, error) {
	cleanRel := filepath.Clean(filepath.FromSlash(slashRel))
	if cleanRel == "." || cleanRel == ".." || filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("noderuntime: 拒绝越界路径: %s", slashRel)
	}
	destPath := filepath.Join(dstDir, cleanRel)
	rel, err := filepath.Rel(dstDir, destPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("noderuntime: 路径逃出目标目录: %s", slashRel)
	}
	return destPath, nil
}

func validateSymlinkTarget(dstDir, linkPath, target string) error {
	if target == "" || strings.Contains(target, "\\") || strings.HasPrefix(target, "/") || filepath.IsAbs(target) {
		return fmt.Errorf("noderuntime: 拒绝指向绝对路径的符号链接: %s", target)
	}
	if len(target) >= 2 && target[1] == ':' {
		return fmt.Errorf("noderuntime: 拒绝带盘符的符号链接: %s", target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), filepath.FromSlash(target)))
	rel, err := filepath.Rel(dstDir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("noderuntime: 拒绝指向目标目录外的符号链接: %s -> %s", linkPath, target)
	}
	return nil
}

func ensureNoSymlinkParent(dstDir, destPath string) error {
	rel, err := filepath.Rel(dstDir, filepath.Dir(destPath))
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := dstDir
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("noderuntime: 拒绝经由符号链接目录写入: %s", cur)
		}
	}
	return nil
}

func writeExtractedFile(destPath string, src io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(destPath, mode)
}
