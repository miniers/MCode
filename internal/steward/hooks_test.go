package steward

import (
	"context"
	"strings"
	"testing"

	"mcode/internal/attention"
	"mcode/internal/config"
)

// TestQuietNotifyPassThrough 用表驱动验证安静时段的放行规则：审批、额度与失败照常提醒，
// 其他类型拦下并给总管「先不发」的新文案，时段外一律放行。
func TestQuietNotifyPassThrough(t *testing.T) {
	cases := []struct {
		name   string
		cfg    config.StewardConfig
		kind   attention.Kind
		seed   bool
		event  string // 事件所属会话
		target string // notify_owner 给的会话
		pass   bool
	}{
		{name: "安静时段放行审批", cfg: quietConfig("09:00", "10:00"), kind: attention.KindThreadApproval, seed: true, event: "thread-1", target: "thread-1", pass: true},
		{name: "安静时段放行额度", cfg: quietConfig("09:00", "10:00"), kind: attention.KindThreadQuota, seed: true, event: "thread-1", target: "thread-1", pass: true},
		{name: "安静时段放行失败", cfg: quietConfig("09:00", "10:00"), kind: attention.KindThreadFailed, seed: true, event: "thread-1", target: "thread-1", pass: true},
		{name: "安静时段拦下自动化完成", cfg: quietConfig("09:00", "10:00"), kind: attention.KindAutomationFinished, seed: true, event: "thread-1", target: "thread-1", pass: false},
		{name: "安静时段无对应事件按普通处理", cfg: quietConfig("09:00", "10:00"), seed: false, target: "thread-1", pass: false},
		{name: "安静时段事件属于别的会话仍拦下", cfg: quietConfig("09:00", "10:00"), kind: attention.KindThreadApproval, seed: true, event: "thread-2", target: "thread-1", pass: false},
		{name: "时段外拦不住任何类型", cfg: enabledConfig(), kind: attention.KindAutomationFinished, seed: true, event: "thread-1", target: "thread-1", pass: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newStewardRig(t, tc.cfg)
			if tc.seed {
				h.events.emit(evOfKind(tc.kind, "ev-1", tc.event, "proj-1"))
			}
			calls := 0
			hooks := h.rt.Hooks(nil, func(context.Context, string, string, string) error {
				calls++
				return nil
			})
			err := hooks.NotifyOwner(context.Background(), "标题", "正文", tc.target)
			if tc.pass {
				if err != nil {
					t.Fatalf("应放行，得到错误 %v", err)
				}
				if calls != 1 {
					t.Fatalf("应调用 notify 1 次，实际 %d 次", calls)
				}
				return
			}
			if err == nil {
				t.Fatal("安静时段内应拦下普通通知")
			}
			for _, want := range []string{"当前是安静时段", "需要你处理的审批、额度和失败会照常提醒"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("应返回新文案，缺少 %q，得到 %v", want, err)
				}
			}
			if calls != 0 {
				t.Fatalf("拦下时不应调用 notify，实际 %d 次", calls)
			}
		})
	}
}
