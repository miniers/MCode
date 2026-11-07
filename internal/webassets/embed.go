package webassets

import (
	"embed"
	"encoding/json"
	"io/fs"
)

// 干净 clone 时 dist 目录仅包含被 git 跟踪的点文件（如 .gitkeep）；不带 all: 前缀时 go:embed 会跳过点文件报 contains no embeddable files 导致编译失败。
//
//go:embed all:dist
var distFS embed.FS

// FS 返回前端静态资源文件系统，根路径映射为 dist 目录。
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// BuildID 从嵌入的 remote-manifest.webmanifest 中读取 buildID，若不存在则返回空字符串。
func BuildID() string {
	data, err := distFS.ReadFile("dist/remote-manifest.webmanifest")
	if err != nil {
		return ""
	}
	var manifest struct {
		BuildID string `json:"buildID"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ""
	}
	return manifest.BuildID
}
