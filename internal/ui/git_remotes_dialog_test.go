package mcodeui

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitRemotesFake struct {
	*FakeClient
	mu      sync.Mutex
	calls   []string
	entries []git.RemoteInfo
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
