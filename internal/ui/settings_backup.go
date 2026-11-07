package mcodeui

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/backup"
)

var (
	backupCreate     = backup.Create
	backupStage      = backup.Stage
	backupConfirm    = backup.ConfirmPending
	backupDiscard    = backup.DiscardPending
	backupTakeResult = backup.TakeResult
	backupSaveDialog = func(path string) (string, error) {
		return mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "备份", DefaultPath: path, ButtonLabel: "备份", Filters: []mygo.FileFilter{{Name: "mcode 备份", Extensions: []string{"mcodebackup"}}}, CreateDirectories: true})
	}
	backupOpenDialog = func() ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "恢复", Filters: []mygo.FileFilter{{Name: "mcode 备份", Extensions: []string{"mcodebackup"}}}})
	}
	appRelaunch = func() { mygo.App.Relaunch() }
)

var backupResultOnce sync.Once
var backupResultUI struct {
	mu       sync.Mutex
	failures []backup.SecretFailure
}

func renderBackupStartupResult(c *ui.Context, s *chatState) {
	backupResultOnce.Do(func() {
		dir := resolveDataDirFallback()
		if st := getChatSettings(s); st != nil && archiveSettingsDataDir(st) != "" {
			dir = archiveSettingsDataDir(st)
		}
		go func() {
			result, err := backupTakeResult(dir)
			if err != nil || len(result.SecretFailures) == 0 {
				return
			}
			backupResultUI.mu.Lock()
			backupResultUI.failures = result.SecretFailures
			backupResultUI.mu.Unlock()
			s.invalidate()
		}()
	})
	backupResultUI.mu.Lock()
	failures := append([]backup.SecretFailure(nil), backupResultUI.failures...)
	backupResultUI.mu.Unlock()
	if len(failures) == 0 {
		return
	}
	detail := backupFailureDetails(failures)
	RenderConfirmDialog(c, ConfirmDialog{Key: "backup-result", Open: true, Tone: ConfirmNotice, Title: "部分密钥未恢复", Detail: detail, ConfirmLabel: "好", OnConfirm: func() {
		backupResultUI.mu.Lock()
		backupResultUI.failures = nil
		backupResultUI.mu.Unlock()
		s.invalidate()
	}})
}

func backupFailureDetails(failures []backup.SecretFailure) []string {
	detail := make([]string, 0, len(failures))
	for _, failure := range failures {
		detail = append(detail, failure.Service+" · "+failure.Account+" · "+failure.Reason)
	}
	return detail
}

var backupUI struct {
	mu             sync.Mutex
	working        bool
	operation      string
	cancel         context.CancelFunc
	message        string
	restoreMessage string
	confirm        bool
	relaunching    bool
	mode           string
	password       string
	repeat         string
	history        bool
	source         string
}

func backupError(err error) string {
	switch {
	case errors.Is(err, backup.ErrWrongPassword):
		return "密码错误或备份文件已损坏"
	case errors.Is(err, backup.ErrInvalidArchive):
		return "备份文件已损坏"
	case errors.Is(err, backup.ErrUnsupportedVersion):
		return "备份来自更新版本的 mcode"
	default:
		return err.Error()
	}
}

func clearBackupPassword(password []byte) { clear(password) }

func backupPasswordValid(mode, password, repeat string) bool {
	return password != "" && (mode != "backup" || password == repeat)
}

func startBackup(st *SettingsState, password []byte, history bool) {
	backupUI.mu.Lock()
	if backupUI.working {
		backupUI.mu.Unlock()
		clearBackupPassword(password)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	backupUI.working, backupUI.operation, backupUI.cancel, backupUI.message = true, "backup", cancel, ""
	backupUI.mu.Unlock()
	st.invalidate()
	go func() {
		defer cancel()
		path, err := backupSaveDialog(filepath.Join(downloadsDir(), "mcode-"+time.Now().Format("20060102")+".mcodebackup"))
		if err == nil && path != "" {
			if filepath.Ext(path) == "" {
				path += ".mcodebackup"
			}
			err = backupCreate(ctx, backup.CreateOptions{DataDir: archiveSettingsDataDir(st), Destination: path, Password: password, IncludeHistory: history})
		}
		clearBackupPassword(password)
		backupUI.mu.Lock()
		backupUI.working, backupUI.operation, backupUI.cancel = false, "", nil
		if err != nil {
			backupUI.message = err.Error()
		} else if path != "" {
			backupUI.message = "已备份"
		}
		backupUI.mu.Unlock()
		st.invalidate()
	}()
}

func chooseBackupRestore(st *SettingsState) {
	backupUI.mu.Lock()
	if backupUI.working {
		backupUI.mu.Unlock()
		return
	}
	backupUI.working, backupUI.operation, backupUI.restoreMessage = true, "open", ""
	backupUI.mu.Unlock()
	st.invalidate()
	go func() {
		paths, err := backupOpenDialog()
		backupUI.mu.Lock()
		backupUI.working, backupUI.operation = false, ""
		if err != nil {
			backupUI.restoreMessage = err.Error()
		} else if len(paths) > 0 && paths[0] != "" {
			backupUI.source, backupUI.mode = paths[0], "restore"
		}
		backupUI.mu.Unlock()
		st.invalidate()
	}()
}

func startBackupRestore(st *SettingsState, source string, password []byte) {
	backupUI.mu.Lock()
	if backupUI.working {
		backupUI.mu.Unlock()
		clearBackupPassword(password)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	backupUI.working, backupUI.operation, backupUI.cancel, backupUI.restoreMessage = true, "restore", cancel, ""
	backupUI.mu.Unlock()
	st.invalidate()
	go func() {
		defer cancel()
		_, err := backupStage(ctx, backup.StageOptions{DataDir: archiveSettingsDataDir(st), Source: source, Password: password})
		clearBackupPassword(password)
		backupUI.mu.Lock()
		backupUI.working, backupUI.operation, backupUI.cancel = false, "", nil
		if err != nil {
			backupUI.restoreMessage = backupError(err)
		} else {
			backupUI.confirm = true
		}
		backupUI.mu.Unlock()
		st.invalidate()
	}()
}

func finishBackupRestore(st *SettingsState, confirm bool) {
	backupUI.mu.Lock()
	if !backupUI.confirm || backupUI.relaunching {
		backupUI.mu.Unlock()
		return
	}
	backupUI.confirm = false
	backupUI.relaunching = confirm
	backupUI.mu.Unlock()
	var err error
	if confirm {
		err = backupConfirm(archiveSettingsDataDir(st))
	} else {
		err = backupDiscard(archiveSettingsDataDir(st))
	}
	if err != nil {
		backupUI.mu.Lock()
		backupUI.restoreMessage, backupUI.relaunching = err.Error(), false
		backupUI.mu.Unlock()
	} else if confirm {
		appRelaunch()
	}
	st.invalidate()
}

func renderBackupControl(c *ui.Context, st *SettingsState) {
	backupUI.mu.Lock()
	working, operation, message := backupUI.working, backupUI.operation, backupUI.message
	backupUI.mu.Unlock()
	ui.Row(c.Key("backup-controls")).AlignItems(ui.Center).Gap(Space3).Children(func() {
		if message != "" {
			ui.Text(c, message).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
		}
		label := "备份…"
		if operation == "backup" {
			label = "备份中…"
		}
		Button(c.Key("backup-open"), ButtonOpts{Text: label, Variant: ButtonSecondary, Disabled: working, OnClick: func() {
			backupUI.mu.Lock()
			backupUI.mode, backupUI.password, backupUI.repeat = "backup", "", ""
			backupUI.mu.Unlock()
			st.invalidate()
		}})
	})
	renderBackupPasswordDialog(c, st)
}

func renderRestoreControl(c *ui.Context, st *SettingsState) {
	backupUI.mu.Lock()
	working, operation, message, confirm := backupUI.working, backupUI.operation, backupUI.restoreMessage, backupUI.confirm
	backupUI.mu.Unlock()
	ui.Row(c.Key("restore-controls")).AlignItems(ui.Center).Gap(Space3).Children(func() {
		if message != "" {
			ui.Text(c, message).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
		}
		label := "恢复…"
		if operation == "restore" {
			label = "验证中…"
		}
		Button(c.Key("restore-open"), ButtonOpts{Text: label, Variant: ButtonSecondary, Disabled: working, OnClick: func() { chooseBackupRestore(st) }})
	})
	RenderConfirmDialog(c, ConfirmDialog{Key: "backup-restore-confirm", Open: confirm, Tone: ConfirmDanger, Title: "恢复并重启？", ConfirmLabel: "恢复并重启", OnConfirm: func() { finishBackupRestore(st, true) }, OnCancel: func() { finishBackupRestore(st, false) }})
}

func renderBackupPasswordDialog(c *ui.Context, st *SettingsState) {
	backupUI.mu.Lock()
	mode := backupUI.mode
	backupUI.mu.Unlock()
	if mode == "" {
		return
	}
	open := true
	modalDragModal(c, "backup-password", &open, func() {
		ui.Column(c.Key("backup-password-card")).Width(400).Background(ThemeSurface(c)).BorderColor(ThemeBorder(c)).BorderWidth(1).Radius(RadiusLG).Padding(Space5).Gap(Space4).Children(func() {
			title := "备份"
			if mode == "restore" {
				title = "恢复"
			}
			modalDragHandle(c, "backup-password", ui.Row(c.Key("backup-password-title")).FillWidth().Children(func() { ui.Text(c, title).FontSize(TypeTitle).FontWeight(600).TextColor(ThemePrimaryText(c)) }))
			TextField(c, TextFieldProps{Key: "backup-password-input", Value: &backupUI.password, Label: "密码", Placeholder: "密码", Password: true, FillWidth: true, AutoFocus: true})
			if mode == "backup" {
				TextField(c, TextFieldProps{Key: "backup-password-repeat", Value: &backupUI.repeat, Label: "确认密码", Placeholder: "确认密码", Password: true, FillWidth: true})
				ui.Row(c.Key("backup-history-row")).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, "包含会话历史").Grow(1).TextColor(ThemePrimaryText(c))
					Switch(c, "backup-history", &backupUI.history, "包含会话历史", nil)
				})
			}
			valid := backupPasswordValid(mode, backupUI.password, backupUI.repeat)
			ui.Row(c.Key("backup-password-actions")).FillWidth().Justify(ui.End).Gap(Space3).Children(func() {
				Button(c.Key("backup-password-cancel"), ButtonOpts{Text: "取消", Variant: ButtonSecondary, OnClick: func() { open = false }})
				Button(c.Key("backup-password-confirm"), ButtonOpts{Text: "确认", Variant: ButtonPrimary, Disabled: !valid, OnClick: func() {
					password := []byte(backupUI.password)
					source, history := backupUI.source, backupUI.history
					backupUI.mode, backupUI.password, backupUI.repeat = "", "", ""
					if mode == "backup" {
						startBackup(st, password, history)
					} else {
						startBackupRestore(st, source, password)
					}
				}})
			})
		})
	})
	if !open {
		backupUI.mu.Lock()
		backupUI.mode, backupUI.password, backupUI.repeat = "", "", ""
		backupUI.mu.Unlock()
		st.invalidate()
	}
}
