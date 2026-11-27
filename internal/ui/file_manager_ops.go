package mcodeui

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 文件面板写操作弹窗的动作类型。
const (
	fileManagerOpsNewFile = "new-file"
	fileManagerOpsNewDir  = "new-dir"
	fileManagerOpsTrash   = "trash"
)

// fileManagerOpsCardWidth 是写操作弹窗卡片宽度。
const fileManagerOpsCardWidth float32 = 360
const fileManagerOpsCardHeight float32 = 240
const modalDragFileManagerOp = "modal-drag-file-manager-op"

// fileManagerOpsTargets 保存不可变的多路径目标列表指针，保持 fileManagerOpsDialog 可比较。
type fileManagerOpsTargets struct {
	paths []string
}

// fileManagerOpsDialog 是文件面板唯一的写操作弹窗状态：同一时刻只开一个，
// kind 决定标题、输入框与提交动作。
type fileManagerOpsDialog struct {
	open bool
	kind string
	// targetPath 在新建时是父目录（空串表示工作区根目录），删除时是首个目标路径。
	targetPath string
	targets    *fileManagerOpsTargets
	// threadID 是打开弹窗时的会话：请求回来时会话已经换掉就不再回填结果。
	threadID string
	name     string
	err      string
	busy     bool
	// gen 是提交代数：再次提交或关闭弹窗后，旧请求的结果直接丢弃。
	gen int
}

func (d fileManagerOpsDialog) targetList() []string {
	if d.targets != nil && len(d.targets.paths) > 0 {
		return d.targets.paths
	}
	if d.targetPath != "" {
		return []string{d.targetPath}
	}
	return nil
}

// fileManagerInlineRename 维护文件树/搜索列表行内重命名的状态。
type fileManagerInlineRename struct {
	active     bool
	path       string
	threadID   string
	draft      string
	err        string
	busy       bool
	hadFocus   bool
	needSelect bool
	gen        int
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
	s.openFileManagerOpsDialogTargets(kind, targetPath, nil, name)
}

func (s *chatState) openFileManagerOpsDialogTargets(kind, targetPath string, targetPaths []string, name string) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	var targets *fileManagerOpsTargets
	if len(targetPaths) > 0 {
		targets = &fileManagerOpsTargets{paths: append([]string(nil), targetPaths...)}
	}
	fileManagerMu.Lock()
	// 代数只增不减：关掉再开时旧请求不会撞上新的弹窗。
	st.opsSeq++
	st.ops = fileManagerOpsDialog{
		open:       true,
		kind:       kind,
		targetPath: targetPath,
		targets:    targets,
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

// fileManagerCollapseTrashPaths 去重并折叠嵌套路径：同时包含目录及其内部条目时只保留目录。
func fileManagerCollapseTrashPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p != "" {
			set[p] = true
		}
	}
	collapsed := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		covered := false
		for anc := fileManagerParentPath(p); anc != ""; anc = fileManagerParentPath(anc) {
			if set[anc] {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		seen[p] = true
		collapsed = append(collapsed, p)
	}
	return collapsed
}

// openFileManagerTrashDialog 打开单个条目的「移到废纸篓」确认弹窗。
func openFileManagerTrashDialog(s *chatState, path string) {
	openFileManagerBatchTrashDialog(s, []string{path})
}

// openFileManagerBatchTrashDialog 打开单个或多个条目的「移到废纸篓」确认弹窗。
func openFileManagerBatchTrashDialog(s *chatState, paths []string) {
	targets := fileManagerCollapseTrashPaths(paths)
	if len(targets) == 0 {
		return
	}
	s.openFileManagerOpsDialogTargets(fileManagerOpsTrash, targets[0], targets, "")
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
	// 新建要先校验名字；删除不读输入框。
	if op.kind == fileManagerOpsNewFile || op.kind == fileManagerOpsNewDir {
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
	}

	go func(op fileManagerOpsDialog, newPath string) {
		ctx := context.Background()
		switch op.kind {
		case fileManagerOpsNewFile:
			_, err := client.FilesCreate(ctx, op.threadID, newPath, false)
			s.finishFileManagerOps(op, newPath, err)
		case fileManagerOpsNewDir:
			_, err := client.FilesCreate(ctx, op.threadID, newPath, true)
			s.finishFileManagerOps(op, newPath, err)
		case fileManagerOpsTrash:
			targets := op.targetList()
			var succeeded []string
			var failedPath string
			var err error
			for _, p := range targets {
				if e := client.FilesTrash(ctx, op.threadID, p); e != nil {
					failedPath = p
					err = e
					break
				}
				succeeded = append(succeeded, p)
			}
			s.finishFileManagerTrashOps(op, targets, succeeded, failedPath, err)
		}
	}(op, newPath)
}

func fileManagerUniqueDirs(paths ...string) []string {
	seen := make(map[string]bool, len(paths))
	dirs := make([]string, 0, len(paths))
	for _, dir := range paths {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// finishFileManagerOps 处理新建操作的结果：成功则关弹窗、刷新列表并按需打开新文件；
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

	dirs := fileManagerUniqueDirs(op.targetPath)
	fileTreeInvalidate(s, threadID, dirs)
	if op.kind == fileManagerOpsNewFile {
		s.openFileManagerFile(op.threadID, newPath, true)
	}
}

// finishFileManagerTrashOps 处理单个或批量移到废纸篓的结果：
// 已成功的条目立即更新树与标签，任意一项失败就停在弹窗并展示失败项与已完成数量。
func (s *chatState) finishFileManagerTrashOps(op fileManagerOpsDialog, targets, succeeded []string, failedPath string, err error) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}

	fileManagerMu.Lock()
	if !st.ops.open || st.ops.gen != op.gen {
		fileManagerMu.Unlock()
		return
	}
	if st.threadID != op.threadID {
		st.ops = fileManagerOpsDialog{}
		fileManagerMu.Unlock()
		modalDragReset(modalDragFileManagerOp)
		s.invalidate()
		return
	}
	threadID := st.threadID
	if err != nil {
		st.ops.busy = false
		if len(targets) > 1 {
			remaining := append([]string(nil), targets[len(succeeded):]...)
			st.ops.targets = &fileManagerOpsTargets{paths: remaining}
			if len(remaining) > 0 {
				st.ops.targetPath = remaining[0]
			}
			st.ops.err = fmt.Sprintf("已删除 %d 项，%s 失败：%s", len(succeeded), failedPath, err.Error())
		} else {
			st.ops.err = err.Error()
		}
		fileManagerMu.Unlock()
		s.applyFileManagerTrashedPaths(threadID, succeeded)
		s.invalidate()
		return
	}
	st.ops = fileManagerOpsDialog{}
	fileManagerMu.Unlock()
	modalDragReset(modalDragFileManagerOp)

	s.applyFileManagerTrashedPaths(threadID, succeeded)
}

func (s *chatState) applyFileManagerTrashedPaths(threadID string, paths []string) {
	if len(paths) == 0 {
		return
	}
	parents := make([]string, 0, len(paths))
	for _, p := range paths {
		s.fileTreeTrashPath(p)
		s.closeFileManagerTabsUnder(p)
		parents = append(parents, fileManagerParentPath(p))
	}
	fileTreeInvalidate(s, threadID, fileManagerUniqueDirs(parents...))
	if fileTreeOnlyChangedActive(getFileManagerState(s)) {
		fileTreeRefreshGit(s, threadID)
	}
}

// performFileManagerRename 调用后端接口执行重命名并返回实际新路径。
func performFileManagerRename(ctx context.Context, client FileOpsClient, threadID, oldPath, newPath string) (string, error) {
	renamed, err := client.FilesRename(ctx, threadID, oldPath, newPath)
	if err != nil {
		return "", err
	}
	if renamed == "" {
		renamed = newPath
	}
	return renamed, nil
}

// applyFileManagerRename 在重命名成功后同步标签、树路径并刷新受影响目录。
func (s *chatState) applyFileManagerRename(threadID, oldPath, newPath string) {
	s.renameFileManagerTabPaths(oldPath, newPath)
	dirs := fileManagerUniqueDirs(fileManagerParentPath(oldPath), fileManagerParentPath(newPath))
	fileTreeInvalidate(s, threadID, dirs)
	if fileTreeOnlyChangedActive(getFileManagerState(s)) {
		fileTreeRefreshGit(s, threadID)
	}
}

// fileManagerRenameSelectEnd 返回行内重命名时默认选中的 rune 结尾下标（不含文件扩展名）。
func fileManagerRenameSelectEnd(name string, isDir bool) int {
	if !isDir {
		if dot := strings.LastIndex(name, "."); dot > 0 {
			return len([]rune(name[:dot]))
		}
	}
	return len([]rune(name))
}

// fileManagerRenameValidate 校验重命名新名称与同目录已加载 children 的重名冲突。
func (s *chatState) fileManagerRenameValidate(oldPath, name string) string {
	if msg := fileManagerValidateName(name); msg != "" {
		return msg
	}
	st := getFileManagerState(s)
	if st == nil {
		return ""
	}
	parentDir := fileManagerParentPath(oldPath)
	newPath := fileManagerJoinPath(parentDir, name)
	if newPath == oldPath {
		return ""
	}
	fileManagerMu.Lock()
	defer fileManagerMu.Unlock()
	if st.tree == nil {
		return ""
	}
	for _, sibling := range st.tree.children[parentDir] {
		if sibling == newPath && sibling != oldPath {
			return "当前目录已存在同名文件或文件夹"
		}
	}
	if st.tree.onlyChanged {
		for _, sibling := range st.tree.changedChildren[parentDir] {
			if sibling == newPath && sibling != oldPath {
				return "当前目录已存在同名文件或文件夹"
			}
		}
	}
	return ""
}

// startFileManagerInlineRename 进入指定条目的行内重命名。
func (s *chatState) startFileManagerInlineRename(path string) {
	st := getFileManagerState(s)
	if st == nil || path == "" {
		return
	}
	if _, ok := fileOpsClient(s); !ok {
		return
	}
	fileManagerMu.Lock()
	if st.threadID == "" || (st.rename.active && st.rename.busy) {
		fileManagerMu.Unlock()
		return
	}
	if st.tree != nil {
		if fileTreeEntryProtectedLocked(st.tree, st.tree.entries, path) {
			fileManagerMu.Unlock()
			return
		}
	}
	st.renameSeq++
	st.rename = fileManagerInlineRename{
		active:     true,
		path:       path,
		threadID:   st.threadID,
		draft:      fileManagerTabName(path),
		needSelect: true,
		gen:        st.renameSeq,
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// cancelFileManagerInlineRename 退出行内重命名；restoreFocus 为 true 时把焦点还给文件树。
func (s *chatState) cancelFileManagerInlineRename(restoreFocus bool) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	fileManagerMu.Lock()
	if !st.rename.active {
		fileManagerMu.Unlock()
		return
	}
	st.rename = fileManagerInlineRename{}
	if restoreFocus && st.tree != nil {
		st.tree.focusPending = true
	}
	fileManagerMu.Unlock()
	s.invalidate()
}

// submitFileManagerInlineRename 校验并提交行内重命名。名称未变时直接退出。
func (s *chatState) submitFileManagerInlineRename(restoreFocus bool) {
	st := getFileManagerState(s)
	if st == nil {
		return
	}
	client, ok := fileOpsClient(s)
	if !ok {
		return
	}

	fileManagerMu.Lock()
	if !st.rename.active || st.rename.busy {
		fileManagerMu.Unlock()
		return
	}
	rn := st.rename
	name := strings.TrimSpace(rn.draft)
	origName := fileManagerTabName(rn.path)
	if name == origName {
		st.rename = fileManagerInlineRename{}
		if restoreFocus && st.tree != nil {
			st.tree.focusPending = true
		}
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}
	fileManagerMu.Unlock()

	if msg := s.fileManagerRenameValidate(rn.path, name); msg != "" {
		fileManagerMu.Lock()
		if st.rename.active && st.rename.gen == rn.gen {
			st.rename.draft = name
			st.rename.err = msg
		}
		fileManagerMu.Unlock()
		s.invalidate()
		return
	}

	newPath := fileManagerJoinPath(fileManagerParentPath(rn.path), name)
	fileManagerMu.Lock()
	if !st.rename.active || st.rename.gen != rn.gen {
		fileManagerMu.Unlock()
		return
	}
	st.rename.draft = name
	st.rename.busy = true
	st.rename.err = ""
	rn = st.rename
	fileManagerMu.Unlock()
	s.invalidate()

	go func(rn fileManagerInlineRename, newPath string, restoreFocus bool) {
		renamed, err := performFileManagerRename(context.Background(), client, rn.threadID, rn.path, newPath)
		fileManagerMu.Lock()
		if !st.rename.active || st.rename.gen != rn.gen {
			fileManagerMu.Unlock()
			return
		}
		if st.threadID != rn.threadID {
			st.rename = fileManagerInlineRename{}
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		if err != nil {
			st.rename.busy = false
			st.rename.err = err.Error()
			fileManagerMu.Unlock()
			s.invalidate()
			return
		}
		st.rename = fileManagerInlineRename{}
		if restoreFocus && st.tree != nil {
			st.tree.focusPending = true
		}
		threadID := st.threadID
		fileManagerMu.Unlock()

		s.applyFileManagerRename(threadID, rn.path, renamed)
		s.invalidate()
	}(rn, newPath, restoreFocus)
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
	threadID := st.threadID
	st.err = ""
	fileManagerMu.Unlock()

	go func() {
		if _, err := client.FilesDuplicate(context.Background(), threadID, path); err != nil {
			s.setToast("复制失败: " + err.Error())
			return
		}
		fileTreeInvalidate(s, threadID, []string{fileManagerParentPath(path)})
	}()
}

func trashActionLabel() string {
	if openInGOOS == "windows" {
		return "移到回收站"
	}
	return "移到废纸篓"
}

func renderFileManagerOpsError(c *ui.Context, msg string) {
	if msg == "" {
		return
	}
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
			ui.Text(c, msg).
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemeDanger(c)).
				Wrap()
		})
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
		RenderConfirmDialog(c, ConfirmDialog{Key: modalDragFileManagerOp, Open: false})
		return
	}

	if ops.kind == fileManagerOpsTrash {
		action := trashActionLabel()
		count := len(ops.targetList())
		title := action + "？"
		body := "确定把 " + ops.targetPath + " " + action + "吗？"
		if count > 1 {
			title = fmt.Sprintf("把 %d 项%s？", count, action)
			body = fmt.Sprintf("确定把选中的 %d 项%s吗？", count, action)
		}
		var footer func()
		if ops.err != "" {
			footer = func() { renderFileManagerOpsError(c, ops.err) }
		}
		RenderConfirmDialog(c, ConfirmDialog{
			Key:             modalDragFileManagerOp,
			Open:            true,
			Tone:            ConfirmDanger,
			Title:           title,
			Body:            body,
			ConfirmLabel:    action,
			ConfirmA11y:     "确认" + action,
			CancelLabel:     "取消",
			ConfirmDisabled: ops.busy,
			Footer:          footer,
			OnConfirm:       func() { s.submitFileManagerOpsDialog() },
			OnCancel:        func() { s.closeFileManagerOpsDialog() },
		})
		return
	}

	title, confirm, inputLabel, placeholder := "", "", "", ""
	switch ops.kind {
	case fileManagerOpsNewFile:
		title, confirm, inputLabel, placeholder = "新建文件", "创建", "文件名", "如：new.txt"
	case fileManagerOpsNewDir:
		title, confirm, inputLabel, placeholder = "新建文件夹", "创建", "文件夹名", "如：docs"
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
					FontSize(uiFont(TypeTitle)).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))

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

				renderFileManagerOpsError(c, ops.err)

				FormActions(c, "file-manager-ops-actions", func() {
					Button(c.Key("file-manager-ops-cancel"), ButtonOpts{
						Text:    "取消",
						Variant: ButtonSecondary,
						OnClick: func() {
							s.closeFileManagerOpsDialog()
						},
					})
					Button(c.Key("file-manager-ops-confirm"), ButtonOpts{
						Text:     confirm,
						Label:    confirm,
						Variant:  ButtonPrimary,
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
