package mcodeui

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitRemotesFake struct {
	*FakeClient
	mu      sync.Mutex
	calls   []string
	entries []git.RemoteInfo
}

type blockedRemotesFake struct {
	*gitRemotesFake
	release chan struct{}
}

func (f *blockedRemotesFake) GitRemotes(ctx context.Context, _ string) ([]git.RemoteInfo, error) {
	select {
	case <-f.release:
		return []git.RemoteInfo{{Name: "new-remote"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestGitRemotesDialogReopenCached(t *testing.T) {
	f := &blockedRemotesFake{gitRemotesFake: &gitRemotesFake{FakeClient: NewFakeClient()}, release: make(chan struct{})}
	s := newChatState(f)
	defer s.close()
	defer close(f.release)
	open := true
	st := gitRemotesStateFor(&open)
	st.entries = []git.RemoteInfo{{Name: "cached-remote", FetchURL: "cached-url"}}
	tt := ui.NewTester(func(c *ui.Context) { renderGitRemotesDialog(c, s, "thread", &open) }, 800, 600)
	st.mu.Lock()
	st.editing, st.name, st.url, st.confirm, st.err = "cached-remote", "draft", "draft-url", "cached-remote", "error"
	st.adding, st.busy, st.completed = true, true, true
	st.mu.Unlock()
	open = false
	tt.Frame()
	st.mu.Lock()
	if st.active || st.loading || st.busy || st.completed || st.adding || st.editing != "" || st.name != "" || st.url != "" || st.confirm != "" || st.err != "" {
		st.mu.Unlock()
		t.Fatal("关闭时应清空临时状态")
	}
	st.mu.Unlock()
	open = true
	tt.Frame()
	st.mu.Lock()
	st.loadingSince = time.Now().Add(-time.Second)
	st.mu.Unlock()
	tt.Frame()
	if !tt.HasText("cached-remote") || !tt.HasText("cached-url") {
		t.Fatal("刷新完成前应显示缓存远端")
	}
	if _, ok := tt.Find("加载中"); ok {
		t.Fatal("缓存远端存在时不应显示加载行")
	}
	st.mu.Lock()
	st.entries = nil
	st.mu.Unlock()
	tt.Frame()
	if _, ok := tt.Find("加载中"); !ok {
		t.Fatal("没有缓存远端时应显示加载行")
	}
	open = false
	tt.Frame()
}

func (f *gitRemotesFake) GitRemotes(context.Context, string) ([]git.RemoteInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]git.RemoteInfo(nil), f.entries...), nil
}
func (f *gitRemotesFake) record(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, v)
}
func (f *gitRemotesFake) GitRemoteAdd(_ context.Context, _, name, url string) error {
	f.record("add:" + name + ":" + url)
	return nil
}
func (f *gitRemotesFake) GitRemoteRemove(_ context.Context, _, name string) error {
	f.record("remove:" + name)
	return nil
}
func (f *gitRemotesFake) GitRemoteRename(_ context.Context, _, old, name string) error {
	f.record("rename:" + old + ":" + name)
	return nil
}
func (f *gitRemotesFake) GitRemoteSetURL(_ context.Context, _, name, url string) error {
	f.record("url:" + name + ":" + url)
	return nil
}
func (f *gitRemotesFake) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestGitRemotesSaveChanges(t *testing.T) {
	old := git.RemoteInfo{Name: "origin", FetchURL: "old"}
	for _, tc := range []struct {
		name, url string
		want      []string
	}{
		{"origin", "new", []string{"url:origin:new"}},
		{"upstream", "old", []string{"rename:origin:upstream"}},
	} {
		f := &gitRemotesFake{FakeClient: NewFakeClient()}
		if err := gitRemoteSave(context.Background(), f, "thread", old, tc.name, tc.url); err != nil {
			t.Fatal(err)
		}
		if got := f.recorded(); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("调用 = %v，期望 %v", got, tc.want)
		}
	}
}

func TestGitRemotesDeleteConfirmation(t *testing.T) {
	f := &gitRemotesFake{FakeClient: NewFakeClient()}
	s := newChatState(f)
	defer s.close()
	st := &gitRemotesState{active: true}
	st.deleteClick(s, f, "thread", "origin")
	if calls := f.recorded(); len(calls) != 0 {
		t.Fatalf("首次点击执行删除：%v", calls)
	}
	st.deleteClick(s, f, "thread", "origin")
	gitPaneWait(t, func() bool { return len(f.recorded()) != 0 })
	if got := f.recorded(); !reflect.DeepEqual(got, []string{"remove:origin"}) {
		t.Fatalf("调用 = %v", got)
	}
}

func TestGitRemotesDialog(t *testing.T) {
	f := &gitRemotesFake{FakeClient: NewFakeClient(), entries: []git.RemoteInfo{{Name: "origin", FetchURL: "https://example.com/repo"}}}
	s := newChatState(f)
	defer s.close()
	open := true
	tt := ui.NewTester(func(c *ui.Context) { renderGitRemotesDialog(c, s, "thread", &open) }, 800, 600)
	st := gitRemotesStateFor(&open)
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.loading })
	tt.Frame()
	if !tt.HasText("origin") || !tt.HasText("https://example.com/repo") {
		t.Fatal("未显示远端")
	}
	st.adding = true
	for _, tc := range []struct {
		name, url string
		disabled  bool
	}{{"", "", true}, {"origin", "", true}, {"", "url", true}, {"origin", "url", false}} {
		st.name, st.url = tc.name, tc.url
		tt.Frame()
		node, ok := tt.Access("添加")
		if !ok || node.Disabled != tc.disabled {
			t.Fatalf("添加按钮状态 = %+v，存在 %v", node, ok)
		}
	}
	open = false
	tt.Frame()
}

func TestGitRemotesUnsupported(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	open := true
	tt := ui.NewTester(func(c *ui.Context) { renderGitRemotesDialog(c, s, "thread", &open) }, 800, 600)
	if !tt.HasText("暂无远端") {
		t.Fatal("未显示空状态")
	}
	open = false
	tt.Frame()
}

func TestGitRemotesAddToast(t *testing.T) {
	f := &gitRemotesFake{FakeClient: NewFakeClient()}
	s := newChatState(f)
	defer s.close()
	st := &gitRemotesState{active: true}
	st.run(s, f, "thread", "已添加远端 origin", func(ctx context.Context) error { return f.GitRemoteAdd(ctx, "thread", "origin", "url") })
	gitPaneWait(t, func() bool { return recallTestToast(s) == "已添加远端 origin" })
}
