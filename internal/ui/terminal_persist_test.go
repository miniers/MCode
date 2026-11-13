package mcodeui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTerminalThreadSnapshotRoundTrip 验证标签与输出快照能原样保存并读回。
func TestTerminalThreadSnapshotRoundTrip(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	dir := t.TempDir()

	want := terminalThreadSnapshot{
		ThreadID: "thread-1",
		Active:   1,
		Tabs: []terminalTabSnapshot{
			{Dir: dir},
			{Dir: dir, Main: true, Title: "zsh", Output: "go test ./...\nok   mcode/internal/ui"},
		},
	}
	if err := saveTerminalThreadSnapshot(want); err != nil {
		t.Fatalf("保存快照失败: %v", err)
	}
	got, ok := loadTerminalThreadSnapshot("thread-1")
	if !ok {
		t.Fatal("快照应当能读回")
	}
	if got.ThreadID != want.ThreadID || got.Active != want.Active || len(got.Tabs) != len(want.Tabs) {
		t.Fatalf("读回的元数据 = %+v，想要 %+v", got, want)
	}
	if got.SavedAt == "" {
		t.Fatal("落盘时间应当被记录")
	}
	for i := range want.Tabs {
		if got.Tabs[i] != want.Tabs[i] {
			t.Fatalf("第 %d 个标签 = %+v，想要 %+v", i, got.Tabs[i], want.Tabs[i])
		}
	}

	info, err := os.Stat(terminalSnapshotPath("thread-1"))
	if err != nil {
		t.Fatalf("快照文件不存在: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("快照权限 = %o，想要 600", perm)
		}
	}
}

// TestLoadTerminalThreadSnapshotCorruptJSON 验证损坏的 JSON 被当成「没有记录」，不 panic 也不误读。
func TestLoadTerminalThreadSnapshotCorruptJSON(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	path := terminalSnapshotPath("broken")
	if path == "" {
		t.Fatal("数据目录可用时应当能算出快照路径")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadTerminalThreadSnapshot("broken"); ok {
		t.Fatal("损坏的 JSON 不该被当成有效快照")
	}

	// 损坏文件仍然可以被一次正常保存覆盖掉。
	if err := saveTerminalThreadSnapshot(terminalThreadSnapshot{ThreadID: "broken", Tabs: []terminalTabSnapshot{{Dir: t.TempDir()}}}); err != nil {
		t.Fatalf("覆盖损坏快照失败: %v", err)
	}
	if _, ok := loadTerminalThreadSnapshot("broken"); !ok {
		t.Fatal("覆盖之后应当能读回")
	}
}

// TestFilterRestorableTerminalTabsDropsMissingDir 验证目录已不存在的标签被丢弃，活动下标随之重映射。
func TestFilterRestorableTerminalTabsDropsMissingDir(t *testing.T) {
	existing := t.TempDir()
	tabs := []terminalTabSnapshot{
		{Dir: filepath.Join(existing, "gone")},
		{Dir: existing, Main: true},
		{Dir: ""},
		{Dir: existing},
	}
	kept, active := filterRestorableTerminalTabs(tabs, 1)
	if len(kept) != 2 {
		t.Fatalf("应当只保留 2 个目录存在的标签，实际 %d 个: %+v", len(kept), kept)
	}
	if kept[0].Dir != existing || !kept[0].Main || kept[1].Dir != existing {
		t.Fatalf("保留的标签 = %+v", kept)
	}
	if active != 0 {
		t.Fatalf("活动标签下标重映射为 %d，想要 0", active)
	}

	// 全部目录都不存在时返回空，调用方据此回退到默认标签。
	empty, _ := filterRestorableTerminalTabs([]terminalTabSnapshot{{Dir: filepath.Join(existing, "nope")}}, 0)
	if len(empty) != 0 {
		t.Fatalf("应当全部丢弃，实际 %+v", empty)
	}
}

// TestRemoveTerminalSnapshot 验证删除快照（会话删除/归档时备用）不报错也不残留文件。
func TestRemoveTerminalSnapshot(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	dir := t.TempDir()
	if err := saveTerminalThreadSnapshot(terminalThreadSnapshot{ThreadID: "gone", Tabs: []terminalTabSnapshot{{Dir: dir}}}); err != nil {
		t.Fatal(err)
	}
	if err := removeTerminalSnapshot("gone"); err != nil {
		t.Fatalf("删除快照失败: %v", err)
	}
	if _, err := os.Stat(terminalSnapshotPath("gone")); !os.IsNotExist(err) {
		t.Fatalf("快照文件应当已删除，Stat 返回 %v", err)
	}
	// 重复删除不报错。
	if err := removeTerminalSnapshot("gone"); err != nil {
		t.Fatalf("重复删除应当幂等: %v", err)
	}
}

// TestLimitTerminalOutput 验证控制字符被清掉、行数只保留最后 2000 行、字节数不超上限。
func TestLimitTerminalOutput(t *testing.T) {
	got := limitTerminalOutput("a\x00\x07b\tc\n\n\n")
	if got != "ab\tc" {
		t.Fatalf("清洗结果 = %q，想要 %q", got, "ab\tc")
	}

	var lines []string
	for i := 0; i < terminalSnapshotMaxLines+500; i++ {
		lines = append(lines, "line")
	}
	limited := limitTerminalOutput(strings.Join(lines, "\n"))
	if n := strings.Count(limited, "\n") + 1; n != terminalSnapshotMaxLines {
		t.Fatalf("保留行数 = %d，想要 %d", n, terminalSnapshotMaxLines)
	}

	huge := limitTerminalOutput(strings.Repeat("汉", 100000))
	if len(huge) > terminalSnapshotMaxBytes {
		t.Fatalf("字节数 = %d，超过上限 %d", len(huge), terminalSnapshotMaxBytes)
	}
	if !utf8.ValidString(huge) {
		t.Fatal("截断把多字节字符切开了")
	}
}

// TestStripRestoredTerminalHistory 验证快照里不会再叠加此前恢复进来的历史，避免重启次数越多内容越长。
func TestStripRestoredTerminalHistory(t *testing.T) {
	text := "旧输出\r\n" + terminalSnapshotMarker + "\r\n新输出\r\n$ "
	if got := stripRestoredTerminalHistory(text); got != "新输出\r\n$ " {
		t.Fatalf("剥离结果 = %q", got)
	}
	if got := stripRestoredTerminalHistory("没有标记的输出"); got != "没有标记的输出" {
		t.Fatalf("没有标记时应当原样返回，得到 %q", got)
	}
}
