package main

import (
	"reflect"
	"testing"
	"time"

	"mcode/internal/app"
	mcodeui "mcode/internal/ui"
)

func TestToUITrayQuotaGroups(t *testing.T) {
	t.Run("nil 输入返回空结果", func(t *testing.T) {
		got := toUITrayQuotaGroups(nil)
		if len(got) != 0 {
			t.Fatalf("expected empty result, got %#v", got)
		}
	})

	t.Run("空切片输入返回空结果", func(t *testing.T) {
		got := toUITrayQuotaGroups([]app.TrayQuotaGroup{})
		if len(got) != 0 {
			t.Fatalf("expected empty result, got %#v", got)
		}
	})

	t.Run("字段转换与 Err 原样保留", func(t *testing.T) {
		now := time.Now()
		input := []app.TrayQuotaGroup{
			{
				Provider: "codex",
				Title:    "Codex",
				Windows: []mcodeui.ProviderQuotaWindow{
					{
						ID:          "window-1",
						Period:      "5h",
						Label:       "Codex 5h",
						UsedPercent: 42.5,
						ResetsAt:    now,
						Account:     "user@example.com",
						Active:      true,
						Disabled:    false,
						Source:      "local",
						Plan:        "pro",
						ReadAt:      now,
					},
				},
				Err: "",
			},
			{
				Provider: "magpie",
				Title:    "Magpie",
				Windows:  nil,
				Err:      "connection timed out",
			},
		}

		want := []mcodeui.TrayQuotaGroup{
			{
				Provider: "codex",
				Title:    "Codex",
				Windows: []mcodeui.ProviderQuotaWindow{
					{
						ID:          "window-1",
						Period:      "5h",
						Label:       "Codex 5h",
						UsedPercent: 42.5,
						ResetsAt:    now,
						Account:     "user@example.com",
						Active:      true,
						Disabled:    false,
						Source:      "local",
						Plan:        "pro",
						ReadAt:      now,
					},
				},
				Err: "",
			},
			{
				Provider: "magpie",
				Title:    "Magpie",
				Windows:  nil,
				Err:      "connection timed out",
			},
		}

		got := toUITrayQuotaGroups(input)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mismatch:\ngot:  %#v\nwant: %#v", got, want)
		}
	})
}
