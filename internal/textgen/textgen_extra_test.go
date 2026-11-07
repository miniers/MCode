package textgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTitlePromptWithAttachments(t *testing.T) {
	p := TitlePromptWithAttachments("", "", []string{"/tmp/x/a.png", "dir/b.go"})
	if !strings.Contains(p, "Task with attachments: a.png, b.go") {
		t.Errorf("missing attachment text: %s", p)
	}
	if strings.Contains(p, "/tmp/x") {
		t.Errorf("should only use base names: %s", p)
	}
	p = TitlePromptWithAttachments("修复登录", "", []string{"a.png"})
	if strings.Contains(p, "Task with attachments") || !strings.Contains(p, "修复登录") {
		t.Errorf("user text should win: %s", p)
	}
	if TitlePrompt("hi", "ok") != TitlePromptWithAttachments("hi", "ok", nil) {
		t.Error("TitlePrompt should equal nil-attachment variant")
	}
}

func TestLocalBranchSlug(t *testing.T) {
	cases := map[string]string{
		"Fix the Login Bug!": "fix-the-login-bug",
		"修复 登录 bug":          "bug",
		"一二三":                "",
		"":                   "",
		"a b c d e f g":      "a-b-c-d-e",
		"implementation-of-extraordinarily long wordsabcdefghijklmnop": "implementation-of-extraordinarily-long",
	}
	for in, want := range cases {
		got := LocalBranchSlug(in)
		if got != want {
			t.Errorf("LocalBranchSlug(%q)=%q want %q", in, got, want)
		}
		if len(got) > 40 {
			t.Errorf("too long: %q", got)
		}
	}
}

func TestCleanBranchNameNormalizes(t *testing.T) {
	cases := map[string]string{
		"  `Fix_Login Bug!!`\nextra": "fix-login-bug",
		"mcode/add-search":           "add-search",
		"中文标点，。":                     "",
		"":                           "",
	}
	for in, want := range cases {
		if got := CleanBranchName(in); got != want {
			t.Errorf("CleanBranchName(%q)=%q want %q", in, got, want)
		}
	}
	if got := CleanBranchName(strings.Repeat("ab-", 30)); len(got) > 40 || strings.HasSuffix(got, "-") {
		t.Errorf("long input = %q", got)
	}
}

func TestBranchNameFallbackAndPrefix(t *testing.T) {
	// 不支持的 provider 会让 Generate 失败，走本地降级。
	got := BranchName(context.Background(), "bogus", "", "Add Search Box")
	if got != "mcode/add-search-box" {
		t.Errorf("got %q", got)
	}
	if got := BranchName(context.Background(), "bogus", "", "纯中文"); got != "mcode/task" {
		t.Errorf("got %q", got)
	}
}

func TestCommitMessageTruncatesAndFails(t *testing.T) {
	if _, err := CommitMessage(context.Background(), "bogus", "", "stat", strings.Repeat("x", 20000), ""); err == nil {
		t.Error("expected error for unsupported provider")
	}
	msg, err := CommitMessage(context.Background(), "memory", "", "stat", "patch", "")
	if err != nil || msg == "" {
		t.Errorf("msg=%q err=%v", msg, err)
	}
}

func TestCleanTitleUnchanged(t *testing.T) {
	if got := CleanTitle("标题：「修复登录」。"); got == "" {
		// 保底：现有行为对常规标题返回非空
		t.Error("CleanTitle regressed")
	}
	if got := CleanTitle("Sorry, I cannot"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestGenerateOpenAICompatible(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.Error(w, "bad", http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  hello  "}}]}`))
	}))
	defer srv.Close()

	out, err := GenerateOpenAICompatible(context.Background(), srv.URL+"/v1/", "k", "m", "hi")
	if err != nil || out != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if gotAuth != "Bearer k" || gotBody["stream"] != false || gotBody["model"] != "m" {
		t.Errorf("auth=%q body=%v", gotAuth, gotBody)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Bearer secretsecret123", http.StatusUnauthorized)
	}))
	defer bad.Close()
	_, err = GenerateOpenAICompatible(context.Background(), bad.URL, "k", "m", "hi")
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secretsecret123") {
		t.Errorf("err=%v", err)
	}
}

func TestParseMemoryCondense(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantTitle   string
		wantContent string
	}{
		{
			name:        "带 TITLE 行",
			input:       "TITLE: 记忆精简\n\n结论放第一句。",
			wantTitle:   "记忆精简",
			wantContent: "结论放第一句。",
		},
		{
			name:        "无 TITLE 行",
			input:       "只有正文\n第二行",
			wantTitle:   "",
			wantContent: "只有正文\n第二行",
		},
		{
			name:        "带代码围栏",
			input:       "```markdown\nTITLE: 精简结果\n\n正文内容\n```",
			wantTitle:   "精简结果",
			wantContent: "正文内容",
		},
		{
			name:        "全角冒号",
			input:       "title：全角标题\n正文",
			wantTitle:   "全角标题",
			wantContent: "正文",
		},
		{
			name:        "空输出",
			input:       "   \n\n",
			wantTitle:   "",
			wantContent: "",
		},
	}

	for _, tc := range cases {
		title, content := ParseMemoryCondense(tc.input)
		if title != tc.wantTitle || content != tc.wantContent {
			t.Errorf("%s: ParseMemoryCondense(%q) = (%q, %q), want (%q, %q)",
				tc.name, tc.input, title, content, tc.wantTitle, tc.wantContent)
		}
	}
}

func TestMemoryCondensePrompt(t *testing.T) {
	solo := MemoryCondensePrompt("旧标题", "旧正文", "")
	if !strings.Contains(solo, "旧标题") || !strings.Contains(solo, "旧正文") {
		t.Errorf("prompt missing memory: %s", solo)
	}
	if strings.Contains(solo, "Existing memory") {
		t.Errorf("prompt should not mention merging: %s", solo)
	}

	merged := MemoryCondensePrompt("新标题", "新正文", "已有标题\n已有正文")
	for _, want := range []string{"新标题", "新正文", "已有标题", "已有正文", "Existing memory"} {
		if !strings.Contains(merged, want) {
			t.Errorf("merged prompt missing %q: %s", want, merged)
		}
	}
	if !strings.Contains(merged, "TITLE: <title>") {
		t.Errorf("prompt missing output format: %s", merged)
	}
}
