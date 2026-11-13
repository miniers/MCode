// 此文件包含 Client 的文件与项目上下文文档方法。
package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"mcode/internal/projctx"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// SearchFiles 在指定会话的工作区内模糊搜索文件和目录。
func (c *Client) SearchFiles(ctx context.Context, threadID, query string) ([]mcodeui.FileMatch, error) {
	result, err := c.SearchFileEntries(ctx, threadID, query)
	return result.Entries, err
}

// SearchFileEntries 在工作区内搜索，并返回后端的截断状态。
func (c *Client) SearchFileEntries(ctx context.Context, threadID, query string) (mcodeui.FileSearchResult, error) {
	res, err := c.backend.FilesSearch(ctx, wire.RemoteFilesSearchParams{
		ThreadID: threadID,
		Query:    query,
	})
	if err != nil {
		return mcodeui.FileSearchResult{}, err
	}
	matches := make([]mcodeui.FileMatch, len(res.Entries))
	for i, entry := range res.Entries {
		matches[i] = mcodeui.FileMatch{
			Name:  entry.Name,
			Path:  entry.Path,
			IsDir: entry.Kind == wire.RemoteFileEntryKindDirectory,
		}
	}
	return mcodeui.FileSearchResult{Entries: matches, Truncated: res.Truncated}, nil
}

// FilesList 列出指定会话工作区内某个目录的单层条目。
func (c *Client) FilesList(ctx context.Context, threadID, subpath string) (mcodeui.FileListing, error) {
	res, err := c.backend.FilesList(ctx, wire.RemoteFilesListParams{
		ThreadID: threadID,
		Subpath:  subpath,
	})
	if err != nil {
		return mcodeui.FileListing{}, err
	}
	entries := make([]mcodeui.FileEntry, len(res.Entries))
	for i, entry := range res.Entries {
		entries[i] = mcodeui.FileEntry{
			Name:      entry.Name,
			Path:      entry.Path,
			IsDir:     entry.Kind == wire.RemoteFileEntryKindDirectory,
			IsSymlink: entry.Kind == wire.RemoteFileEntryKindSymlink,
			Size:      entry.Size,
			Ignored:   entry.Ignored,
			Protected: entry.Protected,
		}
	}
	return mcodeui.FileListing{
		Path:      res.Path,
		Entries:   entries,
		Truncated: res.Truncated,
	}, nil
}

// FilesRead 读取指定会话工作区内的文件分块。
func (c *Client) FilesRead(ctx context.Context, threadID, path string, offset, length int64) (mcodeui.FileContent, error) {
	params := wire.RemoteFilesReadParams{
		ThreadID: threadID,
		Path:     path,
	}
	if offset != 0 {
		o := offset
		params.Offset = &o
	}
	if length > 0 {
		l := int(length)
		params.Length = &l
	}
	res, err := c.backend.FilesRead(ctx, params)
	if err != nil {
		return mcodeui.FileContent{}, err
	}
	content := mcodeui.FileContent{
		Path:      res.Path,
		Kind:      res.Kind,
		TotalSize: res.TotalBytes,
		HasMore:   res.Truncated,
	}
	if res.Text != nil {
		content.Text = *res.Text
	}
	if res.MimeType != nil {
		content.MIME = *res.MimeType
	}
	if res.NextOffset != nil {
		content.NextOffset = *res.NextOffset
	}
	if res.Base64 != nil {
		if data, decErr := base64.StdEncoding.DecodeString(*res.Base64); decErr == nil {
			content.Data = data
		}
	}
	return content, nil
}

// FilesCreate 在指定会话工作区内新建空文件或目录，返回相对工作区的路径。
func (c *Client) FilesCreate(ctx context.Context, threadID, path string, dir bool) (string, error) {
	res, err := c.backend.FilesCreate(ctx, wire.FilesCreateParams{
		ThreadID: threadID,
		Path:     path,
		Dir:      dir,
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// FilesRename 重命名或移动工作区内的条目，返回新的相对路径。
func (c *Client) FilesRename(ctx context.Context, threadID, path, newPath string) (string, error) {
	res, err := c.backend.FilesRename(ctx, wire.FilesRenameParams{
		ThreadID: threadID,
		Path:     path,
		NewPath:  newPath,
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// FilesTrash 把工作区内的条目移到系统废纸篓。
func (c *Client) FilesTrash(ctx context.Context, threadID, path string) error {
	_, err := c.backend.FilesTrash(ctx, wire.FilesTrashParams{
		ThreadID: threadID,
		Path:     path,
	})
	return err
}

// FilesDuplicate 在同一目录下复制普通文件，返回副本的相对路径。
func (c *Client) FilesDuplicate(ctx context.Context, threadID, path string) (string, error) {
	res, err := c.backend.FilesDuplicate(ctx, wire.FilesDuplicateParams{
		ThreadID: threadID,
		Path:     path,
	})
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// ContextDocs 获取指定项目文档的文档列表。
func (c *Client) ContextDocs(projectPath string) ([]mcodeui.ContextDoc, error) {
	if strings.TrimSpace(projectPath) == "" {
		return nil, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return nil, err
	}
	manifest, err := svc.Manifest()
	if err != nil {
		return nil, err
	}
	res := make([]mcodeui.ContextDoc, 0, len(manifest.Docs))
	for _, d := range manifest.Docs {
		if !d.Exists {
			continue
		}
		name := d.File
		if !d.Standard {
			name = projctx.DocsDir + "/" + d.File
		}
		doc, err := svc.ReadRef(name)
		if errors.Is(err, projctx.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		res = append(res, contextDocToUI(name, doc))
	}
	return res, nil
}

// ReadContextDoc 读取指定项目文档中的单篇文档。
func (c *Client) ReadContextDoc(projectPath, name string) (mcodeui.ContextDoc, error) {
	if strings.TrimSpace(projectPath) == "" {
		return mcodeui.ContextDoc{}, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	docName, err := contextDocName(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	doc, err := svc.ReadRef(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	return contextDocToUI(docName, doc), nil
}

// WriteContextDoc 写入或更新项目文档中的单篇文档。
func (c *Client) WriteContextDoc(projectPath, name, content, expectedHash string) (mcodeui.ContextDoc, error) {
	// 不同窗口共享 Client，但每次调用创建的 Service 不共享互斥锁。
	c.contextWriteMu.Lock()
	defer c.contextWriteMu.Unlock()
	if strings.TrimSpace(projectPath) == "" {
		return mcodeui.ContextDoc{}, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	docName, err := contextDocName(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	doc, err := svc.WriteRef(name, content, expectedHash)
	if err != nil {
		if errors.Is(err, projctx.ErrConflict) {
			return mcodeui.ContextDoc{}, fmt.Errorf("%w: %v", mcodeui.ErrContextConflict, err)
		}
		return mcodeui.ContextDoc{}, err
	}
	return contextDocToUI(docName, doc), nil
}

// WatchContextDocs 监听项目文档文档变更。
func (c *Client) WatchContextDocs(projectPath string, onChange func()) (stop func(), err error) {
	if strings.TrimSpace(projectPath) == "" {
		return nil, errors.New("projectPath 不能为空")
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return nil, err
	}
	watcher, err := svc.Watch(onChange)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = watcher.Close()
		})
	}
	return stop, nil
}

// CreateContextDoc 创建空白项目文档文档，不覆盖已有文件。
func (c *Client) CreateContextDoc(projectPath, name string) (mcodeui.ContextDoc, error) {
	svc, err := projctx.New(projectPath)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	docName, err := contextDocName(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	doc, err := svc.CreateDoc(name)
	if err != nil {
		return mcodeui.ContextDoc{}, err
	}
	return contextDocToUI(docName, doc), nil
}
