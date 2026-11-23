package swarm

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLeadPolicyPlansOnlyOmitsDoAlone(t *testing.T) {
	t.Parallel()
	got := LeadPolicy(true, "off", false)
	if strings.Contains(got, "yours to do alone") {
		t.Fatalf("只规划策略不应保留「可独自完成」的说法:\n%s", got)
	}
	if strings.Contains(got, "This overrides every rule above") {
		t.Fatalf("只规划策略不应包含覆盖声明:\n%s", got)
	}
	if !strings.Contains(got, "[Swarm · planning-only lead] You only plan.") {
		t.Fatalf("只规划策略未以预期文案开头:\n%s", got)
	}
	if !strings.HasPrefix(planOnlyRule, "[Swarm · planning-only lead] You only plan.") {
		t.Fatalf("只规划段落应以固定开场句开头:\n%s", planOnlyRule)
	}
	if strings.Contains(got, "Briefs follow the rules above") {
		t.Fatalf("只规划段落不应再保留「照上述规则写 brief」的重复条目:\n%s", planOnlyRule)
	}
}

func TestLeadPolicyDefaultKeepsDoAlone(t *testing.T) {
	t.Parallel()
	got := LeadPolicy(false, "off", false)
	if !strings.Contains(got, "yours to do alone") {
		t.Fatalf("默认策略应保留「可独自完成」的说法:\n%s", got)
	}
	if strings.Contains(got, "planning-only lead") {
		t.Fatalf("默认策略不应包含只规划段落:\n%s", got)
	}
}

func TestLeadPolicyReadOnlyDelegation(t *testing.T) {
	t.Parallel()
	const questionsRule = "- Questions follow the same delegation rule: answer directly only from existing context or one quick lookup; otherwise delegate the investigation, even when no files need changing."
	const oldQuestionsRule = "Questions and explanations, which change nothing, you answer directly from what you have read"
	for _, plansOnly := range []bool{false, true} {
		for _, mode := range []string{"off", "ai", "human"} {
			t.Run(fmt.Sprintf("plansOnly=%v/review=%s", plansOnly, mode), func(t *testing.T) {
				t.Parallel()
				got := LeadPolicy(plansOnly, mode, false)
				for _, want := range []string{
					"For a new user request, if answering requires tracing multiple call sites, comparing files or checking runtime evidence, delegate that investigation to at least one read-only bee in your first reply; a question or a no-edit task is not an exception.",
					"Your own initial inspection is only to scope the brief, not to finish the investigation.",
					"Answer directly only when the answer is already in context or one quick lookup suffices.",
				} {
					if !strings.Contains(got, want) {
						t.Errorf("策略缺少 %q", want)
					}
				}
				if plansOnly && !strings.Contains(got, questionsRule) {
					t.Error("只规划策略缺少问题派发规则")
				}
				if strings.Contains(got, oldQuestionsRule) {
					t.Error("策略不应保留旧的问题例外")
				}
			})
		}
	}
}

// 常驻策略需保持精简：补丁另存与 swarm-answer 两条说明不再常驻，随对应事件消息下发。
func TestLeadPolicyOmitsOnDemandRules(t *testing.T) {
	t.Parallel()
	for _, plansOnly := range []bool{false, true} {
		got := LeadPolicy(plansOnly, "off", false)
		for _, unwanted := range []string{".patch", "swarm-answer", "then Aria 2"} {
			if strings.Contains(got, unwanted) {
				t.Fatalf("plansOnly=%v 的常驻策略不应包含按需说明 %q:\n%s", plansOnly, unwanted, got)
			}
		}
	}
}

// TestLeadPolicyRequiresTestsAndPostMergeRead 验证策略要求验收标准写明要验证的行为、brief 让 bee 补测试，
// 并要求主代理汇总前读一遍各 bee 改过的文件；只规划模式还需说明读改动文件归属主代理。
func TestLeadPolicyRequiresTestsAndPostMergeRead(t *testing.T) {
	t.Parallel()
	for _, plansOnly := range []bool{false, true} {
		got := LeadPolicy(plansOnly, "off", false)
		for _, want := range []string{
			"add or update a test that covers it",
			"read the files each bee changed",
			"grep every reference to the symbol",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("plansOnly=%v 的策略应包含 %q:\n%s", plansOnly, want, got)
			}
		}
		if strings.Contains(got, "never watch bees with git.") {
			t.Fatalf("plansOnly=%v 的策略不应保留「禁止用 git 监视 bee」的旧说法:\n%s", plansOnly, got)
		}
	}
	if !strings.Contains(LeadPolicy(true, "off", false), "reading each bee's changed files") {
		t.Fatalf("只规划策略应说明读改动文件仍归主代理:\n%s", LeadPolicy(true, "off", false))
	}
}

func TestTierPromptSectionCrossFileReferences(t *testing.T) {
	t.Parallel()
	tiers := []Tier{{ID: "fast", Description: "Quick responses"}}
	got := TierPromptSection(tiers, "fast", nil)
	if !strings.Contains(got, "goes to the default tier unless the brief already lists every file and reference to change") {
		t.Fatalf("档位策略应要求跨文件引用清单不完整时使用默认档:\n%s", got)
	}
}

// TestLeadPolicyReviewMode 验证审查档策略：off 与改动前一致（自动合并），
// ai/human 说明改动要过审查才合入，且不再说汇报已落地。
func TestLeadPolicyReviewMode(t *testing.T) {
	t.Parallel()
	off := LeadPolicy(false, "off", false)
	if !strings.Contains(off, "Merging is automatic") {
		t.Fatalf("off 档应保留自动合并规则:\n%s", off)
	}
	if !strings.Contains(off, "Each bee works in its own copy and mcode merges its changes when it reports;") {
		t.Fatalf("off 档应保留汇报即合入的说明:\n%s", off)
	}
	if strings.Contains(off, "Merging waits for review") {
		t.Fatalf("off 档不应出现审查规则:\n%s", off)
	}

	ai := LeadPolicy(false, "ai", false)
	if !strings.Contains(ai, "Merging waits for review") {
		t.Fatalf("ai 档应说明合入要等审查:\n%s", ai)
	}
	if strings.Contains(ai, "already applied") {
		t.Fatalf("ai 档不应再说汇报已落地:\n%s", ai)
	}
	if !strings.Contains(ai, "mcode merges its changes only after they pass review;") {
		t.Fatalf("ai 档应说明只在审查通过后合入:\n%s", ai)
	}
	if !strings.Contains(ai, "path of the saved patch") {
		t.Fatalf("ai 档应说明审查未通过时给出补丁路径:\n%s", ai)
	}
	if strings.Contains(ai, autoMergeRule) {
		t.Fatalf("ai 档不应保留自动合并规则:\n%s", ai)
	}
	human := LeadPolicy(false, " Human ", false)
	if !strings.Contains(human, "Merging waits for review") || strings.Contains(human, "funnel already reviewed the rest") {
		t.Fatalf("human 档应保留基础审查文案且不带漏斗专属说明:\n%s", human)
	}
}

// ai 档额外的漏斗措辞：主代理只看 merge result 的 concerns，自己跑构建与测试；
// human 与 off 档都不出现这句，且 off 档逐字保持原样。
func TestLeadPolicyReviewFunnelWording(t *testing.T) {
	t.Parallel()
	const funnelClause = "read only the review concerns listed in each merge result and the code they point to"
	ai := LeadPolicy(false, "ai", false)
	if !strings.Contains(ai, funnelClause) || !strings.Contains(ai, "the funnel already reviewed the rest") {
		t.Fatalf("ai 档应包含漏斗措辞:\n%s", ai)
	}
	if aiPlans := LeadPolicy(true, "ai", false); !strings.Contains(aiPlans, "a review concern turns out to be real") {
		t.Fatalf("ai 只规划档应替换 planOnlyRule 的读文件句:\n%s", aiPlans)
	}
	if human := LeadPolicy(false, "human", false); strings.Contains(human, funnelClause) {
		t.Fatalf("human 档不应带漏斗措辞:\n%s", human)
	}
	if humanPlans := LeadPolicy(true, "human", false); strings.Contains(humanPlans, "a review concern turns out to be real") {
		t.Fatalf("human 只规划档不应替换 planOnlyRule:\n%s", humanPlans)
	}

	off := LeadPolicy(false, "off", false)
	for _, unwanted := range []string{funnelClause, "Merging waits for review", "review concern"} {
		if strings.Contains(off, unwanted) {
			t.Fatalf("off 档不应包含 %q:\n%s", unwanted, off)
		}
	}
	if !strings.Contains(off, "read the files each bee changed and send a bee to fix what you find") {
		t.Fatalf("off 档应保持原有读文件句:\n%s", off)
	}
}

// bee 可跑有范围的自测并汇报命令与结果；所有策略档都不再说 bee 不构建、不测试。
func TestLeadPolicyScopedBees(t *testing.T) {
	t.Parallel()
	for _, plansOnly := range []bool{false, true} {
		for _, mode := range []string{"off", "ai", "human"} {
			got := LeadPolicy(plansOnly, mode, false)
			if !strings.Contains(got, "Bees may run scoped checks") ||
				!strings.Contains(got, "leaves tests to the acceptance commands") ||
				!strings.Contains(got, "run the full checks yourself once, after the last bee has merged") {
				t.Fatalf("plansOnly=%v mode=%s 的策略应说明 bee 只跑有范围的自测且主代理在最后一只 bee 合并后跑一次完整检查:\n%s", plansOnly, mode, got)
			}
			if mode == "ai" && !strings.Contains(got, "run the build and tests yourself once, after the last bee has merged") {
				t.Fatalf("plansOnly=%v ai 模式应包含合并后跑一次构建与测试:\n%s", plansOnly, got)
			}
			if plansOnly && !strings.Contains(got, "(run builds and checks once, after the last bee has merged)") {
				t.Fatalf("plansOnly=%v mode=%s 只规划规则应包含合并后跑一次:\n%s", plansOnly, mode, got)
			}
			if strings.Contains(got, "They never build, test or commit") {
				t.Fatalf("plansOnly=%v mode=%s 的策略不应保留旧说法:\n%s", plansOnly, mode, got)
			}
		}
	}
}

// RosterPromptSection 需只列在跑的 bee，开头为 [Swarm roster]，末尾附固定规则段；
// 无在跑且无排队时返回空串。
func TestRosterPromptSection(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	newBee := func(name, id, task, tier string, status BeeStatus) Bee {
		return Bee{
			ThreadID:  id,
			Name:      name,
			Task:      task,
			Tier:      tier,
			Status:    status,
			StartedAt: now.Add(-4 * time.Minute),
		}
	}

	alice := newBee("Alice", "t-1", "Roast beans", "edit", StatusRunning)
	alice.LastProgressAt = now.Add(-30 * time.Second)
	birch := newBee("Birch", "t-2", "Pack bags", "edit", StatusRunning)
	birch.LastProgressAt = now.Add(-3 * time.Minute)
	interrupted := newBee("Cove", "t-3", "Split logs", "edit", StatusRunning)
	interrupted.Interrupted = true
	reviewing := newBee("Dove", "t-4", "Roast more", "edit", StatusCompleted)
	reviewing.ReviewState = ReviewRunning
	awaitingReview := newBee("Fern", "t-5", "Pack more", "edit", StatusCompleted)
	awaitingReview.PendingMerge = true
	awaitingReview.ReviewState = ReviewNeedsHuman

	var many []Bee
	for i := 0; i < 13; i++ {
		many = append(many, newBee(RosterName(i), fmt.Sprintf("t-%d", i), fmt.Sprintf("task %d", i), "edit", StatusRunning))
	}

	cases := []struct {
		name     string
		bees     []Bee
		queued   int
		want     []string
		notWant  []string
		wantNone bool
	}{
		{
			name:     "空输入返回空",
			wantNone: true,
		},
		{
			name: "只有已完成或失败的 bee 时返回空",
			bees: []Bee{
				newBee("Alice", "t-1", "A", "edit", StatusCompleted),
				newBee("Birch", "t-2", "B", "edit", StatusFailed),
				newBee("Cove", "t-3", "C", "edit", StatusStopped),
			},
			wantNone: true,
		},
		{
			name: "两个在跑的 bee 含名字 id task 与规则",
			bees: []Bee{alice, birch},
			want: []string{
				"[Swarm roster]",
				`- Alice (id t-1, tier edit): "Roast beans", running 4m, last progress under 1m ago`,
				`- Birch (id t-2, tier edit): "Pack bags", running 4m, last progress 3m ago`,
				"never resend its task",
				`"replaces": "<bee name>"`,
			},
		},
		{
			name: "超过 12 个时合并成 and N more running",
			bees: many,
			want: []string{
				`- Aria (id t-0, tier edit): "task 0", running 4m`,
				"- and 1 more running",
			},
			notWant: []string{
				`- Milo (id t-12,`,
			},
		},
		{
			name:   "排队条数单独一行",
			bees:   []Bee{alice},
			queued: 3,
			want: []string{
				"- 3 more entries queued, not started yet",
			},
		},
		{
			name: "被打断的 bee 标注等待用户",
			bees: []Bee{interrupted},
			want: []string{
				", interrupted by a restart and waiting for the user",
			},
		},
		{
			name: "审查中与待审查的 bee 也列出并标注状态",
			bees: []Bee{reviewing, awaitingReview},
			want: []string{
				`- Dove (id t-4, tier edit): "Roast more", running 4m (reviewing)`,
				`- Fern (id t-5, tier edit): "Pack more", running 4m (awaiting review)`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RosterPromptSection(tc.bees, tc.queued, now)
			if tc.wantNone {
				if got != "" {
					t.Fatalf("应返回空串，实际:\n%s", got)
				}
				return
			}
			if !strings.HasPrefix(got, "[Swarm roster]") {
				t.Fatalf("清单应以 [Swarm roster] 开头:\n%s", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("应包含 %q:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.notWant {
				if strings.Contains(got, unwanted) {
					t.Fatalf("不应包含 %q:\n%s", unwanted, got)
				}
			}
		})
	}
}

// LimitsPromptSection 需给出轮次、并发、bee 预算与总时长四项上限。
func TestLimitsPromptSection(t *testing.T) {
	t.Parallel()
	got := LimitsPromptSection(RequestLimits{
		MaxRounds:     2,
		RoundsUsed:    1,
		MaxConcurrent: 6,
		BeeBudget:     24,
		BeesStarted:   3,
		Timeout:       3 * time.Hour,
		Elapsed:       90 * time.Minute,
	})
	if !strings.HasPrefix(got, "[Swarm limits]") {
		t.Fatalf("上限段落应以 [Swarm limits] 开头:\n%s", got)
	}
	for _, want := range []string{
		"1 of 2 rounds",
		"Up to 6 bees run concurrently",
		"at most 6 entries per block",
		"may start at most 24 bees, 3 started",
		"time budget of 3h",
		"1h30m elapsed",
		"wrap up and report",
		"instead of sending another block",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("上限段落应包含 %q:\n%s", want, got)
		}
	}
	if len(got) > 400 {
		t.Fatalf("上限段落应压在 400 字符以内，实际 %d:\n%s", len(got), got)
	}

	unlimited := LimitsPromptSection(RequestLimits{MaxRounds: 0, MaxConcurrent: 6, BeeBudget: 24, Timeout: 3 * time.Hour})
	if !strings.Contains(unlimited, "no cap on rounds") {
		t.Fatalf("MaxRounds 为 0 时应写 no cap on rounds:\n%s", unlimited)
	}
	if len(unlimited) > 400 {
		t.Fatalf("无轮次上限时段落应压在 400 字符以内，实际 %d:\n%s", len(unlimited), unlimited)
	}
}

// TestLeadNoticePolicy 验证在子代理汇报等通知轮次注入的精简规则文案：
// 默认模式仅输出基础提醒；只规划模式在此基础上追加规划约束。
func TestLeadNoticePolicy(t *testing.T) {
	t.Parallel()
	defaultGot := LeadNoticePolicy(false, false)
	if defaultGot != LeadNoticeReminder {
		t.Fatalf("默认通知提示词应与常量一致:\ngot: %s\nwant: %s", defaultGot, LeadNoticeReminder)
	}
	if !strings.HasPrefix(defaultGot, "[Swarm mode]") {
		t.Fatalf("默认通知提示词应以 [Swarm mode] 开头:\n%s", defaultGot)
	}
	if !strings.Contains(defaultGot, "remain in effect") {
		t.Fatalf("默认通知提示词应说明规则一致且持续生效:\n%s", defaultGot)
	}
	if !strings.Contains(defaultGot, "```swarm") || !strings.Contains(defaultGot, "and stop") {
		t.Fatalf("默认通知提示词应提醒派发后停止:\n%s", defaultGot)
	}
	if !strings.Contains(defaultGot, "if none remains, run your checks and report") {
		t.Fatalf("默认通知提示词应允许没有剩余工作时检查并汇报:\n%s", defaultGot)
	}
	if strings.Contains(defaultGot, "Continue to dispatch") {
		t.Fatalf("默认通知提示词不应再强制继续派发:\n%s", defaultGot)
	}
	if strings.Contains(defaultGot, "You only plan") {
		t.Fatalf("默认通知提示词不应包含只规划段落:\n%s", defaultGot)
	}

	plansOnlyGot := LeadNoticePolicy(true, false)
	if !strings.HasPrefix(plansOnlyGot, LeadNoticeReminder) {
		t.Fatalf("只规划通知提示词应以基础提醒为前缀:\n%s", plansOnlyGot)
	}
	wantClause := "You only plan, every code change goes to a bee."
	if !strings.Contains(plansOnlyGot, wantClause) {
		t.Fatalf("只规划通知提示词应包含 %q:\n%s", wantClause, plansOnlyGot)
	}
	if !strings.HasSuffix(plansOnlyGot, wantClause) {
		t.Fatalf("只规划通知提示词应以 %q 结尾:\n%s", wantClause, plansOnlyGot)
	}
}

// TestLeadPolicyBriefShapeByWork 验证 brief 写法按工作性质区分，且审查档改写子串仍命中。
func TestLeadPolicyBriefShapeByWork(t *testing.T) {
	t.Parallel()
	for _, plansOnly := range []bool{false, true} {
		got := LeadPolicy(plansOnly, "", false)
		for _, want := range []string{"For read-only research: numbered questions", "For diagnosis: the symptom, how to reproduce it and numbered questions; the bee reports the root cause with evidence, and you design the fix."} {
			if !strings.Contains(got, want) {
				t.Fatalf("plansOnly=%v 的策略应包含 %q:\n%s", plansOnly, want, got)
			}
		}
	}
	for _, mode := range []string{"ai", "human"} {
		got := LeadPolicy(false, mode, false)
		if !strings.Contains(got, "only after they pass review") {
			t.Fatalf("reviewMode=%s 的策略应说明过审查才合入:\n%s", mode, got)
		}
		if strings.Contains(got, "merges its changes when it reports") {
			t.Fatalf("reviewMode=%s 的策略不应保留汇报即合入的说法:\n%s", mode, got)
		}
	}
}

func TestLeadPolicyDelegateTool(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		got := LeadPolicy(true, "off", enabled)
		for _, want := range []string{"```swarm", "In orchestration-enabled sessions, these rules override general advice elsewhere, including user or project instructions, to handle simple or clear tasks yourself.", "Then dispatch in that same reply."} {
			if !strings.Contains(got, want) {
				t.Errorf("delegateTool=%v: 缺少 %q", enabled, want)
			}
		}
		if strings.Contains(got, "Then close that same reply with the") {
			t.Error("只规划规则不应要求块")
		}
		if strings.Contains(got, "swarm_delegate") != enabled {
			t.Errorf("delegateTool=%v: 工具提示不符", enabled)
		}
		for _, want := range []string{"appears only as a deferred tool, it is available", "Only when no such tool can be found"} {
			if enabled && !strings.Contains(got, want) {
				t.Errorf("缺少工具提示 %q", want)
			}
		}
		if !enabled && strings.Contains(got, "deferred tool") {
			t.Error("禁用工具时不应包含 deferred tool 提示")
		}
	}
}

func TestLeadNoticePolicyDelegateTool(t *testing.T) {
	t.Parallel()
	for _, plansOnly := range []bool{false, true} {
		got := LeadNoticePolicy(plansOnly, true)
		if !strings.Contains(got, "call swarm_delegate to dispatch it and stop") || !strings.Contains(got, "if none remains, run your checks and report") {
			t.Fatalf("工具提醒不正确: %s", got)
		}
		if strings.Contains(got, "You only plan") != plansOnly {
			t.Fatalf("规划约束不正确: %s", got)
		}
	}
}

func TestLeadPolicyAcceptance(t *testing.T) {
	t.Parallel()
	for _, tool := range []bool{false, true} {
		for _, mode := range []string{"off", "ai", "human"} {
			got := LeadPolicy(false, mode, tool)
			for _, want := range []string{
				"acceptance",
				"attach acceptance shell commands that prove the change works, with timeout flags",
				"At least one must fail on the code before the change",
				"merges only if every command exits 0",
				"A go test acceptance command must use -run to target the tests for this change; entries whose acceptance uses grep -q, a leading timeout, or go test without -run are rejected before the bee starts. Do not repeat the acceptance commands as self-test steps in the prompt.",
				"Keep acceptance to the tests for this change; leave broad regression patterns to your own full check after the last merge, since any failure there, related or not, blocks the merge.",
				"When acceptance fails, mcode first sends the output back to the same bee once, so a failed report has already had that retry; a reason starting with 验收存疑 means the bee judged the acceptance command wrong, so check the command before anything else.",
				"If the acceptance command itself was wrong, send an entry with reworkOf naming that bee, rerun true and the corrected acceptance; mcode reruns it on the kept copy without starting a bee.",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("tool=%v mode=%s missing %q", tool, mode, want)
				}
			}
		}
	}
}

func TestLeadPolicyPlansOnlyPlanDocs(t *testing.T) {
	t.Parallel()
	for _, delegateTool := range []bool{false, true} {
		got := LeadPolicy(true, "off", delegateTool)
		for _, want := range []string{
			"Every code change is sent to a bee.",
			"keep this session's steps in your native todo tool",
			"also write the plan as a markdown file and tick its items as bees merge",
			"the briefs are the plan",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("plansOnly=true delegateTool=%v 的策略应包含 %q:\n%s", delegateTool, want, got)
			}
		}
		if strings.Contains(got, "never edit, write or create a file") {
			t.Fatalf("plansOnly=true delegateTool=%v 的策略不应再包含旧限制 %q:\n%s", delegateTool, "never edit, write or create a file", got)
		}
	}
	nonPlanOnly := LeadPolicy(false, "off", false)
	if strings.Contains(nonPlanOnly, "keep this session's steps in your native todo tool") {
		t.Fatalf("plansOnly=false 的策略不应包含 %q:\n%s", "keep this session's steps in your native todo tool", nonPlanOnly)
	}
}

func TestLeadPolicyMentionsFiles(t *testing.T) {
	t.Parallel()
	for _, delegateTool := range []bool{false, true} {
		got := LeadPolicy(false, "off", delegateTool)
		if !strings.Contains(got, "files the repo-relative file paths this bee will edit") {
			t.Fatalf("delegateTool=%v 策略缺少 files 字段说明:\n%s", delegateTool, got)
		}
		if delegateTool && !strings.Contains(got, "Use the same entry fields: task, prompt, bee, tier, skills, project, files, replaces and acceptance.") {
			t.Fatalf("delegateTool=true 策略未在字段列表包含 files:\n%s", got)
		}
	}
}
