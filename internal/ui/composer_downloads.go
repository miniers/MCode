package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// downloadEntry 表示 ~/Downloads 目录中的单个文件条目。
type downloadEntry struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// incompleteDownloadSuffixes 定义未完成下载的临时文件后缀（浏览器/下载器写入过程中）。
var incompleteDownloadSuffixes = []string{".crdownload", ".download", ".part"}

// isIncompleteDownload 判断文件名是否为未完成下载的临时文件。
func isIncompleteDownload(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range incompleteDownloadSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// recentDownloads 读取 dir 目录项，跳过以 '.' 开头的隐藏文件、目录以及未完成下载的临时文件，
// 按修改时间降序排序，最多返回 limit 项。
func recentDownloads(dir string, limit int) ([]downloadEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var results []downloadEntry
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || entry.IsDir() || isIncompleteDownload(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		results = append(results, downloadEntry{
			Path:    filepath.Join(dir, name),
			Name:    name,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   false,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].ModTime.After(results[j].ModTime)
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// downloadsDir 返回当前用户 ~/Downloads 目录路径。
func downloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "Downloads")
}

type downloadsCacheEntry struct {
	items     []downloadEntry
	err       error
	timestamp time.Time
}

var (
	downloadsCacheMu sync.Mutex
	downloadsCache   = make(map[string]downloadsCacheEntry)
)

func clearDownloadsCache() {
	downloadsCacheMu.Lock()
	defer downloadsCacheMu.Unlock()
	downloadsCache = make(map[string]downloadsCacheEntry)
}

// getCachedDownloads 获取当前状态缓存的最近下载文件列表，5 秒内不重复扫描磁盘。
func getCachedDownloads(s *chatState) ([]downloadEntry, error) {
	downloadsCacheMu.Lock()
	defer downloadsCacheMu.Unlock()

	now := time.Now()
	dir := downloadsDir()
	if entry, ok := downloadsCache[dir]; ok && now.Sub(entry.timestamp) < 5*time.Second {
		return entry.items, entry.err
	}

	items, err := recentDownloads(dir, 15)
	downloadsCache[dir] = downloadsCacheEntry{
		items:     items,
		err:       err,
		timestamp: now,
	}
	return items, err
}

// formatFileSize 将字节数格式化为易读的文件大小字符串。
func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(bytes) / 1024.0
	for _, u := range units {
		if v < 1024.0 || u == "TB" {
			if v >= 100 || v == float64(int(v)) {
				return fmt.Sprintf("%.0f %s", v, u)
			}
			return fmt.Sprintf("%.1f %s", v, u)
		}
		v /= 1024.0
	}
	return fmt.Sprintf("%d B", bytes)
}

// truncateMiddle 截断过长文件名并在中间展示省略号。
func truncateMiddle(name string, maxLen int) string {
	runes := []rune(name)
	if len(runes) <= maxLen {
		return name
	}
	ext := filepath.Ext(name)
	extRunes := []rune(ext)
	if len(extRunes) >= maxLen-3 {
		return string(runes[:maxLen-3]) + "…"
	}
	baseLen := maxLen - 3 - len(extRunes)
	if baseLen <= 0 {
		return string(runes[:maxLen-3]) + "…"
	}
	return string(runes[:baseLen]) + "…" + ext
}

var pickFilesDialog func() ([]string, bool, error) = defaultPickFiles

func defaultPickFiles() ([]string, bool, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:    "选择文件",
		Multiple: true,
	})
	if err != nil {
		return nil, false, err
	}
	if len(paths) == 0 {
		return nil, false, nil
	}
	return paths, true, nil
}

// promptChooseFiles 唤起系统原生文件选择对话框并在选取后作为附件添加。
func promptChooseFiles(s *chatState) {
	if s == nil {
		return
	}
	picker := pickFilesDialog
	if picker == nil {
		picker = defaultPickFiles
	}
	go func() {
		var (
			paths []string
			ok    bool
			err   error
		)
		paths, ok, err = picker()
		if err != nil {
			s.setToast("选择文件失败: " + err.Error())
			return
		}
		if !ok || len(paths) == 0 {
			return
		}
		s.addAttachmentFiles(paths)
	}()
}

// renderDownloadsPanel 渲染宽 360pt 的“最近下载”浮窗内容（卡片外观由 PopoverBase 的外层面板提供）。
func renderDownloadsPanel(c *ui.Context, s *chatState, onDone func()) {
	popoverCardShadow(ui.Column(c.Key("composer-downloads-panel")).
		Width(360).
		Padding(Space4).
		Gap(Space3).
		Radius(RadiusMD).
		Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).
		Children(func() {
			// 标题栏
			ui.Row(c.Key("composer-downloads-header")).
				FillWidth().
				Height(ControlSM).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "最近下载").
						FontSize(TypeBody).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				})

			items, err := getCachedDownloads(s)
			if err != nil {
				ui.Text(c, "读取下载目录失败: "+err.Error()).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c)).
					Padding(Space3, 0, Space3, 0)
			} else if len(items) == 0 {
				ui.Text(c, "下载目录中没有文件").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Padding(Space4, 0, Space4, 0)
			} else {
				ui.Column(c.Key("composer-downloads-list")).
					FillWidth().
					Gap(Space0).
					Children(func() {
						for i, entry := range items {
							idx := i
							item := entry
							rowKey := fmt.Sprintf("composer-download-row-%d", idx)

							rowBtn := ui.ButtonBase(c.Key(rowKey)).
								FillWidth().
								Height(ControlLG).
								Radius(RadiusSM).
								Padding(0, Space3).
								Background(ui.Transparent).
								OnClick(func() {
									if s != nil {
										s.addAttachmentFiles([]string{item.Path})
									}
									if onDone != nil {
										onDone()
									}
								})

							if rowBtn.Hovered() {
								composerHoverBg(c, rowBtn, true)
							}

							rowBtn.Children(func() {
								ui.Row(c.Key(rowKey + "-content")).
									FillWidth().
									Height(ControlLG).
									AlignItems(ui.Center).
									Gap(Space3).
									Children(func() {
										ui.Icon(c, IconFile()).
											Size(IconMD, IconMD).
											TextColor(ThemeSecondaryText(c))

										ui.Text(c, truncateMiddle(item.Name, 26)).
											FontSize(TypeBody).
											TextColor(ThemePrimaryText(c)).
											Grow(1)

										ui.Text(c, formatFileSize(item.Size)).
											FontSize(TypeCaption).
											TextColor(ThemeSecondaryText(c))
									})
							})
						}
					})
			}

			// 分隔线
			ui.Row(c.Key("composer-downloads-divider")).
				FillWidth().
				Height(Hairline).
				Background(ThemeDivider(c)).
				Margin(Space1, 0, Space1, 0)

			// 底部操作区
			ui.Column(c.Key("composer-downloads-footer")).
				FillWidth().
				Gap(Space0).
				Children(func() {
					// 在 Finder 中显示
					finderBtn := ui.ButtonBase(c.Key("composer-downloads-finder-btn")).
						FillWidth().
						Height(ControlMD).
						Radius(RadiusSM).
						Padding(0, Space3).
						Background(ui.Transparent).
						OnClick(func() {
							openDirectoryPath(downloadsDir())
							if onDone != nil {
								onDone()
							}
						})
					composerHoverBg(c, finderBtn, finderBtn.Hovered())
					finderBtn.Children(func() {
						ui.Row(c.Key("composer-downloads-finder-content")).
							FillWidth().
							AlignItems(ui.Center).
							Gap(Space3).
							Children(func() {
								ui.Icon(c, IconFolderOpen()).
									Size(IconSM, IconSM).
									TextColor(ThemeSecondaryText(c))
								ui.Text(c, "在 Finder 中显示").
									FontSize(TypeFootnote).
									TextColor(ThemePrimaryText(c))
							})
					})

					// 选择文件…
					chooseBtn := ui.ButtonBase(c.Key("composer-downloads-choose-btn")).
						FillWidth().
						Height(ControlMD).
						Radius(RadiusSM).
						Padding(0, Space3).
						Background(ui.Transparent).
						OnClick(func() {
							promptChooseFiles(s)
							if onDone != nil {
								onDone()
							}
						})
					composerHoverBg(c, chooseBtn, chooseBtn.Hovered())
					chooseBtn.Children(func() {
						ui.Row(c.Key("composer-downloads-choose-content")).
							FillWidth().
							AlignItems(ui.Center).
							Gap(Space3).
							Children(func() {
								ui.Icon(c, IconFile()).
									Size(IconSM, IconSM).
									TextColor(ThemeSecondaryText(c))
								ui.Text(c, "选择文件…").
									FontSize(TypeFootnote).
									TextColor(ThemePrimaryText(c))
							})
					})
				})
		}))
}
