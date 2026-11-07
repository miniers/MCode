package automation

import (
	"context"
	"testing"
	"time"
)

func TestDecodeSpecLegacyRuntimeMode(t *testing.T) {
	for _, mode := range []string{"autoAcceptEdits", "agent"} {
		t.Run(mode, func(t *testing.T) {
			spec := DecodeSpec(`{"runtimeMode":"` + mode + `"}`)
			if spec.RuntimeMode != "auto" {
				t.Fatalf("旧权限档 %q 规范化后为 %q", mode, spec.RuntimeMode)
			}
		})
	}
}

// runSettingsSpec 构造一份带模型与权限设置的 Spec，供下面的用例复用。
func runSettingsSpec(runMode string) Spec {
	return Spec{
		Schedule:    Schedule{Type: "interval", Seconds: 3600},
		RunMode:     runMode,
		Provider:    "anthropic",
		Model:       "claude-sonnet-4-5",
		Effort:      "high",
		RuntimeMode: "autoAcceptEdits",
	}
}

// assertRunSettings 校验一次 StartSession 收到的请求带上了 Spec 里的模型与权限设置。
func assertRunSettings(t *testing.T, provider, model, effort, runtimeMode string) {
	t.Helper()
	if provider != "anthropic" || model != "claude-sonnet-4-5" || effort != "high" || runtimeMode != "auto" {
		t.Fatalf("run settings not passed through: provider=%q model=%q effort=%q runtimeMode=%q",
			provider, model, effort, runtimeMode)
	}
}

// TestStandaloneRunUsesSpecRunSettings 验证 standalone 每次新建会话时带上自动化的模型与权限设置。
func TestStandaloneRunUsesSpecRunSettings(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	svc, db := setupTestService(t, clock, driver)
	defer db.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "p1", Name: "模型设置", Prompt: "检查",
		Spec: runSettingsSpec(RunModeStandalone),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunNow(ctx, def.ID); err != nil {
		t.Fatal(err)
	}
	if driver.startCount() != 1 {
		t.Fatalf("starts: %d", driver.startCount())
	}
	req := driver.starts[0]
	assertRunSettings(t, req.Provider, req.Model, req.Effort, req.RuntimeMode)
}

// TestDedicatedRunUsesSpecRunSettings 验证 dedicated 首次新建专属会话时同样带上这四项设置。
func TestDedicatedRunUsesSpecRunSettings(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	svc, db := setupTestService(t, clock, driver)
	defer db.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "p1", Name: "专属会话", Prompt: "检查",
		Spec: runSettingsSpec(RunModeDedicated),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunNow(ctx, def.ID); err != nil {
		t.Fatal(err)
	}
	if driver.startCount() != 1 {
		t.Fatalf("starts: %d", driver.startCount())
	}
	req := driver.starts[0]
	assertRunSettings(t, req.Provider, req.Model, req.Effort, req.RuntimeMode)
}

// TestHeartbeatRunIgnoresSpecRunSettings 验证 heartbeat 投递到已有会话，不会新建会话。
func TestHeartbeatRunIgnoresSpecRunSettings(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	driver := newFakeDriver()
	svc, db := setupTestService(t, clock, driver)
	defer db.Close()

	def, err := svc.Create(ctx, CreateInput{
		ProjectID: "p1", Name: "心跳", Prompt: "检查",
		Spec:           runSettingsSpec(RunModeHeartbeat),
		OriginThreadID: "thread-origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 假驱动不支持投递，运行会失败；这里只关心它没有新建会话。
	_, _ = svc.RunNow(ctx, def.ID)
	if driver.startCount() != 0 {
		t.Fatalf("heartbeat 不应新建会话，实际 starts: %d", driver.startCount())
	}
}
