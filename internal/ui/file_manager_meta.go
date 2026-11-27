package mcodeui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// fileManagerMetaSegments 把标签快照转换为元信息细行的文本分段列表。
// 正在加载或加载出错的标签返回 nil；缺失的项不生成分段。
func fileManagerMetaSegments(tab fileManagerTabSnapshot) []string {
	return fileManagerMetaSegmentsAt(tab, time.Now())
}

// fileManagerMetaSegmentsAt 以指定时刻作为相对时间基准，便于测试确定输出。
func fileManagerMetaSegmentsAt(tab fileManagerTabSnapshot, now time.Time) []string {
	if tab.fileLoading || tab.fileErr != "" {
		return nil
	}
	var segments []string
	if tab.totalSize > 0 || (tab.kind != "" && tab.totalSize >= 0) {
		segments = append(segments, formatFileSize(tab.totalSize))
	}
	if !tab.modTime.IsZero() {
		if rel := formatRelativeTimeAt(tab.modTime, now); rel != "" {
			segments = append(segments, rel)
		}
	}
	if tab.kind == "image" {
		if tab.imageBmp != nil {
			if w, h := tab.imageBmp.Size(); w > 0 && h > 0 {
				segments = append(segments, fmt.Sprintf("%d×%d", w, h))
			}
		}
	} else if !tab.hasMore && !tab.limitReached && tab.lineCount > 0 {
		segments = append(segments, fmt.Sprintf("%d 行", tab.lineCount))
	}
	return segments
}

// renderFileManagerMetaBar 在文件预览正文上方渲染 24px 高的元信息细行。
// 正在加载、加载出错或没有任何可显示项时不渲染。
func renderFileManagerMetaBar(c *ui.Context, tab fileManagerTabSnapshot, keyPrefix string) {
	if tab.fileLoading || tab.fileErr != "" {
		return
	}
	segments := fileManagerMetaSegments(tab)
	if len(segments) == 0 && !tab.hasGit {
		return
	}
	ui.Row(c.Key(keyPrefix+"meta-bar")).
		FillWidth().
		Height(RowHeightCompact).
		Padding(0, Space3).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space2).
		Clip().
		Shrink(0).
		Children(func() {
			if len(segments) > 0 {
				ui.Text(c, strings.Join(segments, " · ")).
					FontSize(uiFont(TypeCaption)).
					FontFeatures("tnum").
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					MinWidth(0).
					Shrink(1)
			}
			if tab.hasGit {
				if len(segments) > 0 {
					ui.Text(c, "·").
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeTertiaryText(c)).
						SingleLine().
						Shrink(0)
				}
				renderFileTreeGitTag(c, keyPrefix+"meta-git-"+tab.path, tab.git)
			}
		})
}
