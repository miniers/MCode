package mcodeui

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// riskyOpenExts 是直接交给系统默认程序可能执行代码的扩展名：点击时只在访达中定位，不直接打开。
var riskyOpenExts = map[string]bool{
	".app": true, ".command": true, ".sh": true, ".zsh": true, ".bash": true, ".terminal": true,
	".workflow": true, ".action": true, ".pkg": true, ".dmg": true, ".scpt": true, ".jar": true,
	".tool": true, ".webloc": true, ".url": true,
}

// localFileLinkCandidates 把 Markdown 链接地址解析成可能的本地路径：
// 接受 file:// URL、绝对路径与 ~/ 路径，兼容百分号编码与 #L12 这类位置后缀；其他地址返回 nil。
// 这里只做字符串解析，不访问文件系统，可以在每次构建视图时调用。
func localFileLinkCandidates(dest string) []string {
	path, err := localImagePath(dest)
	if err != nil {
		return nil
	}
	bases := []string{path}
	if i := strings.IndexAny(path, "#?"); i > 0 {
		bases = append(bases, path[:i])
	}
	var candidates []string
	for _, p := range bases {
		candidates = append(candidates, p)
		if dec, err := url.PathUnescape(p); err == nil && dec != p {
			candidates = append(candidates, dec)
		}
	}
	return candidates
}

// localFileLinkPath 把 Markdown 链接地址解析成已存在的本地文件或目录路径：
// 候选路径由 localFileLinkCandidates 给出，这里逐个 stat，第一个存在的为准。
func localFileLinkPath(dest string) (string, bool) {
	for _, p := range localFileLinkCandidates(dest) {
		if _, err := os.Stat(p); err == nil {
			return filepath.Clean(p), true
		}
	}
	return "", false
}

// openLocalFile 用系统默认程序打开文件；可能执行代码的类型改为在访达中定位。
func openLocalFile(path string) {
	if riskyOpenExts[strings.ToLower(filepath.Ext(path))] {
		openItemInFinder(path)
		return
	}
	_ = mygo.Shell.OpenPath(path)
}

// openLocalFilePreview 在有会话工作目录时把工作区内的文件开在右侧文件面板预览；
// 目录、工作区外的文件与无会话时沿用原有打开方式。
func openLocalFilePreview(s *chatState, path string) {
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		openItemInFinder(path)
		return
	}
	threadID, workDir := getSelectedThreadWorkDir(s)
	if err == nil && info.Mode().IsRegular() && threadID != "" &&
		turnFilePreviewPath(workDir, turnEditedFile{path: path, abs: path}) != "" {
		openTurnFile(s, threadID, workDir, turnEditedFile{path: path, abs: path})
		return
	}
	openLocalFile(path)
}

// installLocalFileLinkHook 接管 Markdown 链接点击：本地文件在工作区内预览、否则用默认程序打开，
// http、https、mailto 交给系统浏览器，其余地址（含不存在的文件）给出提示。
func installLocalFileLinkHook(o *markdown.Options, s *chatState) {
	if o == nil || s == nil {
		return
	}
	if o.OnLink == nil {
		o.OnLink = func(dest string) {
			if path, ok := localFileLinkPath(dest); ok {
				openLocalFilePreview(s, path)
				return
			}
			if ui.SafeToOpen(dest) {
				_ = mygo.Shell.OpenExternal(dest)
				return
			}
			if _, err := localImagePath(dest); err == nil {
				s.setToast("文件不存在")
			}
		}
	}
	if o.Preview == nil {
		o.Preview = localFileLinkPreview
	}
	if o.LinkMenu == nil {
		o.LinkMenu = func(menu *ui.Menu, dest string) {
			menu.EditItems()
			if path, ok := localFileLinkPath(dest); ok {
				menu.Separator()
				localFileLinkItems(s, menu, path)
			} else if ui.SafeToOpen(dest) {
				menu.Separator()
				if menu.Item("打开链接").Chosen() {
					_ = mygo.Shell.OpenExternal(dest)
				}
				if menu.Item("复制链接").Chosen() {
					mygo.Clipboard.WriteText(dest)
					s.setToast("已复制")
				}
			}
		}
	}
}

// localRelativePath 返回路径相对当前会话工作目录的路径；不在工作目录内时返回空。
func localRelativePath(s *chatState, path string) string {
	_, workDir := getSelectedThreadWorkDir(s)
	p := filepath.Clean(path)
	return turnFilePreviewPath(workDir, turnEditedFile{path: p, abs: p})
}

func localFileLinkItems(s *chatState, menu *ui.Menu, path string) {
	if menu.Item("侧边栏预览").Chosen() {
		openLocalFilePreview(s, path)
	}
	apps := installedOpenInApps(currentWorkbenchConfig(s), openInStatFn)
	menu.Submenu("打开方式", func(sub *ui.Menu) {
		for _, app := range apps {
			app := app
			if sub.Item(app.Name).Chosen() {
				openInWithApp(app, path)
			}
		}
		if sub.Item("默认程序").Chosen() {
			openLocalFile(path)
		}
	})
	if menu.Item("在访达中显示").Chosen() {
		openItemInFinder(path)
	}
	if menu.Item("复制路径").Chosen() {
		mygo.Clipboard.WriteText(path)
		if s != nil {
			s.setToast("已复制")
		}
	}
	if rel := localRelativePath(s, path); rel != "" {
		if menu.Item("复制相对路径").Chosen() {
			mygo.Clipboard.WriteText(rel)
			if s != nil {
				s.setToast("已复制")
			}
		}
	}
}
