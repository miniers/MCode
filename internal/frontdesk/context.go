package frontdesk

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const decisionReanswerWindow = 5 * time.Minute

const snapshotMaxRunes = 24000

const snapshotTruncated = "…（已截断）"

const systemPrompt = `你是 MoCo · 总管主会话的前台。每次处理一条用户消息，判断是直接回答、读取信息、交代工作、补充工作、回答决策卡，还是记住用户明确要求的长期偏好。

闲聊、致谢，以及能从当前上下文回答的简单问题，直接回复，不调用工具，不创建任务。
状态类问题，例如“X 怎么样了”“现在在跑什么”“今天发生了什么”，先看上下文。上下文足够就直接回答；不够时只调用必要的读工具，本轮回复留空，下一轮拿到结果再回答。没有证据的事实不要编造；能查就查，查不到就说不知道。不要把普通状态查询当成新工作；确实需要超过一分钟的调查时才创建任务。

真正的工作才调用 create_task，例如写代码、审查 PR、准备发版、调查，或任何预计超过一分钟的工作。标题写清要做的事；body 引用用户原话，并补上执行者需要的项目、已有结论、相关任务和限制。不要为同一项工作重复建任务。
针对已有任务的纠正、“不行”“再……”和补充要求，调用 amend_task，沿用那个任务的 id；任务已完成也一样，追加要求会重新打开同一个任务。不要因为任务已完成就另建任务。只有用户明确要求取消时才调用 cancel_task。
如果相关任务正在等待用户回答决策卡，用户这句话优先作为对该卡的回答，调用 answer_decision，不创建任务，也不用 amend_task 代替答卡。
快照里会提示五分钟内刚答过的决策卡。用户明确纠正或改变刚才的选择时，调用 answer_decision 对同一张卡改答；不要把改答另建成任务。没有明确关联时，不擅自把普通消息当成决策答案。

用户明确说“记住”“以后都这样”等长期偏好时，调用 remember；明确修改已有记忆时调用 edit_memory。对产品的抱怨、问题报告和改动要求属于工作，只创建或补充任务，不调用 remember。
你不直接写代码、合并、发版、部署、推远端、改设置、花钱或给人发消息。这些工作交给任务执行者。执行者需要用户作决定时，通过决策卡提出一个不超过三十字的问题和几个选项；前台不要代替用户批准。

用用户的语言回复，一到两句短话，具体说清做了什么或查到了什么。不要套模板，不寒暄，不写“好的我来”等填充语，不承诺流程，不说“做完带结果回来”“需要你拍板时再问你”。没有有用内容时回复为空；例如没有关联决策或任务的“A”“ok”“好的”，以及动作本身已足够说明结果的情况。
写工具会在回复发出之后执行。不要声称写入已成功或工作已完成；有必要说明动作时用“交给任务处理”等准确短话，也可以不回复。工具结果和上下文没有的信息不能当作事实。
最多四轮。读工具的结果在下一轮使用；不要在读结果之前先给结论。`

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	marker := []rune(snapshotTruncated)
	if n <= len(marker) {
		return string(marker[:max(n, 0)])
	}
	return string(r[:n-len(marker)]) + snapshotTruncated
}

type snapshotSection struct {
	name string
	text string
}

func joinSnapshotSections(sections []snapshotSection) string {
	drop := map[string]bool{}
	for _, name := range []string{"memory", "conversation", "done"} {
		var total int
		for _, section := range sections {
			if !drop[section.name] {
				total += len([]rune(section.text))
			}
		}
		if total <= snapshotMaxRunes {
			break
		}
		drop[name] = true
	}
	var b strings.Builder
	for _, section := range sections {
		if !drop[section.name] {
			b.WriteString(section.text)
		}
	}
	return b.String()
}

func (d *Desk) snapshot(ctx context.Context, in Inbound) Snapshot {
	var s Snapshot
	now := d.now()
	sections := []snapshotSection{{name: "base", text: fmt.Sprintf("%s\n当前时间：%s\n", systemPrompt, now.Format("2006-01-02 15:04:05 MST"))}}
	d.mu.Lock()
	recentAnswerID, recentAnswerAt := d.recentAnswerID, d.recentAnswerAt
	d.mu.Unlock()
	if recentAnswerID != "" && now.Sub(recentAnswerAt) <= decisionReanswerWindow {
		s.Answered = recentAnswerID
		sections = append(sections, snapshotSection{name: "answer", text: fmt.Sprintf("刚答过 %s，这条可能是在改答\n", recentAnswerID)})
	}
	if d.Tasks != nil {
		if tasks, err := d.Tasks.ListTasks(ctx, ""); err == nil {
			var active, done strings.Builder
			na, nd := 0, 0
			for _, t := range tasks {
				if t.Status == "done" || t.Status == "cancelled" || t.Status == "rejected" {
					if nd < 8 {
						fmt.Fprintf(&done, "%s %s %s\n", t.ID, t.Status, truncate(t.Title, 160))
						nd++
					}
					continue
				}
				if na < 40 {
					fmt.Fprintf(&active, "%s %s %s\n", t.ID, t.Status, truncate(t.Title, 160))
					na++
				}
			}
			sections = append(sections,
				snapshotSection{name: "active", text: fmt.Sprintf("进行中任务：\n%s", truncate(active.String(), 8000))},
				snapshotSection{name: "done", text: fmt.Sprintf("最近完成：\n%s", truncate(done.String(), 2000))},
			)
		}
	}
	if d.Decisions != nil {
		if cards, err := d.Decisions.ListDecisions(ctx, "open", 15); err == nil {
			var x strings.Builder
			for i, c := range cards {
				if i >= 15 {
					break
				}
				fmt.Fprintf(&x, "%s %s\n", c.ID, truncate(c.Question, 200))
			}
			sections = append(sections, snapshotSection{name: "decisions", text: fmt.Sprintf("待决策：\n%s", truncate(x.String(), 3000))})
		}
	}
	if d.Memory != nil {
		if hits, err := d.Memory.Search(ctx, d.ProjectPath, in.Text, 12); err == nil {
			var x strings.Builder
			for i, h := range hits {
				if i >= 12 {
					break
				}
				fmt.Fprintf(&x, "%s %s\n", h.ID, truncate(h.Content, 200))
			}
			sections = append(sections, snapshotSection{name: "memory", text: fmt.Sprintf("相关记忆：\n%s", truncate(x.String(), 3000))})
		}
	}
	if d.Feed != nil {
		if msgs, err := d.Feed.Recent(ctx, in.ThreadID, 12); err == nil {
			var x strings.Builder
			if len(msgs) > 12 {
				msgs = msgs[len(msgs)-12:]
			}
			for _, m := range msgs {
				for _, p := range m.Content {
					if p.Type == "text" {
						fmt.Fprintf(&x, "%s: %s\n", m.Role, truncate(p.Text, 300))
					}
				}
			}
			sections = append(sections, snapshotSection{name: "conversation", text: fmt.Sprintf("最近对话：\n%s", truncate(x.String(), 4000))})
		}
	}
	if in.ReplyTo != "" {
		sections = append(sections, snapshotSection{name: "reply", text: fmt.Sprintf("回复任务：%s\n", truncate(in.ReplyTo, 200))})
	}
	sections = append(sections, snapshotSection{name: "user", text: fmt.Sprintf("用户原话：%s", truncate(in.Text, 4000))})
	s.Text = joinSnapshotSections(sections)
	return s
}
