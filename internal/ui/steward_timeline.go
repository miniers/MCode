package mcodeui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// 总管会话在时间线上用更紧的形态：思考收成单行摘要，被唤醒的回合与只有 [[静默]] 的回合折起来，
// 让人一眼看到「它被什么唤醒、干活干了多久、几次巡视没有需要处理的事」。
// 本文件放这些形态用到的纯逻辑与两处小渲染，由 timeline_rows.go 调用。

// stewardWakeReasonRunes 是唤醒原因里引用的事件标题最多保留的字符数。
const stewardWakeReasonRunes = 24

// reasoningHeadlineRunes 是单行思考摘要最多保留的字符数。
const reasoningHeadlineRunes = 60

// 唤醒消息正文里的固定写法，来源见 internal/steward 的 buildWakeMessage / buildPatrolMessage。
const (
	stewardPatrolWakePrefix = "兜底巡检"
	stewardWakeTitlePrefix  = "标题："
)

// stewardWakeEventRe 匹配唤醒消息里的编号事件行「1. 事件 ev-1（看板任务失败）」：
// 第一组是事件 ID（只用来跳过它），第二组是事件类型。
var stewardWakeEventRe = regexp.MustCompile(`^\d+\.\s*事件\s*(.*?)（(.*)）\s*$`)

// stewardWakeTotalRe 匹配唤醒正文的抬头「有 3 条新情况，需要你先判断一下。」，
// 第一组是事件总数：事件太多时正文只列前若干条，抬头里的总数才代表实际事件数。
var stewardWakeTotalRe = regexp.MustCompile(`有\s*(\d+)\s*条新情况`)

// stewardWakeReason 从唤醒消息正文里抽一句唤醒原因，供回合折叠项的组头显示：
// 形如「因「登录重构」被唤醒」「因「登录重构」等 3 件事被唤醒」；解析不到时返回空串。
// 正文是一份编号事件列表，每个事件可带一行「   标题：…」：优先用第一条事件的标题，
// 没有标题就退回它括号里的事件类型；兜底巡检的正文以「兜底巡检」开头，直接说明来由。
func stewardWakeReason(text string) string {
	if strings.HasPrefix(strings.TrimSpace(text), stewardPatrolWakePrefix) {
		return "因兜底巡检被唤醒"
	}
	events := 0
	firstKind, firstTitle := "", ""
	inFirst := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := stewardWakeEventRe.FindStringSubmatch(trimmed); m != nil {
			events++
			// 只有第一条事件能提供组头里的名字：多事件时说的是「X 等 N 件事」。
			inFirst = events == 1
			if inFirst {
				firstKind = strings.TrimSpace(m[2])
			}
			continue
		}
		if inFirst && firstTitle == "" && strings.HasPrefix(trimmed, stewardWakeTitlePrefix) {
			firstTitle = strings.TrimSpace(strings.TrimPrefix(trimmed, stewardWakeTitlePrefix))
		}
	}
	if events == 0 {
		return ""
	}
	// 正文只列前若干条时，事件数以抬头里的总数和实际事件行数的较大值为准。
	if m := stewardWakeTotalRe.FindStringSubmatch(text); m != nil {
		if total, err := strconv.Atoi(m[1]); err == nil && total > events {
			events = total
		}
	}
	label := firstTitle
	if label == "" {
		label = firstKind
	}
	if label == "" {
		return ""
	}
	label = truncateRunes(label, stewardWakeReasonRunes)
	if events == 1 {
		return "因「" + label + "」被唤醒"
	}
	return fmt.Sprintf("因「%s」等 %d 件事被唤醒", label, events)
}

// reasoningHeadline 从一条思考的正文里抽一行做单行摘要：按行拆分、跳过空行，
// latest 为 true 取最后一行，否则取第一行；超过上限按 rune 截断并补省略号。没有内容时返回空串。
func reasoningHeadline(text string, latest bool) string {
	picked := reasoningHeadlineFull(text, latest)
	if picked == "" {
		return ""
	}
	return truncateRunes(picked, reasoningHeadlineRunes)
}

// reasoningHeadlineFull 返回未截断的首行（或最后一行）摘要，供标题 Tooltip 展示完整内容。
func reasoningHeadlineFull(text string, latest bool) string {
	picked := ""
	for _, line := range strings.Split(text, "\n") {
		line = reasoningHeadlineText(line)
		if line == "" {
			continue
		}
		picked = line
		if !latest {
			break
		}
	}
	return picked
}

// reasoningHeaderTitle 拼出思考行的标题：有正文时显示「思考过程 · 要旨」，
// 正文为空时只显示标题本身，不留下多余的间隔号。
func reasoningHeaderTitle(text string, streaming bool) string {
	headline := reasoningHeadline(text, streaming)
	if headline == "" {
		return reasoningHeaderLabel(streaming)
	}
	return reasoningHeaderLabel(streaming) + " · " + headline
}

// reasoningHeadlineText 把一行思考正文整理成摘要文字：去掉全部 ** 加粗标记与行内反引号，
// 再反复剥掉行首可能叠在一起的有序列表序号（如「1. 」「2) 」）与 # > - * 标记
// （例如「> - 1. 要点」）以及它们之间的空白。
func reasoningHeadlineText(line string) string {
	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "`", "")
	line = strings.TrimSpace(line)
	for {
		next := strings.TrimSpace(strings.TrimLeft(line, "#> -*"))
		next = stripOrderedListPrefix(next)
		if next == line {
			break
		}
		line = next
	}
	return line
}

// stripOrderedListPrefix 去掉行首的有序列表序号，要求序号后跟空白，
// 避免把「1.5 秒」这类小数误当成列表。
func stripOrderedListPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 3 || i >= len(line) {
		return line
	}
	if line[i] != '.' && line[i] != ')' {
		return line
	}
	if i+1 < len(line) && line[i+1] != ' ' && line[i+1] != '\t' {
		return line
	}
	return strings.TrimSpace(line[i+1:])
}

// reasoningHoldOpen 决定一条刚结束流式输出的思考要怎么收起：hold 表示这一帧先保持展开，
// forget 表示已消费掉「曾经流式」这个状态（下一帧起不再特殊对待）。
// wasLive 为假说明不是刚结束的帧，什么都不做；用户正在往上翻阅（不在底部）且开着自动折叠时，
// 突然收起会把视线跳走，所以先保持展开；其余情况（在底部、或关掉了自动折叠）都按普通规则收起。
func reasoningHoldOpen(wasLive, autoCollapse, atEnd bool) (hold, forget bool) {
	if !wasLive {
		return false, false
	}
	if autoCollapse && !atEnd {
		return true, false
	}
	return false, true
}

// reasoningAutoCollapse 返回某个会话类型下的「自动折叠思考过程」是否开启：
// 总管会话只看总管自己的开关（默认开），其他会话只看 Session.AutoCollapseReasoning，两者互不影响。
func reasoningAutoCollapse(cfg config.Config, isSteward bool) bool {
	if isSteward {
		return cfg.Orchestration.Steward.AutoCollapseReasoningEnabled()
	}
	return cfg.Session.AutoCollapseReasoningEnabled()
}

// stewardCompactReasoning 判断当前选中的是不是总管会话：总管会话的思考一律收成单行摘要，
// 运行中的工具组头下方补一行「思考中 · …」，不再把整段思考铺开。
// 调用方不能持有 s.mu：isAutoCollapseReasoningEnabled 会锁设置自己的锁，stewardSelected 也会回读会话状态。
func stewardCompactReasoning(s *chatState) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()
	if !stewardSelected(s, threadID) {
		return false
	}
	// 用户关掉总管自己的「自动折叠思考过程」时也不再收成单行：那时思考本来就要完整展开。
	return isAutoCollapseReasoningEnabled(s)
}

// stewardSilentHeaderText 拼出静默回合折叠项的组头：「后台巡视 3 次，无需处理 · 10:24」。
// 结束时间拿不到（零值）时省略后半段。
func stewardSilentHeaderText(rounds int, finishedAt time.Time) string {
	text := fmt.Sprintf("后台巡视 %d 次，无需处理", rounds)
	if finishedAt.IsZero() {
		return text
	}
	return text + " · " + finishedAt.Local().Format("15:04")
}

// renderToolGroupReasoningHint 在运行中、折叠着的工具组头下方补一行「思考中 · 最近的思考开头」：
// 总管会话的思考只在组头下露这一行摘要，省掉整段思考铺满时间轴。
// 组内没有思考或摘不出来时整行不渲染。
func renderToolGroupReasoningHint(c *ui.Context, tools []Message, groupID string) {
	var latest Message
	found := false
	for _, m := range tools {
		if !isReasoningMessage(m) {
			continue
		}
		latest = m
		found = true
	}
	if !found {
		return
	}
	headline := reasoningHeadline(latest.Text, true)
	if headline == "" {
		return
	}
	// 缩进与下面的工具预览一致，一眼看出这两行同属这个组。
	ui.Row(c.Key("group-reasoning-hint-"+groupID)).
		FillWidth().
		Padding(0, Space2, Space0, toolBeatPadLeft).
		Children(func() {
			ui.Text(c, "思考中 · "+headline).
				FontSize(scaledFont(TypeFootnote)).
				TextColor(ThemeSecondaryText(c).Alpha(0.8)).
				MaxLines(1).
				Ellipsis("…")
		})
}
