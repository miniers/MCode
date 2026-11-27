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

// composerDownloadsScrollMaxHeight 限制浮层内「最近下载」列表的最大滚动高度。
const composerDownloadsScrollMaxHeight float32 = 240

// renderDownloadsPanel 渲染可折叠的“最近下载”分组（默认展开，点击标题切换折叠状态）。
func renderDownloadsPanel(c *ui.Context, s *chatState, onDone func()) {
	panel := ui.Column(c.Key("composer-downloads-panel")).
		FillWidth().
		Gap(Space1)

	var expanded bool
	var toggleExpanded func()
	if s != nil {
		expanded = downloadsExpandedFor(s)
		toggleExpanded = func() {
			toggleDownloadsExpanded(s)
			s.invalidate()
		}
	} else {
		localExpanded := ui.Local(panel, "composer-downloads-expanded", func() bool { return true })
		expanded = *localExpanded
		toggleExpanded = func() {
			*localExpanded = !*localExpanded
		}
	}

	panel.Children(func() {
		// 可折叠分组标题栏：带箭头，点击展开/收起列表。
		hdrBtn := ui.ButtonBase(c.Key("composer-downloads-header-btn")).
			Label("最近下载").
			Expanded(expanded).
			FillWidth().
			Height(ControlSM).
			Radius(RadiusXS).
			Padding(0, Space2).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Background(ui.Transparent).
			OnClick(toggleExpanded)
		composerHoverBg(c, hdrBtn, hdrBtn.Hovered())
		focusRing(c, hdrBtn)
		hdrBtn.Children(func() {
			ui.Text(c, "最近下载").
				FontSize(uiFont(TypeCaption)).
				FontWeight(weightFor(c, 500)).
				TextColor(ThemeSecondaryText(c))

			chevron := ui.Icon(c.Key("composer-downloads-chevron"), IconChevronDown()).
				Size(IconSM, IconSM).
				TextColor(ThemeSecondaryText(c)).
				Shrink(0)
			targetAngle := float32(0)
			if !expanded {
				targetAngle = -90
			}
			if reduceMotion(c) {
				chevron.Jump("composer-downloads-chevron", targetAngle)
				chevron.Rotate(targetAngle)
			} else {
				chevron.Rotate(chevron.AnimateTo("composer-downloads-chevron", targetAngle, ui.Animation{
					Duration: animateDuration(c, motionUnfold),
					Ease:     motionEaseStandard,
				}))
			}
		})

		motionUnfoldBody(c, "composer-downloads-body", expanded, func() {
			items, err := getCachedDownloads(s)
			if err != nil {
				ui.Text(c, "读取下载目录失败: "+err.Error()).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeDanger(c)).
					Padding(Space2, Space2, Space2, Space2)
				return
			}
			if len(items) == 0 {
				ui.Text(c, "下载目录中没有文件").
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c)).
					Padding(Space2, Space2, Space2, Space2)
				return
			}

			ui.Scroll(c.Key("composer-downloads-scroll")).
				Label("composer-downloads-scroll").
				FillWidth().
				MaxHeight(composerDownloadsScrollMaxHeight).
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
									Height(RowHeight).
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
										Height(RowHeight).
										AlignItems(ui.Center).
										Gap(Space2).
										Children(func() {
											ui.Icon(c, IconFile()).
												Size(IconSM, IconSM).
												TextColor(ThemeSecondaryText(c)).
												Shrink(0)

											ui.Text(c, item.Name).
												FontSize(uiFont(TypeFootnote)).
												TextColor(ThemePrimaryText(c)).
												SingleLine().
												TruncateMiddle().
												Grow(1).
												MinWidth(0)

											ui.Text(c, formatFileSize(item.Size)).
												FontSize(uiFont(TypeCaption)).
												TextColor(ThemeTertiaryText(c)).
												Shrink(0)
										})
								})
							}
						})
				})
		})
	})
}
