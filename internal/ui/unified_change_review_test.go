package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

func testUnifiedReviewFiles() []git.FileDiff {
	return []git.FileDiff{
		{
			OldPath: "a.go",
			NewPath: "a.go",
			Status:  "modified",
			Hunks: []git.Hunk{
				{
					Header:   "@@ -1,2 +1,2 @@",
					OldStart: 1,
					OldLines: 2,
					NewStart: 1,
					NewLines: 2,
					Lines: []git.Line{
						{Kind: ' ', Text: "package main", OldNo: 1, NewNo: 1},
						{Kind: '-', Text: "var oldVal = 1", OldNo: 2},
						{Kind: '+', Text: "var newVal = 2", NewNo: 2},
					},
				},
			},
		},
		{
			NewPath: "b.ts",
			Status:  "added",
			Hunks: []git.Hunk{
				{
					Header:   "@@ -0,0 +1,1 @@",
					OldStart: 0,
					OldLines: 0,
					NewStart: 1,
					NewLines: 1,
					Lines: []git.Line{
						{Kind: '+', Text: "export const ready = true;", NewNo: 1},
					},
				},
			},
		},
	}
}

func TestUnifiedChangeReviewSplitToggleExpandCollapseAndRead(t *testing.T) {
	files := testUnifiedReviewFiles()
	st := newChangeReviewState()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c.Key("test-review-scroll")).Fill().Children(func() {
			renderChangeReviewDiff(c, "test-review", st, changeReviewDiffOpts{
				Files:              files,
				MaxExpandedDefault: 3,
				ShowReadToggle:     true,
			})
		})
	}, 800, 600)
	tt.Frame()

	if !tt.HasText("2 个文件") || !tt.HasText("+2") || !tt.HasText("-1") || !tt.HasText("已查看 0/2") {
		t.Fatalf("工具条统计不完整: %v", tt.Texts())
	}
	if !tt.HasText("修改") || !tt.HasText("新增") || !tt.HasText("a.go") || !tt.HasText("b.ts") {
		t.Fatalf("文件头状态或路径缺失: %v", tt.Texts())
	}
	if !tt.HasText("@@ -1,2 +1,2 @@") || !tt.HasText("@@ -0,0 +1,1 @@") {
		t.Fatalf("多文件 DiffView hunk 未渲染: %v", tt.Texts())
	}

	st.mu.Lock()
	viewA := st.views["a.go"]
	viewB := st.views["b.ts"]
	st.mu.Unlock()
	if viewA == nil || viewB == nil {
		t.Fatalf("未缓存 DiffState: a=%v b=%v", viewA, viewB)
	}
	if viewA.Inline != editor.DiffInlineWordAlt || viewA.Indicators != editor.DiffClassic || viewA.Layout != editor.DiffUnified {
		t.Fatalf("DiffState 默认配置错误: inline=%v indicators=%v layout=%v", viewA.Inline, viewA.Indicators, viewA.Layout)
	}
	if viewA.New.Language() == nil || viewB.New.Language() == nil {
		t.Fatalf("未按路径启用语法高亮: a=%v b=%v", viewA.New.Language(), viewB.New.Language())
	}

	// 再次渲染一帧，验证缓存复用同一 *editor.DiffState 实例
	tt.Frame()
	st.mu.Lock()
	reusedA := st.views["a.go"]
	st.mu.Unlock()
	if reusedA != viewA {
		t.Fatal("重复渲染未复用缓存的 *editor.DiffState")
	}

	// 切换并排视图（宽屏 >= 520）
	if err := tt.Click("并排"); err != nil {
		t.Fatalf("点击「并排」失败: %v", err)
	}
	tt.Frame()
	st.mu.Lock()
	splitOn := st.split
	layoutWide := st.views["a.go"].Layout
	reusedAfterSplit := st.views["a.go"]
	st.mu.Unlock()
	if !splitOn || layoutWide != editor.DiffSplit || reusedAfterSplit != viewA {
		t.Fatalf("并排切换失败: split=%v layout=%v reused=%v", splitOn, layoutWide, reusedAfterSplit == viewA)
	}

	// 窄宽 < 520 自动降级统一视图，但保留 split 偏好；加宽后恢复并排
	tt.SetSize(420, 600)
	tt.Frame()
	tt.Frame()
	st.mu.Lock()
	layoutNarrow := st.views["a.go"].Layout
	splitKept := st.split
	st.mu.Unlock()
	if !splitKept || layoutNarrow != editor.DiffUnified {
		t.Fatalf("窄屏未自动降级统一视图: split=%v layout=%v", splitKept, layoutNarrow)
	}

	tt.SetSize(800, 600)
	tt.Frame()
	tt.Frame()
	st.mu.Lock()
	layoutRestored := st.views["a.go"].Layout
	st.mu.Unlock()
	if layoutRestored != editor.DiffSplit {
		t.Fatalf("恢复宽屏后未回到并排视图: layout=%v", layoutRestored)
	}

	// 切回统一视图
	if err := tt.Click("统一"); err != nil {
		t.Fatalf("点击「统一」失败: %v", err)
	}
	tt.Frame()
	st.mu.Lock()
	layoutUnified := st.views["a.go"].Layout
	st.mu.Unlock()
	if layoutUnified != editor.DiffUnified {
		t.Fatalf("切回统一视图失败: layout=%v", layoutUnified)
	}

	// 全部收起 / 全部展开
	if err := tt.Click("全部收起"); err != nil {
		t.Fatalf("点击「全部收起」失败: %v", err)
	}
	tt.Advance(DurBase + 50*time.Millisecond)
	tt.Frame()
	if tt.HasText("@@ -1,2 +1,2 @@") || tt.HasText("@@ -0,0 +1,1 @@") {
		t.Fatalf("全部收起后不应继续渲染 DiffView 正文: %v", tt.Texts())
	}

	if err := tt.Click("全部展开"); err != nil {
		t.Fatalf("点击「全部展开」失败: %v", err)
	}
	tt.Advance(DurBase + 50*time.Millisecond)
	tt.Frame()
	if !tt.HasText("@@ -1,2 +1,2 @@") || !tt.HasText("@@ -0,0 +1,1 @@") {
		t.Fatalf("全部展开后应重新显示 DiffView 正文: %v", tt.Texts())
	}

	// 单文件「已查看」进度计数
	if err := tt.Click("标记为已查看"); err != nil {
		t.Fatalf("点击「标记为已查看」失败: %v", err)
	}
	tt.Advance(DurSlow)
	tt.Frame()
	if !tt.HasText("已查看 1/2") {
		t.Fatalf("已查看计数未更新为 1/2: %v", tt.Texts())
	}

	// reset 清理缓存与单文件状态
	st.reset()
	st.mu.Lock()
	if len(st.views) != 0 || len(st.viewed) != 0 || st.diffKey != "" {
		st.mu.Unlock()
		t.Fatalf("reset 未清空状态: views=%d viewed=%d diffKey=%q", len(st.views), len(st.viewed), st.diffKey)
	}
	st.mu.Unlock()
}

func TestUnifiedChangeReviewSwarmAndKanbanIntegration(t *testing.T) {
	t.Run("swarm", func(t *testing.T) {
		for _, reduced := range []bool{false, true} {
			t.Run(fmt.Sprintf("reduce=%v", reduced), func(t *testing.T) {
				setAppReduceMotion(t, reduced)
				fake := NewFakeClient()
				s := newChatState(fake)
				t.Cleanup(s.close)
				s.selectedThreadID = "lead-1"

				st := SwarmState{
					LeadThreadID: "lead-1",
					Available:    true,
					Enabled:      true,
					Bees: []SwarmBee{
						{
							ThreadID:       "bee-1",
							Name:           "Bee1",
							Status:         "completed",
							PendingMerge:   true,
							Acceptance:     []string{"go test ./internal/ui"},
							AcceptanceStep: 1,
						},
					},
				}
				fake.SetSwarmState("lead-1", st)

				hp := getSwarmPanelState(s)
				swarmPanelMu.Lock()
				hp.open = true
				hp.loadedFor = "lead-1"
				hp.reviewOpen = true
				hp.reviewBeeID = "bee-1"
				hp.reviewFiles = testUnifiedReviewFiles()[:1]
				swarmPanelMu.Unlock()

				builds := 0
				tt := ui.NewTesterWith(func(c *ui.Context) {
					builds++
					s.init(c)
					if st.Bees[0].AcceptanceStep > 0 && st.Bees[0].AcceptanceStartedAt.IsZero() {
						st.Bees[0].AcceptanceStartedAt = c.Now().Truncate(time.Second)
					}
					ui.Scroll(c.Key("swarm-test-scroll")).Fill().Children(func() {
						renderSwarmMergeReview(c, s, st)
					})
				}, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
				tt.Frame()

				if !tt.HasText("验收 1/1 · go test ./internal/ui · 0s") {
					t.Fatalf("Swarm 审查未展示实时验收进度: %v", tt.Texts())
				}
				if !tt.HasText("@@ -1,2 +1,2 @@") || !tt.HasText("批准合并") || !tt.HasText("放弃") {
					t.Fatalf("Swarm 审查缺少现代 DiffView 或审批操作: %v", tt.Texts())
				}

				if err := tt.Click("并排"); err != nil {
					t.Fatalf("Swarm 切换并排失败: %v", err)
				}
				tt.Frame()
				tt.Frame()
				swarmPanelMu.Lock()
				cr := hp.changeReview
				swarmPanelMu.Unlock()
				cr.mu.Lock()
				splitLayout := cr.views["a.go"].Layout
				cr.mu.Unlock()
				if splitLayout != editor.DiffSplit {
					t.Fatalf("Swarm 宽屏未切换到并排 DiffView: layout=%v", splitLayout)
				}

				// 验收结束：卸载动态扫光与计时，推进时钟后界面静止不重绘
				st.Bees[0].AcceptanceStep = 0
				tt.Frame()
				tt.Advance(2 * time.Second)
				if tt.HasText("验收 1/1") || !tt.HasText("验收命令（1 条）：go test ./internal/ui") {
					t.Fatalf("验收结束后未切换为静态摘要: %v", tt.Texts())
				}
				settledBuilds := builds
				tt.Advance(2 * time.Second)
				if tt.FramePending() || builds != settledBuilds {
					t.Fatalf("Swarm 审查静止后仍在重绘: pending=%v builds=%d->%d", tt.FramePending(), settledBuilds, builds)
				}
			})
		}
	})

	t.Run("kanban", func(t *testing.T) {
		for _, reduced := range []bool{false, true} {
			t.Run(fmt.Sprintf("reduce=%v", reduced), func(t *testing.T) {
				setAppReduceMotion(t, reduced)
				fake := NewFakeClient()
				task := KanbanTask{
					ID:         "task-k1",
					ProjectID:  "p1",
					Title:      "看板现代 Diff 审查",
					ThreadID:   "thread-k1",
					Status:     "review",
					Column:     "review",
					CanReview:  true,
					CanApprove: true,
					Revision:   1,
				}
				fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
				fake.SetThreadDiff("thread-k1", "", ThreadDiff{
					Truncated: true,
					Diff: `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,2 @@
 package main
-var x = 1
+var x = 2
`,
				})

				s := newChatState(fake)
				t.Cleanup(s.close)
				ks := getKanbanState(s)
				kanbanMu.Lock()
				ks.board = KanbanBoard{Tasks: []KanbanTask{task}}
				kanbanMu.Unlock()

				builds := 0
				tt := ui.NewTesterWith(func(c *ui.Context) {
					builds++
					s.init(c)
					s.applyWorkspaceUpdates()
					renderTaskDetailContent(c, s, ks)
				}, 900, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})

				s.openTaskDetail(task)
				waitFor(t, tt, func() bool {
					return tt.HasText("main.go") && tt.HasText("@@ -1,2 +1,2 @@") && tt.HasText("改动过多，已截断")
				})
				if _, ok := tt.Find("kanban-task-detail-accept"); !ok {
					t.Fatal("缺少 kanban-task-detail-accept")
				}
				if _, ok := tt.Find("kanban-task-detail-reject"); !ok {
					t.Fatal("缺少 kanban-task-detail-reject")
				}

				if err := tt.Click("并排"); err != nil {
					t.Fatalf("Kanban 切换并排失败: %v", err)
				}
				tt.Frame()
				tt.Frame()

				kanbanMu.Lock()
				dr := ks.detailReview
				kanbanMu.Unlock()
				dr.mu.Lock()
				splitLayout := dr.views["main.go"].Layout
				dr.mu.Unlock()
				if splitLayout != editor.DiffSplit {
					t.Fatalf("Kanban 宽屏未切换到并排 DiffView: layout=%v", splitLayout)
				}

				// 动画/重排结束后推进时钟，断言静止时不重绘
				tt.Advance(2 * time.Second)
				tt.Frame()
				settledBuilds := builds
				tt.Advance(2 * time.Second)
				if tt.FramePending() || builds != settledBuilds {
					t.Fatalf("Kanban 审查静止后仍在重绘: pending=%v builds=%d->%d", tt.FramePending(), settledBuilds, builds)
				}
			})
		}
	})
}
