package frontdesk

import (
 "context"
 "fmt"
 "strings"
)

const systemPrompt = "你是前台：回答问题，把工作变成任务，把用户的话接回已有任务。不写代码、不合并、不发布。"
func truncate(s string, n int) string { r:=[]rune(s); if len(r)>n { return string(r[:n]) }; return s }
func (d *Desk) snapshot(ctx context.Context, in Inbound) Snapshot {
 var s Snapshot
 var b strings.Builder
 fmt.Fprintf(&b, "%s\n当前时间：%s\n", systemPrompt, d.now().Format("2006-01-02 15:04:05 MST"))
 if d.Tasks!=nil { if tasks,err:=d.Tasks.ListTasks(ctx, ""); err==nil {
  var active, done strings.Builder; na,nd:=0,0
  for _,t:=range tasks { if t.Status=="done" || t.Status=="cancelled" || t.Status=="rejected" { if nd<8 { fmt.Fprintf(&done,"%s %s %s\n",t.ID,t.Status,truncate(t.Title,160)); nd++ }; continue }; if na<40 { fmt.Fprintf(&active,"%s %s %s\n",t.ID,t.Status,truncate(t.Title,160)); s.OpenTaskIDs=append(s.OpenTaskIDs,t.ID); na++ } }
  fmt.Fprintf(&b,"进行中任务：\n%s最近完成：\n%s",truncate(active.String(),8000),truncate(done.String(),2000))
 } }
 if d.Decisions!=nil { if cards,err:=d.Decisions.ListDecisions(ctx,"open",15); err==nil { var x strings.Builder; for i,c:=range cards { if i>=15 { break }; s.OpenDecisionIDs=append(s.OpenDecisionIDs,c.ID); fmt.Fprintf(&x,"%s %s\n",c.ID,truncate(c.Question,200)) }; fmt.Fprintf(&b,"待决策：\n%s",truncate(x.String(),3000)) } }
 if d.Memory!=nil { if hits,err:=d.Memory.Search(ctx,d.ProjectPath,in.Text,12); err==nil { var x strings.Builder; for i,h:=range hits { if i>=12 { break }; fmt.Fprintf(&x,"%s %s\n",h.ID,truncate(h.Content,200)) }; fmt.Fprintf(&b,"相关记忆：\n%s",truncate(x.String(),3000)) } }
 if d.Feed!=nil { if msgs,err:=d.Feed.Recent(ctx,in.ThreadID,12); err==nil { var x strings.Builder; if len(msgs)>12 { msgs=msgs[len(msgs)-12:] }; for _,m:=range msgs { for _,p:=range m.Content { if p.Type=="text" { fmt.Fprintf(&x,"%s: %s\n",m.Role,truncate(p.Text,300)) } } }; fmt.Fprintf(&b,"最近对话：\n%s",truncate(x.String(),4000)) } }
 if in.ReplyTo!="" { fmt.Fprintf(&b,"回复任务：%s\n",truncate(in.ReplyTo,200)) }
 fmt.Fprintf(&b,"用户原话：%s",truncate(in.Text,4000)); s.Text=b.String(); return s
}
