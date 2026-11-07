package mcodeui

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// openLineGOOS 是「按行打开」使用的平台标识，测试中可替换；只有 macOS 的编辑器支持这种 URL scheme。
var openLineGOOS = runtime.GOOS

// openEditorURLRunner 执行打开编辑器 URL 的命令，测试中可替换。
var openEditorURLRunner = func(target string) error {
	cmd := exec.Command("open", target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// openLineInstalledApps 返回「打开方式」里已安装的应用列表，测试中可替换。
// 单独抽出是因为 installedOpenInApps 依赖文件存在判断，测试里不便构造真实 .app 目录。
var openLineInstalledApps = func(s *chatState) []openInApp {
	return installedOpenInApps(currentWorkbenchConfig(s), openInStatFn)
}

// editorLineScheme 按应用名、BundleID 或 .app 名识别支持「打开到指定行」的编辑器，返回其 URL scheme。
// 识别忽略大小写；不支持的应用返回空串。
func editorLineScheme(app openInApp) string {
	name := strings.ToLower(strings.TrimSpace(app.Name))
	bundle := strings.ToLower(strings.TrimSpace(app.BundleID))
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(strings.TrimSpace(app.AppPath)), ".app"))
	switch {
	case name == "visual studio code" || name == "vscode" || base == "visual studio code" || bundle == "com.microsoft.vscode":
		return "vscode"
	case name == "cursor" || base == "cursor" || bundle == "com.todesktop.230313mzl4w4u92":
		return "cursor"
	case name == "windsurf" || base == "windsurf" || bundle == "com.exafunction.windsurf":
		return "windsurf"
	case name == "zed" || base == "zed" || bundle == "dev.zed.zed":
		return "zed"
	}
	return ""
}

// editorLineURL 把应用、文件与行号拼成编辑器能直接跳行的 URL，如 vscode://file/a/b.go:12:5。
// 路径按 URL 路径规则转义（保留斜杠）；col 小于 1 时省略。应用不支持时返回 false。
func editorLineURL(app openInApp, path string, line, col int) (string, bool) {
	scheme := editorLineScheme(app)
	if scheme == "" || strings.TrimSpace(path) == "" || line < 1 {
		return "", false
	}
	escaped := (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
	if !strings.HasPrefix(escaped, "/") {
		escaped = "/" + escaped
	}
	target := fmt.Sprintf("%s://file%s:%d", scheme, escaped, line)
	if col >= 1 {
		target += fmt.Sprintf(":%d", col)
	}
	return target, true
}

// openPathAtLine 用「打开方式」里第一个支持跳行的编辑器打开文件并跳到指定行。
// 非 macOS、没有行号、找不到编辑器或打开失败时返回 false，调用方沿用原有打开方式。
func openPathAtLine(s *chatState, path string, line, col int) bool {
	if line < 1 || strings.TrimSpace(path) == "" || openLineGOOS != "darwin" {
		return false
	}
	target := path
	if !filepath.IsAbs(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return false
		}
		target = abs
	}
	for _, app := range openLineInstalledApps(s) {
		link, ok := editorLineURL(app, target, line, col)
		if !ok {
			continue
		}
		if err := openEditorURLRunner(link); err != nil {
			return false
		}
		return true
	}
	return false
}
