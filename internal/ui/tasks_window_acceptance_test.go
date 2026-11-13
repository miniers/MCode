package mcodeui

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

type tasksAcceptanceClient struct{ *FakeClient }

func (c *tasksAcceptanceClient) Projects(context.Context) ([]Project, error) {
	return []Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}}, nil
}

func TestTasksAcceptanceToolbarGeometry(t *testing.T) {
	for _, width := range []int{840, 1140, 1440} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
			defer s.close()
			s.projects = []Project{{ID: "p1", Name: "项目一"}}
			getKanbanState(s).initialized = true
			tt := ui.NewTester(view, width, 720)
			tt.Frame()
			sidebar, ok := tt.Find("tasks-sidebar")
			if !ok || sidebar.W != TasksSidebarWidth {
				t.Fatalf("侧栏: %+v", sidebar)
			}
			bar, ok := tt.Find("tasks-window-toolbar")
			if !ok || bar.X != TasksSidebarWidth || bar.H != TasksToolbarHeight || bar.X+bar.W > float32(width) {
				t.Fatalf("顶栏: %+v", bar)
			}
			if _, ok := tt.Find("kanban-modal-header"); ok {
				t.Fatal("出现旧顶栏")
			}
			for _, label := range []string{"筛选任务", "kanban-view-switch-kanban", "kanban-view-switch-list", "新建任务", "新建菜单"} {
				r, ok := tt.Find(label)
				if !ok || r.X < bar.X || r.X+r.W > bar.X+bar.W || r.Y < bar.Y || r.Y+r.H > bar.Y+bar.H {
					t.Errorf("%s: %+v", label, r)
				}
			}
		})
	}
}

func TestTasksAcceptanceAttentionIgnoresSearch(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	defer s.close()
	ks := getKanbanState(s)
	ks.initialized = true
	ks.board = KanbanBoard{Tasks: []KanbanTask{{ID: "a", Title: "审批任务", Status: "failed"}}, Goals: []KanbanGoal{{ID: "g", Title: "目标", Status: "plan_review"}}}
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	if !tt.HasText("2") {
		t.Fatal("未显示待处理计数")
	}
	ks.searchText = "不匹配"
	tt.Frame()
	if !tt.HasText("2") {
		t.Fatal("搜索影响待处理计数")
	}
	ks.board.Tasks[0].Status = "done"
	tt.Frame()
	if !tt.HasText("1") || tt.HasText("2") {
		t.Fatal("处理后计数未下降")
	}
	ks.board.Goals[0].Status = "running"
	tt.Frame()
	if tt.HasText("0") || tt.HasText("1") {
		t.Fatal("零计数不应显示")
	}
}

func TestTasksAcceptanceDetailWidthPersistence(t *testing.T) {
	resetLastSelectionMergeState(t)
	initial := &kanbanState{persistTasksDetailWidth: true}
	rememberTasksDetailWidth(initial, 500)
	waitUIStateWrites()
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	defer s.close()
	ks := getKanbanState(s)
	ks.initialized = true
	ks.detailTaskID = "a"
	if ks.detailWidth != 500 {
		t.Fatalf("恢复宽度 %v", ks.detailWidth)
	}
	tt := ui.NewTester(view, 1440, 900)
	tt.Frame()
	r, ok := tt.Find("调整详情宽度")
	if !ok {
		t.Fatal("找不到分栏手柄")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x-40, y)
	tt.Release(x-40, y)
	waitUIStateWrites()
	if got := loadTasksDetailWidth(); got != 540 {
		t.Fatalf("保存宽度 %v", got)
	}
}

func TestTasksAcceptanceSearchScopeAndCreate(t *testing.T) {
	client := &tasksAcceptanceClient{NewFakeClient()}
	s, view := buildTasksRoot(client, WithTasksWindowMode("tasks"))
	defer s.close()
	s.projects = []Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}}
	ks := getKanbanState(s)
	ks.initialized = true
	ks.searchText = "任务查询"
	ks.automationSearch = "自动查询"
	tt := ui.NewTester(view, 1140, 720)
	tt.Frame()
	s.setTasksWindowView("automations")
	s.setKanbanScope("p2", false)
	s.setTasksWindowView("tasks")
	s.setKanbanScope("", true)
	s.setTasksWindowView("automations")
	tt.Frame()
	if ks.searchText != "任务查询" || ks.automationSearch != "自动查询" {
		t.Fatal("切换后搜索丢失")
	}
	if err := tt.Click("新建自动化"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	as := getAutomationState(s)
	if !as.chooseProjectOpen || as.editorOpen {
		t.Fatal("未打开项目选择")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if as.chooseProjectOpen || as.editorOpen {
		t.Fatal("取消后打开编辑器")
	}
	if err := tt.Click("新建自动化"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("选择项目 项目二"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !as.editorOpen || as.editorProjectID != "p2" || ks.scopeID != "" {
		t.Fatalf("选择项目后状态错误: %v %q %q", as.editorOpen, as.editorProjectID, ks.scopeID)
	}
	as.name = "未保存"
	s.setTasksWindowView("tasks")
	tt.Frame()
	if ks.destination != "automations" || !as.confirmDiscard {
		t.Fatal("未保护草稿")
	}
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ks.destination != "tasks" {
		t.Fatal("放弃后未切换")
	}
}

func TestTasksAcceptanceMatrix(t *testing.T) {
	for _, size := range [][2]int{{840, 520}, {1140, 720}, {1440, 900}} {
		for _, dark := range []bool{false, true} {
			for _, reduced := range []bool{false, true} {
				t.Run(fmt.Sprintf("%dx%d/dark=%t/reduceMotion=%t", size[0], size[1], dark, reduced), func(t *testing.T) {
					setAppReduceMotion(t, reduced)
					s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
					defer s.close()
					project := "一个非常长的项目名称，用于检查边界截断"
					s.projects = []Project{{ID: "p1", Name: project}, {ID: "p2", Name: "第二项目"}}
					ks := getKanbanState(s)
					ks.initialized = true
					pane, _, _ := kanbanDetailLayout(float32(size[0])-TasksSidebarWidth, ks.detailWidth)
					title := "这是一个非常长的任务标题，用于检查标题是否越过内容区域边界"
					ks.board = KanbanBoard{Tasks: []KanbanTask{{ID: "long", Title: title, Status: "todo", Column: "todo"}}}
					var transition ui.ElementTransition
					tt := ui.NewTesterWith(func(c *ui.Context) {
						if !pane {
							transition = motionDialog(c)
						} else {
							transition = motionSidePanel(c, false)
						}
						view(c)
					}, size[0], size[1], ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
					tt.SetDark(dark)
					tt.Frame()
					sidebar, ok := tt.Find("tasks-sidebar")
					if !ok || sidebar.W != TasksSidebarWidth {
						t.Fatalf("侧栏宽度: %+v", sidebar)
					}
					toolbar, ok := tt.Find("tasks-window-toolbar")
					if !ok || toolbar.X != TasksSidebarWidth || toolbar.H != TasksToolbarHeight {
						t.Fatalf("顶栏: %+v", toolbar)
					}
					if _, ok := tt.Find("kanban-modal-header"); ok {
						t.Fatal("存在旧顶栏")
					}
					bars := 0
					for _, text := range tt.Texts() {
						if text == "tasks-window-toolbar" {
							bars++
						}
					}
					if bars != 1 {
						t.Fatalf("顶栏数量 = %d，期望 1", bars)
					}
					row, ok := tt.Find(project)
					if !ok || row.X+row.W > sidebar.X+sidebar.W {
						t.Fatalf("项目行越过侧栏: row=%+v sidebar=%+v", row, sidebar)
					}
					column, ok := tt.Find("kanban-column-todo")
					if !ok {
						t.Fatal("找不到待办列")
					}
					card, ok := tt.Find("kanban-task-card-long")
					if !ok || card.X+card.W > column.X+column.W {
						t.Fatalf("任务卡越过列: card=%+v column=%+v", card, column)
					}
					titleBounds, ok := tt.Find(title)
					if !ok || titleBounds.X+titleBounds.W > card.X+card.W {
						t.Fatalf("任务标题越过卡片: title=%+v card=%+v", titleBounds, card)
					}
					if err := tt.Click("kanban-task-card-long"); err != nil {
						t.Fatal(err)
					}
					detail, ok := tt.Find("kanban-task-detail")
					if !ok {
						t.Fatal("找不到任务详情")
					}
					t.Run("详情层级", func(t *testing.T) {
						if pane {
							if detail.Y < toolbar.Y+toolbar.H || detail.X < sidebar.X+sidebar.W {
								t.Fatalf("详情覆盖顶栏或侧栏: detail=%+v toolbar=%+v sidebar=%+v", detail, toolbar, sidebar)
							}
							return
						}
						// 详情内容填满 sheet；测试器只能查找文本或 Label，不能查找 key。
						sheet, ok := tt.Find("kanban-task-detail")
						if !ok || sheet.X < 0 || sheet.Y < 0 || sheet.X+sheet.W > float32(size[0]) || sheet.Y+sheet.H > float32(size[1]) {
							t.Fatalf("详情 sheet 不在窗口内: sheet=%+v found=%t", sheet, ok)
						}
						if _, ok := tt.Find("调整详情宽度"); ok {
							t.Fatal("sheet 打开时出现详情分栏")
						}
						details := 0
						for _, text := range tt.Texts() {
							if text == "kanban-task-detail" {
								details++
							}
						}
						if details != 1 {
							t.Fatalf("详情数量 = %d，期望 1", details)
						}
						if reduced {
							w, h := min(settingsSheetWidth, float32(size[0])-Space8), float32(size[1])-Space8
							if sheet.X != (float32(size[0])-w)/2 || sheet.Y != (float32(size[1])-h)/2 || sheet.W != w || sheet.H != h {
								t.Errorf("减少动效时 sheet 第一帧未到终点: %+v", sheet)
							}
						}
						for _, method := range []string{"Escape", "遮罩"} {
							t.Run(method, func(t *testing.T) {
								if _, ok := tt.Find("kanban-task-detail"); !ok {
									if err := tt.Click("kanban-task-card-long"); err != nil {
										t.Fatal(err)
									}
									if _, ok := tt.Find("kanban-task-detail"); !ok {
										t.Fatal("重新打开后找不到详情 sheet")
									}
								}
								if method == "Escape" {
									tt.Key(0, ui.KeyEscape)
								} else {
									tt.ClickAt(1, 1)
								}
								tt.Frame()
								if _, ok := tt.Find("kanban-task-detail"); ok {
									t.Fatal("详情 sheet 未关闭")
								}
								if got, ok := tt.Find("tasks-sidebar"); !ok || got != sidebar {
									t.Errorf("关闭后侧栏位置变化: before=%+v after=%+v found=%t", sidebar, got, ok)
								}
								if got, ok := tt.Find("tasks-window-toolbar"); !ok || got != toolbar {
									t.Errorf("关闭后顶栏位置变化: before=%+v after=%+v found=%t", toolbar, got, ok)
								}
							})
						}
					})
					if reduced {
						if transition.Enter != nil || transition.Duration >= 0 {
							t.Errorf("减少动效时详情进入动画未立即结束: %+v", transition)
						}
						if pane && detail.X+detail.W != float32(size[0]) {
							t.Errorf("减少动效时详情未到终点: %+v", detail)
						}
					}
				})
			}
		}
	}
}

func TestTasksAcceptanceEmptyAndNoResults(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	defer s.close()
	s.projects = []Project{{ID: "p1", Name: "项目一"}}
	ks := getKanbanState(s)
	ks.initialized = true
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	if !tt.HasText("还没有任务") {
		t.Fatal("未显示空状态")
	}
	ks.board.Tasks = []KanbanTask{{ID: "a", Title: "现有任务", Status: "todo"}}
	ks.searchText = "无匹配"
	tt.Frame()
	if !tt.HasText("未找到任务") {
		t.Fatal("未显示搜索无结果状态")
	}
}

func TestTasksAcceptanceProjectError(t *testing.T) {
	s, view := buildTasksRoot(nil, WithTasksWindowMode("tasks"))
	defer s.close()
	s.projects = []Project{{ID: "p1", Name: "项目一"}}
	ks := getKanbanState(s)
	ks.initialized, ks.err = true, "读取失败"
	tt := ui.NewTester(view, 840, 520)
	tt.Frame()
	if !tt.HasText("读取失败") {
		t.Fatal("未显示错误状态")
	}
	if _, ok := tt.Find("tasks-sidebar"); !ok {
		t.Fatal("错误状态下侧栏不可用")
	}
}

func TestTasksAcceptanceCloseReleasesState(t *testing.T) {
	client := NewFakeClient()
	s, _ := buildTasksRoot(client, WithTasksWindowMode("tasks"))
	ks := getKanbanState(s)
	ks.initialized = true
	s.startKanbanWatch("")
	client.mu.Lock()
	before := len(client.kanbanWatchers)
	client.mu.Unlock()
	if before != 1 {
		s.close()
		t.Fatalf("关闭前订阅数量 = %d，期望 1", before)
	}
	s.close()
	client.mu.Lock()
	after := len(client.kanbanWatchers)
	client.mu.Unlock()
	if after != 0 {
		t.Errorf("关闭后仍有 %d 个看板订阅", after)
	}
	kanbanKeyboardMu.Lock()
	_, keyboard := kanbanKeyboardStates[s]
	kanbanKeyboardMu.Unlock()
	kanbanBatchMu.Lock()
	_, batch := kanbanBatchStates[s]
	kanbanBatchMu.Unlock()
	automationMu.Lock()
	_, automation := automationStates[s]
	automationMu.Unlock()
	if keyboard || batch || automation {
		t.Fatalf("关闭后状态未释放: keyboard=%v batch=%v automation=%v", keyboard, batch, automation)
	}
}

func TestTasksAcceptanceFallbackSameShell(t *testing.T) {
	s, view := buildRoot(NewFakeClient())
	s.openWorkspace("tasks")
	s.projects = []Project{{ID: "p1", Name: "项目一"}}
	getKanbanState(s).initialized = true
	tt := ui.NewTester(view, 1140, 720)
	tt.Frame()
	if _, ok := tt.Find("tasks-sidebar"); !ok {
		t.Fatal("回退路径未显示任务侧栏")
	}
	if _, ok := tt.Find("tasks-window-toolbar"); !ok {
		t.Fatal("回退路径未显示单条顶栏")
	}
	for _, old := range []string{"kanban-modal-header", "项目", "目标筛选"} {
		if _, ok := tt.Find(old); ok {
			t.Fatalf("回退路径出现旧控件 %q", old)
		}
	}
}
