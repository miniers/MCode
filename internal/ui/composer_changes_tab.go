package mcodeui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// threadChangeStatsTTL 是空闲状态下改动统计缓存的刷新间隔。
const threadChangeStatsTTL = 15 * time.Second

var (
	threadChangeStatsMu     sync.Mutex
	threadChangeStatsStates = make(map[*chatState]*threadChangeStats)
)

// threadChangeStats 维护单个窗口的会话改动统计缓存。
type threadChangeStats struct {
	threadID    string
	additions   int
	deletions   int
	files       int
	loading     bool
	loadedAt    time.Time
	lastRunning bool
}

// ensureThreadChangeStats 按需在后台刷新指定会话的增删行统计。
// 会话切换、运行刚结束（isRunning 由 true 变 false）或空闲超过 15 秒时重新拉取。
func (s *chatState) ensureThreadChangeStats(threadID string, isRunning bool) {
	if s == nil || threadID == "" {
		return
	}
	threadChangeStatsMu.Lock()
	st := threadChangeStatsStates[s]
	if st == nil {
		st = &threadChangeStats{}
		threadChangeStatsStates[s] = st
	}
	switched := st.threadID != threadID
	wasRunning := st.lastRunning
	st.lastRunning = isRunning
	needLoad := false
	switch {
	case switched:
		needLoad = true
	case st.loading:
		// 已有加载在途，等它落地。
	case wasRunning && !isRunning:
		// 运行刚结束，改动可能已经变化，强制刷新一次。
		needLoad = true
	case !isRunning && time.Since(st.loadedAt) > threadChangeStatsTTL:
		needLoad = true
	}
	if !needLoad {
		threadChangeStatsMu.Unlock()
		return
	}
	if switched {
		// 切换会话时先清掉旧统计，避免显示上一个会话的数字。
		st.additions, st.deletions, st.files = 0, 0, 0
		st.loadedAt = time.Time{}
		st.threadID = threadID
	}
	st.loading = true
	threadChangeStatsMu.Unlock()

	go func(tid string) {
		data, err := s.client.Diff(context.Background(), tid, "")
		add, del, files := 0, 0, 0
		if err == nil && data.Diff != "" {
			if parsed, parseErr := git.ParseUnifiedDiff([]byte(data.Diff)); parseErr == nil {
				files = len(parsed)
				for _, f := range parsed {
					if f.IsDeferred {
						add += f.Additions
						del += f.Deletions
						continue
					}
					for _, h := range f.Hunks {
						for _, l := range h.Lines {
							switch l.Kind {
							case '+':
								add++
							case '-':
								del++
							}
						}
					}
				}
			}
		}

		threadChangeStatsMu.Lock()
		defer threadChangeStatsMu.Unlock()
		if st.threadID != tid {
			return
		}
		st.loading = false
		st.loadedAt = time.Now()
		// 出错或解析失败时清零，不提示。
		st.additions, st.deletions, st.files = add, del, files
		s.invalidate()
	}(threadID)
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
	if st == nil || st.threadID != threadID || st.loadedAt.IsZero() {
		return 0, 0, 0, false
	}
	return st.additions, st.deletions, st.files, true
}

// renderThreadChangesTab 渲染输入框上方的紧凑统计行：
// 左侧是改动文件胶囊（文件数加增删行数，点击打开改动面板），中间是调用方传入的状态胶囊，
// 右侧是「会话统计」按钮，几种信息共用一行，时间线底部不再重复占行。
// 窄屏时胶囊可以换行，避免把右侧按钮挤出行外。
func renderThreadChangesTab(c *ui.Context, s *chatState, add, del, files int, capsules []composerCapsule) {
	ui.Row(c.Key("composer-changes-row")).
		FillWidth().
		Gap(Space3).
		Wrap().
		AlignItems(ui.Center).
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
			// 改动 tab 与排队胶囊同为浅底胶囊，悬停时提亮一级。
			composerHoverBg(c, tab, tab.Hovered())
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
			renderSessionStatsButton(c, s, sessionStatsThreadID(s), "composer-session-stats")
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
