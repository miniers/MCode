package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/remote/wire"
)

func callContextRPC(t *testing.T, d *Dispatcher, conn Conn, method string, params any) (json.RawMessage, *wire.RPCError) {
	t.Helper()
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s,"id":1}`, method, string(rawParams))
	rawResp := d.Handle(context.Background(), conn, []byte(req))
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *wire.RPCError  `json:"error"`
	}
	if err := json.Unmarshal(rawResp, &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v, raw=%s", err, string(rawResp))
	}
	return resp.Result, resp.Error
}

func TestContextExt_ListDocs_EmptyAndPopulated(t *testing.T) {
	_, d, workDir, projectID, _ := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	// 空目录：列出 4 篇标准文档且 exists=false，不创建 .mcode。
	raw, rpcErr := callContextRPC(t, d, conn, "context.listDocs", wire.RemoteContextListDocsParams{ProjectID: projectID})
	if rpcErr != nil {
		t.Fatalf("context.listDocs unexpected error: %+v", rpcErr)
	}
	var res wire.RemoteContextListDocsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.ProjectID != projectID {
		t.Fatalf("projectID mismatch: got %q want %q", res.ProjectID, projectID)
	}
	if len(res.Docs) != 4 {
		t.Fatalf("expected 4 standard docs, got %d: %+v", len(res.Docs), res.Docs)
	}
	wantStd := []string{"instructions", "overview", "decisions", "notes"}
	for i, name := range wantStd {
		doc := res.Docs[i]
		if doc.Name != name || !doc.Standard || doc.Exists || doc.Size != 0 || doc.UpdatedAt != "" {
			t.Fatalf("unexpected empty standard doc[%d]: %+v", i, doc)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, ".mcode")); !os.IsNotExist(err) {
		t.Fatalf("context.listDocs must not create .mcode directory, stat err=%v", err)
	}

	// 写入标准文档与自定义文档，验证存在状态与自定义文档按 name 升序排序。
	ctxDir := filepath.Join(workDir, ".mcode", "context")
	docsDir := filepath.Join(ctxDir, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "instructions.md"), []byte("# Rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "zebra.md"), []byte("# Zebra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "alpha.md"), []byte("# Alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, rpcErr = callContextRPC(t, d, conn, "context.listDocs", wire.RemoteContextListDocsParams{ProjectID: projectID})
	if rpcErr != nil {
		t.Fatalf("context.listDocs populated error: %+v", rpcErr)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Docs) != 6 {
		t.Fatalf("expected 6 docs, got %d: %+v", len(res.Docs), res.Docs)
	}
	if !res.Docs[0].Exists || res.Docs[0].Size != 8 || res.Docs[0].UpdatedAt == "" {
		t.Fatalf("instructions should exist with size 8 and updatedAt, got %+v", res.Docs[0])
	}
	if res.Docs[4].Name != "docs/alpha.md" || res.Docs[4].Standard || !res.Docs[4].Exists {
		t.Fatalf("expected docs/alpha.md at index 4, got %+v", res.Docs[4])
	}
	if res.Docs[5].Name != "docs/zebra.md" || res.Docs[5].Standard || !res.Docs[5].Exists {
		t.Fatalf("expected docs/zebra.md at index 5, got %+v", res.Docs[5])
	}
}

func TestContextExt_ReadDoc_StandardAndCustom(t *testing.T) {
	_, d, workDir, projectID, _ := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	ctxDir := filepath.Join(workDir, ".mcode", "context")
	docsDir := filepath.Join(ctxDir, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "instructions.md"), []byte("# Rules\nKeep simple."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "api-guide.md"), []byte("# API Guide\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 读取已存在的标准文档
	raw, rpcErr := callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: projectID,
		Name:      "instructions",
	})
	if rpcErr != nil {
		t.Fatalf("readDoc instructions failed: %+v", rpcErr)
	}
	var res wire.RemoteContextReadDocResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Exists || !res.Standard || res.Truncated || res.Content != "# Rules\nKeep simple." || res.Name != "instructions" {
		t.Fatalf("unexpected standard readDoc result: %+v", res)
	}

	// 读取自定义文档
	raw, rpcErr = callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: projectID,
		Name:      "docs/api-guide.md",
	})
	if rpcErr != nil {
		t.Fatalf("readDoc custom failed: %+v", rpcErr)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Exists || res.Standard || res.Name != "docs/api-guide.md" || res.Content != "# API Guide\n" {
		t.Fatalf("unexpected custom readDoc result: %+v", res)
	}

	// 缺失的标准文档返回 exists=false（不报错）
	raw, rpcErr = callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: projectID,
		Name:      "overview",
	})
	if rpcErr != nil {
		t.Fatalf("missing standard doc should not error, got %+v", rpcErr)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Exists || !res.Standard || res.Content != "" || res.Size != 0 || res.Truncated {
		t.Fatalf("expected missing standard doc with exists=false, got %+v", res)
	}

	// 缺失的自定义文档返回 ErrCodeResourceNotFound
	_, rpcErr = callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: projectID,
		Name:      "docs/missing.md",
	})
	if rpcErr == nil || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ErrCodeResourceNotFound for missing custom doc, got %+v", rpcErr)
	}
}

func TestContextExt_ReadDoc_SecurityAndLimits(t *testing.T) {
	_, d, workDir, projectID, _ := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	ctxDir := filepath.Join(workDir, ".mcode", "context")
	docsDir := filepath.Join(ctxDir, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. 路径穿越与非法名称拒绝
	for _, bad := range []string{"", "  ", "../escape.md", "docs/../x.md", `docs\x.md`, ".hidden.md", "secret.txt", "docs/foo.json"} {
		_, rpcErr := callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
			ProjectID: projectID,
			Name:      bad,
		})
		if rpcErr == nil || rpcErr.Code != wire.InvalidParams {
			t.Fatalf("readDoc(%q) expected InvalidParams, got %+v", bad, rpcErr)
		}
	}

	// 2. 符号链接逃逸拒绝
	outsideFile := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkDoc := filepath.Join(docsDir, "escape-link.md")
	if err := os.Symlink(outsideFile, symlinkDoc); err != nil {
		t.Fatalf("symlink setup failed: %v", err)
	}
	_, rpcErr := callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: projectID,
		Name:      "docs/escape-link.md",
	})
	if rpcErr == nil || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for escaping symlink, got %+v", rpcErr)
	}

	// 3. 二进制 0x00 拒绝
	binDoc := filepath.Join(docsDir, "binary.md")
	if err := os.WriteFile(binDoc, []byte("hello\x00world"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rpcErr = callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: projectID,
		Name:      "docs/binary.md",
	})
	if rpcErr == nil || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for binary content, got %+v", rpcErr)
	}

	// 4. 超过 256 KiB 时按合法 UTF-8 边界截断并标记 truncated=true
	// 使用 1 字节前缀 + 3 字节中文字符，刻意让 256 KiB 边界落在多字节字符中间。
	unit := "中" // 3 bytes
	repeatCount := (maxContextDocReadBytes / len(unit)) + 100
	largeContent := "a" + strings.Repeat(unit, repeatCount)
	largeDoc := filepath.Join(docsDir, "large.md")
	if err := os.WriteFile(largeDoc, []byte(largeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, rpcErr := callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: projectID,
		Name:      "docs/large.md",
	})
	if rpcErr != nil {
		t.Fatalf("readDoc large failed: %+v", rpcErr)
	}
	var res wire.RemoteContextReadDocResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatalf("expected truncated=true for >256KiB doc")
	}
	if len(res.Content) > maxContextDocReadBytes {
		t.Fatalf("content length %d exceeds maxContextDocReadBytes %d", len(res.Content), maxContextDocReadBytes)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatalf("truncated content is not valid UTF-8")
	}
	if res.Size != int64(len(largeContent)) {
		t.Fatalf("expected full file size %d, got %d", len(largeContent), res.Size)
	}
}

func TestContextExt_UnknownProjectAndEmptyProjectID(t *testing.T) {
	_, d, _, _, _ := setupWorkspaceTestEnv(t)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	_, rpcErr := callContextRPC(t, d, conn, "context.listDocs", wire.RemoteContextListDocsParams{ProjectID: "   "})
	if rpcErr == nil || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for empty projectID, got %+v", rpcErr)
	}

	_, rpcErr = callContextRPC(t, d, conn, "context.listDocs", wire.RemoteContextListDocsParams{ProjectID: "unknown-proj"})
	if rpcErr == nil || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ErrCodeResourceNotFound for unknown projectID in listDocs, got %+v", rpcErr)
	}

	_, rpcErr = callContextRPC(t, d, conn, "context.readDoc", wire.RemoteContextReadDocParams{
		ProjectID: "unknown-proj",
		Name:      "instructions",
	})
	if rpcErr == nil || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ErrCodeResourceNotFound for unknown projectID in readDoc, got %+v", rpcErr)
	}
}

func TestContextExt_SchemaAndCapabilities(t *testing.T) {
	data, err := os.ReadFile("../../docs/remote-control.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	methods := []string{"context.listDocs", "context.readDoc"}
	for _, m := range methods {
		foundReq := false
		for name, raw := range doc.Defs {
			var d struct {
				Properties struct {
					Method struct {
						Const string `json:"const"`
					} `json:"method"`
				} `json:"properties"`
			}
			if json.Unmarshal(raw, &d) == nil && d.Properties.Method.Const == m {
				foundReq = true
				if strings.Count(string(data), `"$ref": "#/$defs/`+name+`"`) != 2 {
					t.Fatalf("%s request $ref count != 2", m)
				}
				respName := strings.TrimSuffix(name, "Request") + "Response"
				if strings.Count(string(data), `"$ref": "#/$defs/`+respName+`"`) != 2 {
					t.Fatalf("%s response (%s) $ref count != 2", m, respName)
				}
				break
			}
		}
		if !foundReq {
			t.Fatalf("schema missing %s", m)
		}

		registered := false
		for _, impl := range ImplementedMethods() {
			if impl == m {
				registered = true
				break
			}
		}
		if !registered {
			t.Fatalf("ImplementedMethods missing %s", m)
		}
	}

	hasFeature := false
	for _, f := range DefaultFeatures() {
		if f == "context.readonly" {
			hasFeature = true
			break
		}
	}
	if !hasFeature {
		t.Fatalf("DefaultFeatures missing context.readonly: %v", DefaultFeatures())
	}
}
