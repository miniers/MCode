package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// turnFilesDisplayLimit 是卡片收起时最多显示的文件行数，多出来的折成「还有 N 个」。
const turnFilesDisplayLimit = 6

// turnFilesStatMu 保护 turnFilesStatCache：stat 结果按「消息 ID + 绝对路径」缓存，
// 避免每帧对同一批文件重复 stat。
var (
	turnFilesStatMu    sync.Mutex
	turnFilesStatCache = make(map[string]bool)
)

// resolveTurnFilePath 把工具里记录的路径解析成本机绝对路径：绝对路径只做清洗，
// 相对路径按会话工作目录拼接；拿不到工作目录的相对路径解析不成绝对路径，返回空串。
func resolveTurnFilePath(workDir, path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(dir, p))
}

// turnFileExists 判断绝对路径在本机是否是存在的普通文件；结果按「消息 ID + 路径」缓存。
func turnFileExists(msgID, abs string) bool {
	key := msgID + "\x00" + abs
	turnFilesStatMu.Lock()
	ok, hit := turnFilesStatCache[key]
	turnFilesStatMu.Unlock()
	if hit {
		return ok
	}
	info, err := os.Stat(abs)
	ok = err == nil && info.Mode().IsRegular()
	turnFilesStatMu.Lock()
	if turnFilesStatCache == nil {
		turnFilesStatCache = make(map[string]bool)
	}
	turnFilesStatCache[key] = ok
	turnFilesStatMu.Unlock()
	return ok
}

// turnEditedFilesFor 把本回合的改动路径解析成绝对路径，并过滤掉本机不存在的文件
// （例如在别的容器或 worktree 里写下的路径）。
func turnEditedFilesFor(msgs []Message, turnIndex int, workDir string) []turnEditedFile {
	paths := collectTurnEditedPaths(msgs, turnIndex)
	if len(paths) == 0 {
		return nil
	}
	files := make([]turnEditedFile, 0, len(paths))
	for _, p := range paths {
		abs := resolveTurnFilePath(workDir, p.path)
		if abs == "" || !turnFileExists(p.msgID, abs) {
			continue
		}
		files = append(files, turnEditedFile{path: p.path, abs: abs, name: filepath.Base(abs), status: p.status})
	}
	return files
}

// turnFilesCardKey 是本回合文件卡片在 s.expandedTool 里的展开状态键。
func turnFilesCardKey(msgID string) string {
	return "turn-files-" + msgID
}

// turnFilesWorkDir 返回会话的工作目录：优先客户端记录的会话工作目录（可能指向 AI 自建 worktree），
// 拿不到时退回会话所属项目的根目录。
func turnFilesWorkDir(s *chatState, threadID string) string {
	if s == nil || threadID == "" {
		return ""
	}
	if s.client != nil {
		if dir, ok := s.client.ThreadWorkDir(threadID); ok && strings.TrimSpace(dir) != "" {
			return strings.TrimSpace(dir)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var projectID string
	for _, th := range s.threads {
		if th.ID == threadID {
			projectID = th.ProjectID
			break
		}
	}
	for _, p := range s.projects {
		if p.ID == projectID {
			return strings.TrimSpace(p.Path)
		}
	}
	return ""
}

// turnFilesForTurn 持锁拷贝本回合消息与工作目录，再收集本回合改动且存在的文件
// （拷贝方式与 turnToolStatsFor 一致）。
func turnFilesForTurn(s *chatState, threadID string, turnIndex int) []turnEditedFile {
	if s == nil || turnIndex <= 0 {
		return nil
	}
	s.mu.Lock()
	msgs := s.messages
	s.mu.Unlock()
	return turnEditedFilesFor(msgs, turnIndex, turnFilesWorkDir(s, threadID))
}

// turnFilesExpanded 读取用户对某张文件卡片的显式开合选择，没有选择时收起。
func (s *chatState) turnFilesExpanded(msgID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expandedTool[turnFilesCardKey(msgID)]
}

// toggleTurnFilesCard 记录用户对文件卡片开合的显式选择，并让时间线缓存失效（与 toggleTodoCard 一致）。
func (s *chatState) toggleTurnFilesCard(msgID string) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	key := turnFilesCardKey(msgID)
	s.expandedTool[key] = !s.expandedTool[key]
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// turnFilesPlan 计算收起 / 展开时实际显示的文件胶囊数与被折叠的数量：
// 不超过上限时全部显示；收起态只显示前 turnFilesDisplayLimit 个，其余折成「还有 N 个」。
func turnFilesPlan(total int, expanded bool) (visible, hidden int) {
	if total <= 0 {
		return 0, 0
	}
	if expanded || total <= turnFilesDisplayLimit {
		return total, 0
	}
	return turnFilesDisplayLimit, total - turnFilesDisplayLimit
}

// turnFileStat 是单个文件的增删行数统计。
type turnFileStat struct {
	adds int
	dels int
}

// turnFileDiffStats 把回合级 git diff 匹配到卡片里的每个改动文件，返回按绝对路径索引的统计。
// Binary 文件与匹配不到的文件记为未知；IsDeferred 的大文件补丁只有 Additions / Deletions，
// 其余文件用 toolDiffFileStatsFor 从 hunk 行数统计；单个文件 0 增 0 删也视为未知（不画 0/0）。
// 只有每个文件都拿到可信统计时才返回 complete=true，调用方可显示整卡总计。
func turnFileDiffStats(files []git.FileDiff, edited []turnEditedFile) (map[string]turnFileStat, bool) {
	stats := make(map[string]turnFileStat, len(edited))
	if len(files) == 0 || len(edited) == 0 {
		return stats, false
	}
	complete := true
	for _, e := range edited {
		var matched *git.FileDiff
		for i := range files {
			if toolDiffPathMatch(files[i].NewPath, e.abs) || toolDiffPathMatch(files[i].OldPath, e.abs) {
				matched = &files[i]
				break
			}
		}
		if matched == nil || matched.Binary {
			complete = false
			continue
		}
		var stat turnFileStat
		if matched.IsDeferred {
			stat = turnFileStat{adds: matched.Additions, dels: matched.Deletions}
		} else {
			fileStat := toolDiffFileStatsFor(*matched)
			stat = turnFileStat{adds: fileStat.adds, dels: fileStat.dels}
		}
		if stat.adds == 0 && stat.dels == 0 {
			complete = false
			continue
		}
		stats[e.abs] = stat
	}
	return stats, complete
}

// turnFileStatusFromDiff 把 git diff 的文件状态映射成卡片用的 A / M / D；无法识别时返回空串。
func turnFileStatusFromDiff(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "added":
		return "A"
	case "deleted":
		return "D"
	case "modified", "renamed":
		return "M"
	}
	return ""
}

// turnFileDiffStatuses 把回合级 diff 里的文件状态映射成 A / M / D，按绝对路径索引；
// 匹配不到或状态无法识别时不写，卡片保留工具侧推断的结果。
func turnFileDiffStatuses(files []git.FileDiff, edited []turnEditedFile) map[string]string {
	if len(files) == 0 || len(edited) == 0 {
		return nil
	}
	out := make(map[string]string, len(edited))
	for _, e := range edited {
		for i := range files {
			if toolDiffPathMatch(files[i].NewPath, e.abs) || toolDiffPathMatch(files[i].OldPath, e.abs) {
				if status := turnFileStatusFromDiff(files[i].Status); status != "" {
					out[e.abs] = status
				}
				break
			}
		}
	}
	return out
}

// turnFileChangeSummary 是一次回合改动的展示摘要：文件数与增删行数。
// StatsReady 为假表示回合级 diff 还没就绪或不完整，此时只有文件数可信。
type turnFileChangeSummary struct {
	Files      int
	Adds       int
	Dels       int
	StatsReady bool
}

// turnFileChangeSummaryForTurn 汇总某一回合的改动统计，只读缓存、不写任何状态：
// 文件列表复用 turnFilesForTurn（过滤本机不存在的文件），增删行数复用回合级 diff 缓存
// 与 turnFileDiffStats，和改动卡 renderTurnFilesCard 走同一条链路。缓存未命中时
// toolDiffEntryFor 会在后台拉取并重绘，此处只返回「统计未就绪」。
func turnFileChangeSummaryForTurn(s *chatState, threadID string, turnIndex int) turnFileChangeSummary {
	if s == nil || threadID == "" || turnIndex <= 0 {
		return turnFileChangeSummary{}
	}
	files := turnFilesForTurn(s, threadID, turnIndex)
	if len(files) == 0 {
		return turnFileChangeSummary{}
	}
	// diff 缓存按消息定位：有 TurnID 时按轮次 ID 取，旧数据回退到轮次序号；
	// 取本回合最后一条带 TurnID 的消息，与改动卡传进来的那条保持一致。
	var msg Message
	s.mu.Lock()
	for _, m := range s.messages {
		if m.TurnIndex != turnIndex {
			continue
		}
		if msg.TurnID == "" || m.TurnID != "" {
			msg = m
		}
	}
	s.mu.Unlock()
	msg.TurnIndex = turnIndex

	entry := s.toolDiffEntryFor(threadID, msg)
	summary := turnFileChangeSummary{Files: len(files)}
	if entry.loading || entry.err != "" || entry.failure != "" || entry.truncated {
		return summary
	}
	stats, complete := turnFileDiffStats(entry.files, files)
	if !complete {
		return summary
	}
	adds, dels := sumTurnFileStats(stats)
	summary.Adds, summary.Dels, summary.StatsReady = adds, dels, true
	return summary
}

// sumTurnFileStats 汇总各文件的增删行数。
func sumTurnFileStats(stats map[string]turnFileStat) (adds, dels int) {
	for _, stat := range stats {
		adds += stat.adds
		dels += stat.dels
	}
	return adds, dels
}

// turnFilePreviewPath 返回文件面板可用的相对路径：工具给的相对路径原样使用，
// 绝对路径按会话工作目录换算；落在工作目录外时返回空串（面板只读工作区内的文件）。
func turnFilePreviewPath(workDir string, f turnEditedFile) string {
	if !filepath.IsAbs(f.path) {
		return f.path
	}
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		return ""
	}
	rel, err := filepath.Rel(dir, f.abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

// openTurnFile 打开改动文件的预览：工作区内的文件先开文件面板再预览，
// 工作区外的文件面板读不到，退回在访达里定位。
func openTurnFile(s *chatState, threadID, workDir string, f turnEditedFile) {
	openTurnFileAt(s, threadID, workDir, f, 0, 0)
}

func openTurnFileAt(s *chatState, threadID, workDir string, f turnEditedFile, first, last int) {
	if s == nil || threadID == "" || f.abs == "" {
		return
	}
	rel := turnFilePreviewPath(workDir, f)
	if rel == "" {
		openItemInFinder(f.abs)
		return
	}
	if !isFileManagerOpen(s) {
		toggleFileManager(s)
	}
	// 从聊天卡片打开的文件是固定 tab，不会被单击列表时的临时预览替换；
	// 它属于外部入口，关掉最后一个 tab 时收起整个侧栏。
	s.openFileManagerFileExternalAt(threadID, rel, first, last)
}

// turnFileContextMenu 是文件胶囊的右键菜单：打开、在访达中显示、复制路径。
func turnFileContextMenu(c *ui.Context, s *chatState, threadID, workDir string, f turnEditedFile) func(*ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item("在文件面板打开").Chosen() {
			openTurnFile(s, threadID, workDir, f)
		}
		if m.Item("在访达中显示").Chosen() {
			openItemInFinder(f.abs)
		}
		if m.Item("复制路径").Chosen() {
			c.WriteClipboard(f.abs)
			s.setToast("已复制路径")
		}
	}
}

// turnFileStatusInk 返回状态字母胶囊的文字色：新增绿、删除红、修改用强调色。
func turnFileStatusInk(c *ui.Context, status string) ui.Color {
	switch status {
	case "A":
		return ThemeSuccess(c)
	case "D":
		return ThemeDanger(c)
	default:
		return ThemeAccent(c)
	}
}

// turnFileStatColumnWidth 返回右侧增删数字列的宽度：按 4 个数字字符测量，
// 各行共用同一宽度，右对齐后上下自然成列。
func turnFileStatColumnWidth(c *ui.Context) float32 {
	width, _ := c.MeasureText(0, ui.Span{Text: "0000", Size: scaledFont(11)})
	if width < 24 {
		return 24
	}
	return width
}

// renderTurnFileRow 渲染改动卡里的一行文件：状态字母胶囊、文件图标、单行省略的文件名，
// 右侧两列定宽的 +N / -N。statusShown 为真时每一行都给状态列留出同样宽度，
// 状态未知的文件空着，保证图标与文件名上下对齐。
func renderTurnFileRow(c *ui.Context, s *chatState, threadID, workDir, turnID string, f turnEditedFile, key string, statusShown bool) {
	row := ui.Row(c.Key("turn-file-"+key)).
		FillWidth().
		Height(ControlSM).
		Padding(0, Space2, 0, Space2).
		Gap(Space2).
		Radius(RadiusSM).
		AlignItems(ui.Center).
		Cursor(ui.CursorPointer).
		Tooltip(f.abs).
		OnClick(func() {
			if rel := turnFilePreviewPath(workDir, f); turnID != "" && rel != "" {
				openReviewPath(s, turnID, rel)
			} else {
				openTurnFile(s, threadID, workDir, f)
			}
		}).
		ContextMenu(turnFileContextMenu(c, s, threadID, workDir, f))
	if row.Hovered() {
		row.Background(ThemeHover(c).Alpha(0.5))
	}
	row.Children(func() {
		if statusShown {
			if f.status != "" {
				ink := turnFileStatusInk(c, f.status)
				ui.Row(c.Key("turn-file-status-" + key)).
					Width(16).
					Height(IconMD).
					Radius(RadiusXS).
					AlignItems(ui.Center).
					Background(ink.Alpha(0.08).Over(ThemeSurface(c))).
					Children(func() {
						ui.Text(c, f.status).
							FillWidth().
							TextAlign(ui.Center).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ink)
					})
			} else {
				// 未知状态也占同样的宽度，避免同一列里图标左右跳。
				ui.Row(c.Key("turn-file-status-empty-" + key)).Width(16)
			}
		}
		ui.Icon(c, IconFile()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		ui.Text(c, f.name).
			FontSize(scaledFont(TypeCaption)).
			TextColor(ThemePrimaryText(c)).
			SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		ui.Spacer(c)
		statWidth := turnFileStatColumnWidth(c)
		adds, dels := "", ""
		if f.hasStat {
			if f.adds > 0 {
				adds = fmt.Sprintf("+%d", f.adds)
			}
			if f.dels > 0 {
				dels = fmt.Sprintf("-%d", f.dels)
			}
		}
		// 每列都用定宽容器占位：文件没有可信统计时也保留列宽，上下行才不会错位。
		ui.Row(c.Key("turn-file-adds-" + key)).
			Width(statWidth).
			Height(ControlSM).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, adds).
					FillWidth().
					TextAlign(ui.End).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSuccess(c))
			})
		ui.Row(c.Key("turn-file-dels-" + key)).
			Width(statWidth).
			Height(ControlSM).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, dels).
					FillWidth().
					TextAlign(ui.End).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeDanger(c))
			})
	})
}

// renderTurnFilesToggleCapsule 渲染卡片底部的「还有 N 个」/「收起」胶囊。
func renderTurnFilesToggleCapsule(c *ui.Context, key, label string, onClick func()) {
	ui.Row(c.Key(key)).
		Padding(Space0, Space3, Space0, Space3).
		Radius(RadiusLG).
		Border(Hairline, ThemeBorder(c)).
		AlignItems(ui.Center).
		Cursor(ui.CursorPointer).
		OnClick(onClick).
		Children(func() {
			ui.Text(c, label).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
		})
}

// renderTurnFilesCard 在回合完成行下面渲染「本回合改动 N 个文件」卡片：
// 标题一行（拿到可信的回合级 diff 统计时右侧补总计 +A / -D），下面每行一个文件
// （状态字母、图标、文件名、右侧定宽的 +N / -N），收起时最多 turnFilesDisplayLimit 行。
// 本回合没有可展示的改动文件时什么都不画。
// 增删行数只来自回合级 git diff：未就绪、失败、被截断时只显示文件数，绝不显示 0/0。
func renderTurnFilesCard(c *ui.Context, s *chatState, m Message) {
	threadID := selectedThreadIDSnapshot(s)
	files := turnFilesForTurn(s, threadID, m.TurnIndex)
	if len(files) == 0 {
		return
	}
	workDir := turnFilesWorkDir(s, threadID)
	expanded := s.turnFilesExpanded(m.ID)
	visible, hidden := turnFilesPlan(len(files), expanded)

	// 回合级 diff 与时间线里的逐轮 diff 共用缓存：这里只是读它，未就绪时不占位。
	entry := s.toolDiffEntryFor(threadID, m)
	diffReady := !entry.loading && entry.err == "" && entry.failure == "" && !entry.truncated
	diffDegraded := !entry.loading && (entry.err != "" || entry.failure != "" || entry.truncated)
	statsComplete := false
	stats := make(map[string]turnFileStat, len(files))
	if diffReady {
		stats, statsComplete = turnFileDiffStats(entry.files, files)
		statuses := turnFileDiffStatuses(entry.files, files)
		for i := range files {
			if stat, ok := stats[files[i].abs]; ok {
				files[i].adds = stat.adds
				files[i].dels = stat.dels
				files[i].hasStat = true
			}
			if status, ok := statuses[files[i].abs]; ok {
				files[i].status = status
			}
		}
	}
	// 整张卡片都没有状态时整列不画，不留空位。
	statusShown := false
	for _, f := range files {
		if f.status != "" {
			statusShown = true
			break
		}
	}
	totalAdds, totalDels := 0, 0
	if statsComplete {
		totalAdds, totalDels = sumTurnFileStats(stats)
	}

	title := "改动文件"
	titleTooltip := ""
	if diffDegraded {
		titleTooltip = "本回合增删行数暂不可用"
	}

	ui.Column(c.Key("turn-files-card-"+m.ID)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).
		Gap(Space2).
		Children(func() {
			titleRow := ui.Row(c.Key("turn-files-card-title-" + m.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2)
			if titleTooltip != "" {
				titleRow = titleRow.Tooltip(titleTooltip)
			}
			titleRow.Children(func() {
				ui.Text(c, title).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c))
				// 每个文件都拿到统计时才给总计，避免小计被误读为全量。
				if totalAdds > 0 || totalDels > 0 {
					ui.Spacer(c)
					if totalAdds > 0 {
						ui.Text(c, fmt.Sprintf("+%d", totalAdds)).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemeSuccess(c))
					}
					if totalDels > 0 {
						ui.Text(c, fmt.Sprintf("-%d", totalDels)).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemeDanger(c))
					}
				}
			})
			ui.Column(c.Key("turn-files-rows-" + m.ID)).
				FillWidth().
				Gap(Space0).
				Children(func() {
					for i := 0; i < visible; i++ {
						renderTurnFileRow(c, s, threadID, workDir, m.TurnID, files[i],
							fmt.Sprintf("%s-%d", m.ID, i), statusShown)
					}
				})
			if hidden > 0 {
				ui.Row(c.Key("turn-files-toggle-"+m.ID)).
					Padding(Space0, 0, 0, 0).
					Children(func() {
						renderTurnFilesToggleCapsule(c, "turn-files-more-"+m.ID,
							fmt.Sprintf("还有 %d 个", hidden), func() {
								s.toggleTurnFilesCard(m.ID)
							})
					})
			} else if expanded && len(files) > turnFilesDisplayLimit {
				ui.Row(c.Key("turn-files-toggle-"+m.ID)).
					Padding(Space0, 0, 0, 0).
					Children(func() {
						renderTurnFilesToggleCapsule(c, "turn-files-less-"+m.ID, "收起", func() {
							s.toggleTurnFilesCard(m.ID)
						})
					})
			}
		})
}
