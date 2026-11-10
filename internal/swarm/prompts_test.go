package swarm

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLeadPolicyPlansOnlyOmitsDoAlone(t *testing.T) {
	got := LeadPolicy(true, "off")
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
	got := LeadPolicy(false, "off")
	if !strings.Contains(got, "yours to do alone") {
		t.Fatalf("默认策略应保留「可独自完成」的说法:\n%s", got)
	}
	if strings.Contains(got, "planning-only lead") {
		t.Fatalf("默认策略不应包含只规划段落:\n%s", got)
	}
}

// 常驻策略需保持精简：补丁另存与 swarm-answer 两条说明不再常驻，随对应事件消息下发。
func TestLeadPolicyOmitsOnDemandRules(t *testing.T) {
	for _, plansOnly := range []bool{false, true} {
		got := LeadPolicy(plansOnly, "off")
		for _, unwanted := range []string{".patch", "swarm-answer", "then Aspen 2"} {
			if strings.Contains(got, unwanted) {
				t.Fatalf("plansOnly=%v 的常驻策略不应包含按需说明 %q:\n%s", plansOnly, unwanted, got)
			}
		}
	}
}

// TestLeadPolicyRequiresTestsAndPostMergeRead 验证策略要求验收标准写明要验证的行为、brief 让 bee 补测试，
// 并要求主代理汇总前读一遍各 bee 改过的文件；只规划模式还需说明读改动文件归属主代理。
func TestLeadPolicyRequiresTestsAndPostMergeRead(t *testing.T) {
	for _, plansOnly := range []bool{false, true} {
		got := LeadPolicy(plansOnly, "off")
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
	if !strings.Contains(LeadPolicy(true, "off"), "reading each bee's changed files") {
		t.Fatalf("只规划策略应说明读改动文件仍归主代理:\n%s", LeadPolicy(true, "off"))
	}
}

func TestTierPromptSectionCrossFileReferences(t *testing.T) {
	tiers := []Tier{{ID: "fast", Description: "Quick responses"}}
	got := TierPromptSection(tiers, "fast", nil)
	if !strings.Contains(got, "goes to the default tier unless the brief already lists every file and reference to change") {
		t.Fatalf("档位策略应要求跨文件引用清单不完整时使用默认档:\n%s", got)
	}
}

// TestLeadPolicyReviewMode 验证审查档策略：off 与改动前一致（自动合并），
// ai/human 说明改动要过审查才合入，且不再说汇报已落地。
func TestLeadPolicyReviewMode(t *testing.T) {
	off := LeadPolicy(false, "off")
	if !strings.Contains(off, "Merging is automatic") {
		t.Fatalf("off 档应保留自动合并规则:\n%s", off)
	}
	if !strings.Contains(off, "Each bee works in its own copy and mcode merges its changes when it reports;") {
		t.Fatalf("off 档应保留汇报即合入的说明:\n%s", off)
	}
	if strings.Contains(off, "Merging waits for review") {
		t.Fatalf("off 档不应出现审查规则:\n%s", off)
	}

	ai := LeadPolicy(false, "ai")
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
	human := LeadPolicy(false, " Human ")
	if !strings.Contains(human, "Merging waits for review") || strings.Contains(human, "funnel already reviewed the rest") {
		t.Fatalf("human 档应保留基础审查文案且不带漏斗专属说明:\n%s", human)
	}
}

// ai 档额外的漏斗措辞：主代理只看 merge result 的 concerns，自己跑构建与测试；
// human 与 off 档都不出现这句，且 off 档逐字保持原样。
func TestLeadPolicyReviewFunnelWording(t *testing.T) {
	const funnelClause = "read only the review concerns listed in each merge result and the code they point to"
	ai := LeadPolicy(false, "ai")
	if !strings.Contains(ai, funnelClause) || !strings.Contains(ai, "the funnel already reviewed the rest") {
		t.Fatalf("ai 档应包含漏斗措辞:\n%s", ai)
	}
	if aiPlans := LeadPolicy(true, "ai"); !strings.Contains(aiPlans, "a review concern turns out to be real") {
		t.Fatalf("ai 只规划档应替换 planOnlyRule 的读文件句:\n%s", aiPlans)
	}
	if human := LeadPolicy(false, "human"); strings.Contains(human, funnelClause) {
		t.Fatalf("human 档不应带漏斗措辞:\n%s", human)
	}
	if humanPlans := LeadPolicy(true, "human"); strings.Contains(humanPlans, "a review concern turns out to be real") {
		t.Fatalf("human 只规划档不应替换 planOnlyRule:\n%s", humanPlans)
	}

	off := LeadPolicy(false, "off")
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
	for _, plansOnly := range []bool{false, true} {
		for _, mode := range []string{"off", "ai", "human"} {
			got := LeadPolicy(plansOnly, mode)
			if !strings.Contains(got, "Bees may run scoped checks") || !strings.Contains(got, "run the full checks yourself") {
				t.Fatalf("plansOnly=%v mode=%s 的策略应说明 bee 只跑有范围的自测:\n%s", plansOnly, mode, got)
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
				`- Aspen (id t-0, tier edit): "task 0", running 4m`,
				"- and 1 more running",
			},
			notWant: []string{
				`- Moss (id t-12,`,
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
	defaultGot := LeadNoticePolicy(false)
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

	plansOnlyGot := LeadNoticePolicy(true)
	if !strings.HasPrefix(plansOnlyGot, LeadNoticeReminder) {
		t.Fatalf("只规划通知提示词应以基础提醒为前缀:\n%s", plansOnlyGot)
	}
	wantClause := "You only plan, every change goes to a bee."
	if !strings.Contains(plansOnlyGot, wantClause) {
		t.Fatalf("只规划通知提示词应包含 %q:\n%s", wantClause, plansOnlyGot)
	}
	if !strings.HasSuffix(plansOnlyGot, wantClause) {
		t.Fatalf("只规划通知提示词应以 %q 结尾:\n%s", wantClause, plansOnlyGot)
	}
}

// TestLeadPolicyBriefShapeByWork 验证 brief 写法按工作性质区分，且审查档改写子串仍命中。
func TestLeadPolicyBriefShapeByWork(t *testing.T) {
	for _, plansOnly := range []bool{false, true} {
		got := LeadPolicy(plansOnly, "")
		for _, want := range []string{"For read-only research: numbered questions", "For diagnosis or design"} {
			if !strings.Contains(got, want) {
				t.Fatalf("plansOnly=%v 的策略应包含 %q:\n%s", plansOnly, want, got)
			}
		}
	}
	for _, mode := range []string{"ai", "human"} {
		got := LeadPolicy(false, mode)
		if !strings.Contains(got, "only after they pass review") {
			t.Fatalf("reviewMode=%s 的策略应说明过审查才合入:\n%s", mode, got)
		}
		if strings.Contains(got, "merges its changes when it reports") {
			t.Fatalf("reviewMode=%s 的策略不应保留汇报即合入的说法:\n%s", mode, got)
		}
	}
}
