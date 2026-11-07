package mcodeui

import (
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// composerDockTravelAnimation 是 Hero→Docked 输入框位移动画。
// 行程约半个窗口高（几百 pt），250ms 会显得抢；弹簧 420ms、Bounce 0 不回弹，
// 落到底部时速度自然衰减，不会在停靠位弹一下。
var composerDockTravelAnimation = ui.Animation{
	Spring: ui.SpringOptions{Duration: 420 * time.Millisecond, Bounce: 0},
}

// composerHeroExitDuration 是引导块淡出、时间线淡入的时长：比位移短，
// 让「旧内容退场」比「输入框落位」先结束，视觉上有个先淡后定的顺序。
const composerHeroExitDuration = 220 * time.Millisecond

// composerDockInput 是 Hero/Docked 判定的全部输入，便于纯函数单测。
type composerDockInput struct {
	Empty        bool // 当前会话内容为空：消息、已裁决计划、已答复提问都为 0
	EmbeddedBee  bool // 浮动面板里的子代理会话
	PromoteArmed bool // 已发出首条消息、后端还没推回、还没切到 Docked
	PrevDocked   bool // 上一帧（同一会话）是否已经停靠
}

// composerDockDecision 是本帧的布局结论。
type composerDockDecision struct {
	Docked  bool // 输入框是否停靠到底部
	Promote bool // 本帧是否是「发送首条消息」引发的 Hero→Docked 切换帧（要播长距离位移）
}

// composerThreadEmpty 是「会话内容为空」的唯一口径：
// 消息、已裁决计划、已答复提问三者都为空才算 Hero。
// chat.go 的 hero 判定与 composer.go 的停靠判定都走这里，避免两处口径不一致。
func composerThreadEmpty(messages, resolvedPlans, answeredQuestions int) bool {
	return messages == 0 && resolvedPlans == 0 && answeredQuestions == 0
}

// decideComposerDock 汇总判定：浮动面板永远停靠且不播动画；
// 内容非空或带透视标记都停靠；只有「带标记 + 上一帧还没停靠」才是切换帧。
func decideComposerDock(in composerDockInput) composerDockDecision {
	if in.EmbeddedBee {
		return composerDockDecision{Docked: true}
	}
	docked := !in.Empty || in.PromoteArmed
	return composerDockDecision{Docked: docked, Promote: docked && in.PromoteArmed && !in.PrevDocked}
}

// composerDockPromoState 记录「发送了首条消息、后端还没推回」的会话。
// 自己的锁：发送可能在 createPendingThread 的 goroutine 里发生，不能依赖 UI 线程。
// 与 chatAnchorMu 的用法一致（都在持有 s.mu 时被调用，锁序固定为 s.mu → 本锁）。
type composerDockPromoState struct {
	mu       sync.Mutex
	threadID string
	armed    bool
}

// armComposerDockPromote 在发送首条消息时置位。threadID 用发送当帧已经确定的会话 ID：
// 待创建会话走 createPendingThread → sendMessage 的递归，进到这里时已是真实 ID。
func (s *chatState) armComposerDockPromote(threadID string) {
	if threadID == "" {
		return
	}
	s.composerDockPromo.mu.Lock()
	s.composerDockPromo.threadID, s.composerDockPromo.armed = threadID, true
	s.composerDockPromo.mu.Unlock()
}

// composerDockPromoArmed 报告该会话是否带着「首条消息已发出」的标记。
func (s *chatState) composerDockPromoArmed(threadID string) bool {
	if threadID == "" {
		return false
	}
	s.composerDockPromo.mu.Lock()
	defer s.composerDockPromo.mu.Unlock()
	return s.composerDockPromo.armed && s.composerDockPromo.threadID == threadID
}

// disarmComposerDockPromote 清掉标记：消息已推回、会话被切走、发送失败回退时都调用。
func (s *chatState) disarmComposerDockPromote(threadID string) {
	s.composerDockPromo.mu.Lock()
	if s.composerDockPromo.threadID == threadID {
		s.composerDockPromo.threadID, s.composerDockPromo.armed = "", false
	}
	s.composerDockPromo.mu.Unlock()
}

// composerDockDecisionFor 是渲染期用的只读版本：不写任何状态，可以被同一帧多处调用
// （chat.go 判分支、composer.go 注册过渡），两次结果必须一致。
func (s *chatState) composerDockDecisionFor(empty bool, selectedThreadID string) composerDockDecision {
	armed := s.composerDockPromoArmed(selectedThreadID)
	prevDocked := s.composerView.dockDocked
	if s.composerView.dockThreadID != selectedThreadID && !armed {
		// 换会话不播位移动画：把上一帧当成「已在目标布局」。带标记的那次切换是自己的，照播。
		prevDocked = true
	}
	return decideComposerDock(composerDockInput{
		Empty:        empty,
		EmbeddedBee:  s.embeddedBee,
		PromoteArmed: armed,
		PrevDocked:   prevDocked,
	})
}
