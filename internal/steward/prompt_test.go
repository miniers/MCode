package steward

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestOpeningMessage_ColleagueTone 验证开场白是同事自我介绍，不再是「分诊」口吻：
// 带昵称时用「昵称 · 总管」称呼自己，并且不再先列举故障类型。
// 首句还带「拿不准就先问你一句」的姿态，能力里含定时任务。
func TestOpeningMessage_ColleagueTone(t *testing.T) {
	msg := OpeningMessage("小王")
	for _, want := range []string{"小王", "你的同事", "拿不准就先问你一句", "定时任务", "遇到要你拍板的才来问你"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("开场白缺少 %q: %q", want, msg)
		}
	}
	for _, banned := range []string{"会话卡住", "只把需要你拍板的事"} {
		if strings.Contains(msg, banned) {
			t.Fatalf("开场白不应再出现分诊口吻 %q: %q", banned, msg)
		}
	}
}

// TestOpeningMessage_DefaultName 验证没有昵称时用默认称呼「总管」。
func TestOpeningMessage_DefaultName(t *testing.T) {
	msg := OpeningMessage("")
	if !strings.Contains(msg, "我是总管，你的同事") {
		t.Fatalf("无昵称时开场白应自称「总管」: %q", msg)
	}
}

// TestIdentitySection_ColleagueNotTriage 验证身份段仍是同事口吻，没有「只读」定位的残留。
// 实调用方传的是 cfg.Label()，有昵称时是「昵称 · 总管」。
func TestIdentitySection_ColleagueNotTriage(t *testing.T) {
	section := IdentitySection("小王 · 总管")
	for _, want := range []string{"你是「小王 · 总管」", "用户的同事", "能在当前对话做完的事，你自己做"} {
		if !strings.Contains(section, want) {
			t.Fatalf("身份段缺少 %q", want)
		}
	}
	for _, banned := range []string{"你是只读", "不能改代码", "只读观察"} {
		if strings.Contains(section, banned) {
			t.Fatalf("身份段不应出现 %q", banned)
		}
	}
}

// TestIdentitySection_WakeRoundTriageOnly 验证「边界」新增第 4 条：被事件唤醒的轮次只做分诊，
// 不跑构建、测试等耗时命令；用户亲自发消息的轮次不受限制。
func TestIdentitySection_WakeRoundTriageOnly(t *testing.T) {
	section := IdentitySection("小王 · 总管")
	for _, want := range []string{
		"只做分诊",
		"不要跑构建、测试或其他耗时命令",
		"用户亲自发消息的轮次不受这条限制",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("边界段缺少 %q", want)
		}
	}
}

// TestIdentitySection_ToolDigest 验证「可用工具」段已压缩成三类职责句，
// 不再逐个工具写一行用法，而安全与行为约束段原样保留。
func TestIdentitySection_ToolDigest(t *testing.T) {
	section := IdentitySection("小王 · 总管")
	for _, want := range []string{
		"## 可用工具",
		"看情况", "开话题与派活", "通知与记账",
		"## 边界",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("身份段缺少 %q", want)
		}
	}
}

// TestIdentitySection_ToolDigestKeepsKeyActions 验证瘦身后的工具段仍点名四个关键动作，
// 模型才不会忘记它们存在（用法靠工具的 schema 与说明补充）。
func TestIdentitySection_ToolDigestKeepsKeyActions(t *testing.T) {
	section := IdentitySection("小王 · 总管")
	for _, want := range []string{"steward_open_topic", "plan_request", "steward_assign_task", "notify_owner"} {
		if !strings.Contains(section, want) {
			t.Fatalf("工具段缺少关键动作名 %q", want)
		}
	}
}

// TestIdentitySection_ToolDigestHasFallback 验证工具段有「不确定就先看工具说明」的兜底句。
func TestIdentitySection_ToolDigestHasFallback(t *testing.T) {
	section := IdentitySection("小王 · 总管")
	if !strings.Contains(section, "先看该工具的说明") {
		t.Fatalf("工具段缺少兜底句: %q", section)
	}
}

// TestIdentitySection_ToolSectionLength 验证「可用工具」段不再超过瘦身前的一半：
// 瘦身前这一段是 698 个 rune（含段标题与结尾空行，到「## 边界」为止）。
func TestIdentitySection_ToolSectionLength(t *testing.T) {
	section := IdentitySection("小王 · 总管")
	start := strings.Index(section, "## 可用工具")
	end := strings.Index(section, "## 边界")
	if start < 0 || end <= start {
		t.Fatalf("身份段里找不到完整的「可用工具」段")
	}
	got := utf8.RuneCountInString(section[start:end])
	if got > 698/2 {
		t.Fatalf("「可用工具」段 %d 字，超过瘦身前一半（349 字）", got)
	}
}

// TestIdentitySection_DecisionLineRule 验证「回复要求」新增第 6 条：需要用户拍板时正文首行固定
// 写「需要你决定：」，并且 [[静默]] 规则仍在。
func TestIdentitySection_DecisionLineRule(t *testing.T) {
	section := IdentitySection("总管")
	for _, want := range []string{"需要你决定：", "[[静默]]"} {
		if !strings.Contains(section, want) {
			t.Fatalf("身份段缺少 %q", want)
		}
	}
}

// TestIdentitySection_ReplyCadence 验证「回复节奏」段接在「回复要求」之后：
// 确认、进度、结果三条消息各自独立，且 [[静默]] 规则优先；新增内容不超过 400 字。
func TestIdentitySection_ReplyCadence(t *testing.T) {
	section := IdentitySection("总管")
	requirements := strings.Index(section, "## 回复要求")
	start := strings.Index(section, "## 回复节奏")
	if requirements < 0 || start < 0 || start <= requirements {
		t.Fatalf("「回复节奏」应出现在「回复要求」之后")
	}
	for _, want := range []string{"确认", "进度", "结果", "[[静默]]"} {
		if !strings.Contains(section[start:], want) {
			t.Fatalf("「回复节奏」段缺少 %q", want)
		}
	}
	if got := utf8.RuneCountInString(section[start:]); got > 400 {
		t.Fatalf("「回复节奏」段 %d 字，超过 400 字", got)
	}
}

// TestOrdinaryHandoffSection_Name 验证普通会话移交提示带上总管名字与移交入口。
func TestOrdinaryHandoffSection_Name(t *testing.T) {
	section := OrdinaryHandoffSection("小明")
	for _, want := range []string{"小明", "交给总管", "⇧⌘T"} {
		if !strings.Contains(section, want) {
			t.Fatalf("移交提示缺少 %q: %q", want, section)
		}
	}
	// 提示里不提具体工具名，避免普通会话去猜总管的能力。
	for _, banned := range []string{"automation_create", "steward_", "plan_request"} {
		if strings.Contains(section, banned) {
			t.Fatalf("移交提示不应出现工具名 %q: %q", banned, section)
		}
	}
}

// TestTopicIdentitySection_RunningActionTools 验证执行中阶段告诉话题会话可以催促、中断与移动本目标的对象。
func TestTopicIdentitySection_RunningActionTools(t *testing.T) {
	section := TopicIdentitySection("总管", Topic{
		ThreadID:  "topic-1",
		ProjectID: "proj-1",
		Title:     "重构登录",
		GoalID:    "goal-1",
	}, PhaseRunning)
	for _, want := range []string{"thread_send_message", "thread_interrupt", "kanban_move_task", "notify_owner", "本话题目标"} {
		if !strings.Contains(section, want) {
			t.Fatalf("执行中阶段身份段缺少 %q: %q", want, section)
		}
	}
}
