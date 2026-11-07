package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// headerPermissionCapsuleLabel 是默认档位下权限圆钮的 Label，点击它打开权限菜单。
const headerPermissionCapsuleLabel = "自动 · 改文件和只读命令不询问"

// personaMenuTestPersonas 返回测试用的人设列表。
func personaMenuTestPersonas() []MagpiePersona {
	return []MagpiePersona{
		{ID: "strict", Name: "严谨审阅", Description: "先找风险再动手"},
		{ID: "cheerful", Name: "轻松搭档", Description: "口语化回复"},
	}
}

// localThreadPersona 返回本地会话列表里该会话的 Persona，saving 表示偏好仍在校验/保存中。
// updateComposerPrefs 在保存进行中会丢弃新的写入，测试必须等它落地后再点下一次。
func localThreadPersona(s *chatState, threadID string) (persona string, saving bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.Persona, s.composerPrefsSaving[threadID]
		}
	}
	return "", s.composerPrefsSaving[threadID]
}

// 有人设时，权限菜单底部出现「人设」标题、「无人设」与两个人设项。
func TestChatHeaderPermissionPersonaMenu(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.SetMagpiePersonas(personaMenuTestPersonas())
	_, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	if err := tt.Click(headerPermissionCapsuleLabel); err != nil {
		t.Fatalf("click permission capsule failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("严谨审阅")
		return ok
	})
	if _, ok := tt.Find("人设"); !ok {
		t.Fatalf("人设分区缺少标题, texts: %q", tt.Texts())
	}
	for _, name := range []string{"无人设", "严谨审阅", "轻松搭档"} {
		if _, ok := tt.Find(name); !ok {
			t.Fatalf("人设分区缺少 %q, texts: %q", name, tt.Texts())
		}
	}
}

// 人设列表为空时整块不渲染：菜单里只有「权限」标题，没有「人设」标题与「无人设」项。
func TestChatHeaderPermissionHidesPersonaSection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	_, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	if err := tt.Click(headerPermissionCapsuleLabel); err != nil {
		t.Fatalf("click permission capsule failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("权限")
		return ok
	})
	if _, ok := tt.Find("人设"); ok {
		t.Fatalf("没有人设时不应渲染人设标题, texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("无人设"); ok {
		t.Fatalf("没有人设时不应渲染无人设项, texts: %q", tt.Texts())
	}
}

// 点人设项写入其 id，再点「无人设」写入 none；从未设置时点「无人设」不产生写入。
func TestChatHeaderPermissionPersonaSelect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.SetMagpiePersonas(personaMenuTestPersonas())
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	s.selectThread("t1")

	openMenu := func() {
		if err := tt.Click(headerPermissionCapsuleLabel); err != nil {
			t.Fatalf("click permission capsule failed: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, ok := tt.Find("无人设")
			return ok
		})
	}

	// 从未设置：点「无人设」不写。
	before := fake.ComposerPrefsCallCount()
	openMenu()
	if err := tt.Click("无人设"); err != nil {
		t.Fatalf("click 无人设 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("无人设")
		return !ok
	})
	if n := fake.ComposerPrefsCallCount(); n != before {
		t.Fatalf("从未设置时点无人设不应写入, got %d 次", n-before)
	}

	// 选人设：写入该人设 id。
	openMenu()
	if err := tt.Click("严谨审阅"); err != nil {
		t.Fatalf("click 严谨审阅 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		persona, saving := localThreadPersona(s, "t1")
		return persona == "strict" && !saving
	})
	if threadID, prefs, ok := fake.LastComposerPrefsCall(); !ok || threadID != "t1" || prefs.Persona != "strict" {
		t.Fatalf("人设写入错误, threadID=%q prefs.Persona=%q ok=%v", threadID, prefs.Persona, ok)
	}

	// 已有的人设改回无人设：写入 none。
	openMenu()
	if err := tt.Click("无人设"); err != nil {
		t.Fatalf("click 无人设 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		persona, saving := localThreadPersona(s, "t1")
		return persona == "none" && !saving
	})
	if _, prefs, ok := fake.LastComposerPrefsCall(); !ok || prefs.Persona != "none" {
		t.Fatalf("无人设应写入 none, prefs.Persona=%q ok=%v", prefs.Persona, ok)
	}
}

// personaMenuChecked 与 personaMenuWriteValue 覆盖三态语义：空串/none/具体 id、以及 id 已不在列表里。
func TestPersonaMenuTriState(t *testing.T) {
	personas := personaMenuTestPersonas()

	checked := [][3]string{
		{"", personaNoneValue},
		{"none", personaNoneValue},
		{"gone", personaNoneValue}, // 人设已被删掉：回落到无人设
		{"strict", personaNoneValue},
		{"strict", "strict"},
		{"strict", "cheerful"},
		{"", "strict"},
		{"none", "strict"},
	}
	want := []bool{true, true, true, false, true, false, false, false}
	for i, c := range checked {
		if got := personaMenuChecked(c[0], c[1], personas); got != want[i] {
			t.Fatalf("personaMenuChecked(%q, %q) = %v, want %v", c[0], c[1], got, want[i])
		}
	}

	if value, ok := personaMenuWriteValue("", personaNoneValue); ok {
		t.Fatalf("从未设置时点无人设不应写入, got %q", value)
	}
	if value, ok := personaMenuWriteValue("strict", personaNoneValue); !ok || value != "none" {
		t.Fatalf("已有人设时点无人设应写 none, got %q ok=%v", value, ok)
	}
	if value, ok := personaMenuWriteValue("", "strict"); !ok || value != "strict" {
		t.Fatalf("选人设应写入其 id, got %q ok=%v", value, ok)
	}
	if _, ok := personaMenuWriteValue("strict", "strict"); ok {
		t.Fatalf("重复选中同一人设不应重复写入")
	}
}
