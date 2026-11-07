// Package reviewfunnel 实现 swarm 合并审查漏斗的纯逻辑部分：
// L1 零 token 风险分级、L2 单次模型调用的提示词构造与结果解析，
// 以及返工增量审查的提示词片段。
//
// 本包只依赖 internal/reviewcheck，不导入 internal/swarm 或 internal/git；
// 所有函数都不做 I/O，便于被漏斗 worker 调用并单独测试。
package reviewfunnel

import (
	"fmt"
	"strings"

	"mcode/internal/reviewcheck"
)

// Change 是 L1 分级使用的一条改动文件记录。
type Change struct {
	// Path 是 repo 相对路径，用于敏感文件判定。
	Path string
	// Class 是 reviewcheck 给出的文件分类；为空或 other 时 DecideL1 会按 Path 重新分类。
	Class reviewcheck.Class
}

// L1Input 是 L1 风险分级的输入。
type L1Input struct {
	// Changes 是本次补丁改动的全部文件。
	Changes []Change
	// Added 是新增行数。
	Added int
	// Deleted 是删除行数。
	Deleted int
	// Scope 是 brief 与改动文件的范围比对结果。
	Scope reviewcheck.Scope
	// MaxLines 是自动通过的行数上限；小于等于 0 表示关闭按行数自动通过。
	MaxLines int
}

// Decision 是一条审查结论，L1 自动通过和 L2 解析结果共用。
type Decision struct {
	// Verdict 是结论，取 VerdictApprove、VerdictReject 或 VerdictEscalate。
	Verdict string
	// Reason 是一句话结论说明。
	Reason string
	// Concerns 是具体疑点，建议以 path:line 开头。
	Concerns []string
}

// 审查结论的合法取值。
const (
	// VerdictApprove 表示可以通过并合入。
	VerdictApprove = "approve"
	// VerdictReject 表示驳回，必须带具体理由和 path:line 疑点。
	VerdictReject = "reject"
	// VerdictEscalate 表示无法判断，升级到更重的审查层。
	VerdictEscalate = "escalate"
)

// L1 自动通过时使用的固定短句，便于统计。
const (
	reasonL1PlanOnly   = "L1：仅计划文档"
	reasonL1DocsOnly   = "L1：仅文档"
	reasonL1TestsOnly  = "L1：仅测试"
	reasonL1DocsTests  = "L1：文档与测试"
	reasonL1SmallLines = "L1：小改动（+%d/-%d 行，无越界）"
)

// DecideL1 对改动做零 token 风险分级。
//
// 返回 true 表示 L1 直接 approve，第二个返回值忽略；返回 false 表示需要送 L2，
// 此时 Decision 仅作为提示，Verdict 固定为 escalate，调用方不应直接应用。
//
// 规则优先级：
//  1. 没有改动文件时不自动通过（空改动交给 L2）。
//  2. 只要有一个敏感文件（依赖、锁文件、CI 配置），就不自动通过；
//     唯一例外是全部改动都是计划文档。
//  3. 全部是计划文档、全部是文档、全部是测试，或文档与测试混合时自动通过。
//  4. Scope.Known 为 true、没有硬越界、MaxLines > 0 且增删行数不超过
//     MaxLines 时自动通过；MaxLines <= 0 关闭这条规则。
func DecideL1(in L1Input) (Decision, bool) {
	if len(in.Changes) == 0 {
		return Decision{
			Verdict:  VerdictEscalate,
			Reason:   "L1：没有可审查的改动，交给 L2 复核",
			Concerns: []string{"空改动：补丁没有列出任何文件"},
		}, false
	}

	allPlan := true
	allDocOrPlan := true
	allTest := true
	allPlanDocTest := true
	hasSensitive := false
	var sensitiveFile string
	for _, change := range in.Changes {
		class := change.Class
		if class == "" || class == reviewcheck.ClassOther {
			if change.Path != "" {
				class = reviewcheck.Classify(change.Path)
			}
		}
		switch class {
		case reviewcheck.ClassPlan:
			// 计划文档同时属于文档类；它不是测试，因此清掉“全部是测试”。
			allTest = false
		case reviewcheck.ClassDoc:
			allPlan = false
			allTest = false
		case reviewcheck.ClassTest:
			allPlan = false
			allDocOrPlan = false
		default:
			allPlan = false
			allDocOrPlan = false
			allTest = false
			allPlanDocTest = false
		}
		if change.Path != "" && reviewcheck.IsSensitive(change.Path) && !hasSensitive {
			hasSensitive = true
			sensitiveFile = change.Path
		}
	}

	softConcerns := scopeConcerns(in.Scope, false)

	if hasSensitive && !allPlan {
		concerns := append(softConcerns, "敏感文件："+sensitiveFile+"，依赖、锁文件或 CI 配置必须由模型复核")
		return Decision{
			Verdict:  VerdictEscalate,
			Reason:   "L1：改动包含敏感文件，强制进入 L2 审查",
			Concerns: concerns,
		}, false
	}

	if allPlan {
		return Decision{
			Verdict:  VerdictApprove,
			Reason:   reasonL1PlanOnly,
			Concerns: softConcerns,
		}, true
	}
	if allDocOrPlan {
		return Decision{
			Verdict:  VerdictApprove,
			Reason:   reasonL1DocsOnly,
			Concerns: softConcerns,
		}, true
	}
	if allTest {
		return Decision{
			Verdict:  VerdictApprove,
			Reason:   reasonL1TestsOnly,
			Concerns: softConcerns,
		}, true
	}
	if allPlanDocTest {
		return Decision{
			Verdict:  VerdictApprove,
			Reason:   reasonL1DocsTests,
			Concerns: softConcerns,
		}, true
	}

	concerns := softConcerns
	if len(in.Scope.Hard) > 0 {
		concerns = append(concerns, scopeConcerns(in.Scope, true)...)
	}

	switch {
	case in.MaxLines <= 0:
		return Decision{
			Verdict:  VerdictEscalate,
			Reason:   "L1：按行数自动通过已关闭，交给 L2 审查",
			Concerns: concerns,
		}, false
	case !in.Scope.Known:
		return Decision{
			Verdict:  VerdictEscalate,
			Reason:   "L1：brief 没有给出可核对的范围，交给 L2 审查",
			Concerns: concerns,
		}, false
	case len(in.Scope.Hard) > 0:
		return Decision{
			Verdict:  VerdictEscalate,
			Reason:   "L1：存在硬越界文件，交给 L2 审查",
			Concerns: concerns,
		}, false
	}

	if in.Added+in.Deleted > in.MaxLines {
		return Decision{
			Verdict:  VerdictEscalate,
			Reason:   fmt.Sprintf("L1：改动 %d 行超过阈值 %d 行，交给 L2 审查", in.Added+in.Deleted, in.MaxLines),
			Concerns: concerns,
		}, false
	}

	return Decision{
		Verdict:  VerdictApprove,
		Reason:   fmt.Sprintf(reasonL1SmallLines, in.Added, in.Deleted),
		Concerns: softConcerns,
	}, true
}

// scopeConcerns 把范围比对结果转成疑点文本；includeHard 为 true 时也包含硬越界。
func scopeConcerns(scope reviewcheck.Scope, includeHard bool) []string {
	concerns := make([]string, 0, len(scope.Soft)+len(scope.Hard))
	for _, p := range scope.Soft {
		if p = strings.TrimSpace(p); p != "" {
			concerns = append(concerns, "软越界："+p)
		}
	}
	if includeHard {
		for _, p := range scope.Hard {
			if p = strings.TrimSpace(p); p != "" {
				concerns = append(concerns, "硬越界："+p)
			}
		}
	}
	return concerns
}
