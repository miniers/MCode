package git

import (
	"reflect"
	"testing"
)

func TestIsBuildOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"顶层 node_modules 目录", "node_modules/react/index.js", true},
		{"嵌套 node_modules 目录", "app/node_modules/x.js", true},
		{"顶层 .DS_Store 文件", ".DS_Store", true},
		{"嵌套 .DS_Store 文件", "a/.DS_Store", true},
		{".noindex 后缀目录", "proj/Build.noindex/obj", true},
		{"Xcode 编译缓存 -Xcc", "proj/-Xcc/record", true},
		{"DerivedData 缓存", "ios/DerivedData/x", true},
		{"Python 缓存目录", "pkg/__pycache__/m.pyc", true},
		{"Gradle 缓存目录", "sub/.gradle/cache", true},
		{"Rust 工具缓存", "sub/.ruff_cache/entry", true},
		{"Swift 包缓存", ".swiftpm/x", true},
		{"工具状态目录 .commandcode", "a/b/.commandcode/taste", true},
		{"正常源码文件 build.go", "src/build.go", false},
		{"正常文档 turbo.md", "docs/turbo.md", false},
		{"node_modules 前缀但非同名组件", "app/node_modules_backup/x.js", false},
		{".DS_Store 后缀但非完整组件", "a/My.DS_Storex", false},
		{"空路径", "", false},
		{"普通嵌套文件", "internal/git/buildoutput.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBuildOutput(tt.path); got != tt.want {
				t.Fatalf("IsBuildOutput(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestFilterBuildOutputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{"nil 输入返回非 nil 空切片", nil, []string{}},
		{"空输入返回非 nil 空切片", []string{}, []string{}},
		{
			"过滤构建产物并保持原有顺序",
			[]string{"src/build.go", "app/node_modules/x.js", "docs/turbo.md", "a/.DS_Store", "README.md"},
			[]string{"src/build.go", "docs/turbo.md", "README.md"},
		},
		{
			"全部为构建产物时返回空切片",
			[]string{"node_modules/a", "a/.DS_Store", "sub/.turbo/x"},
			[]string{},
		},
		{
			"无构建产物时原样返回",
			[]string{"a.txt", "internal/git/buildoutput.go"},
			[]string{"a.txt", "internal/git/buildoutput.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterBuildOutputs(tt.paths)
			if got == nil {
				t.Fatalf("FilterBuildOutputs(%v) 返回 nil，期望非 nil 切片", tt.paths)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FilterBuildOutputs(%v) = %v, want %v", tt.paths, got, tt.want)
			}
		})
	}
}
