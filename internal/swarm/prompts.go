package swarm

import (
	"fmt"
	"strings"
	"time"
)

// autoMergeRule 声明子代理汇报后改动会自动合入工作区，主代理不需要手动执行 git 命令。
const autoMergeRule = `Merging is automatic: as soon as a bee reports, mcode merges its changes into the checkout. Do not run git merge, git cherry-pick, git commit or any other git command to bring a bee's work in, do not ask the user to do it, and do not count merging as a step of the task.`

// reviewMergeRule 声明改动要等审查结论后才合入：审查模型或用户裁决后 mcode 才合并。
const reviewMergeRule = `Merging waits for review: when a bee finishes, its changes are reviewed (by a review model or by the user) before mcode merges them, and the bee's report reaches you only after that decision. Each bee's merge result says what happened: merged after review, review rejected with the reviewer's reason and the path of the saved patch, or discarded. Rejected or discarded changes are not in the checkout: do not build on them; send a bee to redo the work if it is still needed, pointing it at the saved patch and the reviewer's reason. Do not run git merge, git cherry-pick, git commit or any other git command to bring a bee's work in, and do not ask the user to do it.`

// reportStyleRule 约束主代理的汇报文案：每个子代理一行结果说明，不要审计腔。
const reportStyleRule = `When you report back, give each bee one line: its name, what it changed and where. No audit-style sections, no restated briefs, no scores or verdicts; beyond that line, add at most one short clause per bee, and only for a failure or an open risk.`

// 补丁另存与 swarm-answer 块两条规则不再常驻主策略，已随事件消息下发：
// 补丁处理指引随编排汇报下发，见 internal/swarm/manager.go 的 BuildReport；
// 答案块格式与 defer、skip 写法随转发提问消息下发，见 internal/swarm/answer.go 的 buildForwardQuestionMessage。
// 不要再把它们加回常驻段，事件消息里已有等价指引。

// NativeSubagentRule 是未开启编排时追加的软约束：默认禁止主线程自行调用原生子代理工具。
const NativeSubagentRule = `Native sub-agent tools (Agent, Task, spawn_agent and similar) are disabled by default in this session: do the work yourself with your own tools, and only use a native sub-agent when the user explicitly asks for one.`

// planOnlyRule 是「主导只规划」模式追加在策略末尾的规则段。
var planOnlyRule = `[Swarm · planning-only lead] You only plan. You never edit, write or create a file yourself: not a one-line change in a single file, and not to fix something a report or your own review turns up. Every change is sent to a bee.
- First reply: orient before you plan. List the directories, grep for the key symbols, and read the signatures of types and functions rather than their bodies, a few files at most. Then close that same reply with the ` + "```swarm" + ` block.
- If the scope or the code is unclear, put one to three read-only bees in that first block, each told to change nothing and to report the relevant files, symbols, signatures and call sites. Once they report, plan the edits and send the editing bees.
- Questions and explanations, which change nothing, you answer directly from what you have read; if a proper answer needs more than a quick look, send a read-only bee to dig.
- Builds, checks and reading each bee's changed files are still yours to do in the checkout after the bees return; when a check fails or the read turns up a problem, send a bee to fix it.`

// LeadPolicy 返回编排主代理会话的英文策略提示文本。
// plansOnly 为 true 时追加仅规划策略，要求主代理不得亲自修改文件，审查发现的问题也必须派子代理修复；
// 同时改写基础策略中「可独自完成单个小改动」的句子并省略「无需 block」的句子，避免与仅规划规则冲突。
// 「补丁另存为 .patch」与「swarm-answer 块」两条说明不再常驻，改为随对应事件消息下发。
// brief 的写法按工作性质区分：改代码写编号步骤、锚点与验收并要求补测试；只读调研写编号问题；
// 诊断或设计只写症状、目标、边界与方案须覆盖的内容，不替定位与设计档写步骤和锚点。
// 策略还要求主代理汇总前读一遍各 bee 改过的文件。
// reviewMode 为 ai 或 human 时，改动要过审查才合入：换掉「汇报即合入」的两句说明与自动合并规则。
func LeadPolicy(plansOnly bool, reviewMode string) string {
	aloneClause := "Only a request with one focus, one file and one obvious edit is yours to do alone; size the team by the work, not by idle bees."
	noBlockClause := " If it is yours to do alone, do it and end without a block."
	if plansOnly {
		aloneClause = "Size the team by the work, not by idle bees: a job whose parts depend on each other is one bee's task, not several."
		noBlockClause = ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, `[Swarm mode] You coordinate helper agents called bees, and handing work out is your default: anything but one obvious edit in one file goes to bees — audits, reviews, multi-file work, broad research, bundled tasks. In your first reply read only enough to brief well, then dispatch every bee in that same block, within the [Swarm limits] entry cap. %s When a bee fails, diagnose it and send a corrected brief; never take the job back. End your reply with exactly one fenced block

`+"```swarm"+`
[{"task": "short title", "prompt": "full standalone instructions naming the exact files and the acceptance criteria"}]
`+"```"+`

then stop: no waiting or polling after it.%s`, aloneClause, noBlockClause)
	b.WriteString(` An entry may name its bee, tier, skills and project: bee and tier say who runs it, skills the ones it must read first (only when essential, since every bee sees a skill index), project another project. Names come from the roster in order; a wrong or empty name becomes the next one.`)
	b.WriteString(` An entry may also carry replaces, the name of a running bee it supersedes; mcode stops that bee first. Keep the [Swarm roster] in mind: never resend a task listed there as running.`)
	b.WriteString(` Never put a fenced code block in a prompt: three backticks close the swarm block early; describe code in words or indent it four spaces. Bees start as their entry's closing brace arrives, so order entries by start time.`)
	b.WriteString(` Each bee works in its own copy and mcode merges its changes when it reports; bees share none of your context, so brief each for a colleague starting cold, in the shape its work needs. For a code change: numbered steps naming the file, the line anchor, the exact change and the acceptance criteria; the criteria name the behavior to verify, and the brief tells the bee to add or update a test that covers it. For read-only research: numbered questions. For diagnosis: the symptom, how to reproduce it and numbered questions; the bee reports the root cause with evidence, and you design the fix. Never give two bees one file, or a bee work that depends on another bee's unfinished changes. Before a brief lists the files a deletion, rename or signature change may touch, grep every reference to the symbol, tests included, and list every file that has one; when such a file belongs to a running bee, wait for its report or fold both jobs into one bee. Bees work without permission prompts: brief them to do the work, not to describe it. Bees may run scoped checks (build, vet, and tests of the packages they touched, each with a timeout via the tool's own flag such as go test -timeout, since macOS has no timeout command) and report the commands and results; they never commit or run the full suite, so run the full checks yourself. The swarm block is the only way to dispatch: never an agent or sub-agent tool of your own. Reports come back already applied: build on them, and before you report back, read the files each bee changed and send a bee to fix what you find; never send bees to check other bees' work, and never watch bees with git while they work. A message starting with [编排] (or older [Hydra]) comes from mcode, not from the user.`)

	policy := b.String() + "\n\n" + autoMergeRule + "\n" + reportStyleRule
	// off 档保持原文；ai/human 档把「汇报即合入」的说明换成「过审查才合入」。
	mode := strings.ToLower(strings.TrimSpace(reviewMode))
	if mode == reviewModeAI || mode == reviewModeHuman {
		policy = strings.ReplaceAll(policy, "Each bee works in its own copy and mcode merges its changes when it reports;", "Each bee works in its own copy and mcode merges its changes only after they pass review;")
		policy = strings.ReplaceAll(policy, "Reports come back already applied: build on them,", "Reports arrive only after review: build only on changes whose merge result says merged,")
		policy = strings.Replace(policy, autoMergeRule, reviewMergeRule, 1)
	}
	if plansOnly {
		policy += "\n\n" + planOnlyRule
	}
	if mode == reviewModeAI {
		// 漏斗已经看过完整 diff，主代理只看 merge result 里的 concerns，并自己跑构建与测试。
		policy = strings.Replace(policy,
			"read the files each bee changed and send a bee to fix what you find",
			"read only the review concerns listed in each merge result and the code they point to, run the build and tests yourself, and send a bee to fix what you find; the funnel already reviewed the rest", 1)
		if plansOnly {
			// planOnlyRule 在替换之后才追加，所以这一句要放在最后处理。
			policy = strings.Replace(policy,
				"- Builds, checks and reading each bee's changed files are still yours to do in the checkout after the bees return; when a check fails or the read turns up a problem, send a bee to fix it.",
				"- Builds and checks are still yours to do in the checkout after the bees return; when a check fails, or a review concern turns out to be real, send a bee to fix it.", 1)
		}
	}
	return policy
}

// DelegateToolRule 是开启 swarm_delegate 工具后追加在主代理策略末尾的说明：
// 工具与 swarm 块用同样的条目，二选一，不要对同一条目两边都写。
const DelegateToolRule = `[Swarm tool] The swarm_delegate tool dispatches bees with the same entries as the swarm block. Prefer it when a brief needs code fences, which the block cannot carry, and call it as soon as a brief is ready: each entry starts a bee at once and the result tells you what was started. Use the tool or the block for an entry, never both. After your last call, stop as you would after a block.`

// LeadNoticeReminder 是在子代理汇报等通知轮次注入的精简规则提醒常量：
// 还有工作就继续用 swarm 块派发并收尾，没有了就跑检查并汇报，不再强制继续派发。
const LeadNoticeReminder = `[Swarm mode] The swarm rules injected at the start of this request are identical and remain in effect. If work remains, dispatch it as before (the ` + "```swarm" + ` block ends your reply) and stop; if none remains, run your checks and report.`

// LeadNoticePolicy 返回编排主代理会话在通知轮次的精简提示文本；
// plansOnly 为 true 时追加主导只规划模式的精简约束。
func LeadNoticePolicy(plansOnly bool) string {
	if plansOnly {
		return LeadNoticeReminder + " You only plan, every change goes to a bee."
	}
	return LeadNoticeReminder
}

// rosterMaxLines 是清单里最多列出的在跑 bee 行数，超出部分合并成一行。
const rosterMaxLines = 12

// rosterRule 是随清单固定附上的英文规则：在跑或排队的 bee 不算失败，不得重发。
const rosterRule = `A running or queued bee has not failed: its report arrives later as a [编排] notice, so never resend its task. If its brief must change, send the new entry with "replaces": "<bee name>"; mcode stops that bee before starting the new one. Only a bee reported as failed or stopped may be sent again.`

// rosterDuration 把时长格式化为分钟粒度的英文简写，不足 1 分钟写 under 1m。
func rosterDuration(d time.Duration) string {
	if d < time.Minute {
		return "under 1m"
	}
	return formatLimitDuration(d)
}

// RosterPromptSection 生成向主代理列出在跑 bee 的英文清单段落，
// 审查中或待审查的 bee 虽然已经结束，但结果还没汇报给主代理，同样要列出并标注状态。
// 末尾固定附一段规则说明在跑或排队的 bee 不算失败、改 brief 用 replaces 取代。
// 没有在跑的 bee、没有待审查的 bee 且没有排队条目时返回空字符串。
func RosterPromptSection(bees []Bee, queued int, now time.Time) string {
	listed := make([]Bee, 0, len(bees))
	for _, bee := range bees {
		if bee.Status == StatusRunning || bee.ReviewState == ReviewRunning || bee.PendingMerge {
			listed = append(listed, bee)
		}
	}
	if len(listed) == 0 && queued <= 0 {
		return ""
	}

	shown := listed
	if len(shown) > rosterMaxLines {
		shown = shown[:rosterMaxLines]
	}

	var b strings.Builder
	b.WriteString("[Swarm roster]")
	for _, bee := range shown {
		tier := ""
		if t := strings.TrimSpace(bee.Tier); t != "" {
			tier = ", tier " + t
		}
		elapsed := "under 1m"
		if !bee.StartedAt.IsZero() {
			elapsed = rosterDuration(now.Sub(bee.StartedAt))
		}
		fmt.Fprintf(&b, "\n- %s (id %s%s): %q, running %s", bee.Name, bee.ThreadID, tier, bee.Task, elapsed)
		if !bee.LastProgressAt.IsZero() {
			fmt.Fprintf(&b, ", last progress %s ago", rosterDuration(now.Sub(bee.LastProgressAt)))
		}
		if bee.Interrupted {
			b.WriteString(", interrupted by a restart and waiting for the user")
		}
		if bee.ReviewState == ReviewRunning {
			b.WriteString(" (reviewing)")
		}
		if bee.PendingMerge {
			b.WriteString(" (awaiting review)")
		}
	}
	if rest := len(listed) - len(shown); rest > 0 {
		fmt.Fprintf(&b, "\n- and %d more running", rest)
	}
	if queued > 0 {
		fmt.Fprintf(&b, "\n- %d more entries queued, not started yet", queued)
	}
	b.WriteString("\n" + rosterRule)
	return b.String()
}

// formatLimitDuration 把时长格式化为英文简写（3h、2h30m、45m），供上限提示使用。
func formatLimitDuration(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	hours := minutes / 60
	rest := minutes % 60
	switch {
	case hours == 0:
		return fmt.Sprintf("%dm", rest)
	case rest == 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dh%dm", hours, rest)
	}
}

// LimitsPromptSection 生成向主代理说明本次请求编排上限的英文提示段落。
func LimitsPromptSection(l RequestLimits) string {
	rounds := fmt.Sprintf("%d of %d rounds used so far", l.RoundsUsed, l.MaxRounds)
	if l.MaxRounds <= 0 {
		rounds = fmt.Sprintf("%d rounds used so far, no cap on rounds", l.RoundsUsed)
	}
	return fmt.Sprintf(`[Swarm limits] %s. Up to %d bees run concurrently, at most %d entries per block. This request may start at most %d bees, %d started. A time budget of %s runs from the user's latest message, %s elapsed; after that, new blocks are rejected. Keep each block within that limit; when one is near, wrap up and report instead of sending another block.`, rounds, l.MaxConcurrent, l.MaxConcurrent, l.BeeBudget, l.BeesStarted, formatLimitDuration(l.Timeout), formatLimitDuration(l.Elapsed))
}

// TierPromptSection 生成供主代理使用的档位说明提示段落；tiers 为空时返回空字符串。
// resolved 非 nil 时，某档解析出模型就追加一句英文后缀，说明它本次实际跑在哪个 provider/model 与推理强度上；
// 后缀只依赖档位解析结果，不含时间或随机值，保证相同配置下的文本稳定可去重。
func TierPromptSection(tiers []Tier, defaultTier string, resolved map[string]ResolvedTier) string {
	if len(tiers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Bee tiers:\n")
	writeOnly := false
	for _, t := range tiers {
		ro := ""
		if t.ReadOnly {
			ro = " (read-only)"
		}
		if len(t.WritePaths) > 0 {
			writeOnly = true
			ro += " (writes only " + strings.Join(t.WritePaths, ", ") + ")"
		}
		b.WriteString(fmt.Sprintf("- %s%s: %s%s\n", t.ID, ro, t.Description, tierRuntimeSuffix(t.ID, resolved)))
	}
	// 只有存在写入白名单档位时才插入这句规则，其余情况保持原文本逐字节不变。
	keepTier := "Re-sending a failed or stopped task keeps its tier; never raise it; "
	if writeOnly {
		keepTier = "Re-sending a failed or stopped task keeps its tier. " + tierWriteOnlyRule + " Never raise it; "
	}
	b.WriteString(fmt.Sprintf("every entry carries \"tier\" with one of the ids above. Pick it by what the brief already settles, not by how large or important the task is, checking in this order: (1) the bee writes no file, including diagnosis that reproduces, bisects or measures and reports the root cause with evidence: a read-only tier. (2) the brief names the files and the change, through file:line anchors, numbered steps or a list of files it may touch; this includes replaying or redoing a patch, renames, formatting, fixing or adding tests and doc edits: the default tier %s or a cheaper one whose description fits, even when the change spans several files. A deletion, rename or signature change whose references span several files goes to the default tier unless the brief already lists every file and reference to change. (3) the steps are settled but landing them takes careful reasoning, such as concurrency and locking, lifecycles and state machines, or a refactor across packages: the tier described for hard changes, when one exists. You design every fix yourself; never send a bee a symptom and ask it for the fix. "+keepTier+
		"an entry without \"tier\" runs on the default tier %s; in a brief for a read-only bee, number the questions and require every finding to cite the full repo-relative path and a single line number as path:line (never a bare filename, never an absolute path, never a line range), followed on the same line by the quoted source line in backticks; before acting on a finding that drives a decision, spot-check it yourself with a grep.", defaultTier, defaultTier))
	return b.String()
}

// tierWriteOnlyRule 是存在写入白名单档位时追加给主代理的规则句，
// 说明这类档位只能落地白名单内的改动，代码改动要派给不受限的档位。
const tierWriteOnlyRule = "A tier marked writes only lands changes only under its allowed paths: use it for work that lives there, such as docs; send any code change to a tier without that limit."

// tierRuntimeSuffix 生成档位行尾的运行模型后缀，例如 " [runs on pi/model-x, effort high]"；
// 该档没有解析结果或模型为空时返回空串，让提示行保持原样。
func tierRuntimeSuffix(id string, resolved map[string]ResolvedTier) string {
	if len(resolved) == 0 {
		return ""
	}
	rt, ok := resolved[id]
	if !ok || rt.Model == "" {
		return ""
	}
	target := rt.Model
	if rt.Provider != "" {
		target = rt.Provider + "/" + rt.Model
	}
	if rt.Effort == "" {
		return fmt.Sprintf(" [runs on %s]", target)
	}
	return fmt.Sprintf(" [runs on %s, effort %s]", target, rt.Effort)
}
