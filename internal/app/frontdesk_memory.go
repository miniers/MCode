package app

import (
	"context"
	"errors"
	"strings"

	"mcode/internal/frontdesk"
	"mcode/internal/config"
	"mcode/internal/localmem"
	"mcode/internal/nmem"
	"mcode/internal/store"
)

type frontdeskMemoryBackend interface {
	search(context.Context, string, nmem.SearchQuery) ([]nmem.Memory, error)
	get(context.Context, string, string) (nmem.Memory, error)
	forget(context.Context, string, string) (bool, error)
	commit(context.Context, string) (store.MemoryProposal, string, error)
}
type frontdeskMemory struct { store *store.Store; backend frontdeskMemoryBackend }

func newFrontdeskMemory(db *store.Store, dataDir string) frontdeskMemory {
	if getMemoryBackend() == config.MemoryBackendLocal {
		return frontdeskMemory{store:db, backend:&localMemoryTools{docs:localmem.New(""), store:db}}
	}
	client, err := nmem.Default()
	if err != nil { return frontdeskMemory{store:db} }
	return frontdeskMemory{store:db, backend:&nowledgeTools{client:client,dataDir:dataDir,store:db}}
}

var errFrontdeskMemoryUnavailable = errors.New("记忆服务未接入前台")

func (m frontdeskMemory) Search(ctx context.Context, path, query string, limit int) ([]frontdesk.MemoryHit,error) {
	if m.backend == nil { return nil,errFrontdeskMemoryUnavailable }
	hits,err:=m.backend.search(ctx,path,nmem.SearchQuery{Query:query,Limit:limit}); if err!=nil{return nil,err}
	out:=make([]frontdesk.MemoryHit,0,len(hits)); for _,h:=range hits {out=append(out,frontdesk.MemoryHit{ID:h.ID,Title:h.Title,Content:h.Content})}; return out,nil
}
func (m frontdeskMemory) Get(ctx context.Context,path,id string) (frontdesk.MemoryHit,error) {
	if m.backend==nil{return frontdesk.MemoryHit{},errFrontdeskMemoryUnavailable}; h,err:=m.backend.get(ctx,path,id); return frontdesk.MemoryHit{ID:h.ID,Title:h.Title,Content:h.Content},err
}
func (m frontdeskMemory) Forget(ctx context.Context,path,id string) (bool,error) { if m.backend==nil{return false,errFrontdeskMemoryUnavailable};return m.backend.forget(ctx,path,id) }
func (m frontdeskMemory) Commit(ctx context.Context,id string) (frontdesk.ProposeResult,error) { if m.backend==nil{return frontdesk.ProposeResult{ID:id,Status:store.MemoryProposalStatusPending},errFrontdeskMemoryUnavailable};p,_,err:=m.backend.commit(ctx,id);return frontdesk.ProposeResult{ID:p.ID,Status:p.Status},err }
func (m frontdeskMemory) Propose(ctx context.Context, req frontdesk.ProposeRequest) (frontdesk.ProposeResult,error) {
	title:=strings.TrimSpace(req.Content); if len([]rune(title))>80 {title=string([]rune(title)[:80])}
	p,err:=m.store.CreateMemoryProposal(ctx,store.MemoryProposal{ProjectPath:req.ProjectPath,Title:title,Content:req.Content,TargetMemoryID:req.TargetMemoryID,Source:store.MemoryProposalSourceUser})
	if err!=nil{return frontdesk.ProposeResult{},err}
	result:=frontdesk.ProposeResult{ID:p.ID,Status:p.Status}
	if req.UserRequested {return m.Commit(ctx,p.ID)}
	return result,nil
}
