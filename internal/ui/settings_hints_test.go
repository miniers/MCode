package mcodeui

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func TestSettingHintKeysExist(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	var corpus strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "settings_hints.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		corpus.Write(data)
		corpus.WriteByte('\n')
	}
	allSrc := corpus.String()

	for key := range settingHints {
		quoted := strconv.Quote(key)
		if !strings.Contains(allSrc, quoted) {
			t.Errorf("settingHints 键 %s 未在 internal/ui 非测试源码中作为字符串字面量出现", quoted)
		}
	}
	for prefix := range settingHintPrefixes {
		quoted := strconv.Quote(prefix)
		if !strings.Contains(allSrc, quoted) {
			t.Errorf("settingHintPrefixes 键 %s 未在 internal/ui 非测试源码中作为字符串字面量出现", quoted)
		}
	}
}

func TestSettingHintCopy(t *testing.T) {
	checkHint := func(key, hint string) {
		t.Helper()
		if hint == "" {
			t.Errorf("%s 的说明为空", key)
			return
		}
		if n := utf8.RuneCountInString(hint); n > 60 {
			t.Errorf("%s 的说明超过 60 字 (%d): %q", key, n, hint)
		}
		if strings.HasPrefix(hint, "可能") || strings.HasPrefix(hint, "大概") {
			t.Errorf("%s 的说明不能以「可能」「大概」开头: %q", key, hint)
		}
		if strings.Contains(hint, "等。") {
			t.Errorf("%s 的说明不能包含「等。」: %q", key, hint)
		}
	}

	for key, hint := range settingHints {
		checkHint(key, hint)
	}
	for prefix, hint := range settingHintPrefixes {
		checkHint(prefix, hint)
	}

	wantPrefix := settingHintPrefixes["row-mcp-newthread-"]
	if got := settingHint("row-mcp-newthread-foo"); got != wantPrefix || got == "" {
		t.Errorf("settingHint(%q) = %q, want %q", "row-mcp-newthread-foo", got, wantPrefix)
	}
	if got := settingHint("row-nonexistent-key"); got != "" {
		t.Errorf("settingHint(%q) = %q, want empty", "row-nonexistent-key", got)
	}
}

func TestSettingHintTooltipOnHover(t *testing.T) {
	tt := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c.Key("test-hint-rows")).FillWidth().Children(func() {
			SettingRow(c, "row-motion", "动效", func() {})
			SettingRow(c, "row-no-hint-test", "无说明项", func() {})
		})
	}, 400, 240, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})

	wantHint := settingHint("row-motion")
	if wantHint == "" {
		t.Fatal("row-motion 说明为空")
	}

	hintRect, ok := tt.Find("动效")
	if !ok {
		t.Fatal("未找到「动效」标题")
	}
	tt.Move(hintRect.X+hintRect.W/2, hintRect.Y+hintRect.H/2)
	tt.Advance(650 * time.Millisecond)
	tt.Frame()

	tipAcc, ok := tt.AccessByRole(ui.RoleTooltip, "")
	if !ok {
		t.Fatalf("悬停有说明的设置项标题后未出现 RoleTooltip，texts=%v", tt.Texts())
	}
	if tipAcc.Name != wantHint {
		t.Fatalf("Tooltip = %q, want %q", tipAcc.Name, wantHint)
	}
	if !tt.HasText(wantHint) {
		t.Fatalf("界面文本中未显示说明 %q，texts=%v", wantHint, tt.Texts())
	}

	noHintRect, ok := tt.Find("无说明项")
	if !ok {
		t.Fatal("未找到「无说明项」标题")
	}
	tt.Move(noHintRect.X+noHintRect.W/2, noHintRect.Y+noHintRect.H/2)
	tt.Advance(650 * time.Millisecond)
	tt.Frame()

	if tipAcc, ok := tt.AccessByRole(ui.RoleTooltip, ""); ok {
		t.Fatalf("悬停无说明的设置项标题不应显示 Tooltip，got %q", tipAcc.Name)
	}
}
