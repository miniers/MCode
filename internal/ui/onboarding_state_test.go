package mcodeui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
)

// TestPrepareOnboarding 验证新手引导的对应行为。
func TestPrepareOnboarding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		file    string
		content string
		want    bool
		wantErr bool
	}{
		{name: "全新安装", want: true},
		{name: "已有配置", file: "config", content: "{}"},
		{name: "已有数据库", file: "mcode.db", content: "database"},
		{name: "已有界面状态", file: uiStateFileName, content: `{ "sidebar_hidden": true }`},
		{name: "尚未完成", file: uiStateFileName, content: `{ "onboarding": "pending" }`, want: true},
		{name: "已经完成", file: uiStateFileName, content: `{ "onboarding": "completed" }`},
		{name: "损坏的状态", file: uiStateFileName, content: `{`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() { SetUIStateDataDir("") })
			path := filepath.Join(dir, tc.file)
			if tc.file == "config" {
				path = config.ConfigPath(dir)
			}
			if tc.file != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := PrepareOnboarding(dir)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("PrepareOnboarding() = %v, %v; want %v, error %v", got, err, tc.want, tc.wantErr)
			}
			statePath := filepath.Join(dir, uiStateFileName)
			if tc.file == "" {
				data, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]string
				if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 1 || fields["onboarding"] != onboardingPending {
					t.Fatalf("初始状态 = %s, %v", data, err)
				}
				if again, err := PrepareOnboarding(dir); err != nil || !again {
					t.Fatalf("重启后 = %v, %v", again, err)
				}
			} else {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != tc.content {
					t.Fatalf("原文件发生变化: %q, %v", data, err)
				}
				if tc.file != uiStateFileName {
					if _, err := os.Stat(statePath); !os.IsNotExist(err) {
						t.Fatalf("不应创建状态文件: %v", err)
					}
				}
			}
		})
	}
}

// TestCompleteOnboarding 验证新手引导的对应行为。
func TestCompleteOnboarding(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { SetUIStateDataDir("") })
	SetUIStateDataDir(dir)
	path := filepath.Join(dir, uiStateFileName)
	original := `{"onboarding":"pending","continue_dismissed":{"thread":"message"},"unknown":{"keep":true}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CompleteOnboarding(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var dismissed map[string]string
	var unknown map[string]bool
	if err := json.Unmarshal(fields["continue_dismissed"], &dismissed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fields["unknown"], &unknown); err != nil {
		t.Fatal(err)
	}
	if string(fields["onboarding"]) != `"completed"` || dismissed["thread"] != "message" || !unknown["keep"] {
		t.Fatalf("完成后的状态不正确: %s", data)
	}
	if autoOpen, err := PrepareOnboarding(dir); err != nil || autoOpen {
		t.Fatalf("完成后 = %v, %v", autoOpen, err)
	}
}

// TestOnboardingAutoOpenOnce 验证新手引导的对应行为。
func TestOnboardingAutoOpenOnce(t *testing.T) {
	s := &chatState{}
	WithOnboarding(true)(s)
	if !takeOnboardingAutoOpen(s) || takeOnboardingAutoOpen(s) {
		t.Fatal("自动打开状态应当只消费一次")
	}
}
