package mcodeui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// quickReplyItem 是快捷回复行的渲染项，与配置里的快捷回复一一对应：
// Label 是按钮上的文字，Message 是点击后填进草稿的正文（只填入、不发送）。
type quickReplyItem struct {
	Label   string
	Message string
}

// quickReplyMaxLabelRunes 是按钮文字的上限，与计划里的配置校验（label ≤ 40 字）一致；
// 超长时截断显示而不是丢掉整条，正文仍然完整填进草稿。
const quickReplyMaxLabelRunes = 40

// composerQuickReplyMaxWidth 是快捷回复胶囊的最大宽度，超长文本在胶囊内截断。
const composerQuickReplyMaxWidth float32 = 240

// quickRepliesFromConfig 读取配置里的快捷回复列表并做基础清洗。
// 配置字段 config.Config.QuickReplies（[]config.QuickReply{Label, Message}）由设置页那批改动新增；
// 这里按字段名反射读取、再按 JSON 字段名（大小写不敏感）解码，配置里还没有该字段时视为未配置：
// 这样本文件在字段落地前后都能编译，落地后自动生效，不需要再改这里。
func quickRepliesFromConfig(cfg *config.Config) []quickReplyItem {
	if cfg == nil {
		return nil
	}
	field := reflect.ValueOf(cfg).Elem().FieldByName("QuickReplies")
	if !field.IsValid() || field.Kind() != reflect.Slice || field.Len() == 0 {
		return nil
	}
	return decodeQuickReplies(field.Interface())
}

// decodeQuickReplies 把配置里的快捷回复列表解成渲染项并做基础清洗。
// 单独拿出来是为了在配置字段刚落地的过渡期也能测：传任意带 Label/Message 字段的切片都成立。
func decodeQuickReplies(value any) []quickReplyItem {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var raw []quickReplyItem
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	items := make([]quickReplyItem, 0, len(raw))
	for _, item := range raw {
		message := strings.TrimSpace(item.Message)
		if message == "" {
			continue
		}
		label := strings.TrimSpace(item.Label)
		if label == "" {
			// 没填标签时退回正文首行，保证按钮上总有一句话可读。
			label = firstLineLabel(message)
		}
		label = truncateReplyLabel(label, quickReplyMaxLabelRunes)
		items = append(items, quickReplyItem{Label: label, Message: message})
	}
	return items
}

// truncateReplyLabel 按字符裁到 n 个，超出时补省略号。
func truncateReplyLabel(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// firstLineLabel 取正文首个非空行作为按钮文字。
func firstLineLabel(message string) string {
	for _, line := range strings.Split(message, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return message
}

// quickRepliesVisible 判断快捷回复行现在是否该出现：会话已经开始（有消息）、没有在跑轮次、
// 草稿为空且没有附件。只在“准备写新消息”的空闲时刻提示，不打断输入与执行。
func (s *chatState) quickRepliesVisible() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sending || len(s.messages) == 0 || strings.TrimSpace(s.draft) != "" {
		return false
	}
	for _, att := range s.attachments {
		if att.ID != "" || att.Uploading || att.Err != "" {
			return false
		}
	}
	threadID := s.selectedThreadID
	if threadID == "" {
		return s.pendingThread != nil
	}
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			return !s.threads[i].IsRunning && !s.threads[i].SwarmBusy
		}
	}
	return false
}

// applyQuickReply 把快捷回复的正文填进草稿并请求下一帧聚焦输入框，不发送。
// 走与手动输入相同的同步路径（syncDraftTags + 草稿持久化），保证标签与草稿一致。
func (s *chatState) applyQuickReply(message string) {
	message = strings.TrimSpace(message)
	if s == nil || message == "" {
		return
	}
	s.mu.Lock()
	s.draft = message
	s.composerFocusRequest = true
	threadID := s.selectedThreadID
	s.mu.Unlock()
	s.syncDraftTags()
	if threadID != "" {
		s.noteComposerDraft(threadID, message)
	}
	s.invalidate()
}

// quickReplyItemsFor 返回当前会话该展示的快捷回复；渲染路径只认这一处，测试可以替换它。
// 配置里还没有该字段（设置页改动尚未落地）时视为未配置，整行不出现。
var quickReplyItemsFor = func(s *chatState) []quickReplyItem {
	settings := getChatSettings(s)
	if settings == nil {
		return nil
	}
	return quickRepliesFromConfig(&settings.cfg)
}

// renderQuickReplies 渲染输入卡片上方的快捷回复行；配置为空或当前不该显示时什么都不画。
func renderQuickReplies(c *ui.Context, s *chatState) {
	if s == nil || s.embeddedBee {
		return
	}
	items := quickReplyItemsFor(s)
	if len(items) == 0 || !s.quickRepliesVisible() {
		return
	}
	ui.Row(c.Key("composer-quick-replies")).
		Wrap().
		Gap(Space3).
		Children(func() {
			for i, item := range items {
				item := item
				btn := ui.ButtonBase(c.Key(fmt.Sprintf("composer-quick-reply-%d", i))).
					Label(item.Label).
					Tooltip(item.Message).
					Height(ControlLG).
					Radius(RadiusPill).
					Padding(0, Space4).
					MaxWidth(composerQuickReplyMaxWidth).
					Background(ThemeSurface(c)).
					Border(Hairline, ThemeBorder(c)).
					OnClick(func() {
						s.applyQuickReply(item.Message)
					})
				if btn.Hovered() {
					btn.Background(ThemeHover(c))
				}
				scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
				btn.Scale(scale, scale)
				btn.Children(func() {
					ui.Text(c, item.Label).
						FontSize(uiFont(TypeFootnote)).
						MaxLines(1).
						TextColor(ThemeSecondaryText(c))
				})
			}
		})
}
