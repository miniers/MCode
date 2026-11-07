// Package reviewcheck 提供 swarm 合并审查漏斗 L0 层的确定性检查：
// 变更分类、brief 范围判定、受影响的检查命令和项目级命令配置。
//
// 本包只做 0 token 的本地判断，不依赖任何模型，也不导入 internal/swarm 或
// internal/reviewfunnel，便于被漏斗的任意阶段单独调用和测试。
package reviewcheck

import (
	"path"
	"strings"
)

// Class 是变更文件的粗分类，决定 L0 范围越界是软越界还是硬越界。
type Class string

const (
	// ClassCode 表示普通源码或配置文件，越界按硬越界处理。
	ClassCode Class = "code"
	// ClassTest 表示测试文件，越界按软越界处理。
	ClassTest Class = "test"
	// ClassDoc 表示普通文档，越界按软越界处理。
	ClassDoc Class = "doc"
	// ClassPlan 表示 docs/superpowers/plans/ 下的计划文档，永远不是驳回理由。
	ClassPlan Class = "plan"
	// ClassOther 表示空路径等无法分类的输入。
	ClassOther Class = "other"
)

// planDocPrefix 是计划文档的固定目录前缀。
const planDocPrefix = "docs/superpowers/plans/"

// testFileSuffixes 列出按后缀判定的测试文件名。
var testFileSuffixes = []string{
	"_test.go",
	".test.ts",
	".test.tsx",
	".spec.ts",
	".spec.tsx",
}

// docFileSuffixes 列出按后缀判定的文档文件名。
var docFileSuffixes = []string{
	".md",
	".mdx",
	".markdown",
	".txt",
}

// sensitiveFileNames 列出即使只改一行也必须送 L2 的依赖与锁文件。
var sensitiveFileNames = map[string]struct{}{
	"go.mod":            {},
	"go.sum":            {},
	"package.json":      {},
	"package-lock.json": {},
	"pnpm-lock.yaml":    {},
	"yarn.lock":         {},
	"cargo.lock":        {},
	"gemfile.lock":      {},
	"poetry.lock":       {},
}

// Classify 返回路径对应的分类；计划文档的判定先于普通文档。
func Classify(p string) Class {
	p = normalizeSlashes(p)
	if p == "" {
		return ClassOther
	}
	if IsPlanDoc(p) {
		return ClassPlan
	}
	if IsTestFile(p) {
		return ClassTest
	}
	if isDocFile(p) {
		return ClassDoc
	}
	return ClassCode
}

// IsTestFile 判断路径是否是测试文件：_test.go、.test.ts(x)、.spec.ts(x)、
// 位于 __tests__/ 目录下，或位于 testdata/ 目录下。
func IsTestFile(p string) bool {
	p = strings.ToLower(normalizeSlashes(p))
	if p == "" {
		return false
	}
	for _, suffix := range testFileSuffixes {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "__tests__" || segment == "testdata" {
			return true
		}
	}
	return false
}

// IsPlanDoc 判断路径是否是 docs/superpowers/plans/ 下的 Markdown 计划文档。
func IsPlanDoc(p string) bool {
	p = strings.ToLower(normalizeSlashes(p))
	return strings.HasPrefix(p, planDocPrefix) && strings.HasSuffix(p, ".md")
}

// IsSensitive 判断路径是否是需要强制送 L2 的敏感文件：依赖清单、锁文件，
// 或 .github/ 下的工作流与配置。本函数只做判断，不决定审查流程。
func IsSensitive(p string) bool {
	p = normalizeSlashes(p)
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, ".github/") {
		return true
	}
	_, ok := sensitiveFileNames[strings.ToLower(path.Base(p))]
	return ok
}

// isDocFile 按扩展名判断是否为文档。
func isDocFile(p string) bool {
	lower := strings.ToLower(p)
	for _, suffix := range docFileSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// normalizeSlashes 统一路径分隔符并去掉开头的 ./。
func normalizeSlashes(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	return p
}
