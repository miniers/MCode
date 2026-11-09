package mcodeui

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 文件面板写操作弹窗的动作类型。
const (
	fileManagerOpsNewFile = "new-file"
	fileManagerOpsNewDir  = "new-dir"
	fileManagerOpsRename  = "rename"
	fileManagerOpsTrash   = "trash"
)

// fileManagerOpsCardWidth 是写操作弹窗卡片宽度。
const fileManagerOpsCardWidth float32 = 360
const fileManagerOpsCardHeight float32 = 240
const modalDragFileManagerOp = "modal-drag-file-manager-op"

// fileManagerOpsDialog 是文件面板唯一的写操作弹窗状态：同一时刻只开一个，
// kind 决定标题、输入框与提交动作。
type fileManagerOpsDialog struct {
	open bool
	kind string
	// targetPath 在新建时是父目录（空串表示工作区根目录），重命名和删除时是目标路径。
	targetPath string
	// threadID 是打开弹窗时的会话：请求回来时会话已经换掉就不再回填结果。
	threadID string
	name     string
	err      string
	busy     bool
	// gen 是提交代数：再次提交或关闭弹窗后，旧请求的结果直接丢弃。
	gen int
}

// fileManagerOpsOpen 返回写操作弹窗当前是否打开。
func fileManagerOpsOpen(s *chatState) bool {
	st := getFileManagerState(s)
	if st == nil {
		return false
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	return st.ops.open
}

// closeFileManagerOpsDialog 关闭弹窗；进行中的请求回来时会因代数不符被丢弃。
func (s *chatState) closeFileManagerOpsDialog() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	st.ops = fileManagerOpsDialog{}
	fileManagerMu.Unlock()
	modalDragReset(modalDragFileManagerOp)
	s.invalidate()
}

// openFileManagerOpsDialog 打开写操作弹窗，重置输入框、错误与忙碌状态。
func (s *chatState) openFileManagerOpsDialog(kind, targetPath, name string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	// 代数只增不减：关掉再开时旧请求不会撞上新的弹窗。
	st.opsSeq++
	st.ops = fileManagerOpsDialog{
		open:       true,
		kind:       kind,
		targetPath: targetPath,
		threadID:   st.threadID,
		name:       name,
		gen:        st.opsSeq,
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// openFileManagerNewDialog 打开「新建文件 / 新建文件夹」弹窗；parentDir 为空表示工作区根目录。
func openFileManagerNewDialog(s *chatState, parentDir string, dir bool) {
	kind := fileManagerOpsNewFile
	if dir {
		kind = fileManagerOpsNewDir
	}
	s.openFileManagerOpsDialog(kind, parentDir, "")
}

// openFileManagerRenameDialog 打开重命名弹窗，输入框预填原文件名。
func openFileManagerRenameDialog(s *chatState, path string) {
	s.openFileManagerOpsDialog(fileManagerOpsRename, path, fileManagerTabName(path))
}

// openFileManagerTrashDialog 打开「移到废纸篓」确认弹窗。
func openFileManagerTrashDialog(s *chatState, path string) {
	s.openFileManagerOpsDialog(fileManagerOpsTrash, path, "")
}

// fileManagerValidateName 校验新建 / 重命名的名字，返回给用户看的错误（空串表示合法）。
func fileManagerValidateName(name string) string {
	switch {
	case name == "":
		return "名字不能为空"
	case strings.Contains(name, "/"):
		return "名字不能包含斜杠"
	case name == "." || name == "..":
		return "名字不能是 . 或 .."
	}
	return ""
}

// fileManagerJoinPath 拼接相对路径，父目录为空时就是名字本身。
func fileManagerJoinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

// fileManagerParentPath 返回相对路径的父目录（不含最后的文件名），根目录下返回空串。
func fileManagerParentPath(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return ""
	}
	return path[:idx]
}

// submitFileManagerOpsDialog 校验输入并在后台提交当前弹窗的写操作。
func (s *chatState) submitFileManagerOpsDialog() {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	client, ok := fileOpsClient(s)
	if !ok {
		return
	}

	fileManagerMu.Lock()
	if !st.ops.open || st.ops.busy {
		fileManagerMu.Unlock()
		return
	}
	op := st.ops
	op.name = strings.TrimSpace(op.name)
	// 新建要先校验名字；重命名和删除不读输入框。
	if op.kind == fileManagerOpsNewFile || op.kind == fileManagerOpsNewDir || op.kind == fileManagerOpsRename {
		if msg := fileManagerValidateName(op.name); msg != "" {
			st.ops.name = op.name
			st.ops.err = msg
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
	}
	op.busy = true
	op.err = ""
	st.ops = op
	fileManagerMu.Unlock()
	s.invalidate()

	target := op.targetPath
	newPath := ""
	switch op.kind {
	case fileManagerOpsNewFile, fileManagerOpsNewDir:
		newPath = fileManagerJoinPath(target, op.name)
	case fileManagerOpsRename:
		newPath = fileManagerJoinPath(fileManagerParentPath(target), op.name)
	}

	go func(op fileManagerOpsDialog, target, newPath string) {
		ctx := context.Background()
		var err error
		switch op.kind {
		case fileManagerOpsNewFile:
			_, err = client.FilesCreate(ctx, op.threadID, newPath, false)
		case fileManagerOpsNewDir:
			_, err = client.FilesCreate(ctx, op.threadID, newPath, true)
		case fileManagerOpsRename:
			_, err = client.FilesRename(ctx, op.threadID, target, newPath)
		case fileManagerOpsTrash:
			err = client.FilesTrash(ctx, op.threadID, target)
		}
		s.finishFileManagerOps(op, newPath, err)
	}(op, target, newPath)
}

// finishFileManagerOps 处理写操作的结果：成功则关弹窗、同步 tab 并刷新列表；
// 失败留在弹窗里显示错误。
func (s *chatState) finishFileManagerOps(op fileManagerOpsDialog, newPath string, err error) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	if !st.ops.open || st.ops.gen != op.gen {
		fileManagerMu.Unlock()
		return
	}
	if err != nil {
		st.ops.busy = false
		st.ops.err = err.Error()
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	// 请求期间切走了会话：列表与 tab 都已经换过一轮，结果不再回填。
	if st.threadID != op.threadID {
		st.ops = fileManagerOpsDialog{}
		fileManagerMu.Unlock()
		modalDragReset(modalDragFileManagerOp)
		s.invalidate()
		return
	}
	st.ops = fileManagerOpsDialog{}
	threadID := st.threadID
	fileManagerMu.Unlock()
	modalDragReset(modalDragFileManagerOp)

	switch op.kind {
	case fileManagerOpsRename:
		s.renameFileManagerTabPaths(op.targetPath, newPath)
	case fileManagerOpsTrash:
		s.closeFileManagerTabsUnder(op.targetPath)
	}

	// 重命名目录时 st.dir 已被改到新路径，这里重新取一次当前目录。
	fileManagerMu.Lock()
	dir := st.dir
	fileManagerMu.Unlock()

	s.loadFileManagerDir(threadID, dir)
	if op.kind == fileManagerOpsNewFile {
		s.openFileManagerFile(op.threadID, newPath, true)
	}
}

// duplicateFileManagerEntry 复制一份文件或目录，不弹窗；失败走面板内的错误提示。
func (s *chatState) duplicateFileManagerEntry(path string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	client, ok := fileOpsClient(s)
	if !ok {
		return
	}

	fileManagerMu.Lock()
	threadID, dir := st.threadID, st.dir
	st.err = ""
	fileManagerMu.Unlock()

	go func() {
		if _, err := client.FilesDuplicate(context.Background(), threadID, path); err != nil {
			fileManagerMu.Lock()
			if st.threadID == threadID {
				st.err = err.Error()
			}
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		s.loadFileManagerDir(threadID, dir)
	}()
}

// renderFileManagerOpsDialog 渲染新建 / 重命名 / 移到废纸篓的弹窗卡片。
func renderFileManagerOpsDialog(c *ui.Context, s *chatState) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	ops := st.ops
	fileManagerMu.Unlock()
	if !ops.open {
		return
	}

	title, confirm, inputLabel, placeholder := "", "", "", ""
	destructive := false
	switch ops.kind {
	case fileManagerOpsNewFile:
		title, confirm, inputLabel, placeholder = "新建文件", "创建", "文件名", "如：new.txt"
	case fileManagerOpsNewDir:
		title, confirm, inputLabel, placeholder = "新建文件夹", "创建", "文件夹名", "如：docs"
	case fileManagerOpsRename:
		title, confirm, inputLabel, placeholder = "重命名", "重命名", "新名称", "输入新名称"
	case fileManagerOpsTrash:
		title, confirm, destructive = "移到废纸篓", "移到废纸篓", true
	default:
		return
	}

	open := true
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragFileManagerOp, fileManagerOpsCardWidth, fileManagerOpsCardHeight, 360, 160, winW, winH)
	Sheet(c, SheetProps{Key: modalDragFileManagerOp, PanelKey: "file-manager-ops-panel", CardKey: "file-manager-ops-card", Label: "file-manager-ops-card", Open: &open, Width: width, Height: height}, func() {
		ui.Column(c).
			FillWidth().Grow(1).
			Padding(Space6).
			Gap(Space4).
			Children(func() {
				ui.Text(c, title).
					FontSize(TypeTitle).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))

				if destructive {
					ui.Text(c, "确定把 "+ops.targetPath+" 移到废纸篓吗？").
						FontSize(TypeBody).
						TextColor(ThemePrimaryText(c)).
						Wrap()
				} else {
					TextField(c, TextFieldProps{Key: "file-manager-ops-input", Value: &st.ops.name, Label: inputLabel, Placeholder: placeholder, FillWidth: true, AutoFocus: true, OnKeyDown: func(k ui.TextKey) bool {
						if k.Key == ui.KeyEscape {
							s.closeFileManagerOpsDialog()
							return true
						}
						if k.Key == ui.KeyEnter {
							s.submitFileManagerOpsDialog()
							return true
						}
						return false
					}})
				}

				if ops.err != "" {
					ui.Row(c.Key("file-manager-ops-error")).
						FillWidth().
						Padding(Space2, Space3).
						Radius(RadiusSM).
						Background(ThemeDanger(c).Alpha(0.12)).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							ui.Icon(c, IconOctagonAlert()).
								Size(IconSM, IconSM).
								TextColor(ThemeDanger(c))
							ui.Text(c, ops.err).
								FontSize(TypeFootnote).
								TextColor(ThemeDanger(c)).
								Wrap()
						})
				}

				FormActions(c, "file-manager-ops-actions", func() {
					Button(c.Key("file-manager-ops-cancel"), ButtonOpts{
						Text:    "取消",
						Variant: ButtonSecondary,
						OnClick: func() {
							s.closeFileManagerOpsDialog()
						},
					})
					variant := ButtonPrimary
					label := confirm
					if destructive {
						variant = ButtonDanger
						// 弹窗标题也是「移到废纸篓」，给确认按钮单独一个无障碍标签便于区分。
						label = "确认" + confirm
					}
					Button(c.Key("file-manager-ops-confirm"), ButtonOpts{
						Text:     confirm,
						Label:    label,
						Variant:  variant,
						Disabled: ops.busy,
						OnClick: func() {
							s.submitFileManagerOpsDialog()
						},
					})
				})
				modalResizeGripsMin(c, modalDragFileManagerOp, width, height, 360, 160)
			})
	})
	if !open {
		s.closeFileManagerOpsDialog()
	}
}
