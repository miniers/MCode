package mcodeui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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

func preserveBackupUI(t *testing.T) {
	t.Helper()
	backupUI.mu.Lock()
	working, operation, cancel := backupUI.working, backupUI.operation, backupUI.cancel
	message, restoreMessage := backupUI.message, backupUI.restoreMessage
	confirm, relaunching := backupUI.confirm, backupUI.relaunching
	mode, password, repeat := backupUI.mode, backupUI.password, backupUI.repeat
	history, source := backupUI.history, backupUI.source
	backupUI.mu.Unlock()
	t.Cleanup(func() {
		backupUI.mu.Lock()
		backupUI.working, backupUI.operation, backupUI.cancel = working, operation, cancel
		backupUI.message, backupUI.restoreMessage = message, restoreMessage
		backupUI.confirm, backupUI.relaunching = confirm, relaunching
		backupUI.mode, backupUI.password, backupUI.repeat = mode, password, repeat
		backupUI.history, backupUI.source = history, source
		backupUI.mu.Unlock()
	})
}

func TestBackupSectionAndPasswords(t *testing.T) {
	for _, section := range pageGeneral.Sections {
		if section.ID != "general.backup" {
			continue
		}
		if section.Title != "备份与恢复" || len(section.Rows) != 2 || section.Rows[0].ID != "row-general-backup" || section.Rows[0].Title != "备份" || section.Rows[1].ID != "row-general-restore" || section.Rows[1].Title != "恢复" {
			t.Fatalf("备份区块不正确: %+v", section)
		}
		if backupPasswordValid("backup", "abc", "def") || backupPasswordValid("backup", "", "") || !backupPasswordValid("backup", "abc", "abc") {
			t.Fatal("密码校验不正确")
		}
		return
	}
	t.Fatal("缺少备份区块")
}

func TestBackupPasswordValidation(t *testing.T) {
	for _, tc := range []struct {
		mode, password, repeat string
		want                   bool
	}{
		{"backup", "", "", false},
		{"backup", "secret", "different", false},
		{"backup", "secret", "secret", true},
		{"restore", "", "secret", false},
		{"restore", "secret", "", true},
		{"restore", "secret", "different", true},
	} {
		if got := backupPasswordValid(tc.mode, tc.password, tc.repeat); got != tc.want {
			t.Errorf("backupPasswordValid(%q, %q, %q) = %v", tc.mode, tc.password, tc.repeat, got)
		}
	}
}

func TestBackupHistoryAndPasswordClearing(t *testing.T) {
	preserveBackupUI(t)
	oldSave, oldCreate := backupSaveDialog, backupCreate
	t.Cleanup(func() { backupSaveDialog, backupCreate = oldSave, oldCreate })
	backupSaveDialog = func(string) (string, error) { return "/tmp/backup.mcodebackup", nil }
	st := NewSettingsState(t.TempDir(), nil, nil)
	for _, history := range []bool{true, false} {
		var received backup.CreateOptions
		calls := 0
		backupCreate = func(_ context.Context, opts backup.CreateOptions) error {
			calls++
			received = opts
			return nil
		}
		password := []byte("secret")
		startBackup(st, password, history)
		waitBackupIdle(t)
		if calls != 1 || received.IncludeHistory != history {
			t.Errorf("history=%v: calls=%d, IncludeHistory=%v", history, calls, received.IncludeHistory)
		}
		for _, b := range password {
			if b != 0 {
				t.Fatal("密码未清零")
			}
		}
	}
}

func TestBackupCreateAndCancel(t *testing.T) {
	preserveBackupUI(t)
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
	preserveBackupUI(t)
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
	backupUI.mu.Lock()
	restoreMessage := backupUI.restoreMessage
	backupUI.mu.Unlock()
	if restoreMessage != "confirm failed" {
		t.Fatalf("恢复错误 = %q", restoreMessage)
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

func TestBackupStartupResult(t *testing.T) {
	oldTakeResult := backupTakeResult
	backupResultUI.mu.Lock()
	oldFailures, oldErrorText := backupResultUI.failures, backupResultUI.errorText
	backupResultUI.mu.Unlock()
	t.Cleanup(func() {
		backupTakeResult = oldTakeResult
		backupResultOnce = sync.Once{}
		backupResultUI.mu.Lock()
		backupResultUI.failures, backupResultUI.errorText = oldFailures, oldErrorText
		backupResultUI.mu.Unlock()
	})

	for _, tc := range []struct {
		name      string
		failures  []backup.SecretFailure
		errorText string
	}{
		{"failures", []backup.SecretFailure{{Service: "service-a", Account: "account-a", Reason: "failed"}, {Service: "service-b", Account: "account-b", Reason: "failed"}}, ""},
		{"error", nil, "x"},
		{"empty", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backupResultOnce = sync.Once{}
			backupResultUI.mu.Lock()
			backupResultUI.failures, backupResultUI.errorText = nil, ""
			backupResultUI.mu.Unlock()
			release := make(chan struct{})
			taken := make(chan struct{})
			backupTakeResult = func(string) (backup.Result, error) {
				<-release
				close(taken)
				return backup.Result{SecretFailures: tc.failures, Error: tc.errorText}, nil
			}
			s := newChatState(nil)
			tester := ui.NewTester(func(c *ui.Context) { renderBackupStartupResult(c, s) }, 400, 200)
			tester.HasText("unused")
			close(release)
			deadline := time.Now().Add(2 * time.Second)
			for {
				backupResultUI.mu.Lock()
				failures := append([]backup.SecretFailure(nil), backupResultUI.failures...)
				errorText := backupResultUI.errorText
				backupResultUI.mu.Unlock()
				select {
				case <-taken:
					if len(failures) == len(tc.failures) && errorText == tc.errorText {
						details := backupFailureDetails(failures)
						for i, failure := range tc.failures {
							if failures[i] != failure || !strings.Contains(details[i], failure.Service) || !strings.Contains(details[i], failure.Account) || strings.Contains(strings.ToLower(details[i]), "value") {
								t.Fatalf("密钥明细错误: %v", details)
							}
						}
						return
					}
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("恢复结果未就绪")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
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
	preserveBackupUI(t)
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
