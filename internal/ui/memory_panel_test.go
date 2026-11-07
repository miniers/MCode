package mcodeui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// openMemoryPanel 用测试替身打开记忆面板：先写入测试数据，再切换面板并等待渲染。
func openMemoryPanel(t *testing.T, fake *FakeClient, s *chatState, view func(c *ui.Context), proposals ...MemoryProposal) (*ui.Tester, string) {
	t.Helper()
	tt := ui.NewTester(view, 1000, 700)
	// 面板按「当前选中会话所属项目」的路径过滤提议。项目与会话是异步加载的，
	// 必须等路径就绪再写入测试数据，否则替身会把提议挂在空路径下，面板按真实路径查不到。
	waitFor(t, tt, func() bool { return s.getSelectedProjectPath() != "" })
	path := s.getSelectedProjectPath()
	for _, p := range proposals {
		if p.ProjectPath == "" {
			p.ProjectPath = path
		}
		fake.SetMemoryProposal(p)
	}
	s.toggleMemoryPanel()
	return tt, path
}

// 待审列表：标题、内容摘要、类型、重要度、来源与线程都要渲染出来。
func TestMemoryPanelRendersPendingProposal(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:         "mp-1",
		ThreadID:   "t9",
		Title:      "项目统一使用 WAL 模式",
		Content:    "所有 SQLite 连接都开启 WAL 与 busy_timeout。",
		UnitType:   "fact",
		Importance: 0.8,
		Source:     "agent",
		Status:     "pending",
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("项目统一使用 WAL 模式") &&
			tt.HasText("所有 SQLite 连接都开启 WAL 与 busy_timeout。") &&
			tt.HasText("事实") &&
			tt.HasText("重要度 0.8") &&
			tt.HasText("来自 Agent") &&
			tt.HasText("线程 t9")
	})
}

// 点「接受并写入」调用 AcceptMemoryProposal，成功后从待审列表移除。
func TestMemoryPanelAcceptProposal(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:       "mp-accept",
		Title:    "接口一律走 Client",
		Content:  "UI 层不得直接访问 store。",
		UnitType: "preference",
		Source:   "agent",
		Status:   "pending",
	})

	waitFor(t, tt, func() bool { return tt.HasText("接口一律走 Client") })
	if err := tt.Click("接受并写入"); err != nil {
		t.Fatalf("click 接受并写入 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.MemoryAcceptCalls()
		return len(calls) == 1 && calls[0] == "mp-accept" && tt.HasText("暂无待审记忆")
	})
}

// 点「拒绝」调用 RejectMemoryProposal，成功后从待审列表移除。
func TestMemoryPanelRejectProposal(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:       "mp-reject",
		Title:    "临时调试开关",
		Content:  "debug=true 仅用于本地。",
		UnitType: "fact",
		Source:   "agent",
		Status:   "pending",
	})

	waitFor(t, tt, func() bool { return tt.HasText("临时调试开关") })
	if err := tt.Click("拒绝"); err != nil {
		t.Fatalf("click 拒绝 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.MemoryRejectCalls()
		return len(calls) == 1 && calls[0] == "mp-reject" && tt.HasText("暂无待审记忆")
	})
}

// 写入远端失败时卡片上要显示红色错误条，提议仍留在待审列表里。
func TestMemoryPanelShowsProposalError(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:       "mp-error",
		Title:    "需要远端写入的记忆",
		Content:  "写入会失败。",
		UnitType: "fact",
		Source:   "agent",
		Status:   "pending",
	})
	fake.SetMemoryErrors(nil, errors.New("未配置 Nowledge Mem"))

	waitFor(t, tt, func() bool { return tt.HasText("需要远端写入的记忆") })
	if err := tt.Click("接受并写入"); err != nil {
		t.Fatalf("click 接受并写入 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("未配置 Nowledge Mem") && tt.HasText("需要远端写入的记忆")
	})
}

// 写入空间二选一：两个选项同时可见，点「全局」后提议 scope 切换为 global。
func TestMemoryPanelProposalScopeOptions(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:       "mp-scope",
		Title:    "部署流程",
		Content:  "先跑测试再发版。",
		UnitType: "procedure",
		Source:   "agent",
		Status:   "pending",
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("当前项目") && tt.HasText("全局")
	})
	if err := tt.Click("全局"); err != nil {
		t.Fatalf("click 全局 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		st := getMemoryPanelState(s)
		if st == nil {
			return false
		}
		memoryPanelMu.Lock()
		defer memoryPanelMu.Unlock()
		for _, p := range st.proposals {
			if p.ID == "mp-scope" {
				return p.Scope == memoryScopeGlobal
			}
		}
		return false
	})
}

// 点「查看 / 编辑」进入详情编辑区：字数提示、固定类型选项，选中类型写入草稿。
func TestMemoryPanelProposalEditToggle(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:       "mp-edit",
		Title:    "编辑入口可发现",
		Content:  strings.Repeat("字", 601),
		UnitType: "fact",
		Source:   "agent",
		Status:   "pending",
	})

	waitFor(t, tt, func() bool { return tt.HasText("编辑入口可发现") })
	if err := tt.Click("查看 / 编辑"); err != nil {
		t.Fatalf("click 查看 / 编辑 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("保存修改") &&
			tt.HasText("601 字") &&
			tt.HasText("建议精简到 600 字以内：检索摘要只取前 400 字")
	})
	for _, label := range []string{"事实", "偏好", "决策", "计划", "流程", "经验", "背景", "事件"} {
		if !tt.HasText(label) {
			t.Fatalf("编辑区缺少类型选项 %q", label)
		}
	}

	// 展开后的编辑区高于可视区，先滚动到底部再点类型选项。
	tt.Scroll(176, 300, 0, 600)
	if err := tt.Click("决策"); err != nil {
		t.Fatalf("click 决策 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		st := getMemoryPanelState(s)
		if st == nil {
			return false
		}
		memoryPanelMu.Lock()
		defer memoryPanelMu.Unlock()
		return st.editUnitType == "decision"
	})
}

// 搜索页：输入关键字后点「搜索」调用 SearchMemories 并渲染结果。
func TestMemoryPanelSearch(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, path := openMemoryPanel(t, fake, s, view)
	fake.SetMemorySearchResults([]MemoryItem{{
		ID:         "mem-1",
		Title:      "部署流程约定",
		Content:    "发版前先跑 make test。",
		UnitType:   "insight",
		Importance: 0.6,
	}})
	s.switchMemoryTab(memoryTabSearch)
	waitFor(t, tt, func() bool { return tt.HasText("输入关键字后按「搜索」") })

	if err := tt.Click("搜索记忆"); err != nil {
		t.Fatalf("focus 搜索记忆 input failed: %v", err)
	}
	tt.Type("部署")
	if err := tt.Click("执行搜索"); err != nil {
		t.Fatalf("click 执行搜索 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("部署流程约定") && tt.HasText("洞见") && tt.HasText("重要度 0.6")
	})
	calls := fake.MemorySearchCalls()
	if len(calls) != 1 || calls[0][0] != path || calls[0][1] != "部署" {
		t.Fatalf("SearchMemories calls = %v, want one call with %q/部署", calls, path)
	}
}

// Working Memory 页：切到该标签后加载正文，正文只读展示。
func TestMemoryPanelWorkingMemory(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view)
	fake.SetWorkingMemory("## 今日要点\n\n- 完成记忆面板\n")
	s.switchMemoryTab(memoryTabWorking)

	waitFor(t, tt, func() bool {
		st := getMemoryPanelState(s)
		if st == nil {
			return false
		}
		memoryPanelMu.Lock()
		text := st.wmText
		memoryPanelMu.Unlock()
		return strings.Contains(text, "今日要点") && tt.HasText("今日 Working Memory（只读）")
	})
}

// 展开提议时按标题检索相似记忆：结果渲染出「合并到这条」，点击后记录合并目标。
func TestMemoryPanelSimilarMemoriesTarget(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, path := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:       "mp-similar",
		Title:    "部署流程约定",
		Content:  "发版前先跑 make test。",
		UnitType: "procedure",
		Source:   "agent",
		Status:   "pending",
	})
	fake.SetMemorySearchResults([]MemoryItem{{
		ID:       "mem-a",
		Title:    "发布前跑全量测试",
		Content:  "所有发版都必须先跑 make test 再打标签。",
		UnitType: "procedure",
	}})

	waitFor(t, tt, func() bool { return tt.HasText("查看 / 编辑") })
	if err := tt.Click("查看 / 编辑"); err != nil {
		t.Fatalf("click 查看 / 编辑 failed: %v", err)
	}

	// 展开后异步按提议标题检索，回来后渲染相似记忆与「合并到这条」。
	waitFor(t, tt, func() bool {
		calls := fake.MemorySearchCalls()
		return len(calls) == 1 &&
			calls[0][0] == path &&
			calls[0][1] == "部署流程约定" &&
			tt.HasText("相似的已有记忆") &&
			tt.HasText("发布前跑全量测试") &&
			tt.HasText("合并到这条")
	})

	// 相似记忆区块在编辑区下方，先滚到可见位置再点击。
	tt.Scroll(176, 300, 0, 10000)
	if err := tt.Click("合并到这条"); err != nil {
		t.Fatalf("click 合并到这条 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.MemoryTargetCalls()
		return len(calls) == 1 && calls[0] == [2]string{"mp-similar", "mem-a"}
	})
}

// 点「AI 精简」调用 CondenseMemory，结果只回填编辑草稿，不自动保存。
func TestMemoryPanelCondenseDraft(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view, MemoryProposal{
		ID:       "mp-condense",
		Title:    "很长的记忆标题",
		Content:  strings.Repeat("啰嗦的内容。", 80),
		UnitType: "fact",
		Source:   "agent",
		Status:   "pending",
	})

	waitFor(t, tt, func() bool { return tt.HasText("查看 / 编辑") })
	if err := tt.Click("查看 / 编辑"); err != nil {
		t.Fatalf("click 查看 / 编辑 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("AI 精简") })

	fake.SetMemoryCondenseResult("精简后的标题", "发版前先跑 make test。", nil)
	if err := tt.Click("AI 精简"); err != nil {
		t.Fatalf("click AI 精简 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		st := getMemoryPanelState(s)
		if st == nil {
			return false
		}
		memoryPanelMu.Lock()
		defer memoryPanelMu.Unlock()
		return st.editTitle == "精简后的标题" && st.editContent == "发版前先跑 make test。"
	})

	calls := fake.MemoryCondenseCalls()
	if len(calls) != 1 {
		t.Fatalf("CondenseMemory calls = %v, want one call", calls)
	}
	// 未设合并目标时 mergeWith 应为空。
	if calls[0][3] != "" {
		t.Fatalf("mergeWith = %q, want empty", calls[0][3])
	}
	// 精简结果只进草稿，不自动保存：用户仍需点「保存修改」。
	fake.mu.Lock()
	updates := len(fake.memoryUpdateCalls)
	fake.mu.Unlock()
	if updates != 0 {
		t.Fatalf("AI 精简不应自动保存，UpdateMemoryProposal 调用了 %d 次", updates)
	}
}

// openMemorySearchResults 切到搜索页并检索一次，等到给定标题都渲染出来。
func openMemorySearchResults(t *testing.T, fake *FakeClient, s *chatState, tt *ui.Tester, items ...MemoryItem) {
	t.Helper()
	fake.SetMemorySearchResults(items)
	s.switchMemoryTab(memoryTabSearch)
	waitFor(t, tt, func() bool { return tt.HasText("输入关键字后按「搜索」") })
	s.editMemorySearchQuery("部署")
	if err := tt.Click("执行搜索"); err != nil {
		t.Fatalf("click 执行搜索 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		for _, item := range items {
			if !tt.HasText(item.Title) {
				return false
			}
		}
		return true
	})
}

// 第一次点「忘掉」只进入确认态，不调用 ForgetMemory，按钮文字变成「确认忘掉」。
func TestMemoryPanelForgetNeedsConfirm(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view)
	openMemorySearchResults(t, fake, s, tt, MemoryItem{
		ID:       "mem-forget-1",
		Title:    "部署流程约定",
		Content:  "发版前先跑 make test。",
		UnitType: "procedure",
	})

	if err := tt.Click("忘掉记忆：部署流程约定"); err != nil {
		t.Fatalf("click 忘掉记忆 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认忘掉") })
	if calls := fake.MemoryForgetCalls(); len(calls) != 0 {
		t.Fatalf("第一次点击不应调用 ForgetMemory，实际 %v", calls)
	}
}

// 点「取消」后回到单步的「忘掉」，且仍不发起请求。
func TestMemoryPanelForgetCancel(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view)
	openMemorySearchResults(t, fake, s, tt, MemoryItem{
		ID:       "mem-forget-cancel",
		Title:    "部署流程约定",
		Content:  "发版前先跑 make test。",
		UnitType: "procedure",
	})

	if err := tt.Click("忘掉记忆：部署流程约定"); err != nil {
		t.Fatalf("click 忘掉记忆 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认忘掉") })
	if err := tt.Click("取消忘掉记忆：部署流程约定"); err != nil {
		t.Fatalf("click 取消 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("确认忘掉") })
	if calls := fake.MemoryForgetCalls(); len(calls) != 0 {
		t.Fatalf("取消后不应调用 ForgetMemory，实际 %v", calls)
	}
}

// 第二次点击才调用 ForgetMemory：参数带上当前项目路径与记忆 ID，这一行从结果里消失。
func TestMemoryPanelForgetConfirmed(t *testing.T) {
	fake := NewFakeClient()
	// 模拟服务端支持真删除。
	fake.SetMemoryForgetResult(true, nil)
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, path := openMemoryPanel(t, fake, s, view)
	openMemorySearchResults(t, fake, s, tt,
		MemoryItem{ID: "mem-del", Title: "要删掉的约定", Content: "旧约定。", UnitType: "fact"},
		MemoryItem{ID: "mem-keep", Title: "保留的约定", Content: "新约定。", UnitType: "fact"},
	)

	if err := tt.Click("忘掉记忆：要删掉的约定"); err != nil {
		t.Fatalf("click 忘掉记忆 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认忘掉") })
	if err := tt.Click("忘掉记忆：要删掉的约定"); err != nil {
		t.Fatalf("click 确认忘掉 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.MemoryForgetCalls()
		return len(calls) == 1 && calls[0] == [2]string{path, "mem-del"} && tt.HasText("已删除这条记忆") && !tt.HasText("要删掉的约定")
	})
	if !tt.HasText("保留的约定") {
		t.Fatalf("其它搜索结果不应被移除")
	}
}

// 服务端不支持删除时 deleted=false，提示已标记为废弃，这一行同样移除。
func TestMemoryPanelForgetFallsBackToDeprecated(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt, _ := openMemoryPanel(t, fake, s, view)
	openMemorySearchResults(t, fake, s, tt, MemoryItem{
		ID:       "mem-deprecate",
		Title:    "只能标记废弃的约定",
		Content:  "服务端不支持删除。",
		UnitType: "fact",
	})
	fake.SetMemoryForgetResult(false, nil)

	if err := tt.Click("忘掉记忆：只能标记废弃的约定"); err != nil {
		t.Fatalf("click 忘掉记忆 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("确认忘掉") })
	if err := tt.Click("忘掉记忆：只能标记废弃的约定"); err != nil {
		t.Fatalf("click 确认忘掉 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("已标记为已废弃") && !tt.HasText("只能标记废弃的约定")
	})
}

// 点「导出」调用一次 ExportMemories，保存对话框取消时不留文件。
func TestMemoryPanelExportMemories(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	old := saveMemoryExportDialog
	// 保存对话框在后台 goroutine 里调用，用带缓冲 channel 传默认路径，避免数据竞争。
	saved := make(chan string, 1)
	saveMemoryExportDialog = func(p string) (string, error) {
		saved <- p
		return "", nil // 模拟用户取消
	}
	t.Cleanup(func() { saveMemoryExportDialog = old })

	tt, _ := openMemoryPanel(t, fake, s, view)
	fake.SetMemoryExport("# 记忆\n\n- 一条记忆\n", nil)
	openMemorySearchResults(t, fake, s, tt)

	if err := tt.Click("导出记忆"); err != nil {
		t.Fatalf("click 导出记忆 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fake.MemoryExportCalls()) == 1 })

	var defaultPath string
	select {
	case defaultPath = <-saved:
	case <-time.After(2 * time.Second):
		t.Fatalf("保存对话框未被调用")
	}
	if !strings.HasPrefix(filepath.Base(defaultPath), "mcode-memories-") || filepath.Ext(defaultPath) != ".md" {
		t.Fatalf("默认文件名 = %q, want mcode-memories-*.md", defaultPath)
	}
}

// memoryExportFileName 按 YYYYMMDD-HHMMSS 拼出默认文件名。
func TestMemoryExportFileName(t *testing.T) {
	got := memoryExportFileName(time.Date(2026, 11, 1, 9, 46, 50, 0, time.UTC))
	if got != "mcode-memories-20261101-094650.md" {
		t.Fatalf("memoryExportFileName = %q", got)
	}
}

func TestMemoryPanelBackendTabs(t *testing.T) {
	for _, backend := range []string{"nmem", "local"} {
		t.Run(backend, func(t *testing.T) {
			fake := NewFakeClient()
			fake.memoryBackend = backend
			s := newChatState(fake)
			t.Cleanup(s.close)
			st := getMemoryPanelState(s)
			st.tab = memoryTabWorking
			tt := ui.NewTester(func(c *ui.Context) { renderMemoryTabs(c, s) }, 800, 200)
			if !tt.HasText("待审") || !tt.HasText("搜索") {
				t.Fatal("缺少待审或搜索标签")
			}
			if got := tt.HasText("Working Memory"); got != (backend == "nmem") {
				t.Fatalf("Working Memory 标签显示状态错误: %v", got)
			}
			if backend == "local" && snapshotMemoryPanel(s).tab != memoryTabPending {
				t.Fatal("本地后端没有回退到待审标签")
			}
		})
	}
}

// 旧收件箱一次性迁入的提议用专门的来源文案，不能退回显示原始 source。
func TestMemorySourceLabelInboxMigration(t *testing.T) {
	for _, source := range []string{"inbox-migration", " inbox-migration "} {
		if got := memorySourceLabel(source); got != "旧收件箱迁移" {
			t.Fatalf("来源 %q 的标签错误：%q", source, got)
		}
	}
}

// 本地记忆后端下标题栏入口只显示「记忆」，不再带 Nowledge 后缀。
func TestMemoryHeaderEntryLabelLocalBackend(t *testing.T) {
	fake := NewFakeClient()
	fake.memoryBackend = "local"
	s, view := buildRoot(fake)
	t.Cleanup(s.close)

	tt := ui.NewTester(view, 1200, 700)
	waitFor(t, tt, func() bool {
		for _, text := range tt.Texts() {
			if text == memoryEntryLabelLocal {
				return true
			}
		}
		return false
	})
	if tt.HasText(memoryEntryLabelDefault) {
		t.Fatalf("本地后端不应显示 Nowledge 文案：%v", tt.Texts())
	}
}
