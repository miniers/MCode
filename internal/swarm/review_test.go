package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mcode/internal/kanban"
)

const testReviewFence = "```" + "swarm-review"
const testFenceClose = "`" + "`" + "`"

func TestParseReviewVerdict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want ReviewVerdict
		ok   bool
	}{
		{
			name: "approve with reason",
			text: "Looks fine.\n" + testReviewFence + "\n{\"verdict\": \"approve\", \"reason\": \"ok\"}\n" + testFenceClose + "\n",
			want: ReviewVerdict{Verdict: "approve", Reason: "ok"},
			ok:   true,
		},
		{
			name: "reject with reason",
			text: testReviewFence + "\n{\"verdict\": \"reject\", \"reason\": \"missing tests\"}\n" + testFenceClose + "\n",
			want: ReviewVerdict{Verdict: "reject", Reason: "missing tests"},
			ok:   true,
		},
		{
			name: "needs human",
			text: testReviewFence + "\n{\"verdict\": \"needs_human\", \"reason\": \"needs a decision\"}\n" + testFenceClose + "\n",
			want: ReviewVerdict{Verdict: "needs_human", Reason: "needs a decision"},
			ok:   true,
		},
		{
			name: "explanation around fence",
			text: "I checked the diff first.\n\n" + testReviewFence + "\n{\"verdict\": \"approve\", \"reason\": \"safe\"}\n" + testFenceClose + "\n\nThat is all.",
			want: ReviewVerdict{Verdict: "approve", Reason: "safe"},
			ok:   true,
		},
		{
			name: "last block wins",
			text: testReviewFence + "\n{\"verdict\": \"reject\", \"reason\": \"first\"}\n" + testFenceClose + "\n" +
				testReviewFence + "\n{\"verdict\": \"approve\", \"reason\": \"second\"}\n" + testFenceClose + "\n",
			want: ReviewVerdict{Verdict: "approve", Reason: "second"},
			ok:   true,
		},
		{
			name: "invalid json",
			text: testReviewFence + "\n{\"verdict\": \"approve\",\n" + testFenceClose + "\n",
			ok:   false,
		},
		{
			name: "unknown verdict",
			text: testReviewFence + "\n{\"verdict\": \"lgtm\", \"reason\": \"ok\"}\n" + testFenceClose + "\n",
			ok:   false,
		},
		{
			name: "reject without reason degrades",
			text: testReviewFence + "\n{\"verdict\": \"reject\"}\n" + testFenceClose + "\n",
			want: ReviewVerdict{Verdict: "needs_human", Reason: "审查结论缺少原因"},
			ok:   true,
		},
		{
			name: "uppercase verdict",
			text: testReviewFence + "\n{\"verdict\": \"APPROVE\", \"reason\": \"ok\"}\n" + testFenceClose + "\n",
			want: ReviewVerdict{Verdict: "approve", Reason: "ok"},
			ok:   true,
		},
		{
			name: "no fence",
			text: "approved, go ahead",
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseReviewVerdict(tc.text)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got != tc.want {
				t.Fatalf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReviewPrompt_IncludesInputs(t *testing.T) {
	t.Parallel()
	h := Bee{
		Name:         "Aspen",
		Task:         "add the review file",
		Prompt:       "please add review.txt",
		Summary:      "added review.txt",
		ChangedFiles: []string{"review.txt"},
		WorktreePath: "/tmp/bee-worktree",
	}
	prompt := reviewPrompt(h, "/tmp/bee.patch")
	for _, want := range []string{
		"Aspen",
		"add the review file",
		"please add review.txt",
		"added review.txt",
		"review.txt",
		"/tmp/bee.patch",
		"/tmp/bee-worktree",
		"swarm-review",
		"read-only",
		"must not modify any file",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt 缺少 %q:\n%s", want, prompt)
		}
	}
}

type leadRuntimeFakeDriver struct {
	*fakeDriver
	provider, model, effort string
	ok                      bool
}

func (d *leadRuntimeFakeDriver) LeadRuntime(ctx context.Context, leadThreadID string) (string, string, string, bool) {
	return d.provider, d.model, d.effort, d.ok
}

func TestReviewRuntime(t *testing.T) {
	t.Parallel()
	defaults := Settings{DefaultProvider: "default-prov", DefaultModel: "default-model", DefaultEffort: "low"}
	lead := &leadRuntimeFakeDriver{
		fakeDriver: newFakeDriver(),
		provider:   "lead-prov",
		model:      "lead-model",
		effort:     "high",
		ok:         true,
	}

	t.Run("follows lead", func(t *testing.T) {
		m := NewManager(Options{Driver: lead})
		m.SetSettingsFunc(func() Settings { return defaults })
		provider, model, effort := m.reviewRuntime(context.Background(), "lead-1")
		if provider != "lead-prov" || model != "lead-model" || effort != "high" {
			t.Fatalf("未跟随主会话模型: %q/%q/%q", provider, model, effort)
		}
	})

	t.Run("explicit review model wins", func(t *testing.T) {
		m := NewManager(Options{Driver: lead})
		m.SetSettingsFunc(func() Settings {
			s := defaults
			s.ReviewModel = "review-model"
			return s
		})
		provider, model, _ := m.reviewRuntime(context.Background(), "lead-1")
		if model != "review-model" {
			t.Fatalf("显式审查模型未生效: %q", model)
		}
		if provider != "default-prov" {
			t.Fatalf("未显式指定 provider 时应回落到默认值: %q", provider)
		}
	})

	t.Run("falls back to bee defaults", func(t *testing.T) {
		m := NewManager(Options{Driver: newFakeDriver()})
		m.SetSettingsFunc(func() Settings { return defaults })
		provider, model, effort := m.reviewRuntime(context.Background(), "lead-1")
		if provider != "default-prov" || model != "default-model" || effort != "low" {
			t.Fatalf("未回落到 bee 默认模型: %q/%q/%q", provider, model, effort)
		}
	})
}

func reviewTestManager(t *testing.T) (*Manager, *fakeDriver, Bee) {
	t.Helper()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	m := NewManager(Options{Git: runner, Driver: driver, WorktreeDir: t.TempDir()})
	bees, err := m.Dispatch(context.Background(), DispatchRequest{
		LeadThreadID: "lead-review-ai",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Aspen", Task: "新增文件"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, driver, bees[0]
}

func markReviewRunning(t *testing.T, m *Manager, beeThreadID, reviewThreadID string, started time.Time) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads["lead-review-ai"]
	if !ok {
		t.Fatal("lead 不存在")
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID != beeThreadID {
			continue
		}
		lead.bees[i].ReviewState = ReviewRunning
		lead.bees[i].ReviewThreadID = reviewThreadID
		lead.bees[i].ReviewStartedAt = &started
		return
	}
	t.Fatal("bee 不存在")
}

func currentReviewBee(t *testing.T, m *Manager, beeThreadID string) Bee {
	t.Helper()
	for _, b := range m.State("lead-review-ai").Bees {
		if b.ThreadID == beeThreadID {
			return b
		}
	}
	t.Fatal("bee 不存在")
	return Bee{}
}

func TestPollReviews_TimeoutStopsSession(t *testing.T) {
	t.Parallel()
	m, driver, h := reviewTestManager(t)
	var stopped []string
	m.opts.StopSession = func(ctx context.Context, threadID string) error {
		stopped = append(stopped, threadID)
		return nil
	}
	markReviewRunning(t, m, h.ThreadID, "review-timeout", time.Now().Add(-11*time.Minute))
	driver.setSessionState("review-timeout", kanban.SessionState{Running: true})

	m.pollReviews(context.Background())

	if len(stopped) != 1 || stopped[0] != "review-timeout" {
		t.Fatalf("超时未停止审查会话: %v", stopped)
	}
	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState != ReviewNeedsHuman {
		t.Fatalf("超时后应转人工: %+v", bee.ReviewState)
	}
	if bee.ReviewReason != "审查超时" {
		t.Fatalf("原因不符: %q", bee.ReviewReason)
	}
}

func TestPollReviews_FailedFallsBackToHuman(t *testing.T) {
	t.Parallel()
	m, driver, h := reviewTestManager(t)
	markReviewRunning(t, m, h.ThreadID, "review-failed", time.Now())
	driver.setSessionState("review-failed", kanban.SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "boom",
	})

	m.pollReviews(context.Background())

	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState != ReviewNeedsHuman {
		t.Fatalf("审查失败后应转人工: %+v", bee.ReviewState)
	}
	if bee.ReviewReason != "审查会话失败：boom" {
		t.Fatalf("原因不符: %q", bee.ReviewReason)
	}
}

func TestPollReviews_ParseFailureFallsBackToHuman(t *testing.T) {
	t.Parallel()
	m, driver, h := reviewTestManager(t)
	markReviewRunning(t, m, h.ThreadID, "review-parse", time.Now())
	driver.setSessionState("review-parse", kanban.SessionState{
		Running:           false,
		LastAssistantText: "no verdict block here",
	})

	m.pollReviews(context.Background())

	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState != ReviewNeedsHuman {
		t.Fatalf("解析失败后应转人工: %+v", bee.ReviewState)
	}
	if bee.ReviewReason != "未能解析审查结论" {
		t.Fatalf("原因不符: %q", bee.ReviewReason)
	}
}

func TestPollReviews_NotStartedFallsBackToHuman(t *testing.T) {
	t.Parallel()
	m, _, h := reviewTestManager(t)
	markReviewRunning(t, m, h.ThreadID, "", time.Now().Add(-3*time.Minute))

	m.pollReviews(context.Background())

	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState != ReviewNeedsHuman {
		t.Fatalf("审查会话未启动应转人工: %+v", bee.ReviewState)
	}
	if bee.ReviewReason != "审查会话未启动" {
		t.Fatalf("原因不符: %q", bee.ReviewReason)
	}
}

// prepareApprovedReview 把 bee 置为审查中，并写入与 MergePatch 一致的补丁快照。
func prepareApprovedReview(t *testing.T, m *Manager, h Bee, reviewThreadID string) {
	t.Helper()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(h.WorktreePath, "review.txt"), []byte("reviewed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.opts.Git.AddAll(ctx, h.WorktreePath); err != nil {
		t.Fatal(err)
	}
	patch, err := m.opts.Git.DiffCachedBinary(ctx, h.WorktreePath, h.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) == 0 {
		t.Fatal("审查补丁为空")
	}
	patchPath := filepath.Join(t.TempDir(), "review.patch")
	if err := os.WriteFile(patchPath, patch, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(patch)
	sha := hex.EncodeToString(sum[:])

	started := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads["lead-review-ai"]
	if !ok {
		t.Fatal("lead 不存在")
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID != h.ThreadID {
			continue
		}
		lead.bees[i].ReviewState = ReviewRunning
		lead.bees[i].ReviewThreadID = reviewThreadID
		lead.bees[i].ReviewStartedAt = &started
		lead.bees[i].ReviewPatchPath = patchPath
		lead.bees[i].ReviewPatchSHA = sha
		lead.bees[i].ReviewFingerprint = m.worktreeFingerprint(ctx, lead.bees[i])
		lead.bees[i].MergePatch = append([]byte(nil), patch...)
		lead.bees[i].PendingMerge = true
		return
	}
	t.Fatal("bee 不存在")
}

func TestPollReviews_ApproveAdvancesReview(t *testing.T) {
	t.Parallel()
	m, driver, h := reviewTestManager(t)
	prepareApprovedReview(t, m, h, "review-approve")
	driver.setSessionState("review-approve", kanban.SessionState{
		Running:           false,
		LastAssistantText: "ok\n" + testReviewFence + "\n{\"verdict\": \"approve\", \"reason\": \"safe\"}\n" + testFenceClose + "\n",
	})

	m.pollReviews(context.Background())

	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState == ReviewRunning {
		t.Fatalf("approve 结论未推进状态: %+v", bee)
	}
}

func TestPollReviews_ApproveStoresReason(t *testing.T) {
	t.Parallel()
	m, driver, h := reviewTestManager(t)
	prepareApprovedReview(t, m, h, "review-approve-reason")
	driver.setSessionState("review-approve-reason", kanban.SessionState{
		Running:           false,
		LastAssistantText: "ok\n" + testReviewFence + "\n{\"verdict\": \"approve\", \"reason\": \"改动正确\"}\n" + testFenceClose + "\n",
	})

	m.pollReviews(context.Background())

	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState != ReviewApproved {
		t.Fatalf("approve 后应记为审查通过: %+v", bee.ReviewState)
	}
	if !bee.Merged {
		t.Fatalf("approve 后应合入主工作区: %+v", bee)
	}
	if bee.ReviewReason != "改动正确" {
		t.Fatalf("审查理由未保存: %q", bee.ReviewReason)
	}
}

func TestPollReviews_RejectAdvancesReview(t *testing.T) {
	t.Parallel()
	m, driver, h := reviewTestManager(t)
	markReviewRunning(t, m, h.ThreadID, "review-reject", time.Now())
	driver.setSessionState("review-reject", kanban.SessionState{
		Running:           false,
		LastAssistantText: testReviewFence + "\n{\"verdict\": \"reject\", \"reason\": \"wrong\"}\n" + testFenceClose + "\n",
	})

	m.pollReviews(context.Background())

	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState == ReviewRunning {
		t.Fatalf("reject 结论未推进状态: %+v", bee)
	}
}

func TestStartMergeReview_StartsReadOnlySession(t *testing.T) {
	t.Parallel()
	m, driver, h := reviewTestManager(t)
	patch := []byte("diff --git a/review.txt b/review.txt\n")
	started := time.Now()
	m.mu.Lock()
	lead := m.leads["lead-review-ai"]
	for i := range lead.bees {
		if lead.bees[i].ThreadID != h.ThreadID {
			continue
		}
		lead.bees[i].ReviewState = ReviewRunning
		lead.bees[i].ReviewStartedAt = &started
		lead.bees[i].MergePatch = append([]byte(nil), patch...)
		lead.bees[i].PendingMerge = true
	}
	m.mu.Unlock()

	var req kanban.SessionRequest
	driver.onStart = func(r kanban.SessionRequest) { req = r }
	m.startMergeReview(context.Background(), "lead-review-ai", h.ThreadID)

	if req.RuntimeMode != "readonly" {
		t.Fatalf("审查会话必须以只读模式启动: %q", req.RuntimeMode)
	}
	if req.WorkDir != h.WorktreePath {
		t.Fatalf("审查会话工作目录不符: %q", req.WorkDir)
	}
	if !strings.Contains(req.Title, "Aspen") {
		t.Fatalf("标题应包含 bee 名: %q", req.Title)
	}
	bee := currentReviewBee(t, m, h.ThreadID)
	if bee.ReviewState != ReviewRunning || bee.ReviewThreadID == "" {
		t.Fatalf("审查会话未写回: %+v", bee)
	}
	if bee.ReviewPatchPath == "" || bee.ReviewPatchSHA == "" {
		t.Fatalf("审查补丁快照未写回: %+v", bee)
	}
	info, err := os.Stat(bee.ReviewPatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("审查补丁权限应为 0600: %v", info.Mode().Perm())
	}
}
