package mcodeui

import (
	"errors"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// TestUIReview17_AddSwarmPresetRefreshRace 验证 addSwarmPreset 在持有 st.mu 的同时
// 也用 st.cfgMu 保护 st.cfg 的写入，并发调用 refreshFromDisk 不会触发数据竞争。
func TestUIReview17_AddSwarmPresetRefreshRace(t *testing.T) {
	dataDir := settingsTestDir(t)
	if err := config.Save(dataDir, config.Defaults()); err != nil {
		t.Fatal(err)
	}
	st := NewSettingsState(dataDir, nil, newChatState(NewFakeClient()))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				st.refreshFromDisk()
			}
		}
	}()

	const iterations = 25
	for range iterations {
		addSwarmPreset(st, swarmPresets[0])
	}
	close(stop)
	wg.Wait()
	settingsSaveWriters.Wait()

	st.mu.Lock()
	st.cfgMu.Lock()
	pairCount := len(st.cfg.Orchestration.Pairs)
	st.cfgMu.Unlock()
	st.mu.Unlock()
	if pairCount == 0 {
		t.Fatal("并发执行 addSwarmPreset 后应至少保留一个预设配对")
	}
}

// TestUIReview17_SwarmPairAndCommitRefreshRace 验证 swarmPairSave、swarmPairDelete
// 与 commitSettingsControl 并发 refreshFromDisk 时均正确加锁，不触发数据竞争。
func TestUIReview17_SwarmPairAndCommitRefreshRace(t *testing.T) {
	dataDir := settingsTestDir(t)
	if err := config.Save(dataDir, config.Defaults()); err != nil {
		t.Fatal(err)
	}
	st := NewSettingsState(dataDir, nil, newChatState(NewFakeClient()))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				st.refreshFromDisk()
			}
		}
	}()

	for i := range 15 {
		draft := config.SwarmPair{
			Name:           "并发配对",
			LeadProvider:   config.ProviderClaude,
			LeadModel:      "Opus",
			WorkerProvider: config.ProviderCodex,
			WorkerModel:    "Terra",
		}
		saved := swarmPairSave(st, draft, true)
		saved.Name = "并发配对-更新"
		swarmPairSave(st, saved, false)
		commitSettingsControl(st, func(cfg *config.Config) {
			if i%2 == 0 {
				cfg.Orchestration.Steward.QuietStart = "00:00"
				cfg.Orchestration.Steward.QuietEnd = "06:00"
			} else {
				cfg.Orchestration.Steward.QuietStart = "01:00"
				cfg.Orchestration.Steward.QuietEnd = "07:00"
			}
		})
		swarmPairDelete(st, saved.ID)
	}
	close(stop)
	wg.Wait()
	settingsSaveWriters.Wait()
}

// TestUIReview17_MagpieGatewayFormDraftLockRace 验证 renderMagpieGatewayForm
// 在锁内读取 st.endpoints 草稿后再绑定控件，与后台修改 st.endpoints 并发渲染无数据竞争。
func TestUIReview17_MagpieGatewayFormDraftLockRace(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, newChatState(NewFakeClient()))
	st.mu.Lock()
	st.endpoints.editName = "Magpie"
	st.endpoints.editBaseURL = magpieDefaultBaseURL
	st.endpoints.editModel = "auto"
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderMagpieFormSnapshot(c, st, "", "hub")
	}, 900, 1200)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 100 {
			st.mu.Lock()
			if i%2 == 0 {
				st.endpoints.editName = "Magpie A"
				st.endpoints.editBaseURL = "http://127.0.0.1:3425/v1"
				st.endpoints.editModel = "claude-opus-4"
				st.endpoints.editPersonaWebURL = "https://magpie.example.com:3430"
				st.endpoints.editPersonaRules = true
			} else {
				st.endpoints.editName = "Magpie B"
				st.endpoints.editBaseURL = "http://127.0.0.1:3426/v1"
				st.endpoints.editModel = "gpt-5"
				st.endpoints.editPersonaWebURL = ""
				st.endpoints.editPersonaRules = false
			}
			st.mu.Unlock()
		}
	}()

	for {
		tt.Frame()
		select {
		case <-done:
			tt.Frame()
			return
		default:
		}
	}
}

// TestUIReview17_RemotePushToggleRollback 验证 remotePushToggleControl 在保存失败时
// 正确加锁回滚 st.cfg.Remote.PushOn 并记录 pushErr，保存成功时正常写盘。
func TestUIReview17_RemotePushToggleRollback(t *testing.T) {
	dataDir := settingsTestDir(t)
	if err := config.Save(dataDir, config.Defaults()); err != nil {
		t.Fatal(err)
	}
	st := NewSettingsState(dataDir, nil, newChatState(NewFakeClient()))
	st.saveConfigFn = func(string, func(*config.Config) error) error {
		return errors.New("disk full")
	}

	row := setting{ID: "row-remote-push", Title: "手机离线时推送", Control: remotePushToggleControl{}}
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSetting(c, st, row)
	}, 900, 200)

	if err := tt.Click("切换手机离线时推送"); err != nil {
		t.Fatalf("点击推送开关失败: %v", err)
	}
	settingsSaveWriters.Wait()

	st.mu.Lock()
	st.cfgMu.Lock()
	pushEnabled := st.cfg.Remote.PushEnabled()
	pushErr := st.remote.pushErr
	st.cfgMu.Unlock()
	st.mu.Unlock()
	if !pushEnabled {
		t.Fatal("保存失败后 PushEnabled 应回滚为 true")
	}
	if pushErr == "" {
		t.Fatal("保存失败后应记录 remote.pushErr")
	}

	st.saveConfigFn = nil
	if err := tt.Click("切换手机离线时推送"); err != nil {
		t.Fatalf("再次点击推送开关失败: %v", err)
	}
	settingsSaveWriters.Wait()

	st.mu.Lock()
	st.cfgMu.Lock()
	pushEnabledAfter := st.cfg.Remote.PushEnabled()
	st.cfgMu.Unlock()
	st.mu.Unlock()
	if pushEnabledAfter {
		t.Fatal("保存成功后 PushEnabled 应更新为 false")
	}
}
