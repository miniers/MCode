package mcodeui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// threadChangeStatsTTL 是空闲状态下改动统计缓存的刷新间隔。
// 统计走 statOnly 只跑一次 numstat，开销小；空闲 15 秒重取一次，
// 覆盖会话在后台跑完、切回时 lastRunning 没经历 true→false 的情况。
const threadChangeStatsTTL = 15 * time.Second

var (
	threadChangeStatsMu     sync.Mutex
	threadChangeStatsStates = make(map[*chatState]*threadChangeStats)
)

// threadChangeStats 维护单个窗口各会话的改动统计缓存。
// 按会话分开保存：切换会话时直接命中该会话上一次的结果，不必重跑整会话 diff
// （一轮统计要对最近 50 个轮次各起一个 git numstat，大会话切换时就是几十个子进程）。
type threadChangeStats struct {
	byThread map[string]*threadChangeStatsEntry
}

// threadChangeStatsEntry 是一个会话的改动统计与加载状态。
type threadChangeStatsEntry struct {
	additions   int
	deletions   int
	files       int
	loading     bool
	dirty       bool
	loadedAt    time.Time
	lastRunning bool
}

// kThreadChangeStatsCache 是统计缓存保留的会话数上限，超出时淘汰最久未加载的会话。
// 一条缓存只有三个数字，留几十个会话的开销可以忽略。
const kThreadChangeStatsCache = 50

// ensureThreadChangeStats 按需在后台刷新指定会话的增删行统计。
// 会话切换、运行刚结束（isRunning 由 true 变 false）或空闲超过 15 秒时重新拉取。
func (s *chatState) ensureThreadChangeStats(threadID string, isRunning bool) {
	if s == nil || threadID == "" {
		return
	}
	threadChangeStatsMu.Lock()
	st := threadChangeStatsStates[s]
	if st == nil || st.byThread == nil {
		st = &threadChangeStats{byThread: make(map[string]*threadChangeStatsEntry)}
		threadChangeStatsStates[s] = st
	}
	entry := st.byThread[threadID]
	if entry == nil {
		entry = &threadChangeStatsEntry{}
		st.byThread[threadID] = entry
		trimThreadChangeStatsLocked(st, threadID)
	}
	wasRunning := entry.lastRunning
	entry.lastRunning = isRunning
	if wasRunning && !isRunning && entry.loading {
		entry.dirty = true
	}
	needLoad := false
	switch {
	case entry.loadedAt.IsZero() && !entry.loading:
		needLoad = true
	case entry.loading:
		// 已有加载在途，等它落地。
	case entry.dirty && !entry.loading:
		// 在途加载期间运行刚结束，补刷一次。
		entry.dirty = false
		needLoad = true
	case wasRunning && !isRunning:
		// 运行刚结束，改动可能已经变化，强制刷新一次。
		needLoad = true
	case !isRunning && time.Since(entry.loadedAt) > threadChangeStatsTTL:
		needLoad = true
	}
	if !needLoad {
		threadChangeStatsMu.Unlock()
		return
	}
	entry.loading = true
	threadChangeStatsMu.Unlock()

	go func(tid string) {
		// 只要三个数字：统计走 statOnly，不生成 diff 正文，也不逐个轮次起 git 子进程。
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		stats, err := s.client.DiffStats(ctx, tid)
		add, del, files := 0, 0, 0
		if err == nil {
			add, del, files = stats.Additions, stats.Deletions, stats.Files
		}

		threadChangeStatsMu.Lock()
		defer threadChangeStatsMu.Unlock()
		if e := st.byThread[tid]; e != nil {
			e.loading = false
			e.loadedAt = time.Now()
			// 出错或解析失败时清零，不提示。
			e.additions, e.deletions, e.files = add, del, files
		}
		s.invalidate()
	}(threadID)
}

// trimThreadChangeStatsLocked 把统计缓存裁剪到 kThreadChangeStatsCache 个会话。
// 在途加载及刚创建的当前会话条目不参与淘汰。调用方需持有 threadChangeStatsMu。
func trimThreadChangeStatsLocked(st *threadChangeStats, keep string) {
	for len(st.byThread) > kThreadChangeStatsCache {
		oldestID := ""
		var oldest time.Time
		for id, e := range st.byThread {
			if id == keep || e.loading {
				continue
			}
			if oldestID == "" || e.loadedAt.Before(oldest) {
				oldestID, oldest = id, e.loadedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(st.byThread, oldestID)
	}
}

// releaseThreadChangeStats 在窗口关闭时清理改动统计缓存。
func releaseThreadChangeStats(s *chatState) {
	threadChangeStatsMu.Lock()
	delete(threadChangeStatsStates, s)
	threadChangeStatsMu.Unlock()
}

// threadChangeStatsFor 返回指定会话的增删行统计缓存；未加载过或属于其它会话时 ok 为 false。
func threadChangeStatsFor(s *chatState, threadID string) (add, del, files int, ok bool) {
	if s == nil || threadID == "" {
		return 0, 0, 0, false
	}
	threadChangeStatsMu.Lock()
	defer threadChangeStatsMu.Unlock()
	st := threadChangeStatsStates[s]
	if st == nil {
		return 0, 0, 0, false
	}
	entry := st.byThread[threadID]
	if entry == nil || entry.loadedAt.IsZero() {
		return 0, 0, 0, false
	}
	return entry.additions, entry.deletions, entry.files, true
}

// renderThreadChangesTab 渲染输入框上方的紧凑统计行：
// 左侧是改动文件胶囊（文件数加增删行数，点击打开改动面板），中间是调用方传入的状态胶囊，
// 右侧是「会话统计」按钮，几种信息共用一行，时间线底部不再重复占行。
// 窄屏时胶囊可以换行，避免把右侧按钮挤出行外。
func renderThreadChangesTab(c *ui.Context, s *chatState, add, del, files int, capsules []composerCapsule) {
	renderThreadChangesTabFloating(c, s, add, del, files, capsules, false)
}

func renderThreadChangesTabFloating(c *ui.Context, s *chatState, add, del, files int, capsules []composerCapsule, floating bool) {
	ui.Row(c.Key("composer-changes-row")).
		FillWidth().
		Gap(Space3).
		Wrap().
		AlignItems(ui.Center).
		PassThrough().
		Children(func() {
			tabText := fmt.Sprintf("%d 个文件", files)
			tab := ui.ButtonBase(c.Key("composer-changes-tab")).
				Label(tabText).
				Tooltip("查看改动").
				Height(ControlSM).
				Radius(RadiusLG).
				Padding(Space0, Space3).
				Border(Hairline, ThemeBorder(c)).
				OnClick(func() {
					s.toggleDiffPanel()
				})
			// 改动 tab 在停靠悬浮态下铺不透明 ThemeSurface 底，悬停时叠加 ThemeHover；Hero 模式维持透明底。
			composerFloatingTabBg(c, tab, tab.Hovered(), floating)
			scale := motionPressScale(c, tab, "button-press-scale", true && tab.Pressed())
			tab.Scale(scale, scale)
			tab.Children(func() {
				ui.Row(c.Key("composer-changes-tab-content")).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, tabText).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemePrimaryText(c))
						if add > 0 {
							ui.Text(c, fmt.Sprintf("+%d", add)).
								FontSize(scaledFont(TypeCaption)).
								FontWeight(500).
								TextColor(ThemeSuccess(c))
						}
						if del > 0 {
							ui.Text(c, fmt.Sprintf("-%d", del)).
								FontSize(scaledFont(TypeCaption)).
								FontWeight(500).
								TextColor(ThemeDanger(c))
						}
					})
			})
			for _, capsule := range capsules {
				renderComposerCapsule(c, s, capsule)
			}
			ui.Spacer(c)
			renderSessionStatsButtonFloating(c, s, sessionStatsThreadID(s), "composer-session-stats", floating)
		})
}

// sessionStatsThreadID 返回当前选中会话 ID（受 s.mu 保护），供会话统计按钮定位数据。
func sessionStatsThreadID(s *chatState) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}
