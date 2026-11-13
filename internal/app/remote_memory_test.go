package app

import (
	"context"
	"strings"
	"testing"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 本文件覆盖远程记忆面板适配层（internal/app/remote_memory.go）。
// 不触网：创建与编辑都只走本地收件箱，接受只在「非待审」等本地就能判定的分支上验证，
// 避免测试真的去连 Nowledge Mem。

func memoryExtTestClient(t *testing.T) *Client {
	t.Helper()
	client := &Client{dataDir: t.TempDir()}
	// memoryStoreConn 惰性打开 SQLite，先关闭连接再让 TempDir 在 Windows 上清理。
	t.Cleanup(func() {
		client.memoryMu.Lock()
		defer client.memoryMu.Unlock()
		if client.memoryStore != nil {
			_ = client.memoryStore.Close()
			client.memoryStore = nil
		}
	})
	return client
}

func memoryExtTestPtrFloat(v float64) *float64 { return &v }
func memoryExtTestPtrString(v string) *string  { return &v }

func TestRemoteMemoryExt_CreateMemory(t *testing.T) {
	client := memoryExtTestClient(t)
	ext := remoteMemoryExt{client: client}
	ctx := context.Background()

	projectPath := t.TempDir()
	proposal, err := ext.CreateMemory(ctx, wire.MemoryExtCreateInput{
		ProjectPath: projectPath,
		Title:       "用 SQLite 存提议",
		Content:     "提议先落本地收件箱，确认后才写入。",
		Type:        "insight",
		Importance:  memoryExtTestPtrFloat(0.8),
		Scope:       "global",
	})
	if err != nil {
		t.Fatalf("新建记忆失败：%v", err)
	}
	if proposal.ID == "" {
		t.Fatal("新建记忆应返回提议 ID")
	}
	// 与桌面端一致：新建出来的是待审提议，来源记为 user。
	if proposal.Status != store.MemoryProposalStatusPending || proposal.Source != store.MemoryProposalSourceUser {
		t.Fatalf("新建记忆应为 user 来源的待审提议，得到 %+v", proposal)
	}
	if proposal.Title != "用 SQLite 存提议" || proposal.UnitType != "insight" || proposal.Importance != 0.8 {
		t.Fatalf("新建记忆应带上类型与重要度，得到 %+v", proposal)
	}
	if proposal.ProjectPath != projectPath {
		t.Fatalf("项目路径不对：%q", proposal.ProjectPath)
	}

	// 收件箱里应能读回同一条提议（含补写的类型与重要度）。
	items, err := client.MemoryProposals(projectPath, "")
	if err != nil {
		t.Fatalf("读取收件箱失败：%v", err)
	}
	if len(items) != 1 || items[0].ID != proposal.ID {
		t.Fatalf("收件箱里应有刚建的提议，得到 %+v", items)
	}
	if items[0].UnitType != "insight" || items[0].Importance != 0.8 {
		t.Fatalf("补写的类型/重要度没落库：%+v", items[0])
	}
}

func TestRemoteMemoryExt_CreateMemory_Validation(t *testing.T) {
	ext := remoteMemoryExt{client: memoryExtTestClient(t)}
	ctx := context.Background()

	// 项目路径与标题必填，与 Client.CreateMemoryProposal 的校验一致。
	if _, err := ext.CreateMemory(ctx, wire.MemoryExtCreateInput{Title: "T"}); err == nil || !strings.Contains(err.Error(), "projectPath") {
		t.Fatalf("缺项目路径应报错，得到 %v", err)
	}
	if _, err := ext.CreateMemory(ctx, wire.MemoryExtCreateInput{ProjectPath: t.TempDir(), Title: "  "}); err == nil || !strings.Contains(err.Error(), "记忆标题不能为空") {
		t.Fatalf("缺标题应报错，得到 %v", err)
	}
}

func TestRemoteMemoryExt_AcceptProposal_WithEdit(t *testing.T) {
	client := memoryExtTestClient(t)
	ext := remoteMemoryExt{client: client}
	ctx := context.Background()

	created, err := client.CreateMemoryProposal(t.TempDir(), "", "原标题", "原正文")
	if err != nil {
		t.Fatalf("准备提议失败：%v", err)
	}
	// 驳回后再接受：接受步骤在碰后端之前就会因状态不对报错，测试不触网。
	if err := client.RejectMemoryProposal(created.ID); err != nil {
		t.Fatalf("驳回提议失败：%v", err)
	}

	edit := &wire.MemoryExtProposalEdit{
		Title:      memoryExtTestPtrString("改过的标题"),
		Content:    memoryExtTestPtrString("改过的正文"),
		Type:       memoryExtTestPtrString("decision"),
		Importance: memoryExtTestPtrFloat(0.9),
	}
	_, err = ext.AcceptProposal(ctx, created.ID, edit)
	if err == nil || !strings.Contains(err.Error(), "只有待审核的记忆提议才能写入") {
		t.Fatalf("非待审提议应被接受步骤拒绝，得到 %v", err)
	}

	// 编辑字段必须在接受之前写入收件箱。
	items, err := client.MemoryProposals("", "")
	if err != nil {
		t.Fatalf("读取收件箱失败：%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("收件箱条数不对：%+v", items)
	}
	got := items[0]
	if got.Title != "改过的标题" || got.Content != "改过的正文" || got.UnitType != "decision" || got.Importance != 0.9 {
		t.Fatalf("编辑字段没写进提议：%+v", got)
	}
}

func TestRemoteMemoryExt_AcceptProposal_NoEditAndUnknownID(t *testing.T) {
	client := memoryExtTestClient(t)
	ext := remoteMemoryExt{client: client}
	ctx := context.Background()

	created, err := client.CreateMemoryProposal(t.TempDir(), "", "原标题", "原正文")
	if err != nil {
		t.Fatalf("准备提议失败：%v", err)
	}
	if err := client.RejectMemoryProposal(created.ID); err != nil {
		t.Fatalf("驳回提议失败：%v", err)
	}

	// 不带编辑字段：直接接受，同样被状态检查拦下。
	if _, err := ext.AcceptProposal(ctx, created.ID, nil); err == nil || !strings.Contains(err.Error(), "只有待审核的记忆提议才能写入") {
		t.Fatalf("非待审提议应被拒绝，得到 %v", err)
	}

	// 未知提议 ID：编辑步骤先报找不到。
	edit := &wire.MemoryExtProposalEdit{Title: memoryExtTestPtrString("改过的标题")}
	if _, err := ext.AcceptProposal(ctx, "no-such-proposal", edit); err == nil || !strings.Contains(err.Error(), "记忆提议不存在或已被删除") {
		t.Fatalf("未知提议应报找不到，得到 %v", err)
	}
}

func TestRemoteMemoryExt_SearchMemories_Validation(t *testing.T) {
	client := memoryExtTestClient(t)
	ext := remoteMemoryExt{client: client}
	ctx := context.Background()

	// 空查询词在碰后端之前就报错，测试不触网。
	if _, err := ext.SearchMemories(ctx, t.TempDir(), "   ", 20); err == nil || !strings.Contains(err.Error(), "搜索内容不能为空") {
		t.Fatalf("空查询词应报错，得到 %v", err)
	}
}

func TestRemoteMemoryExt_NoClient(t *testing.T) {
	ext := remoteMemoryExt{}
	ctx := context.Background()

	if _, err := ext.CreateMemory(ctx, wire.MemoryExtCreateInput{ProjectPath: "/p", Title: "T"}); err == nil || !strings.Contains(err.Error(), "记忆服务不可用") {
		t.Fatalf("没有 Client 时应报服务不可用，得到 %v", err)
	}
	if _, err := ext.AcceptProposal(ctx, "p-1", nil); err == nil || !strings.Contains(err.Error(), "记忆服务不可用") {
		t.Fatalf("没有 Client 时应报服务不可用，得到 %v", err)
	}
	if _, err := ext.SearchMemories(ctx, "/p", "q", 20); err == nil || !strings.Contains(err.Error(), "记忆服务不可用") {
		t.Fatalf("没有 Client 时应报服务不可用，得到 %v", err)
	}
	if err := ext.RejectProposal(ctx, "p-1"); err == nil || !strings.Contains(err.Error(), "记忆服务不可用") {
		t.Fatalf("没有 Client 时应报服务不可用，得到 %v", err)
	}
}
