package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestParseGitHubRepo 验证 Git URL 与 owner/repo 的解析。
func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{"owner/repo", "ada/mcode", "ada/mcode", true},
		{"去除首尾空白", "  ada/mcode  ", "ada/mcode", true},
		{"https 地址", "https://github.com/ada/mcode", "ada/mcode", true},
		{"https 地址带 .git", "https://github.com/ada/mcode.git", "ada/mcode", true},
		{"https 地址带子路径", "https://github.com/ada/mcode/tree/main/internal", "ada/mcode", true},
		{"https 地址带查询串", "https://github.com/ada/mcode?tab=readme-ov-file", "ada/mcode", true},
		{"http 地址", "http://github.com/ada/mcode.git", "ada/mcode", true},
		{"ssh 地址", "ssh://git@github.com/ada/mcode.git", "ada/mcode", true},
		{"scp 风格地址", "git@github.com:ada/mcode.git", "ada/mcode", true},
		{"省略协议", "github.com/ada/mcode", "ada/mcode", true},
		{"仓库名带点与下划线", "ada/my_repo.go", "ada/my_repo.go", true},
		{"缺仓库名", "ada", "", false},
		{"空输入", "   ", "", false},
		{"非 github 域名", "https://gitlab.com/ada/mcode", "", false},
		{"非 github 域名（省略协议）", "gitlab.com/ada/mcode", "", false},
		{"scp 风格多段路径", "git@github.com:ada/mcode/extra", "", false},
		{"非法字符", "ada/mc ode", "", false},
		{"只有分隔符", "/", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseGitHubRepo(tc.input)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseGitHubRepo(%q) = (%q, %v), want (%q, %v)", tc.input, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestGitHubCloneURLAndName 验证克隆地址、目录名与目标目录拼接。
func TestGitHubCloneURLAndName(t *testing.T) {
	if got := githubCloneURL("ada/mcode"); got != "https://github.com/ada/mcode.git" {
		t.Errorf("githubCloneURL = %q", got)
	}
	names := map[string]string{
		"https://github.com/ada/mcode.git":    "mcode",
		"git@github.com:ada/mcode.git":        "mcode",
		"ada/mcode":                           "mcode",
		"https://github.com/ada/mcode/tree/x": "mcode",
		"https://github.com/ada/mcode?tab=x":  "mcode",
	}
	for input, want := range names {
		if got := repoDirName(input); got != want {
			t.Errorf("repoDirName(%q) = %q, want %q", input, got, want)
		}
	}
	if got := cloneTargetDir("/tmp/clones", "ada/mcode.git"); got != "/tmp/clones/mcode" {
		t.Errorf("cloneTargetDir = %q", got)
	}
	if got := cloneTargetDir("/tmp/clones", "   "); got != "" {
		t.Errorf("无法解析仓库名时应返回空路径, got %q", got)
	}
}

// TestCloneNameFromInput 验证展示用的规范名称与解析失败时的原文回退。
func TestCloneNameFromInput(t *testing.T) {
	if got := cloneNameFromInput(" ada/mcode.git "); got != "ada/mcode" {
		t.Errorf("cloneNameFromInput = %q", got)
	}
	if got := cloneNameFromInput("  not a url "); got != "not a url" {
		t.Errorf("解析失败时应返回原文, got %q", got)
	}
	if got := cloneNameFromInput(""); got != "" {
		t.Errorf("空输入应返回空串, got %q", got)
	}
}

// TestGitHubClonePanelOpenAndClose 验证侧边栏入口打开面板、关闭按钮收起面板。
func TestGitHubClonePanelOpenAndClose(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 800)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	// 侧边栏入口收进「添加项目」浮层：先展开浮层再点克隆项
	if err := tt.Click("添加项目"); err != nil {
		t.Fatalf("点击侧边栏添加项目入口失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("从 GitHub 克隆…"); err != nil {
		t.Fatalf("点击侧边栏克隆入口失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("选择仓库，克隆到你指定的目录并作为项目打开。")
	})
	if !tt.HasText("尚未选择目录") || !tt.HasText("从 GitHub 克隆") {
		t.Fatalf("克隆面板缺少目录提示或标题: %q", tt.Texts())
	}

	// 未选择目录时克隆按钮应禁用：点击不产生任何状态变化
	cs := getGitHubCloneState(state)
	cs.search = "ada/mcode"
	tt.Frame()
	if err := tt.Click("克隆"); err != nil {
		t.Fatalf("点击克隆失败: %v", err)
	}
	tt.Frame()
	if cs.cloning || cs.errMsg != "" {
		t.Fatalf("未选择目录时克隆按钮应禁用, 当前 errMsg=%q", cs.errMsg)
	}

	if err := tt.Click("关闭"); err != nil {
		t.Fatalf("点击关闭按钮失败: %v", err)
	}
	tt.Frame()
	if tt.HasText("选择仓库，克隆到你指定的目录并作为项目打开。") {
		t.Fatalf("关闭后不应再渲染克隆面板")
	}
	if cs.open {
		t.Fatalf("关闭后面板状态应为 false")
	}
}

// TestGitHubClonePanelValidation 验证克隆前的输入校验与目标目录冲突提示。
func TestGitHubClonePanelValidation(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 800)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	openGitHubClonePanel(state)
	tt.Frame()
	cs := getGitHubCloneState(state)

	// 输入既不是 Git URL 也不是 owner/repo
	cs.search = "not a url"
	cs.parentDir = t.TempDir()
	tt.Frame()
	if err := tt.Click("克隆"); err != nil {
		t.Fatalf("点击克隆失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("请输入 Git URL 或 owner/repo") })

	// 未选择目录（按钮此时禁用，直接调用启动函数覆盖该分支）
	cs.search = "ada/mcode"
	cs.parentDir = ""
	startGitHubClone(state, cs)
	tt.Frame()
	if cs.errMsg != "请选择克隆到的目录" {
		t.Fatalf("未选择目录时应提示选择目录, got %q", cs.errMsg)
	}

	// 目标目录已存在时拒绝克隆（不触发 git clone）
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "mcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	cs.search = "https://github.com/ada/mcode.git"
	cs.parentDir = parent
	tt.Frame()
	if err := tt.Click("克隆"); err != nil {
		t.Fatalf("点击克隆失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("目标目录已存在：") })
	if cs.cloning {
		t.Fatalf("目标目录冲突时不应进入克隆中状态")
	}
}

// TestGitHubClonePanelOpensFromEmptyState 验证空态项目浮层里的克隆入口。
func TestGitHubClonePanelOpensFromEmptyState(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 800)

	waitFor(t, tt, func() bool { return tt.HasText(" 中构建什么？") })

	// 收起侧边栏，确保点到的是空态浮层里的入口
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()

	if err := tt.Click("切换项目"); err != nil {
		t.Fatalf("点击项目名失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if err := tt.Click("从 GitHub 克隆…"); err != nil {
		t.Fatalf("点击浮层克隆入口失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("选择仓库，克隆到你指定的目录并作为项目打开。")
	})
	if cs := getGitHubCloneState(state); cs == nil || !cs.open {
		t.Fatalf("空态入口应打开克隆面板")
	}
}
