package mcp

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"mcode/internal/projctx"
)

func (r *Registry) registerContextTools(s *Server) {
	r.Register(ToolDefinition{
		Name:        "context_list_documents",
		Description: "Show what is in this project's knowledge base: the four standard documents plus any custom documents under docs/. Open one with context_read_document; record new knowledge with memory_propose (only the local memory backend saves accepted content into these documents).",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
		IsWrite:    false,
		Capability: CapContext,
		Handler:    handleContextListDocuments,
	})

	r.Register(ToolDefinition{
		Name:        "context_read_document",
		Description: "Return the Markdown text of a single knowledge base document. Use a name listed by context_list_documents: instructions, overview, decisions or notes for the standard ones, or a custom document's file name (the docs/ prefix is optional).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Which document to read: a standard name, or a custom one such as docs/<file>.",
				},
			},
			"required": []string{"name"},
		},
		IsWrite:    false,
		Capability: CapContext,
		Handler:    handleContextReadDocument,
	})

}

func handleContextListDocuments(tc ToolContext, args map[string]any) ToolResult {
	ctxSvc, err := tc.Server.getContextService(tc.Project.Path)
	if err != nil {
		return toolError(CodeInternal, "Context service unavailable: "+err.Error(), false)
	}
	manifest, err := ctxSvc.Manifest()
	if err != nil {
		return toolError(CodeInternal, "Failed to list documents: "+err.Error(), false)
	}
	// 四篇标准文档始终列出，另外列出 docs/ 下的自定义文档。
	var b strings.Builder
	b.WriteString("Project knowledge base documents:\n\n")
	b.WriteString("Standard documents:\n")
	for _, doc := range manifest.Standard() {
		fmt.Fprintf(&b, "- name: %s | title: %s | subtitle: %s | path: %s\n",
			doc.Name, doc.Title, doc.Subtitle, doc.RelativePath())
	}
	b.WriteString("\n")
	customs := manifest.Custom()
	if len(customs) == 0 {
		b.WriteString("Custom documents: none.\n")
	} else {
		b.WriteString("Custom documents:\n")
		for _, doc := range customs {
			fmt.Fprintf(&b, "- name: %s | title: %s | path: %s\n",
				doc.RefID(), doc.Title, doc.RelativePath())
		}
	}
	b.WriteString("\nOpen a document with context_read_document; record new knowledge with memory_propose.")
	return toolSuccess(b.String())
}

func handleContextReadDocument(tc ToolContext, args map[string]any) ToolResult {
	raw, _ := args["name"].(string)
	if strings.TrimSpace(raw) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'name'.", false)
	}
	ref, err := projctx.ParseRef(raw)
	if err != nil {
		return toolError(CodeInvalidArguments, fmt.Sprintf("Invalid document name '%s'.", raw), false)
	}
	ctxSvc, err := tc.Server.getContextService(tc.Project.Path)
	if err != nil {
		return toolError(CodeInternal, "Context service unavailable: "+err.Error(), false)
	}
	doc, err := ctxSvc.ReadRefPath(ref)
	if err != nil {
		if errors.Is(err, projctx.ErrNotFound) {
			hint := "context_list_documents shows the names you can use."
			if ref.Standard {
				hint = "This standard document has not been created yet. The user can create it in the Project Docs window, or you can record new knowledge with memory_propose."
			}
			return toolError(
				CodeNotFound,
				fmt.Sprintf("There is no document named '%s'. Existing documents: %s.",
					ref.Name, availableDocumentNames(ctxSvc)),
				false,
				hint,
			)
		}
		if errors.Is(err, projctx.ErrInvalidName) {
			return toolError(CodeInvalidArguments, fmt.Sprintf("Invalid document name '%s'.", raw), false)
		}
		return toolError(CodeInternal, "Failed to read document: "+err.Error(), false)
	}
	content := doc.Content
	if utf8.RuneCountInString(content) > maxDocumentChars {
		content = string([]rune(content)[:maxDocumentChars]) + "\n\n(truncated)"
	}
	return toolSuccess(map[string]any{
		"name":    ref.Name,
		"content": content,
		"hash":    doc.Hash,
	})
}

// maxDocumentChars 限制单次读取返回的文档正文字符数，避免超出模型上下文。
const maxDocumentChars = 20000

// availableDocumentNames 汇总可读取的文档名（标准文档 + 已存在的自定义文档），用于错误提示。
func availableDocumentNames(ctxSvc *projctx.Service) string {
	manifest, err := ctxSvc.Manifest()
	if err != nil {
		return "(none)"
	}
	var names []string
	for _, doc := range manifest.Standard() {
		names = append(names, doc.Name)
	}
	for _, doc := range manifest.Custom() {
		names = append(names, doc.RefID())
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}
