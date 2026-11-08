package mcodeui

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/egoist/mygo/ui"
)

type stewardDecisionOption struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type stewardDecision struct {
	ID          string                  `json:"id"`
	Task        string                  `json:"task"`
	Question    string                  `json:"question"`
	Options     []stewardDecisionOption `json:"options"`
	Recommended string                  `json:"recommended"`
}

type stewardDecisionAnswer struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
}

func parseStewardDecision(text string) (stewardDecision, bool) {
	var decision stewardDecision
	err := json.Unmarshal([]byte(text), &decision)
	return decision, err == nil && decision.ID != "" && decision.Question != ""
}

// 最后一条同编号回答代表当前状态，允许后端追加改答记录。
func stewardDecisionResult(decision stewardDecision, messages []Message) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Kind != stewardDecisionAnswerKind || (m.Role != "user" && m.Role != "system") {
			continue
		}
		var answer stewardDecisionAnswer
		if json.Unmarshal([]byte(m.Text), &answer) != nil || answer.ID != decision.ID {
			continue
		}
		if answer.Answer == "忽略" {
			return "已忽略", true
		}
		for _, option := range decision.Options {
			if option.Key == answer.Answer {
				return "已选 " + answer.Answer + " · " + option.Text, true
			}
		}
		return "已选 " + answer.Answer, true
	}
	return "", false
}

func stewardDecisionShortcut(key ui.Key, options []stewardDecisionOption) (string, bool) {
	if key < ui.Key1 || key > ui.Key9 {
		return "", false
	}
	index := int(key - ui.Key1)
	if index >= len(options) {
		return "", false
	}
	return options[index].Key, true
}

func stewardDecisionMessage(id, answer string) string { return id + " " + answer }

func (s *chatState) answerStewardDecision(id, answer string) {
	client, ok := s.client.(stewardDecisionAnswerClient)
	if !ok {
		s.sendTextOnly(stewardDecisionMessage(id, answer))
		return
	}
	st := getStewardOpenState(s)
	st.mu.Lock()
	delete(st.decisionErrors, id)
	st.mu.Unlock()
	go func() {
		if err := client.AnswerStewardDecision(context.Background(), id, answer); err != nil {
			st.mu.Lock()
			if st.decisionErrors == nil {
				st.decisionErrors = make(map[string]string)
			}
			st.decisionErrors[id] = err.Error()
			st.mu.Unlock()
		}
		s.invalidate()
	}()
}

// stewardDecisionDraft 把决策编号前缀加到草稿前面：草稿为空时只留前缀，
// 已带同一前缀时不重复添加，并保留用户原有内容。
func stewardDecisionDraft(draft, id string) string {
	prefix := id + " "
	trimmed := strings.TrimSpace(draft)
	if trimmed == "" {
		return prefix
	}
	if strings.HasPrefix(trimmed, prefix) || trimmed == id {
		return draft
	}
	return prefix + draft
}

func lastInteractiveStewardDecision(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Kind != stewardDecisionKind {
			continue
		}
		decision, ok := parseStewardDecision(m.Text)
		if !ok {
			continue
		}
		if _, answered := stewardDecisionResult(decision, messages); !answered {
			return m.ID
		}
	}
	return ""
}

func renderStewardDecisionCard(c *ui.Context, s *chatState, m Message) ui.Element {
	decision, ok := parseStewardDecision(m.Text)
	if !ok {
		return renderStewardReply(c, s, m)
	}
	s.mu.Lock()
	messages := append([]Message(nil), s.messages...)
	draftEmpty := strings.TrimSpace(s.draft) == ""
	s.mu.Unlock()
	result, answered := stewardDecisionResult(decision, messages)
	st := getStewardOpenState(s)
	st.mu.Lock()
	answerError := st.decisionErrors[decision.ID]
	st.mu.Unlock()
	if !answered && draftEmpty && lastInteractiveStewardDecision(messages) == m.ID {
		for key := ui.Key1; key <= ui.Key9; key++ {
			if answer, valid := stewardDecisionShortcut(key, decision.Options); valid && c.Shortcut(0, key) {
				s.answerStewardDecision(decision.ID, answer)
			}
		}
	}

	accent := ThemeAccent(c)
	card := ui.Column(c.Key("steward-decision-" + m.ID)).FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).AlignSelf(ui.Center).Radius(RadiusLG).
		Background(accent.Alpha(0.06).Over(ThemeSurface(c))).Padding(Space5).Gap(Space3)
	target := float32(0)
	if answered {
		target = 1
	}
	state := card.AnimateTo("answered", target, ui.Animation{Duration: animateDuration(c, DurFast)})
	card.Border(Hairline, accent.Alpha(0.25).Mix(ThemeBorder(c), state))
	return card.Children(func() {
		ui.Row(c.Key("steward-decision-title-" + m.ID)).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, decision.ID).FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c))
			ui.Text(c, decision.Question).FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c)).Grow(1)
		})
		if answered {
			ui.Text(c, result).FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c))
			return
		}
		if answerError != "" {
			ui.Text(c, answerError).FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c))
		}
		for _, option := range decision.Options {
			option := option
			label := option.Key + " " + option.Text
			btn := ui.ButtonBase(c.Key("steward-decision-option-"+m.ID+"-"+option.Key)).
				Label(label).FillWidth().Padding(Space2, Space3).Radius(RadiusSM).
				Border(Hairline, ThemeBorder(c)).OnClick(func() {
				s.answerStewardDecision(decision.ID, option.Key)
			})
			pressed := float32(0)
			if btn.Pressed() {
				pressed = 1
			}
			press := btn.AnimateTo("press", pressed, ui.Animation{Duration: animateDuration(c, DurFast)})
			btn.Background(ThemeSurface(c).Mix(ThemeHover(c), press)).Children(func() {
				ui.Row(c.Key("steward-decision-option-row-" + m.ID + "-" + option.Key)).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
					ui.Text(c, label).FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c)).Grow(1)
					if option.Key == decision.Recommended {
						renderRecommendedBadge(c, "steward-decision-recommended-"+m.ID+"-"+option.Key)
					}
				})
			})
		}
		ui.Row(c.Key("steward-decision-actions-" + m.ID)).Gap(Space2).Children(func() {
			Button(c, ButtonProps{Key: "steward-decision-ignore-" + m.ID, Text: "忽略", Variant: ButtonSecondary, Size: ControlMD, OnClick: func() {
				s.answerStewardDecision(decision.ID, "忽略")
			}})
			Button(c, ButtonProps{Key: "steward-decision-other-" + m.ID, Text: "其他指示", Variant: ButtonSecondary, Size: ControlMD, OnClick: func() {
				s.mu.Lock()
				s.draft = stewardDecisionDraft(s.draft, decision.ID)
				s.composerFocusRequest = true
				s.completionCaretToEnd = true
				s.mu.Unlock()
				s.invalidate()
			}})
		})
	})
}
