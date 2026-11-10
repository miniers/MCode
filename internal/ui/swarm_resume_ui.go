package mcodeui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 续跑横幅：MCode 重启会打断正在跑的 Swarm 子代理，重开工程后这些 bee 处于「已中断」状态，
// 连同重启前排队的派发条目一起等用户决定。面板里的这条细横栏提供「继续运行」与「放弃」两个出口。
// 放弃只清标记、不删任何文件，所以不弹二次确认。

// swarmResumeClient 是续跑所需的客户端能力；本地 Client 实现了它，旧实现或 FakeClient
// 没有实现时不显示横幅（宁可少一个按钮，也不让面板渲染失败）。
type swarmResumeClient interface {
	ResumeInterruptedSwarm(ctx context.Context, leadThreadID string) (int, error)
	DismissInterruptedSwarm(ctx context.Context, leadThreadID string) (int, error)
}

// 请求进行中的标记按 chatState 记录，只用于禁用按钮与提示失败原因（风格同 chatFindStates）。
var (
	swarmResumeMu   sync.Mutex
	swarmResumeBusy = make(map[*chatState]bool)
)

// swarmResumeBusyFor 返回该 chatState 是否有续跑/放弃请求在途。
func swarmResumeBusyFor(s *chatState) bool {
	swarmResumeMu.Lock()
	defer swarmResumeMu.Unlock()
	return swarmResumeBusy[s]
}

// setSwarmResumeBusy 设置该 chatState 的请求在途标记。
func setSwarmResumeBusy(s *chatState, busy bool) {
	swarmResumeMu.Lock()
	defer swarmResumeMu.Unlock()
	if busy {
		swarmResumeBusy[s] = true
		return
	}
	delete(swarmResumeBusy, s)
}

// swarmInterruptedBannerTitle 生成横幅文案；interrupted 为 0 时返回空串。
func swarmInterruptedBannerTitle(interrupted int) string {
	if interrupted <= 0 {
		return ""
	}
	return fmt.Sprintf("有 %d 个子代理或排队任务在 MCode 重启时被中断", interrupted)
}

// renderSwarmInterruptedBanner 在 bee 列表上方渲染续跑横幅；没有中断条目时不占布局。
func renderSwarmInterruptedBanner(c *ui.Context, s *chatState, leadThreadID string, interrupted int) {
	if s == nil || c == nil {
		return
	}
	title := swarmInterruptedBannerTitle(interrupted)
	if title == "" {
		return
	}
	client, ok := s.client.(swarmResumeClient)
	if !ok || client == nil {
		return
	}
	leadID := leadThreadID
	if leadID == "" {
		return
	}
	busy := swarmResumeBusyFor(s)
	accent := ThemeWarning(c)
	key := "swarm-interrupted-banner"

	ui.Row(c.Key(key)).
		FillWidth().
		Radius(RadiusLG).
		Background(accent.Alpha(0.10).Over(ThemeSurface(c))).
		Border(1, accent.Alpha(0.40)).
		Padding(Space2, Space4, Space2, Space3).
		Gap(Space3).
		AlignItems(ui.Center).
		Margin(0, 0, Space3, 0).
		Children(func() {
			ui.Text(c, title).
				Grow(1).
				MinWidth(0).
				FontSize(TypeFootnote).
				TextColor(ThemePrimaryText(c)).
				MaxLines(2)
			ui.Row(c.Key(key + "-actions")).Gap(Space3).AlignItems(ui.Center).Children(func() {
				renderApprovalButton(c, key+"-resume", "继续运行", "让这些子代理接着把中断的任务做完", true, busy, func() {
					resumeInterruptedSwarm(s, leadID)
				})
				renderApprovalButton(c, key+"-dismiss", "放弃", "放弃后这些任务不会再自动续跑", false, busy, func() {
					dismissInterruptedSwarm(s, leadID)
				})
			})
		})
}

// resumeInterruptedSwarm 在后台续跑被打断的子代理：请求期间禁用按钮，完成后刷新 Swarm 状态。
func resumeInterruptedSwarm(s *chatState, leadID string) {
	if s == nil || leadID == "" || swarmResumeBusyFor(s) {
		return
	}
	client, ok := s.client.(swarmResumeClient)
	if !ok || client == nil {
		return
	}
	setSwarmResumeBusy(s, true)

	go func(lid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		n, err := client.ResumeInterruptedSwarm(ctx, lid)
		setSwarmResumeBusy(s, false)
		if err != nil {
			s.setToast("续跑失败: " + err.Error())
		} else {
			s.setToast(fmt.Sprintf("已继续 %d 个子代理", n))
		}
		s.refreshSwarm()
		s.invalidate()
	}(leadID)
}

// dismissInterruptedSwarm 在后台放弃续跑：只清中断标记与排队条目，不删任何文件。
func dismissInterruptedSwarm(s *chatState, leadID string) {
	if s == nil || leadID == "" || swarmResumeBusyFor(s) {
		return
	}
	client, ok := s.client.(swarmResumeClient)
	if !ok || client == nil {
		return
	}
	setSwarmResumeBusy(s, true)

	go func(lid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		n, err := client.DismissInterruptedSwarm(ctx, lid)
		setSwarmResumeBusy(s, false)
		if err != nil {
			s.setToast("放弃续跑失败: " + err.Error())
		} else {
			s.setToast(fmt.Sprintf("已放弃 %d 个待续跑任务", n))
		}
		s.refreshSwarm()
		s.invalidate()
	}(leadID)
}
