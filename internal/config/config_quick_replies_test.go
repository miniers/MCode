package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// 快捷回复配置的序列化与反序列化：顺序、字段名（label/message）、空列表省略都要对得上。
func TestQuickReplies_RoundTripThroughConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := t.TempDir()

	cfg := Defaults()
	cfg.QuickReplies = []QuickReply{
		{Label: "总结改动", Message: "把这次改动总结成三条要点。"},
		{Label: "写测试", Message: "给这段代码补单元测试，覆盖边界情况。"},
	}

	if err := Save(dataDir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load(dataDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded.QuickReplies) != 2 {
		t.Fatalf("loaded quick replies = %d, want 2: %+v", len(loaded.QuickReplies), loaded.QuickReplies)
	}
	if loaded.QuickReplies[0].Label != "总结改动" || loaded.QuickReplies[0].Message != "把这次改动总结成三条要点。" {
		t.Errorf("first quick reply mismatch: %+v", loaded.QuickReplies[0])
	}
	if loaded.QuickReplies[1].Label != "写测试" {
		t.Errorf("quick reply order mismatch: %+v", loaded.QuickReplies)
	}

	raw, err := json.Marshal(loaded.QuickReplies[0])
	if err != nil {
		t.Fatalf("marshal quick reply failed: %v", err)
	}
	for _, field := range []string{`"label"`, `"message"`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("quick reply JSON %s missing field %s", raw, field)
		}
	}
}

// 空列表不写进 config.json：没有快捷回复时文件里不该多一段空数组。
func TestQuickReplies_EmptyListOmitted(t *testing.T) {
	cfg := Defaults()
	cfg.QuickReplies = nil
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config failed: %v", err)
	}
	if strings.Contains(string(raw), "quick_replies") {
		t.Errorf("empty quick replies should be omitted, got %s", raw)
	}
}

// 校验文案：空标题、空内容、超长都要拒绝，合法条目放行。
func TestValidateQuickReplies(t *testing.T) {
	cases := []struct {
		name    string
		item    QuickReply
		wantErr string
	}{
		{name: "ok", item: QuickReply{Label: "总结", Message: "总结一下"}},
		{name: "empty label", item: QuickReply{Label: "  ", Message: "有内容"}, wantErr: "标题不能为空"},
		{name: "empty message", item: QuickReply{Label: "有标题", Message: "\n\t"}, wantErr: "内容不能为空"},
		{name: "label too long", item: QuickReply{Label: strings.Repeat("标", MaxQuickReplyLabelRunes+1), Message: "有内容"}, wantErr: "标题不能超过"},
		{name: "message too long", item: QuickReply{Label: "标题", Message: strings.Repeat("字", MaxQuickReplyMessageRunes+1)}, wantErr: "内容不能超过"},
	}
	for _, tc := range cases {
		err := ValidateQuickReply(tc.item)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want contains %q", tc.name, err, tc.wantErr)
		}
	}

	errs, first := ValidateQuickReplies([]QuickReply{
		{Label: "正常", Message: "正常"},
		{Label: "", Message: ""},
	})
	if len(errs) != 2 || errs[0] != "" || errs[1] != "标题不能为空" {
		t.Errorf("per-row errors mismatch: %+v", errs)
	}
	if !strings.Contains(first, "第 2 条") {
		t.Errorf("first error should name the row, got %q", first)
	}

	if errs, first := ValidateQuickReplies(nil); len(errs) != 0 || first != "" {
		t.Errorf("empty list should be valid, got %+v / %q", errs, first)
	}
}

// 归一：去首尾空白，丢掉完全空白的行，全空时回到 nil。
func TestNormalizeQuickReplies(t *testing.T) {
	got := NormalizeQuickReplies([]QuickReply{
		{Label: "  总结  ", Message: "\n把改动说清楚。\n"},
		{Label: "   ", Message: " "},
	})
	if len(got) != 1 {
		t.Fatalf("normalized = %+v, want 1 item", got)
	}
	if got[0].Label != "总结" || got[0].Message != "把改动说清楚。" {
		t.Errorf("trim mismatch: %+v", got[0])
	}
	if NormalizeQuickReplies([]QuickReply{{Label: " ", Message: ""}}) != nil {
		t.Errorf("all-blank list should normalize to nil")
	}
}
