package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/remote"
)

// newRemoteSettingsTestClient 造一个只带临时数据目录的 Client，并写入一份配置供读写。
func newRemoteSettingsTestClient(t *testing.T, mutate func(*config.Config)) *Client {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	// Validate 要求 models.provider 是已配置的 provider，默认配置里是空串，先给个内置值。
	cfg.Models.Provider = "claude"
	if mutate != nil {
		mutate(&cfg)
	}
	if err := config.Save(dir, cfg); err != nil {
		t.Fatalf("准备测试配置失败: %v", err)
	}
	return &Client{dataDir: dir}
}

func remoteSettingsValues(t *testing.T, pairs map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := make(map[string]json.RawMessage, len(pairs))
	for key, value := range pairs {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		out[key] = raw
	}
	return out
}

func loadRemoteSettingsTestConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("重新读配置失败: %v", err)
	}
	return cfg
}

// 合法写入要落盘并触发一次配置变更通知（桌面设置页据此刷新）。
func TestRemoteSettingsSetPersistsAndNotifies(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	notifications := 0
	stop := client.WatchConfig(func() { notifications++ })
	defer stop()

	svc := remoteSettingsService{client: client}
	snapshot, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationMaxBees: 5,
		remote.SettingsKeyStewardName:          "小默",
	}))
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if notifications != 1 {
		t.Fatalf("配置变更通知次数 = %d, 期望 1", notifications)
	}

	cfg := loadRemoteSettingsTestConfig(t, client.dataDir)
	if cfg.Orchestration.MaxBees != 5 {
		t.Fatalf("max_bees = %d, 期望 5", cfg.Orchestration.MaxBees)
	}
	if cfg.Orchestration.Steward.Name != "小默" {
		t.Fatalf("steward.name = %q, 期望 %q", cfg.Orchestration.Steward.Name, "小默")
	}
	// 返回值就是落盘后的快照，客户端不用再拉一次。
	if string(snapshot.Values[remote.SettingsKeyOrchestrationMaxBees]) != "5" {
		t.Fatalf("快照 max_bees = %s, 期望 5", snapshot.Values[remote.SettingsKeyOrchestrationMaxBees])
	}
	if len(snapshot.WritableKeys) != len(remote.SettingsWritableKeys) {
		t.Fatalf("快照 writableKeys 数 = %d, 期望 %d", len(snapshot.WritableKeys), len(remote.SettingsWritableKeys))
	}
}

// 任一键非法时整体拒绝：不落盘、配置文件字节不变、也不发通知。
func TestRemoteSettingsSetRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "未知权限档位", key: remote.SettingsKeyPermissionsMode, value: `"yolo"`},
		{name: "bee 数超上限", key: remote.SettingsKeyOrchestrationMaxBees, value: `9`},
		{name: "工作空间模式不在枚举内", key: remote.SettingsKeyOrchestrationWorkspaceMode, value: `"local"`},
		{name: "派发轮次为负", key: remote.SettingsKeyOrchestrationMaxRounds, value: `-1`},
		{name: "派发轮次超上限", key: remote.SettingsKeyOrchestrationMaxRounds, value: `101`},
		{name: "提级秒数超上限", key: remote.SettingsKeyOrchestrationAutoForwardSeconds, value: `3601`},
		{name: "头像超过两个 rune", key: remote.SettingsKeyStewardAvatar, value: `"🤖🤖🤖"`},
		{name: "昵称超长", key: remote.SettingsKeyStewardName, value: `"一二三四五六七八九十一二三四五六七八九十一二三四五"`},
		{name: "安静时段非法", key: remote.SettingsKeyStewardQuietStart, value: `"25:00"`},
		{name: "类型不符", key: remote.SettingsKeyOrchestrationMaxBees, value: `"5"`},
		{name: "白名单外的键", key: "providers.claude.api_key", value: `"sk-secret"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newRemoteSettingsTestClient(t, nil)
			path := config.ConfigPath(client.dataDir)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读原配置失败: %v", err)
			}
			notifications := 0
			stop := client.WatchConfig(func() { notifications++ })
			defer stop()

			svc := remoteSettingsService{client: client}
			values := map[string]json.RawMessage{
				// 一起带上一个合法键：整体拒绝时它也不能生效。
				remote.SettingsKeyOrchestrationAlwaysBees: json.RawMessage(`true`),
				tc.key: json.RawMessage(tc.value),
			}
			if _, err := svc.Set(context.Background(), values); err == nil {
				t.Fatalf("非法值 %s 应被拒绝", tc.value)
			}
			if notifications != 0 {
				t.Fatalf("拒绝时不该发通知，得到 %d 次", notifications)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("回读配置失败: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("拒绝写入后配置文件被改动:\n原: %s\n新: %s", before, after)
			}
			if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.AlwaysBees {
				t.Fatalf("整体拒绝后合法键也不该生效")
			}
		})
	}
}

// models.roles 已下线：旧客户端再写这个键要按未知键拒绝，不能静默生效。
func TestRemoteSettingsSetRejectsRemovedModelRoles(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	_, err := svc.Set(context.Background(), map[string]json.RawMessage{
		"models.roles": json.RawMessage(`{"lead":{"provider":"claude","model":"opus"}}`),
	})
	if err == nil {
		t.Fatal("models.roles 应被拒绝")
	}
	if !strings.Contains(err.Error(), "is not a writable setting") {
		t.Fatalf("错误 = %v, 期望未知键错误", err)
	}
}

// 先后写入不同键时互不覆盖。
func TestRemoteSettingsSetKeepsOtherKeys(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationMaxBees: 3,
	})); err != nil {
		t.Fatalf("第一次写入失败: %v", err)
	}
	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyNotifyWhenForeground: true,
	})); err != nil {
		t.Fatalf("第二次写入失败: %v", err)
	}

	cfg := loadRemoteSettingsTestConfig(t, client.dataDir)
	if cfg.Orchestration.MaxBees != 3 {
		t.Fatalf("第二次写入覆盖了 max_bees：%d", cfg.Orchestration.MaxBees)
	}
	if !cfg.Notifications.NotifyWhenForeground {
		t.Fatalf("第二次写入没落盘")
	}
}

// 昵称允许留空：留空写入不报错，读回的快照也是空串（界面据此显示兜底名「总管」）。
func TestRemoteSettingsSetStewardNameAllowsEmpty(t *testing.T) {
	client := newRemoteSettingsTestClient(t, func(cfg *config.Config) {
		cfg.Orchestration.Steward.Name = "阿默"
	})
	svc := remoteSettingsService{client: client}

	snapshot, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyStewardName: "  ",
	}))
	if err != nil {
		t.Fatalf("留空昵称不应报错: %v", err)
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.Steward.Name != "" {
		t.Fatalf("steward.name = %q, 期望空串", cfg.Orchestration.Steward.Name)
	}
	var got string
	if err := json.Unmarshal(snapshot.Values[remote.SettingsKeyStewardName], &got); err != nil {
		t.Fatalf("快照 steward.name 解析失败: %v", err)
	}
	if got != "" {
		t.Fatalf("快照 steward.name = %q, 期望空串", got)
	}

	readBack, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if err := json.Unmarshal(readBack.Values[remote.SettingsKeyStewardName], &got); err != nil {
		t.Fatalf("读回的 steward.name 解析失败: %v", err)
	}
	if got != "" {
		t.Fatalf("读回的 steward.name = %q, 期望空串（不能被回填成兜底名）", got)
	}
}

// steward.enabled 必须联动 enabled_choice=true，否则下次迁移会把开关改回去。
func TestRemoteSettingsSetStewardEnabledChoosesExplicitly(t *testing.T) {
	client := newRemoteSettingsTestClient(t, func(cfg *config.Config) {
		cfg.Orchestration.Steward.Enabled = true
		cfg.Orchestration.Steward.EnabledChoice = false
	})
	svc := remoteSettingsService{client: client}

	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyStewardEnabled: false,
	})); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	cfg := loadRemoteSettingsTestConfig(t, client.dataDir)
	if cfg.Orchestration.Steward.Enabled {
		t.Fatalf("steward.enabled 应为 false")
	}
	if !cfg.Orchestration.Steward.EnabledChoice {
		t.Fatalf("steward.enabled 写入后 enabled_choice 应为 true")
	}
}

// session.pin_sent_message：默认开启，写入 false 后落盘，快照与配置文件都读到 false。
func TestRemoteSettingsSessionPinSentMessage(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	snapshot, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(snapshot.Values[remote.SettingsKeySessionPinSentMessage]) != `true` {
		t.Fatalf("默认值 = %s, 期望 true", snapshot.Values[remote.SettingsKeySessionPinSentMessage])
	}

	written, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeySessionPinSentMessage: false,
	}))
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	cfg := loadRemoteSettingsTestConfig(t, client.dataDir)
	if cfg.Session.PinSentMessageEnabled() {
		t.Fatalf("session.pin_sent_message 写入 false 后仍为开启")
	}
	if string(written.Values[remote.SettingsKeySessionPinSentMessage]) != `false` {
		t.Fatalf("快照 = %s, 期望 false", written.Values[remote.SettingsKeySessionPinSentMessage])
	}
}

// 新增的编排与总管键：默认值、与桌面端一致的生效值、合法写入后读回一致。
func TestRemoteSettingsOrchestrationAndStewardKeys(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	snapshot, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	wantDefaults := map[string]any{
		remote.SettingsKeyOrchestrationWorkspaceMode:      config.WorkspaceModeWorktree,
		remote.SettingsKeyOrchestrationMaxRounds:          0,
		remote.SettingsKeyOrchestrationRetryFailed:        false,
		remote.SettingsKeyOrchestrationAutoClearFinished:  false,
		remote.SettingsKeyOrchestrationReviewMode:         config.ReviewModeOff,
		remote.SettingsKeyOrchestrationAutoMerge:          false,
		remote.SettingsKeyOrchestrationAutoPush:           false,
		remote.SettingsKeyOrchestrationAutoForwardSeconds: 0,
		remote.SettingsKeyStewardAvatar:                   "",
	}
	for key, want := range wantDefaults {
		raw, ok := snapshot.Values[key]
		if !ok {
			t.Fatalf("快照缺少键 %q", key)
		}
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal 期望值失败: %v", err)
		}
		if !bytes.Equal(raw, encoded) {
			t.Fatalf("键 %q 默认值 = %s, 期望 %s", key, raw, encoded)
		}
	}

	written, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationWorkspaceMode:      config.WorkspaceModeShared,
		remote.SettingsKeyOrchestrationMaxRounds:          5,
		remote.SettingsKeyOrchestrationRetryFailed:        true,
		remote.SettingsKeyOrchestrationAutoClearFinished:  true,
		remote.SettingsKeyOrchestrationReviewMode:         config.ReviewModeAI,
		remote.SettingsKeyOrchestrationAutoMerge:          true,
		remote.SettingsKeyOrchestrationAutoPush:           true,
		remote.SettingsKeyOrchestrationAutoForwardSeconds: 300,
		remote.SettingsKeyStewardAvatar:                   "🤖",
	}))
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	cfg := loadRemoteSettingsTestConfig(t, client.dataDir)
	if cfg.Orchestration.WorkspaceMode != config.WorkspaceModeShared || cfg.Orchestration.MaxRounds != 5 {
		t.Fatalf("工作空间模式/派发轮次未生效: %+v", cfg.Orchestration)
	}
	if !cfg.Orchestration.RetryFailed || !cfg.Orchestration.AutoClearFinished || cfg.Orchestration.ReviewModeValue() != config.ReviewModeAI {
		t.Fatalf("子代理开关未生效: %+v", cfg.Orchestration)
	}
	if !cfg.Orchestration.AutoMerge || !cfg.Orchestration.AutoPush {
		t.Fatalf("完成动作开关未生效: %+v", cfg.Orchestration)
	}
	if cfg.Orchestration.AutoForwardQuestionSeconds != 300 {
		t.Fatalf("提级秒数 = %d, 期望 300", cfg.Orchestration.AutoForwardQuestionSeconds)
	}
	if cfg.Orchestration.Steward.Avatar != "🤖" {
		t.Fatalf("头像 = %q, 期望 %q", cfg.Orchestration.Steward.Avatar, "🤖")
	}

	// 返回的快照就是落盘后的值，客户端不用再拉一次。
	if string(written.Values[remote.SettingsKeyOrchestrationWorkspaceMode]) != `"shared"` {
		t.Fatalf("快照 workspace_mode = %s, 期望 shared", written.Values[remote.SettingsKeyOrchestrationWorkspaceMode])
	}
	if string(written.Values[remote.SettingsKeyStewardAvatar]) != `"🤖"` {
		t.Fatalf("快照 steward.avatar = %s, 期望 🤖", written.Values[remote.SettingsKeyStewardAvatar])
	}

	// 头像留空表示用名字的第一个字，必须写入成功。
	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyStewardAvatar: "",
	})); err != nil {
		t.Fatalf("清空头像失败: %v", err)
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.Steward.Avatar != "" {
		t.Fatalf("头像应为空，得到 %q", cfg.Orchestration.Steward.Avatar)
	}
}

// 审查模式三档：review_mode 可写 ai，非法值报错，旧布尔键 true 仍映射成 human。
func TestRemoteSettingsReviewMode(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationReviewMode: "ai",
	})); err != nil {
		t.Fatalf("写入 review_mode=ai 失败: %v", err)
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.ReviewMode != config.ReviewModeAI {
		t.Fatalf("review_mode = %q, 期望 ai", cfg.Orchestration.ReviewMode)
	}

	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationReviewMode: "auto",
	})); err == nil {
		t.Fatalf("非法 review_mode 应报错")
	}

	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationReviewBeforeMerge: true,
	})); err != nil {
		t.Fatalf("写入旧键 review_before_merge=true 失败: %v", err)
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.ReviewMode != config.ReviewModeHuman {
		t.Fatalf("旧键 true 应得到 human，实际 %q", cfg.Orchestration.ReviewMode)
	}
}

// settings.get 的快照要覆盖全部白名单键，并带上 config 里的快捷回复。
func TestRemoteSettingsGetSnapshotCoversWhitelist(t *testing.T) {
	client := newRemoteSettingsTestClient(t, func(cfg *config.Config) {
		cfg.QuickReplies = []config.QuickReply{{Label: "继续", Message: "接着刚才的做"}}
	})
	svc := remoteSettingsService{client: client}

	snapshot, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	for _, key := range remote.SettingsWritableKeys {
		raw, ok := snapshot.Values[key]
		if !ok {
			t.Fatalf("快照缺少白名单键 %q", key)
		}
		if len(raw) == 0 {
			t.Fatalf("白名单键 %q 的值为空", key)
		}
	}
	if len(snapshot.QuickReplies) != 1 || snapshot.QuickReplies[0].Label != "继续" {
		t.Fatalf("快捷回复没带上：%+v", snapshot.QuickReplies)
	}
}

// steward.auto_collapse_reasoning：总管自己的思考折叠开关，默认读到 true；
// 写入 false 后落盘为 AutoCollapseReasoningOff=true，且不影响通用会话的同名开关。
func TestRemoteSettingsStewardAutoCollapseReasoning(t *testing.T) {
	client := newRemoteSettingsTestClient(t, func(cfg *config.Config) {
		// 通用会话的折叠关掉，总管必须照自己的开关走。
		off := false
		cfg.Session.AutoCollapseReasoning = &off
	})
	svc := remoteSettingsService{client: client}

	snapshot, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(snapshot.Values[remote.SettingsKeyStewardAutoCollapseReasoning]) != `true` {
		t.Fatalf("默认值 = %s, 期望 true", snapshot.Values[remote.SettingsKeyStewardAutoCollapseReasoning])
	}

	written, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyStewardAutoCollapseReasoning: false,
	}))
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	cfg := loadRemoteSettingsTestConfig(t, client.dataDir)
	if !cfg.Orchestration.Steward.AutoCollapseReasoningOff {
		t.Fatalf("写入 false 后 auto_collapse_reasoning_off 应为 true")
	}
	if cfg.Orchestration.Steward.AutoCollapseReasoningEnabled() {
		t.Fatalf("写入 false 后总管折叠应为关闭")
	}
	if cfg.Session.AutoCollapseReasoningEnabled() {
		t.Fatalf("通用会话的折叠开关不应被改动")
	}
	if string(written.Values[remote.SettingsKeyStewardAutoCollapseReasoning]) != `false` {
		t.Fatalf("快照 = %s, 期望 false", written.Values[remote.SettingsKeyStewardAutoCollapseReasoning])
	}
}

// 检查命令按 trim 后的值落盘，超过 500 字节拒绝且不影响已存值。
func TestRemoteSettingsReviewCheckCommand(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	written, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationReviewCheckCommand: "  go test ./...  ",
	}))
	if err != nil {
		t.Fatalf("写入检查命令失败: %v", err)
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.ReviewCheckCommand != "go test ./..." {
		t.Fatalf("检查命令 = %q, 期望 %q", cfg.Orchestration.ReviewCheckCommand, "go test ./...")
	}
	if string(written.Values[remote.SettingsKeyOrchestrationReviewCheckCommand]) != `"go test ./..."` {
		t.Fatalf("快照检查命令 = %s, 期望 go test ./...", written.Values[remote.SettingsKeyOrchestrationReviewCheckCommand])
	}

	tooLong := strings.Repeat("a", config.MaxReviewCheckCommandLen+1)
	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationReviewCheckCommand: tooLong,
	})); err == nil {
		t.Fatalf("超长检查命令应报错")
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.ReviewCheckCommand != "go test ./..." {
		t.Fatalf("超长检查命令被写入: %q", cfg.Orchestration.ReviewCheckCommand)
	}
}

// 自动通过行数上限：默认 40，写 0 表示关闭，必须能落盘并读回。
func TestRemoteSettingsReviewMaxChangedLines(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	snapshot, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("读取默认设置失败: %v", err)
	}
	if got := string(snapshot.Values[remote.SettingsKeyOrchestrationReviewMaxChangedLines]); got != `40` {
		t.Fatalf("默认行数上限 = %s, 期望 40", got)
	}

	written, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationReviewMaxChangedLines: 0,
	}))
	if err != nil {
		t.Fatalf("写入行数上限=0 失败: %v", err)
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.ReviewMaxChangedLines != 0 {
		t.Fatalf("行数上限 = %d, 期望 0", cfg.Orchestration.ReviewMaxChangedLines)
	}
	if string(written.Values[remote.SettingsKeyOrchestrationReviewMaxChangedLines]) != `0` {
		t.Fatalf("快照行数上限 = %s, 期望 0", written.Values[remote.SettingsKeyOrchestrationReviewMaxChangedLines])
	}

	if _, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
		remote.SettingsKeyOrchestrationReviewMaxChangedLines: config.MaxReviewMaxChangedLines,
	})); err != nil {
		t.Fatalf("写入上限值失败: %v", err)
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.ReviewMaxChangedLines != config.MaxReviewMaxChangedLines {
		t.Fatalf("行数上限 = %d, 期望 %d", cfg.Orchestration.ReviewMaxChangedLines, config.MaxReviewMaxChangedLines)
	}
}

// 行数上限超出 0..500 时报错，错误文案与相邻键一致（Invalid params: '<key>': ...）。
func TestRemoteSettingsReviewMaxChangedLinesRejectsOutOfRange(t *testing.T) {
	client := newRemoteSettingsTestClient(t, nil)
	svc := remoteSettingsService{client: client}

	for _, value := range []int{-1, config.MaxReviewMaxChangedLines + 1} {
		_, err := svc.Set(context.Background(), remoteSettingsValues(t, map[string]any{
			remote.SettingsKeyOrchestrationReviewMaxChangedLines: value,
		}))
		if err == nil {
			t.Fatalf("行数上限 %d 应报错", value)
		}
		if !strings.Contains(err.Error(), remote.SettingsKeyOrchestrationReviewMaxChangedLines) {
			t.Fatalf("错误文案未带键名: %v", err)
		}
	}
	if cfg := loadRemoteSettingsTestConfig(t, client.dataDir); cfg.Orchestration.ReviewMaxChangedLines != config.DefaultReviewMaxChangedLines {
		t.Fatalf("越界写入后行数上限 = %d, 期望保持 %d", cfg.Orchestration.ReviewMaxChangedLines, config.DefaultReviewMaxChangedLines)
	}
}
