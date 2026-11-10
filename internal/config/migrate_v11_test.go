package config

import (
	"reflect"
	"testing"
)

func TestMigrateV10ToV11DeepWritePaths(t *testing.T) {
	tests := []struct {
		name        string
		paths       any
		defaultTier string
		wantPaths   any
		wantDefault string
		deleted     bool
	}{
		{"默认白名单与 deep 默认档", []any{"docs/superpowers/plans/**/*.md"}, " Deep ", nil, "edit", true},
		{"默认白名单与 chore 默认档", []any{"docs/superpowers/plans/**/*.md"}, "chore", nil, "chore", true},
		{"自定义白名单", []any{"custom/**"}, "deep", []any{"custom/**"}, "deep", false},
		{"空白名单", []any{}, "deep", []any{}, "deep", false},
		{"非法路径类型", []any{map[string]any{}}, "deep", []any{map[string]any{}}, "deep", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deep := map[string]any{"id": "deep", "write_paths": tt.paths}
			other := map[string]any{"id": "edit", "write_paths": []any{"docs/superpowers/plans/**/*.md"}}
			section := map[string]any{"tiers": []any{deep, other, "invalid"}, "default_tier": tt.defaultTier}
			_, err := migrateV10ToV11(map[string]any{"orchestration": section})
			if err != nil {
				t.Fatal(err)
			}
			paths, exists := deep["write_paths"]
			if exists == tt.deleted || !reflect.DeepEqual(paths, tt.wantPaths) {
				t.Errorf("write_paths = %v（存在：%v），期望 %v（删除：%v）", paths, exists, tt.wantPaths, tt.deleted)
			}
			if section["default_tier"] != tt.wantDefault {
				t.Errorf("default_tier = %v，期望 %s", section["default_tier"], tt.wantDefault)
			}
			if !reflect.DeepEqual(other["write_paths"], []any{"docs/superpowers/plans/**/*.md"}) {
				t.Error("其他档位的白名单被修改")
			}
		})
	}
}

func TestMigrateV10ToV11InvalidSections(t *testing.T) {
	for _, raw := range []map[string]any{
		{"version": 10},
		{"orchestration": "invalid"},
		{"orchestration": map[string]any{}},
		{"orchestration": map[string]any{"tiers": "invalid"}},
	} {
		before := make(map[string]any, len(raw))
		for k, v := range raw {
			before[k] = v
		}
		out, err := migrateV10ToV11(raw)
		if err != nil || !reflect.DeepEqual(out, before) {
			t.Errorf("异常输入应原样返回：%v，错误：%v", out, err)
		}
	}
}
