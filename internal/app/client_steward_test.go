package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mcode/internal/config"
)

// fakeStewardRuntime 是 stewardThreadRuntime 的测试替身：记录 Ensure 调用次数，
// 并按预设返回会话 ID 与错误。
type fakeStewardRuntime struct {
	threadID  string
	ensureID  string
	ensureErr error
	ensureN   int
	// ensureCtxErr 记录 Ensure 收到的 ctx 是否已带截止时间，用来验证调用方限了时。
	ensureHasDeadline bool
}

func (f *fakeStewardRuntime) ThreadID() string {
	return f.threadID
}

func (f *fakeStewardRuntime) Ensure(ctx context.Context) (string, error) {
	f.ensureN++
	_, f.ensureHasDeadline = ctx.Deadline()
	if f.ensureErr != nil {
		return "", f.ensureErr
	}
	f.threadID = f.ensureID
	return f.ensureID, nil
}

// TestClientStewardThreadID 覆盖总管会话 ID 的两种情况：未接线与已接线。
func TestClientStewardThreadID(t *testing.T) {
	client := &Client{}
	if got := client.StewardThreadID(); got != "" {
		t.Fatalf("未接线时应为空串，实际 %q", got)
	}
	rt := &fakeStewardRuntime{threadID: "steward-1"}
	client.SetSteward(rt)
	if got := client.StewardThreadID(); got != "steward-1" {
		t.Fatalf("期望 steward-1，实际 %q", got)
	}
}

// TestClientOpenStewardThreadNotWired 覆盖未接线时报错，而不是静默返回空 ID。
func TestClientOpenStewardThreadNotWired(t *testing.T) {
	client := &Client{}
	id, err := client.OpenStewardThread(context.Background())
	if err == nil {
		t.Fatalf("未接线时应报错，实际返回 %q", id)
	}
	if id != "" {
		t.Fatalf("未接线时应返回空串，实际 %q", id)
	}
}

// TestClientOpenStewardThreadExisting 覆盖已有会话时直接返回，且不去建会话。
func TestClientOpenStewardThreadExisting(t *testing.T) {
	rt := &fakeStewardRuntime{threadID: "steward-1"}
	client := &Client{}
	client.SetSteward(rt)
	id, err := client.OpenStewardThread(context.Background())
	if err != nil {
		t.Fatalf("已有会话不应报错: %v", err)
	}
	if id != "steward-1" {
		t.Fatalf("期望 steward-1，实际 %q", id)
	}
	if rt.ensureN != 0 {
		t.Fatalf("已有会话时不应调用 Ensure，实际调用 %d 次", rt.ensureN)
	}
}

// TestClientOpenStewardThreadEnsures 覆盖没有会话时现建一次，带上限时上下文并返回新 ID。
func TestClientOpenStewardThreadEnsures(t *testing.T) {
	rt := &fakeStewardRuntime{ensureID: "steward-new"}
	client := &Client{}
	client.SetSteward(rt)
	id, err := client.OpenStewardThread(context.Background())
	if err != nil {
		t.Fatalf("建会话不应报错: %v", err)
	}
	if id != "steward-new" {
		t.Fatalf("期望 steward-new，实际 %q", id)
	}
	if rt.ensureN != 1 {
		t.Fatalf("应调用 Ensure 一次，实际 %d 次", rt.ensureN)
	}
	if !rt.ensureHasDeadline {
		t.Fatal("Ensure 应收到带截止时间的上下文")
	}
}

// TestClientOpenStewardThreadDisabled 覆盖未启用：Ensure 返回空串且不报错时给出提示性错误。
func TestClientOpenStewardThreadDisabled(t *testing.T) {
	rt := &fakeStewardRuntime{}
	client := &Client{}
	client.SetSteward(rt)
	id, err := client.OpenStewardThread(context.Background())
	if err == nil {
		t.Fatalf("未启用时应报错，实际返回 %q", id)
	}
	if !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("错误应提示未启用，实际 %q", err.Error())
	}
}

// TestClientOpenStewardThreadEnsureError 覆盖 Ensure 出错：错误要包一层，便于界面区分场景。
func TestClientOpenStewardThreadEnsureError(t *testing.T) {
	boom := errors.New("启动 CLI 失败")
	rt := &fakeStewardRuntime{ensureErr: boom}
	client := &Client{}
	client.SetSteward(rt)
	id, err := client.OpenStewardThread(context.Background())
	if err == nil {
		t.Fatalf("Ensure 出错时应报错，实际返回 %q", id)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("应保留原始错误，实际 %q", err.Error())
	}
	if !strings.Contains(err.Error(), "准备总管会话失败") {
		t.Fatalf("错误应说明准备会话失败，实际 %q", err.Error())
	}
}

// TestClientStewardIdentity 覆盖总管的昵称与头像读取：读不到配置时两者都为空（昵称空由界面显示「总管」），
// 读到配置时返回昵称原文与头像。
func TestClientStewardIdentity(t *testing.T) {
	if name, avatar := (&Client{}).StewardIdentity(); name != "" || avatar != "" {
		t.Fatalf("读不到配置时应返回空昵称与空头像，实际 name=%q avatar=%q", name, avatar)
	}

	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Name = " 小助 "
	cfg.Orchestration.Steward.Avatar = "🤖"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	name, avatar := (&Client{dataDir: dataDir}).StewardIdentity()
	if name != "小助" || avatar != "🤖" {
		t.Fatalf("应返回配置里的昵称与头像，实际 name=%q avatar=%q", name, avatar)
	}

	// 昵称留空时返回空串，界面才能显示兜底名「总管」；头像留空同理交给界面处理。
	blank := config.Defaults()
	blank.Orchestration.Steward.Name = "   "
	blank.Orchestration.Steward.Avatar = ""
	if err := config.Save(dataDir, blank); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	name, avatar = (&Client{dataDir: dataDir}).StewardIdentity()
	if name != "" || avatar != "" {
		t.Fatalf("昵称留空时应返回空昵称与空头像，实际 name=%q avatar=%q", name, avatar)
	}
}

// TestClientStewardWakesTodayNotEnabled 覆盖未启用总管（未接线、配置也读不到）时拿不到今日次数：
// 设置页据此不显示「今天已提醒…还剩…」这一句。
func TestClientStewardWakesTodayNotEnabled(t *testing.T) {
	client := &Client{}
	used, limit, ok := client.StewardWakesToday()
	if ok || used != 0 || limit != 0 {
		t.Fatalf("未启用时应为 false 且计数为 0，实际 used=%d limit=%d ok=%v", used, limit, ok)
	}
}
