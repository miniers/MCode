package mcodeui

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
	"mcode/internal/testenv"
)

type gitBlameFakeClient struct {
	*FakeClient
	mu    sync.Mutex
	paths []string
}

func (f *gitBlameFakeClient) GitBlame(_ context.Context, _, path, rev string) ([]git.BlameLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path+":"+rev)
	return []git.BlameLine{{Line: 1, SHA: strings.Repeat("a", 40), Author: "张三", Summary: path, AuthorTime: time.Now()}, {Line: 2, SHA: strings.Repeat("a", 40), Author: "张三", Summary: path, AuthorTime: time.Now()}, {Line: 3, SHA: strings.Repeat("0", 40), Summary: "未提交"}}, nil
}
func TestGitBlameAlpha(t *testing.T) {
	a := time.Unix(100, 0)
	b := time.Unix(200, 0)
	for _, tc := range []struct {
		start, end, at time.Time
		want           float32
	}{{a, b, a, .15}, {a, b, b, 1}, {a, a, a, 1}} {
		if got := gitBlameTimeAlpha(tc.start, tc.end, tc.at); math.Abs(float64(got-tc.want)) > .00001 {
			t.Fatalf("透明度 = %v，期望 %v", got, tc.want)
		}
	}
}
func TestGitBlameView(t *testing.T) {
	testenv.TempHome(t)
	client := &gitBlameFakeClient{FakeClient: NewFakeClient()}
	s := newChatState(client)
	defer s.close()
	ps := &gitPaneState{owner: s}
	path := "first"
	var opened string
	tt := ui.NewTester(func(c *ui.Context) { renderGitBlameView(c, s, "thread", ps, path, func(sha string) { opened = sha }) }, 600, 300)
	st := gitBlameStateFor(ps)
	gitPaneWait(t, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return len(st.lines) == 3 })
	tt = ui.NewTester(func(c *ui.Context) { renderGitBlameView(c, s, "thread", ps, path, func(sha string) { opened = sha }) }, 600, 300)
	if !tt.HasText("first") {
		t.Fatal("缺少提交标题")
	}
	if err := tt.Click(strings.Repeat("a", 40) + " 2"); err != nil {
		t.Fatal(err)
	}
	if opened != strings.Repeat("a", 40) {
		t.Fatalf("打开提交 %q", opened)
	}
	if err := tt.Click(strings.Repeat("0", 40) + " 3"); err != nil {
		t.Fatal(err)
	}
	if opened != strings.Repeat("a", 40) {
		t.Fatal("未提交行触发回调")
	}
	path = "second"
	tt = ui.NewTester(func(c *ui.Context) { renderGitBlameView(c, s, "thread", ps, path, nil) }, 600, 300)
	_ = tt
	gitPaneWait(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return len(client.paths) == 2 })
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.paths[0] != "first:" || client.paths[1] != "second:" {
		t.Fatalf("请求 = %v", client.paths)
	}
}

func TestGitBlameTitleOnlyFirstLine(t *testing.T) {
	lines := []git.BlameLine{{Line: 1, SHA: "abc", Summary: "独特的标题"}, {Line: 2, SHA: "abc", Summary: "独特的标题"}}
	blocks := git.BlameBlocks(lines)
	st := &gitBlameState{blockByRow: []int{0, 0}, hoverBlock: -1, hoverRow: -1}
	for i := range lines {
		tt := ui.NewTester(func(c *ui.Context) { renderGitBlameLine(c, st, lines, blocks, i, nil) }, 600, 40)
		if got := tt.HasText("独特的标题"); got != (i == 0) {
			t.Fatalf("第 %d 行标题显示 = %v", i+1, got)
		}
	}
}
