package mcodeui

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/backup"
)

func waitBackupIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		backupUI.mu.Lock()
		working := backupUI.working
		backupUI.mu.Unlock()
		if !working {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("备份任务未结束")
}

func TestBackupSectionAndPasswords(t *testing.T) {
	for _, section := range pageGeneral.Sections {
		if section.ID != "general.backup" {
			continue
		}
		if section.Title != "备份与恢复" || len(section.Rows) != 2 || section.Rows[0].ID != "row-general-backup" || section.Rows[1].ID != "row-general-restore" {
			t.Fatalf("备份区块不正确: %+v", section)
		}
		if backupPasswordValid("backup", "abc", "def") || backupPasswordValid("backup", "", "") || !backupPasswordValid("backup", "abc", "abc") {
			t.Fatal("密码校验不正确")
		}
		return
	}
	t.Fatal("缺少备份区块")
}

func TestBackupCreateAndCancel(t *testing.T) {
	oldSave, oldCreate := backupSaveDialog, backupCreate
	t.Cleanup(func() { backupSaveDialog, backupCreate = oldSave, oldCreate })
	st := NewSettingsState(t.TempDir(), nil, nil)
	calls := 0
	var received backup.CreateOptions
	backupCreate = func(_ context.Context, opts backup.CreateOptions) error { calls++; received = opts; return nil }
	backupSaveDialog = func(string) (string, error) { return "", nil }
	password := []byte("secret")
	startBackup(st, password, true)
	waitBackupIdle(t)
	if calls != 0 {
		t.Fatal("取消后调用了备份")
	}
	for _, b := range password {
		if b != 0 {
			t.Fatal("密码未清零")
		}
	}
	backupSaveDialog = func(string) (string, error) { return "/tmp/backup.mcodebackup", nil }
	password = []byte("secret")
	startBackup(st, password, true)
	waitBackupIdle(t)
	if calls != 1 || !received.IncludeHistory {
		t.Fatal("历史选项未传入")
	}
	for _, b := range password {
		if b != 0 {
			t.Fatal("密码未清零")
		}
	}
}

func TestRestoreErrorsAndConfirmation(t *testing.T) {
	for err, want := range map[error]string{
		backup.ErrWrongPassword:      "密码错误或备份文件已损坏",
		backup.ErrInvalidArchive:     "备份文件已损坏",
		backup.ErrUnsupportedVersion: "备份来自更新版本的 mcode",
	} {
		if got := backupError(err); got != want {
			t.Fatalf("错误文案 = %q", got)
		}
	}
	oldConfirm, oldDiscard, oldRelaunch := backupConfirm, backupDiscard, appRelaunch
	t.Cleanup(func() {
		backupConfirm, backupDiscard, appRelaunch = oldConfirm, oldDiscard, oldRelaunch
		backupUI.mu.Lock()
		backupUI.confirm, backupUI.relaunching = false, false
		backupUI.mu.Unlock()
	})
	st := NewSettingsState(t.TempDir(), nil, nil)
	discarded, relaunched := 0, 0
	backupDiscard = func(string) error { discarded++; return nil }
	appRelaunch = func() { relaunched++ }
	backupConfirm = func(string) error { return nil }
	backupUI.mu.Lock()
	backupUI.confirm, backupUI.relaunching = true, false
	backupUI.mu.Unlock()
	finishBackupRestore(st, false)
	if discarded != 1 || relaunched != 0 {
		t.Fatal("取消恢复行为错误")
	}
	backupConfirm = func(string) error { return errors.New("confirm failed") }
	backupUI.mu.Lock()
	backupUI.confirm = true
	backupUI.mu.Unlock()
	finishBackupRestore(st, true)
	if relaunched != 0 {
		t.Fatal("确认失败仍重启")
	}
	backupConfirm = func(string) error { return nil }
	backupUI.mu.Lock()
	backupUI.confirm = true
	backupUI.mu.Unlock()
	finishBackupRestore(st, true)
	finishBackupRestore(st, true)
	if relaunched != 1 {
		t.Fatalf("重启次数 = %d", relaunched)
	}
}

func TestBackupResultDisplaysAccounts(t *testing.T) {
	detail := backupFailureDetails([]backup.SecretFailure{{Service: "service-x", Account: "account-y", Reason: "failed"}})
	if len(detail) != 1 || detail[0] != "service-x · account-y · failed" {
		t.Fatalf("密钥账户明细错误: %v", detail)
	}
}

func TestProjectPathStatusKeepsProject(t *testing.T) {
	old := projectPathMissing
	t.Cleanup(func() { projectPathMissing = old })
	missing := true
	projectPathMissing = func(string) bool { return missing }
	s := newChatState(nil)
	s.projects = []Project{{ID: "p", Path: "/example"}}
	wait := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			value, ok := s.projectPathsMissing["p"]
			count := len(s.projects)
			s.mu.Unlock()
			if ok && value == want && count == 1 {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("项目路径状态未更新")
	}
	refreshProjectPathStatus(s)
	wait(true)
	s.windowActive.Store(false)
	refreshProjectPathStatus(s)
	missing = false
	s.windowActive.Store(true)
	refreshProjectPathStatus(s)
	wait(false)
}

func TestRestoreOpenCanceled(t *testing.T) {
	oldOpen, oldStage := backupOpenDialog, backupStage
	t.Cleanup(func() { backupOpenDialog, backupStage = oldOpen, oldStage })
	calls := 0
	backupOpenDialog = func() ([]string, error) { return nil, nil }
	backupStage = func(context.Context, backup.StageOptions) (backup.Result, error) {
		calls++
		return backup.Result{}, nil
	}
	st := NewSettingsState(t.TempDir(), nil, nil)
	chooseBackupRestore(st)
	waitBackupIdle(t)
	if calls != 0 {
		t.Fatal("取消后验证了备份")
	}
}
