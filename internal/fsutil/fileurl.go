package fsutil

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// FileURL 将绝对路径转换为本地文件 URL。
func FileURL(path string) *url.URL { return fileURL(path, runtime.GOOS) }

func fileURL(path, goos string) *url.URL {
	if goos == "windows" {
		path = strings.ReplaceAll(path, `\`, "/")
		if len(path) >= 2 && path[1] == ':' {
			path = "/" + path
		}
	}
	return &url.URL{Scheme: "file", Path: path}
}

// PathFromFileURL 解析本地文件 URL，并返回平台路径。
func PathFromFileURL(raw string) (string, bool) {
	path, ok := pathFromFileURL(raw, runtime.GOOS)
	if !ok {
		return "", false
	}
	return filepath.FromSlash(path), true
}

func pathFromFileURL(raw, goos string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "file") || (u.Host != "" && !strings.EqualFold(u.Host, "localhost")) || !strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	path := u.Path
	if goos == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return path, true
}
