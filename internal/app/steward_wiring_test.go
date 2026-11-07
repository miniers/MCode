package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// stewardStrPtr 返回字符串指针，用于构造带可选字段的会话摘要。
func stewardStrPtr(s string) *string { return &s }

// stewardTestStateOf 用固定表构造状态查询函数，供纯函数测试。
func stewardTestStateOf(states map[string]kanban.SessionState) func(string) (kanban.SessionState, bool) {
	return func(threadID string) (kanban.SessionState, bool) {
		state, ok := states[threadID]
		return state, ok
	}
}

// 会话状态映射：只保留运行中、等审批、等输入与出错的会话，其余过滤。
func TestStewardThreadStatusItemsFilters(t *testing.T) {
	helperParent := "lead-1"
	summaries := []wire.ThreadSummary{
		{ID: "running", ProjectID: "p1", Title: "运行中", Provider: "claude", Model: stewardStrPtr("sonnet"), UpdatedAt: "2024-05-01T10:00:00Z", LastActivityText: stewardStrPtr("正在读文件")},
		{ID: "approval", ProjectID: "p1", Title: "等审批"},
		{ID: "input", ProjectID: "p1", Title: "等输入"},
		{ID: "failed", ProjectID: "p2", Title: "出错"},
		{ID: "idle", ProjectID: "p1", Title: "空闲"},
		{ID: "helper", ProjectID: "p1", Title: "bee", IsHelper: true},
		{ID: "child", ProjectID: "p1", Title: "子会话", ParentThreadID: &helperParent},
		{ID: "steward", ProjectID: "p1", Title: "总管"},
		{ID: "archived", ProjectID: "p1", Title: "已归档", IsArchived: true},
		{ID: "missing-state", ProjectID: "p1", Title: "没有状态"},
	}
	states := map[string]kanban.SessionState{
		"running":  {Running: true},
		"approval": {AwaitingApproval: true},
		"input":    {AwaitingInput: true},
		"failed":   {Failed: true, FailureReason: "boom"},
		"idle":     {},
		"helper":   {Running: true},
		"child":    {Running: true},
		"steward":  {Running: true},
		"archived": {Running: true},
	}

	items := stewardThreadStatusItems(summaries, stewardTestStateOf(states), "", func(id string) bool {
		return id == "steward"
	})

	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ThreadID)
	}
	want := []string{"running", "approval", "input", "failed"}
	if len(got) != len(want) {
		t.Fatalf("会话数 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("会话 %d = %q, 期望 %q（完整列表 %v）", i, got[i], want[i], got)
		}
	}
}

// 会话状态映射：projectID 非空时只返回该项目的会话。
func TestStewardThreadStatusItemsProjectFilter(t *testing.T) {
	summaries := []wire.ThreadSummary{
		{ID: "p1-running", ProjectID: "p1", Title: "一"},
		{ID: "p2-running", ProjectID: "p2", Title: "二"},
	}
	states := map[string]kanban.SessionState{
		"p1-running": {Running: true},
		"p2-running": {Running: true},
	}

	items := stewardThreadStatusItems(summaries, stewardTestStateOf(states), "p2", nil)
	if len(items) != 1 {
		t.Fatalf("会话数 = %d, 期望 1", len(items))
	}
	if items[0].ThreadID != "p2-running" {
		t.Fatalf("ThreadID = %q, 期望 p2-running", items[0].ThreadID)
	}

	// 空白项目 ID 视为全部项目。
	if all := stewardThreadStatusItems(summaries, stewardTestStateOf(states), "  ", nil); len(all) != 2 {
		t.Fatalf("空白项目 ID 应返回全部会话，实际 %d 条", len(all))
	}
}

// 会话状态映射：字段逐一对应，UpdatedAt 用摘要里的字符串。
func TestStewardThreadStatusItemMapping(t *testing.T) {
	summary := wire.ThreadSummary{
		ID:               "thread-1",
		ProjectID:        "p1",
		Title:            "重构取数逻辑",
		Provider:         "codex",
		Model:            stewardStrPtr("gpt-5"),
		UpdatedAt:        "2024-05-01T10:00:00Z",
		LastActivityText: stewardStrPtr("正在跑测试"),
	}
	state := kanban.SessionState{
		Running:          true,
		AwaitingApproval: true,
		AwaitingInput:    true,
		Failed:           true,
		FailureReason:    "工具调用失败",
	}

	item := stewardThreadStatusItem(summary, state)
	if item.ThreadID != summary.ID || item.ProjectID != summary.ProjectID || item.Title != summary.Title {
		t.Fatalf("标识字段映射错误: %+v", item)
	}
	if item.Provider != "codex" || item.Model != "gpt-5" {
		t.Fatalf("提供方与模型映射错误: %+v", item)
	}
	if !item.Running || !item.AwaitingApproval || !item.AwaitingInput || !item.Failed {
		t.Fatalf("状态标志映射错误: %+v", item)
	}
	if item.FailureReason != "工具调用失败" || item.LastActivityText != "正在跑测试" {
		t.Fatalf("文本字段映射错误: %+v", item)
	}
	if item.UpdatedAt != "2024-05-01T10:00:00Z" {
		t.Fatalf("UpdatedAt = %q, 期望沿用摘要", item.UpdatedAt)
	}

	// 摘要里的可选字段为空时不带出多余内容。
	empty := stewardThreadStatusItem(wire.ThreadSummary{ID: "thread-2"}, kanban.SessionState{})
	if empty.Model != "" || empty.LastActivityText != "" {
		t.Fatalf("空摘要不应带出模型与活动文本: %+v", empty)
	}
}

// 桌面通知未接入时 notify_owner 返回明确错误。
func TestNotifyOwnerWithoutNotifier(t *testing.T) {
	var a App
	err := a.notifyOwner(context.Background(), "标题", "正文", "thread-1")
	if err == nil {
		t.Fatal("未接入桌面通知时应返回错误")
	}
	if err.Error() != "桌面通知未接入" {
		t.Fatalf("错误信息 = %q, 期望「桌面通知未接入」", err.Error())
	}
}

// 接入后 notify_owner 把标题、正文与会话 ID 原样转发给桌面通知。
func TestNotifyOwnerForwards(t *testing.T) {
	var a App
	type call struct{ title, body, threadID string }
	var calls []call
	a.SetOwnerNotifier(func(title, body, threadID string) error {
		calls = append(calls, call{title: title, body: body, threadID: threadID})
		return nil
	})

	if err := a.notifyOwner(context.Background(), "会话出错", "重构取数逻辑", "thread-9"); err != nil {
		t.Fatalf("notifyOwner: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("通知次数 = %d, 期望 1", len(calls))
	}
	if calls[0].title != "会话出错" || calls[0].body != "重构取数逻辑" || calls[0].threadID != "thread-9" {
		t.Fatalf("转发参数错误: %+v", calls[0])
	}

	// 传 nil 恢复未接入状态。
	a.SetOwnerNotifier(nil)
	if err := a.notifyOwner(context.Background(), "t", "b", ""); err == nil {
		t.Fatal("清除通知器后应返回错误")
	}
}

// 远程服务存在但推送未启用时，notify_owner 的结果与只有桌面通知时一致。
func TestNotifyOwnerRemoteWithoutPush(t *testing.T) {
	a := App{remote: NewRemoteService(t.TempDir(), nil)}
	if err := a.notifyOwner(context.Background(), "t", "b", ""); err != errOwnerNotifierUnavailable {
		t.Fatalf("推送未启用且无桌面通知时应返回 errOwnerNotifierUnavailable，得到 %v", err)
	}

	called := 0
	a.SetOwnerNotifier(func(title, body, threadID string) error {
		called++
		return nil
	})
	if err := a.notifyOwner(context.Background(), "t", "b", ""); err != nil {
		t.Fatalf("桌面通知可用时不应报错: %v", err)
	}
	if called != 1 {
		t.Fatalf("桌面通知次数 = %d, 期望 1", called)
	}
}

// 桌面通知被前台抑制、网页推送也没送出时，notify_owner 要求总管直接在主聊天里说。
func TestNotifyOwnerSuppressed(t *testing.T) {
	a := App{remote: NewRemoteService(t.TempDir(), nil)}
	a.SetOwnerNotifier(func(title, body, threadID string) error {
		return fmt.Errorf("应用在前台: %w", ErrOwnerNotifySuppressed)
	})
	err := a.notifyOwner(context.Background(), "t", "b", "")
	if err == nil {
		t.Fatal("通知被抑制且没有推送时应返回错误")
	}
	if !strings.Contains(err.Error(), "请直接在主聊天里告诉用户") {
		t.Fatalf("错误信息 = %q, 应要求总管直接在主聊天里说", err.Error())
	}
}

// 通知器正常返回 nil 时 notify_owner 不报错。
func TestNotifyOwnerDelivered(t *testing.T) {
	var a App
	a.SetOwnerNotifier(func(title, body, threadID string) error { return nil })
	if err := a.notifyOwner(context.Background(), "t", "b", ""); err != nil {
		t.Fatalf("通知已送达时不应报错: %v", err)
	}
}

// 推送服务未启用时 SendOwnerPush 返回 false。
func TestSendOwnerPushWithoutPush(t *testing.T) {
	rs := NewRemoteService(t.TempDir(), nil)
	if rs.SendOwnerPush("t", "b", "thread-1") {
		t.Fatal("推送服务未启用时应返回 false")
	}
}

// stewardTestSelfAlert 构造一个把全部通知记进切片的旁路通知器。
func stewardTestSelfAlert(threadID string, calls *[]string, name string) *stewardSelfAlert {
	return newStewardSelfAlert(
		func(id string) bool { return id == threadID },
		func(_ context.Context, title, body, got string) error {
			*calls = append(*calls, title+"|"+body+"|"+got)
			return nil
		},
		func() string { return name },
		nil,
	)
}

// TestStewardSelfAlertNotifiesAndSkipsInbox 验证总管自身会话失败时另发一条桌面通知，
// 但这条事件不进用户的收件箱；同一原因 10 分钟内只通知一次。
func TestStewardSelfAlertNotifiesAndSkipsInbox(t *testing.T) {
	rig := newAttentionTestRig(t)
	const threadID = "thread-steward"
	rig.wiring.SetIgnoreThread(func(id string) bool { return id == threadID })

	var calls []string
	alert := stewardTestSelfAlert(threadID, &calls, "小助")
	rig.hub.AddObserver(alert.observe)

	broken := func() {
		rig.hub.Notify(threadID, "thread.summary", wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "总管", IsRunning: true})
		rig.hub.Notify(threadID, "thread.summary", wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "总管", Attention: "failed"})
	}
	broken()
	if len(calls) != 1 {
		t.Fatalf("总管自身失败应通知一次，实际 %d 次：%v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "小助出问题了|") {
		t.Fatalf("通知标题应为「{名字}出问题了」，得到 %q", calls[0])
	}
	for _, want := range []string{"执行失败", "设置 → 总管"} {
		if !strings.Contains(calls[0], want) {
			t.Fatalf("通知正文应包含 %q，得到 %q", want, calls[0])
		}
	}

	// 同一原因在 10 分钟窗口内重复出现只通知一次，避免连续失败刷屏。
	broken()
	if len(calls) != 1 {
		t.Fatalf("10 分钟内同一原因只应通知一次，实际 %d 次", len(calls))
	}

	// 总管自己的事件不会进收件箱，避免用户被自己的同事的失败反复打扰。
	if n := len(listUnresolved(t, rig.svc)); n != 0 {
		t.Fatalf("总管自身会话不应产生待处理事件，实际 %d 条", n)
	}
}

// TestStewardSelfAlertIgnoresOtherThreads 验证旁路只关心总管自身会话，别的会话失败不重复通知。
func TestStewardSelfAlertIgnoresOtherThreads(t *testing.T) {
	var calls []string
	alert := stewardTestSelfAlert("thread-steward", &calls, "总管")
	alert.observe("thread-other", "thread.summary", wire.ThreadSummary{ID: "thread-other", ProjectID: "p1", Title: "别的会话", IsRunning: true})
	alert.observe("thread-other", "thread.summary", wire.ThreadSummary{ID: "thread-other", ProjectID: "p1", Title: "别的会话", Attention: "failed"})
	if len(calls) != 0 {
		t.Fatalf("非总管会话不应走这条旁路，实际通知 %d 次", len(calls))
	}
}

// TestStewardSelfAlertMessageKinds 验证只有失败与额度用尽会发通知，其余提醒不发。
func TestStewardSelfAlertMessageKinds(t *testing.T) {
	cases := []struct {
		name   string
		alert  remote.SessionAlert
		wantOK bool
		want   string
	}{
		{name: "失败", alert: remote.SessionAlert{Kind: remote.SessionAlertFailed, Body: "总管"}, wantOK: true, want: "执行失败"},
		{name: "额度", alert: remote.SessionAlert{Kind: remote.SessionAlertQuota, Body: "总管 · 预计 10:00 恢复"}, wantOK: true, want: "额度用完了"},
		{name: "完成", alert: remote.SessionAlert{Kind: remote.SessionAlertDone}, wantOK: false},
		{name: "审批", alert: remote.SessionAlert{Kind: remote.SessionAlertApproval}, wantOK: false},
		{name: "卡住", alert: remote.SessionAlert{Kind: remote.SessionAlertStalled}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, body, ok := stewardSelfAlertMessage("总管", tc.alert)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, 期望 %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if title != "总管出问题了" {
				t.Fatalf("标题 = %q", title)
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("正文应包含 %q，得到 %q", tc.want, body)
			}
		})
	}
}

// 总管失败通知的正文按 hostNoticeBodyMaxRunes 截断，且末尾的设置页提示完整保留；
// 短文本不被改动。
func TestStewardSelfAlertMessageTruncatesBody(t *testing.T) {
	longDetail := strings.Repeat("报错", hostNoticeBodyMaxRunes)
	alert := remote.SessionAlert{Kind: remote.SessionAlertFailed, Body: longDetail}
	title, body, ok := stewardSelfAlertMessage("总管", alert)
	if !ok {
		t.Fatal("失败提醒应产出通知")
	}
	if title != "总管出问题了" {
		t.Fatalf("标题 = %q, 期望短标题不被改动", title)
	}
	if runes := []rune(body); len(runes) > hostNoticeBodyMaxRunes {
		t.Fatalf("正文长度 = %d 字, 期望不超过 %d 字", len(runes), hostNoticeBodyMaxRunes)
	}
	if !strings.HasSuffix(body, stewardSelfAlertHint) {
		t.Fatalf("正文 = %q, 期望以 %q 结尾", body, stewardSelfAlertHint)
	}

	short := remote.SessionAlert{Kind: remote.SessionAlertFailed, Body: "编译失败"}
	if _, shortBody, _ := stewardSelfAlertMessage("总管", short); shortBody != "执行失败：编译失败"+stewardSelfAlertHint {
		t.Fatalf("短正文 = %q, 期望不被改动", shortBody)
	}
}
