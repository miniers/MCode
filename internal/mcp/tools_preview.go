package mcp

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const previewOpenToolName = "preview_open"

func (r *Registry) registerPreviewTools(s *Server) {
	r.Register(ToolDefinition{
		Name:        previewOpenToolName,
		Description: "在 mcode 桌面端右侧浏览器打开工作区内的 HTML 文件或本机开发服务地址，供用户查看设计稿或页面效果。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "工作区内的 HTML 文件路径（相对或绝对路径），或本机开发服务 URL（仅限 localhost、127.0.0.1、::1）。",
				},
			},
			"required": []string{"target"},
		},
		IsWrite: false,
		Handler: handlePreviewOpen,
	})
}

func handlePreviewOpen(tc ToolContext, args map[string]any) ToolResult {
	var threadID string
	if tc.Context != nil {
		threadID = scopeThreadID(tc.Context)
	}
	if threadID == "" {
		return toolError(CodeForbidden, "preview_open needs a session-scoped endpoint to identify the conversation", false)
	}
	rawTarget, _ := args["target"].(string)
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'target'.", false)
	}
	if tc.Server == nil || tc.Server.opts.OpenPreview == nil {
		return toolError(CodeInternal, "桌面预览不可用", false)
	}

	resolved, errRes := resolvePreviewTarget(tc.WorkDir, target)
	if errRes != nil {
		return *errRes
	}
	if err := tc.Server.opts.OpenPreview(threadID, resolved); err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess("已在右侧预览打开")
}

// resolvePreviewTarget 校验预览目标：http(s) URL 仅放行本机回环地址，
// 其余视为工作区内 HTML 文件路径并返回相对工作区的 slash 路径。
func resolvePreviewTarget(workDir, target string) (string, *ToolResult) {
	lower := strings.ToLower(target)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(target)
		if err != nil || u.User != nil || u.Host == "" {
			res := toolError(CodeInvalidArguments, "无效的预览 URL", false)
			return "", &res
		}
		switch strings.ToLower(u.Hostname()) {
		case "localhost", "127.0.0.1", "::1":
			return target, nil
		default:
			res := toolError(CodeInvalidArguments, "仅允许打开本机开发服务地址（localhost、127.0.0.1、::1）", false)
			return "", &res
		}
	}

	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		res := toolError(CodeInvalidArguments, "缺少工作区路径，无法打开本地文件", false)
		return "", &res
	}
	base := filepath.Clean(workDir)
	var full string
	if filepath.IsAbs(target) {
		full = filepath.Clean(target)
	} else {
		full = filepath.Clean(filepath.Join(base, target))
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		res := toolError(CodeInvalidArguments, "文件路径必须位于当前工作区内", false)
		return "", &res
	}
	switch strings.ToLower(filepath.Ext(full)) {
	case ".html", ".htm", ".xhtml":
	default:
		res := toolError(CodeInvalidArguments, "仅支持预览 .html、.htm 或 .xhtml 文件", false)
		return "", &res
	}
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			res := toolError(CodeNotFound, "文件不存在: "+filepath.ToSlash(rel), false)
			return "", &res
		}
		res := toolError(CodeInternal, "无法读取文件信息: "+err.Error(), false)
		return "", &res
	}
	if !info.Mode().IsRegular() {
		res := toolError(CodeInvalidArguments, "目标不是普通文件: "+filepath.ToSlash(rel), false)
		return "", &res
	}
	if realBase, err := filepath.EvalSymlinks(base); err == nil {
		if realFull, err := filepath.EvalSymlinks(full); err == nil {
			if realRel, err := filepath.Rel(realBase, realFull); err != nil || realRel == "." || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) || filepath.IsAbs(realRel) {
				res := toolError(CodeInvalidArguments, "文件路径不能指向当前工作区之外", false)
				return "", &res
			}
		}
	}
	return filepath.ToSlash(rel), nil
}
