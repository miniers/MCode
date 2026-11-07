package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"mcode/internal/harness"
)

// EvaluationResult 表示对 stopWhen 停止条件的评估结果。
type EvaluationResult struct {
	Satisfied  bool    `json:"satisfied"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// NoCodeChangesMarker 是工作目录没有任何未提交变动时 gitDiffSummary 返回的显式标记。
const NoCodeChangesMarker = "(no code changes)"

// heuristicRule 是一条启发式判定规则：停止条件命中 conditionWords 之一、且本轮结果命中
// 某条 evidence 时判为满足。conditionWords 为空表示不看停止条件，只看运行摘要。
type heuristicRule struct {
	conditionWords []string
	evidence       func(summary, diff string) bool
	confidence     float64
	reason         string
}

// 启发式判定的结论说明。
const (
	reasonChecksPassed   = "运行摘要显示测试与检查全部通过。"
	reasonNoneRemaining  = "运行摘要显示没有剩余待处理项。"
	reasonCleanWorkspace = "工作区干净，没有需要改动的内容。"
	reasonAgentDone      = "Agent 在运行摘要里声明目标已完成。"
	reasonNotShown       = "本轮结果里找不到停止条件已满足的明确证据。"
)

func containsAny(text string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// heuristicRules 按顺序尝试，第一条命中的规则决定结论。
var heuristicRules = []heuristicRule{
	{
		conditionWords: []string{"no failing checks", "all tests pass"},
		evidence: func(summary, _ string) bool {
			return containsAny(summary, "tests passed", "0 failed", "all checks passed")
		},
		confidence: 0.95,
		reason:     reasonChecksPassed,
	},
	{
		conditionWords: []string{"no remaining", "zero remaining"},
		evidence: func(summary, _ string) bool {
			return containsAny(summary, "0 remaining", "no remaining items")
		},
		confidence: 0.9,
		reason:     reasonNoneRemaining,
	},
	{
		conditionWords: []string{"clean", "nothing to commit", "no code change", "no uncommitted"},
		evidence: func(summary, diff string) bool {
			// 空字符串表示 git 读取失败或没有数据，不能当作工作区干净的证据；
			// 只认显式无改动标记和运行摘要里的短语。
			return strings.Contains(diff, NoCodeChangesMarker) || containsAny(summary, "working tree clean", "nothing to do")
		},
		confidence: 0.85,
		reason:     reasonCleanWorkspace,
	},
	{
		evidence: func(summary, _ string) bool {
			return containsAny(summary, "objective completed", "task finished successfully")
		},
		confidence: 0.82,
		reason:     reasonAgentDone,
	},
}

// Heuristic 在没有可用评估模型时，按关键字规则粗略判断停止条件是否满足（不区分大小写）。
func Heuristic(stopWhen, runSummary, diffSummary string) EvaluationResult {
	condition := strings.ToLower(strings.TrimSpace(stopWhen))
	summary := strings.ToLower(runSummary)
	diff := strings.ToLower(diffSummary)
	for _, rule := range heuristicRules {
		if len(rule.conditionWords) > 0 && !containsAny(condition, rule.conditionWords...) {
			continue
		}
		if rule.evidence(summary, diff) {
			return EvaluationResult{Satisfied: true, Confidence: rule.confidence, Reason: rule.reason}
		}
	}
	return EvaluationResult{Satisfied: false, Confidence: 0.2, Reason: reasonNotShown}
}

// PromptForEvaluation 生成用于大模型评估停止条件是否满足的 Prompt。
func PromptForEvaluation(stopWhen, runSummary, diffSummary string) string {
	diff := diffSummary
	if diff == "" {
		diff = "(none)"
	}
	return fmt.Sprintf(`An automation in mcode keeps running an agent until a stop condition holds. Judge, from the evidence below only, whether this run has met that condition.

## Stop condition
%s

## What the agent reported
%s

## Changes in the working directory
%s

Be strict: count the condition as met only when the evidence clearly shows it, not when the agent merely claims progress.
Answer with a single JSON object and nothing else:
{"satisfied": <true|false>, "confidence": <number from 0 to 1>, "reason": "<one short sentence>"}`, stopWhen, runSummary, diff)
}

// ParseEvaluation 解析大模型返回的文本，截取第一个 '{' 到最后一个 '}' 间的 JSON 结构。
func ParseEvaluation(text string) (EvaluationResult, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end < start {
		return EvaluationResult{}, false
	}
	sub := text[start : end+1]

	var raw struct {
		Satisfied  *bool   `json:"satisfied"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(sub), &raw); err != nil || raw.Satisfied == nil {
		return EvaluationResult{}, false
	}

	conf := raw.Confidence
	if conf < 0 {
		conf = 0
	} else if conf > 1 {
		conf = 1
	}

	return EvaluationResult{
		Satisfied:  *raw.Satisfied,
		Confidence: conf,
		Reason:     raw.Reason,
	}, true
}

type denyApprover struct{}

func (denyApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	return harness.DecisionDeny, nil
}

// Evaluator 提供基于 Harness 的停止条件评估器。
type Evaluator struct {
	Harness harness.Harness
	Model   string
	Timeout time.Duration
}

// Evaluate 评估停止条件，返回结果和 met (satisfied && confidence >= threshold)。
// 未配置 Harness 时（评估器不可用）才回退启发式；Harness 报错或输出不可解析时
// 一律返回「未验证」，不能把「评估失败」当成「条件满足」。
func (e *Evaluator) Evaluate(ctx context.Context, workDir, stopWhen string, threshold float64, runSummary, diffSummary string) (EvaluationResult, bool) {
	if e == nil || e.Harness == nil {
		res := Heuristic(stopWhen, runSummary, diffSummary)
		return res, res.Satisfied && res.Confidence >= threshold
	}

	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	evalCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	turn := harness.Turn{
		ID:       uuid.NewString(),
		Prompt:   PromptForEvaluation(stopWhen, runSummary, diffSummary),
		WorkDir:  workDir,
		Model:    e.Model,
		Mode:     harness.ModeReadOnly,
		Approver: denyApprover{},
		// 评估用的一次性运行同样要在非交互环境下进行，避免命令卡在 stdin 或分页器上。
		Env: harness.NonInteractiveEnv(),
	}

	var buf strings.Builder
	err := e.Harness.Run(evalCtx, turn, func(ev harness.Event) {
		switch ev.Type {
		case harness.EventText:
			buf.WriteString(ev.Text)
		case harness.EventTextRetract:
			// 流中断后被撤回的残文不参与评估，否则会和重写的回复拼在一起。
			if rest, ok := strings.CutSuffix(buf.String(), ev.Text); ok {
				buf.Reset()
				buf.WriteString(rest)
			}
		}
	})
	if err != nil {
		// 评估本身失败时不能回退启发式：命中关键词就把「未验证」判成「已验证」，
		// 会直接停用自动化。
		return EvaluationResult{
			Satisfied:  false,
			Confidence: 0,
			Reason:     fmt.Sprintf("评估失败，停止条件未验证: %v", err),
		}, false
	}

	res, ok := ParseEvaluation(buf.String())
	if !ok {
		return EvaluationResult{
			Satisfied:  false,
			Confidence: 0,
			Reason:     "评估失败，停止条件未验证: 评估输出无法解析",
		}, false
	}
	return res, res.Satisfied && res.Confidence >= threshold
}
