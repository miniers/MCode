package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"mcode/internal/nmem"
	"mcode/internal/store"
)

// 全局范围的提议写入全局空间；改写提议走 PATCH 原记忆，记下原记忆 ID。
func TestCommitMemoryProposalRoutesByScopeAndTarget(t *testing.T) {
	var gotAddSpace, gotPatchPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotAddSpace, _ = body["space_id"].(string)
			_, _ = io.WriteString(w, `{"memory":{"id":"new-1","space_id":"global"}}`)
		case http.MethodPatch:
			gotPatchPath = r.URL.Path
			_, _ = io.WriteString(w, `{"id":"old-1","space_id":"mcode"}`)
		}
	}))
	defer srv.Close()
	cl := nmem.New(srv.URL, "nmem_test")

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	p, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{Title: "t", Content: "c", Scope: store.MemoryProposalScopeGlobal})
	if err != nil {
		t.Fatal(err)
	}
	saved, space, err := commitMemoryProposal(ctx, st, cl, proposalSpace(p, "mcode"), p)
	if err != nil || gotAddSpace != nmem.GlobalSpace || space != "global" || saved.RemoteMemoryID != "new-1" || saved.Status != store.MemoryProposalStatusAccepted {
		t.Fatalf("全局提议: space=%q add=%q saved=%+v err=%v", space, gotAddSpace, saved, err)
	}

	p, err = st.CreateMemoryProposal(ctx, store.MemoryProposal{Title: "t", Content: "c", TargetMemoryID: "old-1"})
	if err != nil {
		t.Fatal(err)
	}
	saved, space, err = commitMemoryProposal(ctx, st, cl, proposalSpace(p, "mcode"), p)
	if err != nil || gotPatchPath != "/memories/old-1" || space != "mcode" || saved.RemoteMemoryID != "old-1" {
		t.Fatalf("改写提议: patch=%q space=%q saved=%+v err=%v", gotPatchPath, space, saved, err)
	}
	reloaded, err := st.GetMemoryProposal(ctx, p.ID)
	if err != nil || reloaded.Status != store.MemoryProposalStatusAccepted || reloaded.TargetMemoryID != "old-1" || reloaded.Scope != store.MemoryProposalScopeProject {
		t.Fatalf("落库结果 = %+v, %v", reloaded, err)
	}
}
