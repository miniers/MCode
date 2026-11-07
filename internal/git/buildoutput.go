package git

import "strings"

// buildOutputNames 是构建输出与工具缓存的名称黑名单。
// 只收录能明确判定为缓存产物的名字：名为 build 或 dist 的源码目录仍属于项目自身内容，不做过滤。
var buildOutputNames = map[string]struct{}{
	"-Xcc":          {},
	"DerivedData":   {},
	".build":        {},
	"node_modules":  {},
	"__pycache__":   {},
	".pytest_cache": {},
	".mypy_cache":   {},
	".ruff_cache":   {},
	"xcuserdata":    {},
	".swiftpm":      {},
	"ModuleCache":   {},
	".gradle":       {},
	".turbo":        {},
	".parcel-cache": {},
	".commandcode":  {},
}

// IsBuildOutput 判断仓库相对路径是否属于构建产物或工具缓存。
// 路径按 "/" 拆分为组件后逐个匹配：组件为 ".DS_Store"、以 ".noindex" 结尾，
// 或命中 buildOutputNames 时返回 true。匹配的是完整路径组件，
// 因此 "src/build.go"、"docs/turbo.md" 这类正常文件不会被误判。
func IsBuildOutput(path string) bool {
	for _, component := range strings.Split(path, "/") {
		if component == "" {
			continue
		}
		if component == ".DS_Store" || strings.HasSuffix(component, ".noindex") {
			return true
		}
		if _, ok := buildOutputNames[component]; ok {
			return true
		}
	}
	return false
}

// FilterBuildOutputs 返回 paths 中所有非构建产物的路径，保持原有顺序。
// 无论输入是否为空，返回值始终为非 nil 切片。
func FilterBuildOutputs(paths []string) []string {
	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		if !IsBuildOutput(path) {
			filtered = append(filtered, path)
		}
	}
	return filtered
}
