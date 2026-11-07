package mcodeui

import (
	"strings"
	"testing"
)

// TestIsLongPastedText 覆盖行数与字数两条阈值的边界值。
func TestIsLongPastedText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"空文本", "", false},
		{"19 行", strings.Repeat("a\n", 18) + "a", false},
		{"20 行", strings.Repeat("a\n", 19) + "a", true},
		{"21 行", strings.Repeat("a\n", 20) + "a", true},
		{"1499 字", strings.Repeat("字", 1499), false},
		{"1500 字", strings.Repeat("字", 1500), true},
		{"短文本", "hello world", false},
	}
	for _, tc := range tests {
		if got := isLongPastedText(tc.text); got != tc.want {
			t.Fatalf("%s: isLongPastedText = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRegisterPastedPlaceholder 覆盖占位符格式与序号自增。
func TestRegisterPastedPlaceholder(t *testing.T) {
	s := &chatState{}
	first := s.registerPastedPlaceholder("a\nb\nc")
	second := s.registerPastedPlaceholder(strings.Repeat("x\n", 24))

	if first != "[粘贴文本 #1 · 3 行]" {
		t.Fatalf("first placeholder = %q", first)
	}
	if second != "[粘贴文本 #2 · 25 行]" {
		t.Fatalf("second placeholder = %q", second)
	}
	if len(s.pastedPlaceholders) != 2 {
		t.Fatalf("mapping size = %d, want 2", len(s.pastedPlaceholders))
	}
	if s.pastedPlaceholders[first] != "a\nb\nc" {
		t.Fatalf("mapping[%q] = %q", first, s.pastedPlaceholders[first])
	}
}

// TestExpandPastedPlaceholders 覆盖多占位符展开、用户删掉其中一个、未登记占位符原样保留、展开后条目仍保留且再次展开结果相同。
func TestExpandPastedPlaceholders(t *testing.T) {
	s := &chatState{}
	kept := s.registerPastedPlaceholder("第一段原文")
	dropped := s.registerPastedPlaceholder("第二段原文")

	// 用户删掉了第二个占位符，只剩第一个留在草稿里。
	draft := "前言 " + kept + " 后记 [粘贴文本 #99 · 30 行]"
	got := s.expandPastedPlaceholders(draft)

	want := "前言 第一段原文 后记 [粘贴文本 #99 · 30 行]"
	if got != want {
		t.Fatalf("expand = %q, want %q", got, want)
	}
	if _, ok := s.pastedPlaceholders[kept]; !ok {
		t.Fatal("已展开的占位符条目应保留，便于发送失败后重试")
	}
	if _, ok := s.pastedPlaceholders[dropped]; !ok {
		t.Fatal("未出现在文本里的条目应保留")
	}
	// 再次展开同一段文本结果相同（展开是幂等的）。
	if again := s.expandPastedPlaceholders(draft); again != want {
		t.Fatalf("二次展开应得到同样结果，got %q, want %q", again, want)
	}
}

// TestExpandPastedPlaceholdersMultiple 覆盖同一条文本里插入多个占位符的展开。
func TestExpandPastedPlaceholdersMultiple(t *testing.T) {
	s := &chatState{}
	a := s.registerPastedPlaceholder("AAA")
	b := s.registerPastedPlaceholder("BBB")

	got := s.expandPastedPlaceholders(a + "\n中间\n" + b)
	if got != "AAA\n中间\nBBB" {
		t.Fatalf("expand = %q", got)
	}
	if len(s.pastedPlaceholders) != 2 {
		t.Fatalf("展开后映射条目应保留，剩 %d 条", len(s.pastedPlaceholders))
	}
	// 未登记的占位符原样返回。
	if got := s.expandPastedPlaceholders("[粘贴文本 #99 · 30 行]"); got != "[粘贴文本 #99 · 30 行]" {
		t.Fatalf("未登记占位符应原样返回，got %q", got)
	}
	// 映射为空时直接原样返回。
	if got := (&chatState{}).expandPastedPlaceholders(a); got != a {
		t.Fatalf("无映射时应原样返回，got %q", got)
	}
}

// TestInsertPastedPlaceholderAt 覆盖在光标处插入占位符与越界钳制。
func TestInsertPastedPlaceholderAt(t *testing.T) {
	draft := "前面后面"
	// 「前」1 个 rune 后插入。
	next, caret := insertPastedPlaceholderAt(draft, 1, 1, "[粘贴文本 #1 · 20 行]")
	if next != "前[粘贴文本 #1 · 20 行]面后面" {
		t.Fatalf("next = %q", next)
	}
	if caret != 1+len([]rune("[粘贴文本 #1 · 20 行]")) {
		t.Fatalf("caret = %d", caret)
	}

	// 选区替换：把「后面」换掉。
	next, caret = insertPastedPlaceholderAt(draft, 2, 4, "[粘贴文本 #2 · 20 行]")
	if next != "前面[粘贴文本 #2 · 20 行]" {
		t.Fatalf("选区替换 next = %q", next)
	}
	if caret != 2+len([]rune("[粘贴文本 #2 · 20 行]")) {
		t.Fatalf("选区替换 caret = %d", caret)
	}

	// 越界钳制到文末。
	next, _ = insertPastedPlaceholderAt("ab", 99, 99, "X")
	if next != "abX" {
		t.Fatalf("越界 next = %q", next)
	}
}
