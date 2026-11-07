package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
)

// 没有匹配项时面板保留，按补全类型显示空态文案，而不是整块消失。
func TestCompletionEmptyStateKeepsPanel(t *testing.T) {
	tests := []struct {
		name  string
		draft string
		want  string
	}{
		{name: "文件", draft: "@zzz_no_such_file", want: "没有匹配的文件"},
		{name: "会话", draft: "@thread:zzz_no_such_thread", want: "没有匹配的会话"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			tester := composerTester(s)
			tester.Type(tt.draft)
			waitFor(t, tester, func() bool { return tester.HasText(tt.want) })
			if _, ok := tester.Find(tt.want); !ok {
				t.Fatalf("empty state %q missing, texts: %q", tt.want, tester.Texts())
			}
			s.mu.Lock()
			kind := s.completionKind
			s.mu.Unlock()
			if kind != completionMention {
				t.Fatalf("panel must stay open, kind = %v", kind)
			}
		})
	}
}

// 会话候选行用矢量图标，标题不再带 💬 前缀。
func TestCompletionThreadRowsDropEmoji(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}, {ID: "t2", Title: "另一个会话"}}
	tester := ui.NewTester(func(c *ui.Context) {
		renderComposer(c, s)
	}, 900, 500)

	tester.Type("@thread:")
	waitFor(t, tester, func() bool { return tester.HasText("另一个会话") })
	for _, text := range tester.Texts() {
		if strings.Contains(text, "💬") {
			t.Fatalf("thread row still uses emoji: %q", text)
		}
	}
}

// 叶子候选面板底部带键位页脚，空态时也在。
func TestCompletionPanelFooter(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.commandsCache["t1"] = []SlashCommand{{Name: "plan"}}
	tester := composerTester(s)
	tester.Type("/pl")
	waitFor(t, tester, func() bool { return tester.HasText("/plan") })

	for _, label := range []string{"↑↓ 选择", "Tab 补全", "↵ 确认", "esc 关闭"} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("footer hint %q missing, texts: %q", label, tester.Texts())
		}
	}
}

// 技能候选携带来源字段，胶囊文案按 scope 映射；未知来源不画胶囊。
func TestSkillCompletionSourceLabel(t *testing.T) {
	skills := []agentconf.Skill{
		{Agent: "claude", Scope: "project", Name: "deploy", Path: "/p/deploy/SKILL.md"},
		{Agent: "codex", Scope: "global", Name: "review", Path: "/p/review/SKILL.md"},
		{Agent: "claude", Scope: "external", Name: "plugin", Path: "/p/plugin/SKILL.md"},
	}
	items := skillCompletionItems(skills, "")
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	for _, it := range items {
		if it.Source == "" {
			t.Fatalf("skill item lost source: %+v", it)
		}
		if completionSourceLabel(it.Source) == "" {
			t.Fatalf("source %q has no badge label", it.Source)
		}
	}
	cases := map[string]string{
		"project":  "项目",
		"global":   "全局",
		"external": "外部",
		"":         "",
		"unknown":  "",
	}
	for scope, want := range cases {
		if got := completionSourceLabel(scope); got != want {
			t.Fatalf("completionSourceLabel(%q) = %q, want %q", scope, got, want)
		}
	}
}
