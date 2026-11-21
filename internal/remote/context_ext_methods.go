package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mcode/internal/projctx"
	"mcode/internal/remote/wire"
)

// maxContextDocReadBytes 限制单次读取项目知识库文档的最大字节数（256 KiB）。
const maxContextDocReadBytes = 256 * 1024

func init() {
	RegisterMethod("context.listDocs", handleContextListDocs)
	RegisterMethod("context.readDoc", handleContextReadDoc)
}

// contextExtProjectPath 解析 projectID 对应的项目根目录。
// 空 projectID 返回 InvalidParams；未知项目或空路径返回 ErrCodeResourceNotFound。
func contextExtProjectPath(ctx context.Context, d *Dispatcher, projectID string) (string, error) {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" {
		return "", ExtInvalidParams("Invalid params: 'projectID' is required")
	}
	projectPath, err := memoryExtProjectPath(ctx, d, trimmed)
	if err != nil {
		var extErr *ExtError
		if errors.As(err, &extErr) && extErr.Code == wire.InvalidParams {
			return "", &ExtError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("project %s not found", trimmed),
			}
		}
		return "", err
	}
	return projectPath, nil
}

// handleContextListDocs 列出项目的 4 篇标准知识库文档（无论是否存在）及 docs/ 下的自定义文档。
func handleContextListDocs(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.RemoteContextListDocsParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	projectPath, err := contextExtProjectPath(ctx, d, projectID)
	if err != nil {
		return nil, err
	}

	svc, err := projctx.New(projectPath)
	if err != nil {
		return nil, ExtInvalidParams(err.Error())
	}
	if err := checkContextSymlinks(svc, true); err != nil {
		return nil, err
	}
	manifest, err := svc.Manifest()
	if err != nil {
		if errors.Is(err, projctx.ErrRedirected) || errors.Is(err, projctx.ErrInvalidName) {
			return nil, ExtInvalidParams("context directory symlink escape rejected: " + err.Error())
		}
		return nil, err
	}

	stdDocs := manifest.Standard()
	var customDocs []wire.RemoteContextDocSummary
	docs := make([]wire.RemoteContextDocSummary, 0, len(manifest.Docs))
	for _, doc := range stdDocs {
		docs = append(docs, contextDocSummaryFromInfo(doc))
	}
	for _, doc := range manifest.Docs {
		if doc.Standard {
			continue
		}
		customDocs = append(customDocs, contextDocSummaryFromInfo(doc))
	}
	sort.SliceStable(customDocs, func(i, j int) bool {
		return customDocs[i].Name < customDocs[j].Name
	})
	docs = append(docs, customDocs...)

	return wire.RemoteContextListDocsResult{
		ProjectID: projectID,
		Docs:      docs,
	}, nil
}

func contextDocSummaryFromInfo(doc projctx.DocInfo) wire.RemoteContextDocSummary {
	summary := wire.RemoteContextDocSummary{
		Name:     doc.RefID(),
		Title:    doc.Title,
		Standard: doc.Standard,
		Exists:   doc.Exists,
		Size:     int64(doc.Size),
	}
	if doc.Exists && !doc.ModTime.IsZero() {
		summary.UpdatedAt = doc.ModTime.UTC().Format(time.RFC3339)
	}
	return summary
}

// handleContextReadDoc 只读读取项目知识库中的单篇标准或自定义 Markdown 文档（上限 256 KiB）。
func handleContextReadDoc(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.RemoteContextReadDocParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	projectPath, err := contextExtProjectPath(ctx, d, projectID)
	if err != nil {
		return nil, err
	}

	rawName := strings.TrimSpace(p.Name)
	if rawName == "" {
		return nil, ExtInvalidParams("Invalid params: 'name' is required")
	}
	if strings.Contains(rawName, "..") || strings.Contains(rawName, "\\") {
		return nil, ExtInvalidParams("Invalid params: illegal document name")
	}
	if ext := path.Ext(path.Base(rawName)); ext != "" && !strings.EqualFold(ext, ".md") {
		return nil, ExtInvalidParams("Invalid params: only .md documents are supported")
	}
	ref, err := projctx.ParseRef(rawName)
	if err != nil {
		return nil, ExtInvalidParams("Invalid params: " + err.Error())
	}

	svc, err := projctx.New(projectPath)
	if err != nil {
		return nil, ExtInvalidParams(err.Error())
	}

	docName := projctx.DocsDir + "/" + ref.Name
	docTitle := projctx.TitleOf(ref.Name)
	if ref.Standard {
		docName = ref.Info.RefID()
		docTitle = ref.Info.Title
	}

	targetPath, exists, err := resolveContextReadPath(svc, ref)
	if err != nil {
		return nil, err
	}
	if !exists {
		if ref.Standard {
			return wire.RemoteContextReadDocResult{
				ProjectID: projectID,
				Name:      docName,
				Title:     docTitle,
				Standard:  true,
				Exists:    false,
				Content:   "",
				Size:      0,
				Truncated: false,
			}, nil
		}
		return nil, &ExtError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("context document %q not found", docName),
		}
	}

	f, err := os.Open(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if ref.Standard {
				return wire.RemoteContextReadDocResult{
					ProjectID: projectID,
					Name:      docName,
					Title:     docTitle,
					Standard:  true,
					Exists:    false,
				}, nil
			}
			return nil, &ExtError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("context document %q not found", docName),
			}
		}
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ExtInvalidParams("context document is not a regular file")
	}

	buf := make([]byte, maxContextDocReadBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	data := buf[:n]

	checkLen := len(data)
	if checkLen > 512 {
		checkLen = 512
	}
	if bytes.IndexByte(data[:checkLen], 0) >= 0 {
		return nil, ExtInvalidParams("binary content not supported")
	}

	truncated := false
	if len(data) > maxContextDocReadBytes {
		truncated = true
		data = data[:maxContextDocReadBytes]
	}

	content, _, ok := cleanUTF8Chunk(data)
	if !ok {
		return nil, ExtInvalidParams("binary content not supported")
	}

	updatedAt := ""
	if !fi.ModTime().IsZero() {
		updatedAt = fi.ModTime().UTC().Format(time.RFC3339)
	}

	return wire.RemoteContextReadDocResult{
		ProjectID: projectID,
		Name:      docName,
		Title:     docTitle,
		Standard:  ref.Standard,
		Exists:    true,
		Content:   content,
		Size:      fi.Size(),
		Truncated: truncated,
		UpdatedAt: updatedAt,
	}, nil
}

// checkContextSymlinks 检查 .mcode、.mcode/context 及可选 docs 子目录是否为符号链接重定向。
func checkContextSymlinks(svc *projctx.Service, checkDocsDir bool) error {
	ctxDir := svc.ContextDir()
	mcodeDir := filepath.Dir(ctxDir)
	for _, dir := range []string{mcodeDir, ctxDir} {
		if fi, err := os.Lstat(dir); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return ExtInvalidParams("context directory symlink escape rejected")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if checkDocsDir {
		if fi, err := os.Lstat(svc.DocsPath()); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return ExtInvalidParams("context docs directory symlink escape rejected")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// resolveContextReadPath 在校验符号链接未越界的前提下解析目标文档磁盘路径。
func resolveContextReadPath(svc *projctx.Service, ref projctx.DocRef) (string, bool, error) {
	if err := checkContextSymlinks(svc, !ref.Standard); err != nil {
		return "", false, err
	}

	ctxDir := svc.ContextDir()
	dir := svc.DocsPath()
	if ref.Standard {
		dir = ctxDir
	}

	realCtx, err := filepath.EvalSymlinks(ctxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	relDir, err := filepath.Rel(realCtx, realDir)
	if err != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) || filepath.IsAbs(relDir) {
		return "", false, ExtInvalidParams("context target directory outside context root")
	}

	target := filepath.Join(dir, ref.Name)
	fi, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		realTarget, err := filepath.EvalSymlinks(target)
		if err != nil {
			return "", false, ExtInvalidParams("context document symlink escape rejected")
		}
		relTarget, err := filepath.Rel(realDir, realTarget)
		if err != nil || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) || filepath.IsAbs(relTarget) {
			return "", false, ExtInvalidParams("context document symlink escape rejected")
		}
	}
	statInfo, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !statInfo.Mode().IsRegular() {
		return "", false, ExtInvalidParams("context document is not a regular file")
	}
	return target, true, nil
}
