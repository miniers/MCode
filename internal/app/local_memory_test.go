package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mcode/internal/config"
	"mcode/internal/localmem"
	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/store"
)

func TestResolveMemoryBackend(t *testing.T) {
	client := &nmem.Client{}
	readErr := errors.New("nmem: 解析 config.json 失败")
	for _, tc := range []struct {
		mode      string
		client    *nmem.Client
		clientErr error
		want      string
	}{
		// auto：未配置（ErrNotConfigured 或没有错误但没有客户端）时退回本地文档。
		{config.MemoryBackendAuto, nil, nil, config.MemoryBackendLocal},
		{config.MemoryBackendAuto, nil, nmem.ErrNotConfigured, config.MemoryBackendLocal},
		// auto：已配置但读取失败不降级，视为 nmem。
		{config.MemoryBackendAuto, nil, readErr, config.MemoryBackendNmem},
		{config.MemoryBackendAuto, client, nil, config.MemoryBackendNmem},
		{config.MemoryBackendLocal, client, nil, config.MemoryBackendLocal},
		{config.MemoryBackendLocal, nil, readErr, config.MemoryBackendLocal},
		{config.MemoryBackendNmem, nil, nil, config.MemoryBackendNmem},
	} {
		if got := resolveMemoryBackend(tc.mode, tc.client, tc.clientErr); got != tc.want {
			t.Fatalf("mode %q, clientErr %v: got %q, want %q", tc.mode, tc.clientErr, got, tc.want)
		}
	}
}

func TestLocalMemoryWriteError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{localmem.ErrNotFound, "目标记忆不存在"},
		{projctx.ErrConflict, "文档被同时修改，请重试"},
		{errors.New("localmem: invalid heading"), "写入本地文档失败：localmem: invalid heading"},
	} {
		if got := localMemoryWriteError(tc.err); got != tc.want {
			t.Fatalf("err %v: got %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestLocalMemoryCommit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	p, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{ProjectPath: t.TempDir(), Title: "标题", Content: "内容"})
	if err != nil {
		t.Fatal(err)
	}
	tools := &localMemoryTools{docs: localmem.New(t.TempDir()), store: st}
	saved, _, err := tools.commit(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != store.MemoryProposalStatusAccepted || saved.RemoteMemoryID == "" {
		t.Fatalf("unexpected proposal: %+v", saved)
	}
	stored, err := st.GetMemoryProposal(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != saved.Status || stored.RemoteMemoryID != saved.RemoteMemoryID {
		t.Fatalf("unexpected stored proposal: %+v", stored)
	}
	mem, err := tools.get(ctx, p.ProjectPath, saved.RemoteMemoryID)
	if err != nil || mem.Content != p.Content {
		t.Fatalf("memory: %+v, error: %v", mem, err)
	}
}
