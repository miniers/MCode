package frontdesk

import (
 "context"
 "encoding/json"
 "fmt"
 "log"
 "strings"

 "mcode/internal/agent/llm"
)

type LoopError struct { Round int; Err error }
func (e *LoopError) Error() string { return fmt.Sprintf("frontdesk 第 %d 轮：%v",e.Round,e.Err) }
func (e *LoopError) Unwrap() error { return e.Err }
func (d *Desk) toolbox() *Toolbox { if d.Tools!=nil { return d.Tools }; return NewToolbox(d) }
func (d *Desk) stream(ctx context.Context, req llm.Request)(*llm.Response,string,error) {
 var text strings.Builder
 resp,err:=d.Client.Stream(ctx,req,func(delta llm.Delta){ if delta.Type!="reasoning" { text.WriteString(delta.Text) } })
 return resp,text.String(),err
}
func result(call llm.ToolCall, value any, err error) llm.Message { var s string; if err!=nil { s=err.Error() } else if b,e:=json.Marshal(value); e==nil { s=string(b) } else { s=e.Error(); err=e }; return llm.Message{Role:"tool",ToolCallID:call.ID,IsError:err!=nil,Content:[]llm.Part{{Type:"text",Text:s}}} }
func (d *Desk) loop(ctx context.Context,in Inbound,snap Snapshot) error {
 if d.Client==nil { return d.fallback(ctx,in,snap) }
 tools:=d.toolbox(); parts:=[]llm.Part{{Type:"text",Text:in.Text}}; parts=append(parts,in.Images...)
 req:=llm.Request{Model:d.Model,System:snap.Text,Messages:[]llm.Message{{Role:"user",Content:parts}},Tools:tools.Specs()}
 rounds:=d.MaxRounds; if rounds<=0 { rounds=4 }
 for i:=0;i<rounds;i++ {
  resp,text,err:=d.stream(ctx,req); if err!=nil || resp==nil { log.Printf("frontdesk: 模型失败：%v",err); return d.fallback(ctx,in,snap) }
  dirty:=false
  for _,call:=range resp.Message.ToolCalls { tool,ok:=tools.Get(call.Name); if !ok { dirty=true; continue }; if _,err:=validate(tool,call.Arguments); err!=nil || !tool.Write { dirty=true } }
  if !dirty {
   if strings.TrimSpace(text)!="" { if d.Feed==nil { return fmt.Errorf("frontdesk: 回复存储不可用") }; if err:=d.Feed.AppendAssistant(ctx,in.ThreadID,KindStewardReply,text); err!=nil { return err } }
   for _,call:=range resp.Message.ToolCalls { tool,_:=tools.Get(call.Name); if _,err:=tool.Run(ctx,call.Arguments); err!=nil { log.Printf("frontdesk: 写工具 %s 失败：%v",call.Name,err) } }; return nil
  }
  history:=resp.Message
  history.ToolCalls=append([]llm.ToolCall(nil),resp.Message.ToolCalls...)
  for j:=range history.ToolCalls { var obj map[string]json.RawMessage; if json.Unmarshal(history.ToolCalls[j].Arguments,&obj)!=nil || obj==nil { history.ToolCalls[j].Arguments=json.RawMessage(`{}`) } }
  req.Messages=append(req.Messages,history)
  for _,call:=range resp.Message.ToolCalls {
   tool,ok:=tools.Get(call.Name); if !ok { req.Messages=append(req.Messages,result(call,nil,fmt.Errorf("未知工具：%s",call.Name))); continue }
   if _,err:=validate(tool,call.Arguments); err!=nil { req.Messages=append(req.Messages,result(call,nil,err)); continue }
   if tool.Write { req.Messages=append(req.Messages,result(call,"尚未执行，需要的话再发一次",nil)); continue }
   v,err:=tool.Run(ctx,call.Arguments); req.Messages=append(req.Messages,result(call,v,err))
  }
 }
 log.Printf("frontdesk: 达到最大轮数 %d",rounds); return d.fallback(ctx,in,snap)
}
