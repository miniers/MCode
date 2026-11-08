package app

import (
	"context"
	"path/filepath"
	"testing"

	"mcode/internal/frontdesk"
	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestFrontdeskTaskAndMigration(t *testing.T) {
	ctx:=context.Background()
	db,err:=store.Open(filepath.Join(t.TempDir(),"mcode.db")); if err!=nil {t.Fatal(err)}; defer db.Close()
	hub:=remote.NewStreamHub(remote.StreamHubOptions{}); defer hub.Close()
	backend:=remote.NewMemoryBackend(hub); defer backend.Close()
	if err=backend.AttachStore(ctx,db);err!=nil{t.Fatal(err)}
	backend.AddProject(wire.ProjectSummary{ID:"home",Name:"总管",Path:filepath.Join(t.TempDir(),"home")})
	backend.AddProject(wire.ProjectSummary{ID:"project",Name:"项目",Path:filepath.Join(t.TempDir(),"project")})
	backend.AddThread(wire.ThreadSummary{ID:"old",ProjectID:"home",Title:"旧总管",Provider:"memory"},nil)
	if err=db.SetSetting(ctx,"steward.thread_id","old");err!=nil{t.Fatal(err)}
	for i:=0;i<2;i++ {if err=migrateFrontdeskArchive(ctx,db,backend);err!=nil{t.Fatal(err)}}
	thread,err:=db.GetThread(ctx,"old");if err!=nil || thread.Title!="总管 · 归档"{t.Fatalf("归档失败: %+v %v",thread,err)}
	id,_,err:=db.GetSetting(ctx,"steward.archived_thread_id");if err!=nil || id!="old"{t.Fatalf("归档设置失败: %q %v",id,err)}
	svc:=&frontdeskServices{store:db,backend:backend,homeID:"home"}
	task,err:=svc.CreateTask(ctx,frontdesk.CreateTaskInput{Title:"任务"});if err!=nil{t.Fatal(err)}
	if task.ProjectID==nil || *task.ProjectID!="project" || task.GoalID==nil {t.Fatalf("任务归属错误: %+v",task)}
	goal,err:=db.GetGoal(ctx,*task.GoalID);if err!=nil || goal.Title!="总管" || goal.Status!="running" {t.Fatalf("目标错误: %+v %v",goal,err)}
	meta,err:=kanban.ParseTaskMeta(task.Meta);if err!=nil || meta.Origin!="owner" {t.Fatalf("来源错误: %+v %v",meta,err)}
}
