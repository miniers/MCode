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

const downloadsCacheTTL = 5 * time.Second

var downloadsScanFn = func(dir string) ([]downloadEntry, error) {
	return recentDownloads(dir, 15)
}

type downloadsCacheEntry struct {
	dir      string
	items    []downloadEntry
	err      error
	lastScan time.Time
	scanning bool
}

var (
	downloadsCacheMu  sync.Mutex
	downloadsCache    = make(map[string]downloadsCacheEntry)
	downloadsCacheGen uint64
)

func clearDownloadsCache() {
	downloadsCacheMu.Lock()
	defer downloadsCacheMu.Unlock()
	downloadsCache = make(map[string]downloadsCacheEntry)
	downloadsCacheGen++
}

// getCachedDownloads 只读当前内存缓存的最近下载文件列表；缓存为空或超过 5 秒 TTL 时
// 仅派发一次后台 goroutine 扫描磁盘并在完成后调用 s.invalidate()，不在渲染路径同步调 os.ReadDir。
func getCachedDownloads(s *chatState) ([]downloadEntry, error) {
	dir := downloadsDir()
	now := time.Now()

	downloadsCacheMu.Lock()
	entry, ok := downloadsCache[dir]
	fresh := ok && entry.dir == dir && !entry.lastScan.IsZero() && now.Sub(entry.lastScan) < downloadsCacheTTL
	var items []downloadEntry
	var err error
	if ok && entry.dir == dir && !entry.lastScan.IsZero() {
		items = append([]downloadEntry(nil), entry.items...)
		err = entry.err
	}
	if !fresh && !entry.scanning {
		entry.dir = dir
		entry.scanning = true
		downloadsCache[dir] = entry
		gen := downloadsCacheGen
		scanFn := downloadsScanFn
		go func() {
			scanned, scanErr := scanFn(dir)
			downloadsCacheMu.Lock()
			if downloadsCacheGen == gen {
				cur := downloadsCache[dir]
				cur.dir = dir
				cur.items = scanned
				cur.err = scanErr
				cur.lastScan = time.Now()
				cur.scanning = false
				downloadsCache[dir] = cur
			}
			downloadsCacheMu.Unlock()
			if s != nil {
				s.invalidate()
			}
		}()
	}
	downloadsCacheMu.Unlock()
	return items, err
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
	panel := ui.Column(c.Key("composer-downloads-panel")).
		Width(360).
		Padding(Space4).
		Gap(Space3)
	if !isAppFlyoutServices(c.Services()) {
		panel = popoverCardShadow(panel.Radius(RadiusMD).Background(ThemeSurface(c)).Border(Hairline, ThemeBorder(c)))
	}
	panel.Children(func() {
		// 标题栏
		ui.Row(c.Key("composer-downloads-header")).
			FillWidth().
			Height(ControlSM).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, "最近下载").
					FontSize(uiFont(TypeBody)).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))
			})

		items, err := getCachedDownloads(s)
		if err != nil {
			ui.Text(c, "读取下载目录失败: "+err.Error()).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeDanger(c)).
				Padding(Space3, 0, Space3, 0)
		} else if len(items) == 0 {
			ui.Text(c, "下载目录中没有文件").
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c)).
				Padding(Space4, 0, Space4, 0)
		} else {
			ui.Scroll(c.Key("composer-downloads-scroll")).
				Label("composer-downloads-scroll").
				FillWidth().
				MaxHeight(360).
				Children(func() {
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
												TextColor(ThemeSecondaryText(c)).
												Shrink(0)

											ui.Text(c, item.Name).
												FontSize(uiFont(TypeBody)).
												TextColor(ThemePrimaryText(c)).
												SingleLine().
												TruncateMiddle().
												Grow(1).
												MinWidth(0)

											ui.Text(c, formatFileSize(item.Size)).
												FontSize(uiFont(TypeCaption)).
												TextColor(ThemeSecondaryText(c)).
												Shrink(0)
										})
								})
							}
						})
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
							ui.Text(c, revealInFileManagerLabel()).
								FontSize(uiFont(TypeFootnote)).
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
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemePrimaryText(c))
						})
				})
			})
	})
}
