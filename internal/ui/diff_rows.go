package mcodeui

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// diffLargeFileLineThreshold 是单文件默认折叠的行数阈值：改动行数超过它的文件初始只产出
// 文件标题行与一条折叠占位行，用户点开（记在 diffPanelState.expandedLarge）后才摊平全部改动。
// 大文件（生成代码、vendored 依赖）打开面板时不再一次性铺开数千行。
const diffLargeFileLineThreshold = 2000

// diffRowKind 描述行模型里一行的种类。
type diffRowKind uint8

const (
	diffRowFileHeader     diffRowKind = iota // 文件标题行（可点击折叠）
	diffRowHunkHeader                        // hunk 头
	diffRowLine                              // 一行改动内容
	diffRowBinary                            // 二进制文件提示
	diffRowDeferred                          // 延迟解析大文件的「点击加载」占位行
	diffRowLargeCollapsed                    // 大文件默认折叠的「展开全部」占位行
	diffRowHunksOnly                         // 大文件折叠时「只显示改动块」的选项行
	diffRowHunkMore                          // 「只显示改动块」模式下单个 hunk 的「展开此块」占位行
)

// diffHunkPreviewLines 是「只显示改动块」模式下每个 hunk 先铺开的行数，
// 其余行收进「展开此块」占位行，点开后才摊平整块。
const diffHunkPreviewLines = 40

// diffLineNoWidth 与 diffLineMarkWidth 是改动行里旧/新行号与 +− 标记的固定列宽。
const (
	diffLineNoWidth   float32 = 30
	diffLineMarkWidth float32 = 14
)

// diffRowID 是行在虚拟列表里的身份（ListState.Key 用）：与控件 Key 分开，
// 避免 ui.List 的行 wrapper 与行内控件同名。
type diffRowID struct {
	kind diffRowKind
	path string
	hunk int
	line int
}

// diffDisplayRow 是摊平后的一行。文件头、hunk 头、代码行与占位行都是一种行，
// 虚拟列表只渲染视口内的行，不再每帧遍历全部文件、hunk 与行。
type diffDisplayRow struct {
	Kind diffRowKind
	// ID 是行的稳定身份，供 ui.List 的 ListState.Key 使用。
	ID diffRowID
	// Key 是稳定的行标识：既作控件 Key，也作 ui.List 的 ListState.Key，
	// 保证折叠、滚动与行内状态在重建后跟着同一行。
	Key string
	// File 是文件头与占位行携带的文件快照（Hunks 切片共享，不复制内容）。
	File git.FileDiff
	// FileIdx 是文件在 []git.FileDiff 里的下标，用于计算文件之间的间距。
	FileIdx    int
	FilePath   string
	Status     string
	Adds       int
	Dels       int
	Collapsed  bool
	LineCount  int
	HunkHeader string
	// 代码行：预先把行号与 +− 标记转成字符串，渲染时不再 strconv。
	OldNoText string
	NewNoText string
	Mark      string
	Text      string
	LineKind  byte
}

// diffRowFilePath 返回文件在界面与状态表里使用的路径（删除的文件用旧路径）。
func diffRowFilePath(file git.FileDiff) string {
	filePath := file.NewPath
	if file.Status == "deleted" || filePath == "" {
		filePath = file.OldPath
	}
	return filePath
}

// diffFileLineCount 返回文件全部 hunk 的行数。
func diffFileLineCount(file git.FileDiff) int {
	total := 0
	for _, hunk := range file.Hunks {
		total += len(hunk.Lines)
	}
	return total
}

// prefillDiffStats 在加载阶段补齐文件的增删统计，渲染期不再逐行回退扫描。
// 延迟解析的大文件已由解析阶段统计好，二进制文件显示为解析阶段给的值。
func prefillDiffStats(files []git.FileDiff) {
	for i := range files {
		file := &files[i]
		if file.Binary || file.IsDeferred || file.Additions != 0 || file.Deletions != 0 {
			continue
		}
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				switch line.Kind {
				case '+':
					file.Additions++
				case '-':
					file.Deletions++
				}
			}
		}
	}
}

// diffFileAddsDels 返回文件的增删行数。加载阶段已预填；这里只对未经预填的数据
// （合并审查器、看板等其它来源）做一次兜底统计，发生在行模型构建时而非每帧。
func diffFileAddsDels(file git.FileDiff) (int, int) {
	if file.Binary || file.IsDeferred || len(file.Hunks) == 0 ||
		file.Additions != 0 || file.Deletions != 0 {
		return file.Additions, file.Deletions
	}
	adds, dels := 0, 0
	for _, hunk := range file.Hunks {
		for _, line := range hunk.Lines {
			switch line.Kind {
			case '+':
				adds++
			case '-':
				dels++
			}
		}
	}
	return adds, dels
}

// diffHunkStateKey 返回 hunk 在 expandedHunks 里的键：文件路径 + hunk 序号。
func diffHunkStateKey(filePath string, hunkIdx int) string {
	return filePath + "#" + strconv.Itoa(hunkIdx)
}

// diffLineRow 把一行改动转成行模型里的一行。
func diffLineRow(filePath, hunkKey string, fileIdx, hunkIdx, lineIdx int, line git.Line) diffDisplayRow {
	row := diffDisplayRow{
		Kind:     diffRowLine,
		ID:       diffRowID{kind: diffRowLine, path: filePath, hunk: hunkIdx, line: lineIdx},
		Key:      "diff-line-" + hunkKey + "-" + strconv.Itoa(lineIdx),
		FileIdx:  fileIdx,
		FilePath: filePath,
		Text:     line.Text,
		LineKind: line.Kind,
		Mark:     " ",
	}
	switch line.Kind {
	case '+':
		row.Mark = "+"
	case '-':
		row.Mark = "-"
	}
	if line.OldNo > 0 {
		row.OldNoText = strconv.Itoa(line.OldNo)
	}
	if line.NewNo > 0 {
		row.NewNoText = strconv.Itoa(line.NewNo)
	}
	return row
}

// buildDiffRows 按当前折叠状态把文件列表摊平成一维行序：
// 文件头 / hunk 头 / 代码行 / 二进制提示 / 延迟解析占位 / 大文件折叠占位。
// 折叠的文件只产出文件头行；已展开的延迟补丁用缓存结果替换原文件。
// hunksOnly 记录选了「只显示改动块」的大文件，expandedHunks 记录其中已整块展开的 hunk
// （键见 diffHunkStateKey）。
func buildDiffRows(files []git.FileDiff, collapsed map[string]bool, expandedDeferred map[string]git.FileDiff, expandedLarge, hunksOnly, expandedHunks map[string]bool) []diffDisplayRow {
	if len(files) == 0 {
		return nil
	}
	// 先数一遍行数（只走 hunk 计数，不碰行内容），一次分配到位，避免大文件反复扩容
	capacity := len(files) * 2
	for i := range files {
		capacity += diffFileLineCount(files[i])
	}
	rows := make([]diffDisplayRow, 0, capacity)
	for i := range files {
		file := files[i]
		filePath := diffRowFilePath(file)
		if expanded, ok := expandedDeferred[filePath]; ok {
			file = expanded
		}
		adds, dels := diffFileAddsDels(file)
		lineCount := diffFileLineCount(file)
		isCollapsed := collapsed[filePath]

		rows = append(rows, diffDisplayRow{
			Kind:      diffRowFileHeader,
			ID:        diffRowID{kind: diffRowFileHeader, path: filePath},
			Key:       "diff-file-header-" + filePath,
			File:      file,
			FileIdx:   i,
			FilePath:  filePath,
			Status:    file.Status,
			Adds:      adds,
			Dels:      dels,
			Collapsed: isCollapsed,
			LineCount: lineCount,
		})
		if isCollapsed {
			continue
		}

		if file.Binary {
			rows = append(rows, diffDisplayRow{
				Kind:     diffRowBinary,
				ID:       diffRowID{kind: diffRowBinary, path: filePath},
				Key:      "diff-binary-" + filePath,
				File:     file,
				FileIdx:  i,
				FilePath: filePath,
			})
			continue
		}

		// 延迟解析的大文件还没有 hunk：先给一条可点击的占位行，点开后再解析出详细改动
		if file.IsDeferred && len(file.Hunks) == 0 {
			rows = append(rows, diffDisplayRow{
				Kind:      diffRowDeferred,
				ID:        diffRowID{kind: diffRowDeferred, path: filePath},
				Key:       "diff-deferred-" + filePath,
				File:      file,
				FileIdx:   i,
				FilePath:  filePath,
				LineCount: lineCount,
			})
			continue
		}

		// 大文件默认折叠：给出文件头、「展开全部」占位行，以及「只显示改动块」选项行
		if lineCount > diffLargeFileLineThreshold && !expandedLarge[filePath] {
			rows = append(rows, diffDisplayRow{
				Kind:      diffRowLargeCollapsed,
				ID:        diffRowID{kind: diffRowLargeCollapsed, path: filePath},
				Key:       "diff-large-" + filePath,
				File:      file,
				FileIdx:   i,
				FilePath:  filePath,
				LineCount: lineCount,
			})
			if !hunksOnly[filePath] {
				rows = append(rows, diffDisplayRow{
					Kind:      diffRowHunksOnly,
					ID:        diffRowID{kind: diffRowHunksOnly, path: filePath},
					Key:       "diff-hunks-only-" + filePath,
					File:      file,
					FileIdx:   i,
					FilePath:  filePath,
					LineCount: lineCount,
				})
				continue
			}
			// 只显示改动块：每块先给 hunk 头与前 diffHunkPreviewLines 行，
			// 其余行收进「展开此块（共 N 行）」占位行，点开后才摊平整块。
			for hunkIdx := range file.Hunks {
				hunk := file.Hunks[hunkIdx]
				hunkKey := filePath + "-" + strconv.Itoa(hunkIdx)
				rows = append(rows, diffDisplayRow{
					Kind:       diffRowHunkHeader,
					ID:         diffRowID{kind: diffRowHunkHeader, path: filePath, hunk: hunkIdx},
					Key:        "diff-hunk-hdr-" + hunkKey,
					FileIdx:    i,
					FilePath:   filePath,
					HunkHeader: hunk.Header,
				})
				limit := len(hunk.Lines)
				if limit > diffHunkPreviewLines && !expandedHunks[diffHunkStateKey(filePath, hunkIdx)] {
					limit = diffHunkPreviewLines
				}
				for lineIdx := 0; lineIdx < limit; lineIdx++ {
					rows = append(rows, diffLineRow(filePath, hunkKey, i, hunkIdx, lineIdx, hunk.Lines[lineIdx]))
				}
				if limit < len(hunk.Lines) {
					rows = append(rows, diffDisplayRow{
						Kind:      diffRowHunkMore,
						ID:        diffRowID{kind: diffRowHunkMore, path: filePath, hunk: hunkIdx},
						Key:       "diff-hunk-more-" + hunkKey,
						File:      file,
						FileIdx:   i,
						FilePath:  filePath,
						LineCount: len(hunk.Lines),
					})
				}
			}
			continue
		}

		for hunkIdx := range file.Hunks {
			hunk := file.Hunks[hunkIdx]
			hunkKey := filePath + "-" + strconv.Itoa(hunkIdx)
			rows = append(rows, diffDisplayRow{
				Kind:       diffRowHunkHeader,
				ID:         diffRowID{kind: diffRowHunkHeader, path: filePath, hunk: hunkIdx},
				Key:        "diff-hunk-hdr-" + hunkKey,
				FileIdx:    i,
				FilePath:   filePath,
				HunkHeader: hunk.Header,
			})
			for lineIdx := range hunk.Lines {
				rows = append(rows, diffLineRow(filePath, hunkKey, i, hunkIdx, lineIdx, hunk.Lines[lineIdx]))
			}
		}
	}
	return rows
}

// diffHashString / diffHashInt 是零分配的 FNV-1a 累加，用于行模型缓存签名。
func diffHashString(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func diffHashInt(h uint64, v int) uint64 {
	h ^= uint64(uint32(v))
	h *= 1099511628211
	return h
}

// diffRowsSignature 用「数据版本 + 结构 + 折叠状态」算出行模型缓存签名：
// 一致就复用已摊平的行，切轮次、折叠、展开时才重建。
func diffRowsSignature(dp *diffPanelState, files []git.FileDiff) uint64 {
	if dp == nil {
		return 0
	}
	h := uint64(14695981039346656037) // FNV-1a offset basis
	h = diffHashInt(h, int(dp.rowsRev))
	h = diffHashInt(h, len(files))
	for i := range files {
		file := &files[i]
		h = diffHashString(h, file.NewPath)
		h = diffHashString(h, file.OldPath)
		h = diffHashString(h, file.Status)
		h = diffHashInt(h, file.Additions)
		h = diffHashInt(h, file.Deletions)
		if file.Binary {
			h = diffHashInt(h, 1)
		}
		if file.IsDeferred {
			h = diffHashInt(h, 2)
		}
		h = diffHashInt(h, len(file.Hunks))
		for j := range file.Hunks {
			h = diffHashString(h, file.Hunks[j].Header)
			h = diffHashInt(h, len(file.Hunks[j].Lines))
		}
	}
	// 折叠与展开集合与遍历顺序无关，用计数 + 异或哈希，避免每帧排序。
	h = diffHashInt(h, len(dp.collapsed))
	for k, v := range dp.collapsed {
		if v {
			h ^= diffHashString(1, k)
			h *= 1099511628211
		}
	}
	h = diffHashInt(h, len(dp.expandedLarge))
	for k, v := range dp.expandedLarge {
		if v {
			h ^= diffHashString(2, k)
			h *= 1099511628211
		}
	}
	// 「只显示改动块」与按块展开同样影响行模型，一起纳入签名
	h = diffHashInt(h, len(dp.hunksOnly))
	for k, v := range dp.hunksOnly {
		if v {
			h ^= diffHashString(3, k)
			h *= 1099511628211
		}
	}
	h = diffHashInt(h, len(dp.expandedHunks))
	for k, v := range dp.expandedHunks {
		if v {
			h ^= diffHashString(4, k)
			h *= 1099511628211
		}
	}
	h = diffHashInt(h, len(dp.expandedDeferred))
	return h
}

// rebuildDiffRowsLocked 重新摊平并缓存行模型；调用方需持有 diffPanelMu。
func (dp *diffPanelState) rebuildDiffRowsLocked() {
	if dp == nil {
		return
	}
	dp.rows = buildDiffRows(dp.files, dp.collapsed, dp.expandedDeferred, dp.expandedLarge, dp.hunksOnly, dp.expandedHunks)
	dp.rowsSig = diffRowsSignature(dp, dp.files)
}

// diffPanelRowsFor 返回当前数据的行模型，签名一致就复用缓存。
// 传 nil 的 dp（合并审查器、看板等无状态调用方）直接现场摊平。
func diffPanelRowsFor(dp *diffPanelState, files []git.FileDiff) []diffDisplayRow {
	if dp == nil {
		return buildDiffRows(files, nil, nil, nil, nil, nil)
	}
	diffPanelMu.Lock()
	defer diffPanelMu.Unlock()
	sig := diffRowsSignature(dp, files)
	if dp.rows != nil && dp.rowsSig == sig {
		return dp.rows
	}
	dp.rows = buildDiffRows(files, dp.collapsed, dp.expandedDeferred, dp.expandedLarge, dp.hunksOnly, dp.expandedHunks)
	dp.rowsSig = sig
	return dp.rows
}

// renderDiffRow 渲染行模型里的一行；只有视口内的行会被调用。
func renderDiffRow(c *ui.Context, s *chatState, dp *diffPanelState, row diffDisplayRow) {
	switch row.Kind {
	case diffRowFileHeader:
		renderDiffFileHeaderRow(c, s, dp, row)
	case diffRowHunkHeader:
		ui.Row(c.Key(row.Key)).
			FillWidth().
			Padding(Space0, Space3).
			Background(ThemeHover(c)).
			Children(func() {
				ui.Text(c, row.HunkHeader).
					Font("monospace").
					FontSize(TypeCode).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			})
	case diffRowLine:
		renderDiffCodeRow(c, row)
	case diffRowBinary:
		ui.Row(c.Key(row.Key)).
			Padding(Space3, Space4).
			Children(func() {
				ui.Text(c, "二进制文件").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			})
	case diffRowDeferred:
		renderDiffDeferredRow(c, s, dp, row)
	case diffRowLargeCollapsed:
		renderDiffLargeCollapsedRow(c, s, dp, row)
	case diffRowHunksOnly:
		renderDiffHunksOnlyRow(c, s, dp, row)
	case diffRowHunkMore:
		renderDiffHunkMoreRow(c, s, dp, row)
	}
}

// renderDiffFileHeaderRow 渲染文件标题行：折叠箭头、路径、状态标签与 +N/-N。
// 卡片外框在虚拟化后退化为「标题行 + 分隔线 + 行块」。
func renderDiffFileHeaderRow(c *ui.Context, s *chatState, dp *diffPanelState, row diffDisplayRow) {
	filePath := row.FilePath
	headerRow := ui.ButtonBase(c.Key(row.Key)).
		FillWidth().
		Padding(Space3, Space3).
		Background(ThemeSurface(c)).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Label(filePath).
		OnClick(func() {
			if dp == nil {
				return
			}
			diffPanelMu.Lock()
			if dp.collapsed == nil {
				dp.collapsed = make(map[string]bool)
			}
			dp.collapsed[filePath] = !dp.collapsed[filePath]
			dp.rebuildDiffRowsLocked()
			diffPanelMu.Unlock()
			if s != nil {
				s.invalidate()
			}
		})
	if row.FileIdx > 0 {
		headerRow.Margin(Space3, 0, 0, 0)
	}
	if headerRow.Hovered() {
		headerRow.Background(ThemeHover(c))
	}
	headerRow.Children(func() {
		ui.Row(c.Key("diff-file-header-inner-" + filePath)).
			Gap(Space2).
			AlignItems(ui.Center).
			FillWidth().
			Children(func() {
				var chv *ui.SVG = IconChevronDown()
				if row.Collapsed {
					chv = IconChevronRight()
				}
				ui.Icon(c, chv).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))

				ui.Text(c, filePath).
					FontSize(TypeFootnote).
					FontWeight(500).
					TextColor(ThemePrimaryText(c)).
					Grow(1).
					SingleLine()

				renderDiffStatusBadge(c, row.Status)

				if row.Adds > 0 {
					ui.Text(c, "+"+strconv.Itoa(row.Adds)).
						FontSize(TypeFootnote).
						FontWeight(500).
						TextColor(ThemeSuccess(c))
				}
				if row.Dels > 0 {
					ui.Text(c, "-"+strconv.Itoa(row.Dels)).
						FontSize(TypeFootnote).
						FontWeight(500).
						TextColor(ThemeDanger(c))
				}
			})
	})
}

// renderDiffCodeRow 渲染一行改动：旧行号、新行号、+− 标记与正文。
func renderDiffCodeRow(c *ui.Context, row diffDisplayRow) {
	lineBg := ui.Transparent
	markColor := ThemeSecondaryText(c)
	switch row.LineKind {
	case '+':
		lineBg = ThemeSuccess(c).Alpha(0.12)
		markColor = ThemeSuccess(c)
	case '-':
		lineBg = ThemeDanger(c).Alpha(0.12)
		markColor = ThemeDanger(c)
	}

	ui.Row(c.Key(row.Key)).
		FillWidth().
		Background(lineBg).
		Padding(Hairline, Space3, Hairline, Space2).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, row.OldNoText).
				Width(diffLineNoWidth).
				Font("monospace").
				FontSize(TypeCode).
				TextColor(ThemeSecondaryText(c))

			ui.Text(c, row.NewNoText).
				Width(diffLineNoWidth).
				Font("monospace").
				FontSize(TypeCode).
				TextColor(ThemeSecondaryText(c))

			ui.Text(c, row.Mark).
				Width(diffLineMarkWidth).
				Font("monospace").
				FontSize(TypeCode).
				TextColor(markColor)

			ui.Text(c, row.Text).
				Font("monospace").
				FontSize(TypeCode).
				TextColor(ThemePrimaryText(c)).
				Grow(1)
		})
}

// renderDiffDeferredRow 渲染延迟解析大文件的「点击加载详细改动」占位行。
func renderDiffDeferredRow(c *ui.Context, s *chatState, dp *diffPanelState, row diffDisplayRow) {
	filePath := row.FilePath
	deferredRow := ui.ButtonBase(c.Key(row.Key)).
		FillWidth().
		Padding(Space3, Space4).
		Label("文件内容较大，点击加载详细改动").
		Cursor(ui.CursorPointer).
		OnClick(func() {
			expandDeferredDiffFile(s, dp, filePath, row.File)
		})
	if deferredRow.Hovered() {
		deferredRow.Background(ThemeHover(c))
	}
	deferredRow.Children(func() {
		ui.Row(c.Key("diff-deferred-inner-" + filePath)).
			Gap(Space3).
			AlignItems(ui.Center).
			FillWidth().
			Children(func() {
				ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				ui.Text(c, "文件内容较大，点击加载详细改动").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Grow(1)
			})
	})
}

// renderDiffLargeCollapsedRow 渲染大文件默认折叠后的「展开全部」占位行。
// 点开后把文件路径记进 expandedLarge，行模型重建为完整改动，用户显式展开的状态跨轮次保留。
func renderDiffLargeCollapsedRow(c *ui.Context, s *chatState, dp *diffPanelState, row diffDisplayRow) {
	filePath := row.FilePath
	label := "该文件改动 " + strconv.Itoa(row.LineCount) + " 行，已折叠；点击展开全部"
	largeRow := ui.ButtonBase(c.Key(row.Key)).
		FillWidth().
		Padding(Space3, Space4).
		Label(label).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			if dp == nil {
				return
			}
			diffPanelMu.Lock()
			if dp.expandedLarge == nil {
				dp.expandedLarge = make(map[string]bool)
			}
			dp.expandedLarge[filePath] = true
			dp.rebuildDiffRowsLocked()
			diffPanelMu.Unlock()
			if s != nil {
				s.invalidate()
			}
		})
	if largeRow.Hovered() {
		largeRow.Background(ThemeHover(c))
	}
	largeRow.Children(func() {
		ui.Row(c.Key("diff-large-inner-" + filePath)).
			Gap(Space3).
			AlignItems(ui.Center).
			FillWidth().
			Children(func() {
				ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				ui.Text(c, label).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Grow(1)
			})
	})
}

// renderDiffHunksOnlyRow 渲染大文件折叠时的「只显示改动块」选项行：
// 选中后该文件按 hunk 展开，每块先显示前 diffHunkPreviewLines 行。
func renderDiffHunksOnlyRow(c *ui.Context, s *chatState, dp *diffPanelState, row diffDisplayRow) {
	filePath := row.FilePath
	label := "只显示改动块（每块先显示前 " + strconv.Itoa(diffHunkPreviewLines) + " 行）"
	optionRow := ui.ButtonBase(c.Key(row.Key)).
		FillWidth().
		Padding(Space3, Space4).
		Label(label).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			if dp == nil {
				return
			}
			diffPanelMu.Lock()
			if dp.hunksOnly == nil {
				dp.hunksOnly = make(map[string]bool)
			}
			dp.hunksOnly[filePath] = true
			dp.rebuildDiffRowsLocked()
			diffPanelMu.Unlock()
			if s != nil {
				s.invalidate()
			}
		})
	if optionRow.Hovered() {
		optionRow.Background(ThemeHover(c))
	}
	optionRow.Children(func() {
		ui.Row(c.Key("diff-hunks-only-inner-" + filePath)).
			Gap(Space3).
			AlignItems(ui.Center).
			FillWidth().
			Children(func() {
				ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				ui.Text(c, label).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Grow(1)
			})
	})
}

// renderDiffHunkMoreRow 渲染「只显示改动块」模式下单个 hunk 的「展开此块」占位行。
// 点开只展开这一块，其它块保持折叠；显式展开的状态跨轮次保留。
func renderDiffHunkMoreRow(c *ui.Context, s *chatState, dp *diffPanelState, row diffDisplayRow) {
	filePath := row.FilePath
	hunkIdx := row.ID.hunk
	label := "展开此块（共 " + strconv.Itoa(row.LineCount) + " 行）"
	moreRow := ui.ButtonBase(c.Key(row.Key)).
		FillWidth().
		Padding(Space3, Space4).
		Label(label).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			if dp == nil {
				return
			}
			diffPanelMu.Lock()
			if dp.expandedHunks == nil {
				dp.expandedHunks = make(map[string]bool)
			}
			dp.expandedHunks[diffHunkStateKey(filePath, hunkIdx)] = true
			dp.rebuildDiffRowsLocked()
			diffPanelMu.Unlock()
			if s != nil {
				s.invalidate()
			}
		})
	if moreRow.Hovered() {
		moreRow.Background(ThemeHover(c))
	}
	moreRow.Children(func() {
		ui.Row(c.Key("diff-hunk-more-inner-" + filePath + "-" + strconv.Itoa(hunkIdx))).
			Gap(Space3).
			AlignItems(ui.Center).
			FillWidth().
			Children(func() {
				ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				ui.Text(c, label).
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Grow(1)
			})
	})
}
