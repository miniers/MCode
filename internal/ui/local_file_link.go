package mcodeui

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// riskyOpenExts 是直接交给系统默认程序可能执行代码的扩展名：点击时只在访达中定位，不直接打开。
var riskyOpenExts = map[string]bool{
	".app": true, ".command": true, ".sh": true, ".zsh": true, ".bash": true, ".terminal": true,
	".workflow": true, ".action": true, ".pkg": true, ".dmg": true, ".scpt": true, ".jar": true,
	".tool": true, ".webloc": true, ".url": true,
}

// localFileLink 是消息正文里指向本地已存在文件的链接。
type localFileLink struct {
	Path  string
	Label string
}

// localFileLinkPath 把 Markdown 链接地址解析成已存在的本地文件或目录路径：
// 接受 file:// URL、绝对路径与 ~/ 路径，兼容百分号编码与 #L12 这类位置后缀；其他地址返回 false。
func localFileLinkPath(dest string) (string, bool) {
	path, err := localImagePath(dest)
	if err != nil {
		return "", false
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
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return filepath.Clean(p), true
		}
	}
	return "", false
}

// extractLocalFileLinks 按出现顺序返回文本里指向本地存在文件的链接，同一路径只保留一条。
func extractLocalFileLinks(text string) []localFileLink {
	if !strings.ContainsAny(text, "[<") {
		return nil
	}
	doc := parser.New(parser.Default)
	doc.Set(text)
	var out []localFileLink
	seen := map[string]bool{}
	for i := 1; i < doc.Len(); i++ {
		switch doc.Block(i).Kind {
		case parser.Paragraph, parser.Heading, parser.TableCell:
		default:
			continue
		}
		in := doc.Inlines(i)
		for k := 0; k < len(in); k = in[k].Next {
			if in[k].Kind != parser.Link || in[k].Pending {
				continue
			}
			path, ok := localFileLinkPath(in[k].Dest)
			if !ok || seen[path] {
				continue
			}
			seen[path] = true
			label := strings.TrimSpace(parser.PlainText(in[k+1 : in[k].Next]))
			if label == "" {
				label = filepath.Base(path)
			}
			out = append(out, localFileLink{Path: path, Label: label})
		}
	}
	return out
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
	if o == nil || s == nil || o.OnLink != nil {
		return
	}
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

// localFileLinkMenu 把消息里的本地文件链接加进右键菜单：单个文件直接给预览、打开方式、
// 在访达中显示与复制路径，多个文件按文件名各开一个子菜单。
func localFileLinkMenu(c *ui.Context, menu *ui.Menu, s *chatState, text string) {
	links := extractLocalFileLinks(text)
	if len(links) == 0 {
		return
	}
	menu.Separator()
	if len(links) == 1 {
		localFileLinkItems(c, s, menu, links[0])
		return
	}
	for _, l := range links {
		menu.Submenu(filepath.Base(l.Path), func(sub *ui.Menu) {
			localFileLinkItems(c, s, sub, l)
		})
	}
}

// localRelativePath 返回路径相对当前会话工作目录的路径；不在工作目录内时返回空。
func localRelativePath(s *chatState, path string) string {
	_, workDir := getSelectedThreadWorkDir(s)
	p := filepath.Clean(path)
	return turnFilePreviewPath(workDir, turnEditedFile{path: p, abs: p})
}

func localFileLinkItems(c *ui.Context, s *chatState, menu *ui.Menu, l localFileLink) {
	if menu.Item("侧边栏预览").Chosen() {
		openLocalFilePreview(s, l.Path)
	}
	apps := installedOpenInApps(currentWorkbenchConfig(s), openInStatFn)
	menu.Submenu("打开方式", func(sub *ui.Menu) {
		for _, app := range apps {
			app := app
			if sub.Item(app.Name).Chosen() {
				openInWithApp(app, l.Path)
			}
		}
		if sub.Item("默认程序").Chosen() {
			openLocalFile(l.Path)
		}
	})
	if menu.Item("在访达中显示").Chosen() {
		openItemInFinder(l.Path)
	}
	if menu.Item("复制路径").Chosen() {
		if c != nil {
			c.WriteClipboard(l.Path)
		}
		if s != nil {
			s.setToast("已复制路径")
		}
	}
	if rel := localRelativePath(s, l.Path); rel != "" {
		if menu.Item("复制相对路径").Chosen() && c != nil {
			c.WriteClipboard(rel)
		}
	}
}
