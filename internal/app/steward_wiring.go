package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/mcp"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/steward"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

const (
	// stewardProjectName 是总管主目录对应项目的名称。
	stewardProjectName = "总管"
	// stewardEnsureTimeout 是启动时创建总管会话的超时：建会话要走一次完整的
	// CLI 首轮启动，不能无限期挂着，失败时下一轮唤醒会再次 Ensure。
	stewardEnsureTimeout = 3 * time.Minute
)

// errOwnerNotifierUnavailable 是桌面通知未接入时 notify_owner 返回的错误。
// 远程端与无桌面环境的启动方式不会接入通知器，总管据此知道汇报没有真正送达用户。
var errOwnerNotifierUnavailable = errors.New("桌面通知未接入")

// ErrOwnerNotifySuppressed 表示桌面通知被前台抑制或落在安静时段：通知器没有弹出任何东西，
// 用户也不会看到。通知器返回它时，notifyOwner 会告诉总管直接在主聊天里说一遍。
var ErrOwnerNotifySuppressed = errors.New("桌面通知被抑制（应用在前台或处于安静时段）")

// errOwnerNotifyHidden 是桌面通知被抑制、网页推送也没送出时 notify_owner 返回的错误：
// 它要求总管改在主聊天里直接告诉用户，而不是以为已经汇报过了。
var errOwnerNotifyHidden = errors.New("用户正在看 mcode（或处于安静时段），桌面通知没有发出；请直接在主聊天里告诉用户")

// stewardWiringOptions 是总管接线的构造参数。
type stewardWiringOptions struct {
	// Attention 是待处理事件服务，总管从它读事件并订阅变更。
	Attention *attention.Service
	// Backend 提供会话列表、会话状态与项目增改。
	Backend *remote.MemoryBackend
	// Settings 持久化总管会话 ID 与唤醒计数。
	Settings *store.Store
	// DataDir 是应用数据目录，用于解析总管主目录与现读配置。
	DataDir string
	// Kanban 是看板服务：话题确认开工时建 Goal、规划待审时推进开工都走它。
	Kanban *kanban.Service
	// Now 提供当前时间，测试可注入；为空时使用 time.Now。
	Now func() time.Time
	// RecentBees 返回主会话最近派出的子代理（新到旧），供换新前言列出；为空时前言不带这一小节。
	RecentBees func(leadThreadID string) []steward.RotationBee
}

// newStewardRuntime 构造总管运行时。配置每次调用都从数据目录现读，改设置后无需重启；
// 读配置失败时返回零值（未启用），避免配置损坏时误建总管会话。
func newStewardRuntime(opts stewardWiringOptions) *steward.Runtime {
	return steward.New(steward.Deps{
		Events:   stewardAttentionDep(opts.Attention),
		Sessions: opts.Backend,
		Settings: opts.Settings,
		Config: func() config.StewardConfig {
			c, err := config.Load(opts.DataDir)
			if err != nil {
				log.Printf("steward: 读取配置失败，按未启用处理: %v", err)
				return config.StewardConfig{}
			}
			return c.Orchestration.Steward
		},
		DataDir: opts.DataDir,
		EnsureHomeProject: func(ctx context.Context, dir string) (string, error) {
			return ensureStewardProject(ctx, opts.Backend, dir)
		},
		ProjectRoot: func(ctx context.Context, projectRef string) (string, string, error) {
			return resolveStewardProject(ctx, opts.Backend, projectRef)
		},
		Kanban:     stewardKanbanDep(opts.Kanban),
		Names:      stewardNames{backend: opts.Backend},
		RecentBees: opts.RecentBees,
		Now:        opts.Now,
		// 用户刚在总管会话里说过话时这一批唤醒推迟，与动作护栏里「10 分钟内发过消息」共用一份判定。
		UserActedRecently: stewardUserActedRecently(opts.Backend, stewardClock(opts.Now)),
	})
}

// stewardClock 归一当前时间来源：未注入时用 time.Now。
func stewardClock(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return time.Now
}

// stewardUserActedRecently 是总管「别插话」的判定：用户 10 分钟内在总管会话里发过消息，
// 或总管会话正在跑一轮时返回 true——这时总管不该被唤醒插话，事件留在积压里等用户停下来。
// 用户消息的判定复用 stewardUserMessagedRecently，与动作护栏（checkStewardThreadTarget）同一口径。
func stewardUserActedRecently(backend *remote.MemoryBackend, now func() time.Time) func(ctx context.Context, threadID string) bool {
	return func(ctx context.Context, threadID string) bool {
		threadID = strings.TrimSpace(threadID)
		if backend == nil || threadID == "" {
			return false
		}
		summary, err := backend.GetThread(ctx, threadID)
		if err != nil {
			var rpcErr *wire.RPCError
			// 会话还没建或已被删：没什么可插话的，不算用户在操作。
			if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeResourceNotFound {
				log.Printf("steward: 查询总管会话 %s 状态失败: %v", threadID, err)
			}
			return false
		}
		if summary.IsRunning || summary.SwarmBusy {
			return true
		}
		return stewardUserMessagedRecently(summary.LastUserMessageAt, now())
	}
}

// stewardAttentionDep 把待处理事件服务转成总管要的接口；服务未构造时返回 nil 接口，
// 避免 typed nil 被当成已接线，在换新前言里调用到空指针。
func stewardAttentionDep(service *attention.Service) steward.Events {
	if service == nil {
		return nil
	}
	return service
}

// stewardKanbanDep 把看板服务转成总管要的接口；服务未构造时返回 nil 接口，
// 避免 typed nil 被当成已接线而在调用时崩在空指针上。
func stewardKanbanDep(service *kanban.Service) steward.Kanban {
	if service == nil {
		return nil
	}
	return service
}

// stewardNames 用会话后端实现总管的可选改名能力：
// 会话标题走 threads.update（沿用 RenameThread 路径的持久化与广播），项目名走项目登记。
type stewardNames struct {
	backend *remote.MemoryBackend
}

// ThreadTitle 返回会话当前标题。
func (n stewardNames) ThreadTitle(ctx context.Context, threadID string) (string, error) {
	if n.backend == nil {
		return "", errors.New("会话后端未接入")
	}
	thread, err := n.backend.GetThread(ctx, threadID)
	if err != nil {
		return "", err
	}
	return thread.Title, nil
}

// RenameThread 改会话标题；后端会写穿到 store 并把新标题推给界面，标题栏与搜索随之更新。
func (n stewardNames) RenameThread(ctx context.Context, threadID, title string) error {
	if n.backend == nil {
		return errors.New("会话后端未接入")
	}
	newTitle := title
	_, err := n.backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: threadID, Title: &newTitle})
	return err
}

// HomeProjectName 返回主目录对应项目的名称；没有对应项目时返回空串。
func (n stewardNames) HomeProjectName(ctx context.Context, dir string) (string, error) {
	project, ok, err := findStewardProject(ctx, n.backend, dir)
	if err != nil || !ok {
		return "", err
	}
	return project.Name, nil
}

// RenameHomeProject 把主目录对应项目改名；没有对应项目时什么也不做。
// 只看清理后的路径是否等于总管主目录，因此不会碰到其他项目。
func (n stewardNames) RenameHomeProject(ctx context.Context, dir, name string) error {
	project, ok, err := findStewardProject(ctx, n.backend, dir)
	if err != nil || !ok {
		return err
	}
	if project.Name == name {
		return nil
	}
	project.Name = name
	n.backend.AddProject(project)
	return nil
}

// findStewardProject 按清理后的路径查找已登记的项目；没登记时 ok 为 false。
func findStewardProject(ctx context.Context, backend *remote.MemoryBackend, dir string) (wire.ProjectSummary, bool, error) {
	if backend == nil {
		return wire.ProjectSummary{}, false, errors.New("会话后端未接入")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return wire.ProjectSummary{}, false, fmt.Errorf("解析总管主目录失败: %w", err)
	}
	cleanDir := filepath.Clean(absDir)
	projects, err := backend.ListProjects(ctx)
	if err != nil {
		return wire.ProjectSummary{}, false, fmt.Errorf("列出项目失败: %w", err)
	}
	for _, project := range projects.Projects {
		if project.Path != "" && filepath.Clean(project.Path) == cleanDir {
			return project, true, nil
		}
	}
	return wire.ProjectSummary{}, false, nil
}

// resolveStewardProject 把项目 ID 或目录解析成项目 ID 与项目根目录。
// 总管开话题时用它当会话的 ProjectID 与 WorkDir：模型可能写 ID，也可能写它从对话里看到的路径，
// 两种都认。先按 ID 精确匹配，再按清理后的路径匹配，都不命中就报错，让总管去问用户是哪个项目。
func resolveStewardProject(ctx context.Context, backend *remote.MemoryBackend, projectRef string) (string, string, error) {
	projectRef = strings.TrimSpace(projectRef)
	if projectRef == "" {
		return "", "", errors.New("缺少项目 ID 或目录")
	}
	if backend == nil {
		return "", "", errors.New("会话后端未接入")
	}
	list, err := backend.ListProjects(ctx)
	if err != nil {
		return "", "", err
	}
	for _, project := range list.Projects {
		if project.ID == projectRef {
			return project.ID, project.Path, nil
		}
	}
	cleaned := filepath.Clean(projectRef)
	for _, project := range list.Projects {
		if project.Path != "" && filepath.Clean(project.Path) == cleaned {
			return project.ID, project.Path, nil
		}
	}
	return "", "", fmt.Errorf("找不到项目 %s", projectRef)
}

// stewardIdentitySection 返回总管的身份段：运行时未接线或该会话不是总管时为空。
// 提示词装饰器每一轮都调它，把结果填进 policy.Sections.Steward。
func stewardIdentitySection(rt *steward.Runtime, threadID string) string {
	if rt == nil {
		return ""
	}
	return rt.IdentitySection(threadID)
}

// stewardHandoffSection 返回普通会话里的「可以交给总管」提示段：总管未启用、
// 或该会话本身是总管、话题会话时返回空串。bee 子会话由调用方另行排除。
// 名字随时可改，因此每次渲染都现读配置，与侧边栏、设置页显示的名字一致。
func stewardHandoffSection(rt *steward.Runtime, dataDir, threadID string) string {
	if rt == nil || strings.TrimSpace(threadID) == "" {
		return ""
	}
	if rt.IsSteward(threadID) || rt.IsTopic(threadID) {
		return ""
	}
	c, err := config.Load(dataDir)
	if err != nil {
		return ""
	}
	cfg := c.Orchestration.Steward
	if !cfg.Enabled {
		return ""
	}
	return steward.OrdinaryHandoffSection(cfg.Label())
}

// stewardSessionRotator 把总管的换新判定接给会话后端：只有总管主会话才会问阈值与前言，
// 其他会话直接不换新，不必为它们读配置。Manual（用户主动分割）不看阈值：
// 总管会话给分割版前言，其它会话（后端能力对所有会话通用）同意换新但不带前言。
func stewardSessionRotator(rt *steward.Runtime) remote.SessionRotator {
	return func(ctx context.Context, info remote.SessionRotationInfo) (bool, string) {
		if rt == nil {
			return info.Manual, ""
		}
		if info.Manual {
			if rt.IsSteward(info.ThreadID) {
				return true, rt.SplitPreamble(ctx)
			}
			return true, ""
		}
		if !rt.IsSteward(info.ThreadID) {
			return false, ""
		}
		if !rt.ShouldRotate(info.ThreadID, info.Idle, info.ContextTokens) {
			return false, ""
		}
		return true, rt.RotationPreamble(ctx)
	}
}

// stewardSplitCounter 把总管的交接计数接给会话后端：只有总管主会话才在分割线上写计数，
// 其它会话与读到一半失败的情况都退回基础文案。
func stewardSplitCounter(rt *steward.Runtime) remote.SplitCounter {
	return func(ctx context.Context, threadID string) (remote.SplitCounts, bool) {
		if rt == nil || !rt.IsSteward(threadID) {
			return remote.SplitCounts{}, false
		}
		counts, ok := rt.HandoffCounts(ctx)
		if !ok {
			return remote.SplitCounts{}, false
		}
		return remote.SplitCounts{Ledger: counts.Ledger, Topics: counts.Topics, Pending: counts.Pending}, true
	}
}

// stewardRecentBees 把编排管理器转成总管要的「最近子代理」查询：管理器和它返回的
// 函数都保持 nil 而不是 typed nil，未接编排时总管按未接线处理，不会调用到空指针。
func stewardRecentBees(m *swarm.Manager) func(leadThreadID string) []steward.RotationBee {
	if m == nil {
		return nil
	}
	return func(leadThreadID string) []steward.RotationBee {
		return rotationBeeLines(m.State(leadThreadID).Bees)
	}
}

// rotationBeeLines 把编排状态里的子代理转成换新前言的一行行：按开始时间新到旧排，
// 最多取 steward.RotationBeesMax 只。入参切片是编排状态的快照，这里复制后再排，不原地改动。
func rotationBeeLines(bees []swarm.Bee) []steward.RotationBee {
	if len(bees) == 0 {
		return nil
	}
	sorted := make([]swarm.Bee, len(bees))
	copy(sorted, bees)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].StartedAt.After(sorted[j].StartedAt)
	})
	if len(sorted) > steward.RotationBeesMax {
		sorted = sorted[:steward.RotationBeesMax]
	}
	out := make([]steward.RotationBee, 0, len(sorted))
	for _, bee := range sorted {
		out = append(out, steward.RotationBee{
			Name:         bee.Name,
			Task:         bee.Task,
			Status:       rotationBeeStatus(bee),
			LinesAdded:   bee.LinesAdded,
			LinesDeleted: bee.LinesDeleted,
		})
	}
	return out
}

// rotationBeeStatus 把子代理的运行时状态翻成中文：合并失败与已打断优先于运行状态，
// 其余按失败、完成、已停止、运行中归类。
func rotationBeeStatus(bee swarm.Bee) string {
	switch {
	case bee.MergeError != "":
		return "合并失败"
	case bee.Interrupted:
		return "已打断"
	case bee.Status == swarm.StatusFailed:
		return "失败"
	case bee.Status == swarm.StatusCompleted:
		return "已完成"
	case bee.Status == swarm.StatusStopped:
		return "已停止"
	default:
		return "运行中"
	}
}

// wireSteward 把总管接进已构造好的 App：注册会话排除判定、MCP 钩子并启动运行时；
// 配置已启用时在后台创建一次总管会话，用户启用后能立刻在侧边栏看到它。
// 返回运行时供调用方在清理阶段停止；依赖缺失时返回 nil，此时总管完全不存在。
func wireSteward(a *App, dataDir string) *steward.Runtime {
	if a == nil || a.backend == nil || a.store == nil || a.attention == nil {
		return nil
	}
	rt := newStewardRuntime(stewardWiringOptions{
		Attention: a.attention,
		Backend:   a.backend,
		Settings:  a.store,
		DataDir:   dataDir,
		Kanban:    a.kanban,
		// a.swarm 在 App 构造时已就位，这里取它的管理器；闭包在调用时才读编排状态，
		// 管理器为空时返回 nil，前言就不带子代理小节。
		RecentBees: stewardRecentBees(a.swarm),
	})
	a.steward = rt
	// 总管主会话空闲够久或上下文够大时，下一轮丢掉旧 provider 会话、带账本与话题索引另开一条。
	a.backend.SetSessionRotator(stewardSessionRotator(rt))
	// 用户点分割时把账本与话题的条数写进分割线文案的第二行。
	a.backend.SetSplitCounter(stewardSplitCounter(rt))
	if a.client != nil {
		a.client.SetSteward(rt)
	}
	// 总管自己的会话不产生待处理事件，否则它会把自己的活动当成要分诊的事件；
	// 但它自己出错或额度用尽时用户仍要知道，另走一条桌面通知旁路。
	a.SetAttentionIgnoreThread(rt.IsSteward)
	// 会话摘要带出 isSteward，前端据此认出总管行；rt.IsSteward 只短暂拿自己的锁，
	// steward 侧没有在持锁时回调 backend 的路径，不会与 b.mu 反序死锁。
	a.backend.SetStewardPredicate(rt.IsSteward)
	a.watchStewardSelfAlerts(rt, dataDir)
	if a.mcp != nil {
		hooks := rt.Hooks(a.stewardThreadStatus, a.notifyOwner)
		// 总管要自己判断需求属于哪个项目，因此把项目清单原样给它；
		// 它自己的家目录项目也在清单里，不做过滤。
		hooks.ListProjects = func(ctx context.Context) ([]mcp.ProjectItem, error) {
			projects, err := a.store.ListProjects(ctx)
			if err != nil {
				return nil, err
			}
			items := make([]mcp.ProjectItem, 0, len(projects))
			for _, p := range projects {
				items = append(items, mcp.ProjectItem{ID: p.ID, Name: p.Name, Path: p.Path})
			}
			return items, nil
		}
		hooks.CanAct = rt.CanAct
		// 只有用户亲自发消息的那一轮才允许派方案子代理：事件唤醒与收到子代理汇报时都拒绝。
		hooks.TurnStartedByUser = func(threadID string) bool {
			kind, text, active := a.backend.CurrentTurnInput(threadID)
			return stewardTurnIsUser(kind, text, active)
		}
		// 派发管理器未构造时保留空钩子，plan_request 会被 MCP 层判为未启用，而不是调用到空指针。
		if a.swarm != nil {
			// 方案与派活共用同一套项目解析与会话项目查询：两边都要从总管会话取项目 ID，
			// 也都按同样的规则把项目参数解析成名字与目录。
			resolveProject := func(ctx context.Context, ref string) (string, string, error) {
				return resolveStewardPlanProject(ctx, a.backend, ref)
			}
			// 总管线程没有开编排，lead 记录里没有项目 ID，只能从会话本身取。
			threadProject := func(ctx context.Context, threadID string) (string, error) {
				thread, err := a.backend.GetThread(ctx, threadID)
				if err != nil {
					return "", err
				}
				return thread.ProjectID, nil
			}
			planner := &stewardPlanner{
				dispatch:       a.swarm.DispatchSingleEntry,
				resolveProject: resolveProject,
				workDir:        a.backend.ThreadWorkDir,
				threadProject:  threadProject,
				settings: func() (config.StewardConfig, error) {
					cfg, err := config.Load(dataDir)
					if err != nil {
						return config.StewardConfig{}, err
					}
					return cfg.Orchestration.Steward, nil
				},
				now: time.Now,
			}
			hooks.PlanRequest = planner.Request
			// 派活由宿主直接给权限模式与项目 ID：不显式覆盖权限模式时子代理会继承总管会话的档位，
			// 无人值守时会卡在审批上。
			assigner := &stewardAssigner{
				dispatch:       a.swarm.DispatchSingleEntry,
				resolveProject: resolveProject,
				workDir:        a.backend.ThreadWorkDir,
				threadProject:  threadProject,
			}
			hooks.AssignTask = assigner.Request
		}
		actions := &stewardActions{
			backend:    a.backend,
			kanban:     a.kanban,
			tasks:      a.store,
			automation: a.automation,
			client:     a.client,
			runtime:    rt,
			guard:      steward.NewActionGuard(a.store, nil, 0),
			now:        time.Now,
		}
		hooks.SendMessage = actions.SendMessage
		hooks.InterruptThread = actions.InterruptThread
		if a.client != nil {
			hooks.SuggestApproval = a.client.SuggestApproval
		}
		// 服务未构造时保留空钩子，动作工具会被 MCP 层判为不可用，而不是调用到空指针。
		if a.automation != nil {
			hooks.RunAutomation = actions.RunAutomation
		}
		if a.kanban != nil {
			hooks.MoveKanbanTask = actions.MoveKanbanTask
		}
		a.mcp.SetSteward(hooks)
	}
	rt.Start()
	if stewardEnabled(dataDir) {
		// 交给运行时后台准备：Stop 会取消并等待它结束，避免关闭 store 之后还在写库。
		rt.EnsureInBackground(stewardEnsureTimeout)
	}
	return rt
}

// stewardEnabled 现读配置判断总管是否启用；读配置失败按未启用处理。
func stewardEnabled(dataDir string) bool {
	cfg, err := config.Load(dataDir)
	if err != nil {
		return false
	}
	return cfg.Orchestration.Steward.Enabled
}

// ensureStewardProject 确保总管主目录对应一个项目并返回项目 ID：
// 已有同路径项目直接复用，没有时以「总管」为名新建。目录不存在时先创建。
// 新建时的名字只是占位：配置名不是「总管」时由总管运行时的名字同步改成配置名。
func ensureStewardProject(ctx context.Context, backend *remote.MemoryBackend, dir string) (string, error) {
	if backend == nil {
		return "", errors.New("未提供会话后端，无法登记总管项目")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析总管主目录失败: %w", err)
	}
	cleanDir := filepath.Clean(absDir)
	if err := os.MkdirAll(cleanDir, 0o755); err != nil {
		return "", fmt.Errorf("创建总管主目录 %s 失败: %w", cleanDir, err)
	}
	if existing, ok, err := findStewardProject(ctx, backend, cleanDir); err != nil {
		return "", err
	} else if ok {
		return existing.ID, nil
	}
	created, err := backend.AddProjectPath(ctx, cleanDir)
	if err != nil {
		return "", fmt.Errorf("登记总管项目失败: %w", err)
	}
	// AddProjectPath 用目录名当项目名；总管项目固定叫「总管」，用户才认得出它是谁建的。
	if created.Name != stewardProjectName {
		created.Name = stewardProjectName
		backend.AddProject(created)
	}
	return created.ID, nil
}

// SetOwnerNotifier 设置向用户发桌面通知的实现，并发安全；传 nil 表示未接入。
// 实现返回错误时（例如前台抑制或安静时段），notify_owner 会改叫总管在主聊天里直接说。
// 桌面端在 setupNative 之后接入，远程端与无桌面环境的启动方式保持未接入。
func (a *App) SetOwnerNotifier(fn func(title, body, threadID string) error) {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	a.ownerNotify = fn
}

// notifyOwner 是总管 notify_owner 工具的实现：把标题、正文与会话 ID 交给已接入的桌面通知，
// 远程服务存在时同时发一条网页推送。两者任一送出即算成功；都不可用时返回错误，
// 让总管知道汇报没有送达用户，而不是误以为已经通知过。
// 桌面通知被抑制（应用在前台或处于安静时段）而网页推送也没送出时，返回的错误会明确
// 要求总管直接在主聊天里告诉用户。
func (a *App) notifyOwner(_ context.Context, title, body, threadID string) error {
	a.notifyMu.RLock()
	fn := a.ownerNotify
	a.notifyMu.RUnlock()
	var notifyErr error
	if fn != nil {
		notifyErr = fn(title, body, threadID)
	}
	pushed := a.remote != nil && a.remote.SendOwnerPush(title, body, threadID)
	if pushed {
		// 网页推送已经送出，用户能在手机上看到，不必再要求主聊天重复。
		return nil
	}
	if fn == nil {
		return errOwnerNotifierUnavailable
	}
	if notifyErr != nil {
		if errors.Is(notifyErr, ErrOwnerNotifySuppressed) {
			return errOwnerNotifyHidden
		}
		return fmt.Errorf("桌面通知没有发出（原因：%v）；请直接在主聊天里告诉用户", notifyErr)
	}
	return nil
}

// watchStewardSelfAlerts 给总管自身会话挂一条旁路通知：总管会话被排除在待处理事件之外
// （它不该把自己的活动当成要分诊的事件），但它自己执行失败或额度用尽时用户必须知道，
// 否则总管会静情情地不再干活。通知走与 notify_owner 同一条桌面通知 + 网页推送路径。
func (a *App) watchStewardSelfAlerts(rt *steward.Runtime, dataDir string) {
	if a == nil || a.hub == nil || rt == nil {
		return
	}
	alert := newStewardSelfAlert(rt.IsSteward, a.notifyOwner, func() string {
		// 名字随时可改：每次通知时现读配置，与侧边栏和设置页显示一致。
		c, err := config.Load(dataDir)
		if err != nil {
			log.Printf("steward: 读取配置失败，通知里改回默认称呼: %v", err)
			return config.StewardConfig{}.Label()
		}
		return c.Orchestration.Steward.Label()
	}, time.Now)
	// 观察者随事件总线一起活到进程结束；App 关闭时 hub 关闭，不需要单独注销。
	a.hub.AddObserver(alert.observe)
}

// stewardSelfAlertWindow 是同一原因重复报告的间隔：模型配错时会连着失败，
// 不限制就会一分钟弹一次，反而盖住真正需要注意的提醒。
const stewardSelfAlertWindow = 10 * time.Minute

// stewardSelfAlert 把总管自身会话的失败与额度用尽变成一条桌面通知。
type stewardSelfAlert struct {
	// isSteward 判定会话是否就是总管自身，接的是 steward.Runtime.IsSteward。
	isSteward func(threadID string) bool
	// notify 是向用户发通知的实现，接的是 App.notifyOwner（桌面通知 + 网页推送）。
	notify func(ctx context.Context, title, body, threadID string) error
	// name 返回当前称呼，每次通知时现读配置。
	name func() string
	// now 是当前时间来源，测试可注入。
	now func() time.Time
	// detector 复用会话提醒探测器，把会话广播翻成「失败 / 额度」等提醒。
	detector *remote.SessionAlertDetector
	mu       sync.Mutex
	// sent 记录每个原因上次通知的时刻，键是「类型:会话」。
	sent map[string]time.Time
}

// newStewardSelfAlert 构造旁路通知器；now 为空时用 time.Now。
func newStewardSelfAlert(
	isSteward func(threadID string) bool,
	notify func(ctx context.Context, title, body, threadID string) error,
	name func() string,
	now func() time.Time,
) *stewardSelfAlert {
	if now == nil {
		now = time.Now
	}
	return &stewardSelfAlert{
		isSteward: isSteward,
		notify:    notify,
		name:      name,
		now:       now,
		detector:  remote.NewSessionAlertDetector(),
		sent:      make(map[string]time.Time),
	}
}

// observe 是事件总线的观察者：只看总管自身会话的失败与额度用尽。
func (s *stewardSelfAlert) observe(threadID, method string, params any) {
	if s == nil || s.isSteward == nil || !s.isSteward(threadID) {
		return
	}
	alert, ok := s.detector.Observe(threadID, method, params)
	if !ok {
		return
	}
	title, body, ok := stewardSelfAlertMessage(s.messageName(), alert)
	if !ok || s.notify == nil {
		return
	}
	if !s.allow(alert.Kind, alert.ThreadID, s.now()) {
		return
	}
	if err := s.notify(context.Background(), title, body, alert.ThreadID); err != nil {
		log.Printf("steward: 报告总管自身问题失败: %v", err)
	}
}

// allow 判断同一原因现在能不能再通知一次：stewardSelfAlertWindow 内已通知过就不重复。
func (s *stewardSelfAlert) allow(kind remote.SessionAlertKind, threadID string, now time.Time) bool {
	key := string(kind) + ":" + threadID
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.sent[key]; ok && now.Sub(last) < stewardSelfAlertWindow {
		return false
	}
	s.sent[key] = now
	return true
}

// messageName 返回当前称呼；名字回调为空时回落到默认名字「总管」。
func (s *stewardSelfAlert) messageName() string {
	if s.name == nil {
		return config.StewardConfig{}.Label()
	}
	return s.name()
}

// stewardSelfAlertMessage 把提醒翻成给用户的通知：标题点明是谁出问题，正文写原因，
// 并提示去设置页检查模型。只处理失败与额度用尽两类，其余提醒（完成、审批、卡住）返回 ok=false。
func stewardSelfAlertMessage(name string, alert remote.SessionAlert) (title, body string, ok bool) {
	var reason string
	switch alert.Kind {
	case remote.SessionAlertFailed:
		reason = "执行失败"
	case remote.SessionAlertQuota:
		reason = "额度用完了"
	default:
		return "", "", false
	}
	if detail := strings.TrimSpace(alert.Body); detail != "" {
		reason += "：" + detail
	}
	return truncateHostNotice(name+"出问题了", hostNoticeTitleMaxRunes), stewardSelfAlertBody(reason), true
}

// stewardSelfAlertHint 是失败通知末尾固定的排查提示，截断正文时必须完整保留。
const stewardSelfAlertHint = "。可以去「设置 → 总管」检查模型。"

// stewardSelfAlertBody 拼出失败通知正文：先按剩余额度截断原因，再接上排查提示，
// 保证整条正文不超过 hostNoticeBodyMaxRunes 且提示语不被截断。
func stewardSelfAlertBody(reason string) string {
	room := hostNoticeBodyMaxRunes - len([]rune(stewardSelfAlertHint))
	if room < 0 {
		room = 0
	}
	return truncateHostNotice(reason, room) + stewardSelfAlertHint
}

// stewardThreadStatus 是总管 thread_status 工具的实现：列出所有项目里需要它关注的会话。
// 会话状态查询按需进行，只有通过静态过滤（非 helper、非总管自身、项目匹配）的会话才查。
func (a *App) stewardThreadStatus(ctx context.Context, projectID string) ([]mcp.ThreadStatusItem, error) {
	if a.backend == nil {
		return nil, nil
	}
	list, err := a.backend.ListThreads(ctx, wire.ThreadListParams{})
	if err != nil {
		return nil, fmt.Errorf("列出会话失败: %w", err)
	}
	stateOf := func(threadID string) (kanban.SessionState, bool) {
		state, err := a.backend.SessionState(ctx, threadID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				log.Printf("steward: 查询会话 %s 状态失败: %v", threadID, err)
			}
			return kanban.SessionState{}, false
		}
		return state, true
	}
	items := stewardThreadStatusItems(list.Threads, stateOf, projectID, a.isStewardThread)
	if a.client == nil {
		return items, nil
	}
	for i := range items {
		if !items[i].AwaitingApproval {
			continue
		}
		pending, err := a.client.stewardPendingRequests(ctx, items[i].ThreadID)
		if err != nil {
			log.Printf("steward: 查询会话 %s 的待审批请求失败: %v", items[i].ThreadID, err)
			continue
		}
		items[i].PendingRequests = pending
	}
	return items, nil
}

// isStewardThread 报告会话是否就是总管自身；未接线时没有任何会话是总管。
func (a *App) isStewardThread(threadID string) bool {
	return a.steward != nil && a.steward.IsSteward(threadID)
}

// stewardThreadStatusItems 从会话列表里挑出需要总管关注的会话并映射成工具结果。
// 只保留运行中、等审批、等输入或出错的会话；helper 会话（子代理、bee）与总管自身跳过，
// projectID 非空时只保留该项目的会话。纯函数，stateOf 负责按需取运行时状态。
func stewardThreadStatusItems(
	summaries []wire.ThreadSummary,
	stateOf func(threadID string) (kanban.SessionState, bool),
	projectID string,
	isSteward func(threadID string) bool,
) []mcp.ThreadStatusItem {
	items := make([]mcp.ThreadStatusItem, 0, len(summaries))
	wantProject := strings.TrimSpace(projectID)
	for _, summary := range summaries {
		if summary.IsArchived || summary.IsHelper {
			continue
		}
		if summary.ParentThreadID != nil && strings.TrimSpace(*summary.ParentThreadID) != "" {
			continue
		}
		if isSteward != nil && isSteward(summary.ID) {
			continue
		}
		if wantProject != "" && summary.ProjectID != wantProject {
			continue
		}
		if stateOf == nil {
			continue
		}
		state, ok := stateOf(summary.ID)
		if !ok || !stewardThreadNeedsAttention(state) {
			continue
		}
		items = append(items, stewardThreadStatusItem(summary, state))
	}
	return items
}

// stewardThreadNeedsAttention 报告会话是否处于总管该关心的状态。
func stewardThreadNeedsAttention(state kanban.SessionState) bool {
	return state.Running || state.AwaitingApproval || state.AwaitingInput || state.Failed
}

// stewardThreadStatusItem 把会话摘要与运行时状态映射成一条工具结果；UpdatedAt 直接用
// 摘要里的字符串，与界面显示的最近活动时间保持同一口径。
func stewardThreadStatusItem(summary wire.ThreadSummary, state kanban.SessionState) mcp.ThreadStatusItem {
	item := mcp.ThreadStatusItem{
		ThreadID:         summary.ID,
		ProjectID:        summary.ProjectID,
		Title:            summary.Title,
		Provider:         summary.Provider,
		Running:          state.Running,
		AwaitingApproval: state.AwaitingApproval,
		AwaitingInput:    state.AwaitingInput,
		Failed:           state.Failed,
		FailureReason:    state.FailureReason,
		UpdatedAt:        summary.UpdatedAt,
	}
	if summary.Model != nil {
		item.Model = *summary.Model
	}
	if summary.LastActivityText != nil {
		item.LastActivityText = *summary.LastActivityText
	}
	return item
}
