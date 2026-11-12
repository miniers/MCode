package remote

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

type panelTestService struct {
	*gitExtFakeService
	gitPanelService
	options  git.LogOptions
	staged   []string
	strategy string
	tagRev   string
}

func (s *panelTestService) GitStatusEntries(context.Context, string) ([]git.StatusEntry, error) {
	return []git.StatusEntry{{Path: "a", Index: 'M', Worktree: ' '}}, nil
}
func (s *panelTestService) GitLog(_ context.Context, _ string, o git.LogOptions) ([]git.LogCommit, error) {
	s.options = o
	return []git.LogCommit{{SHA: "abcd", Subject: "提交"}}, nil
}
func (s *panelTestService) GitOpState(context.Context, string) (git.OpState, error) {
	return git.OpState{Branch: "main"}, nil
}
func (s *panelTestService) GitStageFiles(_ context.Context, _ string, p []string) error {
	s.staged = p
	return nil
}
func (s *panelTestService) GitCommitIndex(_ context.Context, _ string, m string, a bool) (string, error) {
	if len(s.staged) == 0 || m != "提交" || !a {
		return "", context.Canceled
	}
	return "abcd", nil
}
func (s *panelTestService) GitPullWith(_ context.Context, _ string, v string) error {
	s.strategy = v
	return nil
}

func (s *panelTestService) GitTagCreate(_ context.Context, _ string, _, rev, _ string) error {
	s.tagRev = rev
	return nil
}

func TestGitPanelMethods(t *testing.T) {
	svc := &panelTestService{gitExtFakeService: &gitExtFakeService{}}
	d, c := newGitExtTestDispatcher(&gitExtTestBackend{services: map[string]any{GitExtServiceName: svc}})
	call := func(method, extra string) wire.Response {
		return callExtension(t, d, c, `{"jsonrpc":"2.0","method":"`+method+`","params":{"threadID":"`+gitExtTestThreadID+`"`+extra+`},"id":1}`)
	}
	var entries []struct{ Path, Index, Worktree string }
	gitExtResult(t, call("git.status.entries", ""), &entries)
	if len(entries) != 1 || entries[0].Index != "M" || entries[0].Worktree != " " {
		t.Fatalf("状态不符：%+v", entries)
	}
	var logs []struct{ SHA, Subject string }
	gitExtResult(t, call("git.log", `,"limit":900,"ref":"main"`), &logs)
	if svc.options.Limit != 500 || svc.options.Ref != "main" || len(logs) != 1 || logs[0].SHA != "abcd" {
		t.Fatalf("日志不符：%+v %+v", svc.options, logs)
	}
	gitExtResult(t, call("git.log", ""), &logs)
	if svc.options.Limit != 50 {
		t.Fatalf("默认条数不符：%d", svc.options.Limit)
	}
	var state struct{ Branch string }
	gitExtResult(t, call("git.op.state", ""), &state)
	if state.Branch != "main" {
		t.Fatalf("操作状态不符：%+v", state)
	}
	var ok struct{ OK bool }
	gitExtResult(t, call("git.stage", `,"paths":["a"]`), &ok)
	if !ok.OK {
		t.Fatal("暂存失败")
	}
	var commit struct{ SHA string }
	gitExtResult(t, call("git.commit", `,"message":"提交","amend":true`), &commit)
	if commit.SHA != "abcd" {
		t.Fatalf("提交不符：%+v", commit)
	}
	for _, tc := range []struct{ method, params string }{{"git.reset", `,"rev":"HEAD","mode":"invalid"`}, {"git.pull", `,"strategy":"invalid"`}} {
		r := call(tc.method, tc.params)
		if r.Error == nil || r.Error.Code != wire.InvalidParams {
			t.Fatalf("%s 未拒绝无效参数：%+v", tc.method, r.Error)
		}
	}
	gitExtResult(t, call("git.tag.create", `,"name":"v1"`), &ok)
	if svc.tagRev != "HEAD" {
		t.Fatalf("标签默认版本不符：%q", svc.tagRev)
	}
	gitExtResult(t, call("git.pull", `,"strategy":"rebase"`), &ok)
	if svc.strategy != "rebase" {
		t.Fatalf("策略不符：%q", svc.strategy)
	}
}

func TestGitPanelSchemaMethods(t *testing.T) {
	data, err := os.ReadFile("../../docs/remote-control.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"git.status.entries", "git.diff.file", "git.log", "git.show", "git.blame", "git.branch.list", "git.tag.list", "git.remote.list", "git.op.state", "git.stage", "git.unstage", "git.commit", "git.push.forceWithLease", "git.branch.rename", "git.branch.delete", "git.tag.create", "git.tag.delete", "git.op.continue", "git.op.skip", "git.op.abort", "git.cherryPick", "git.revert", "git.reset"} {
		found := false
		for name, raw := range doc.Defs {
			var d struct {
				Properties struct {
					Method struct {
						Const string `json:"const"`
					} `json:"method"`
				} `json:"properties"`
			}
			if json.Unmarshal(raw, &d) == nil && d.Properties.Method.Const == m {
				found = true
				if strings.Count(string(data), `"$ref": "#/$defs/`+name+`"`) != 2 {
					t.Fatalf("%s 请求未加入顶层和批量 schema", m)
				}
				break
			}
		}
		if !found {
			t.Fatalf("schema 缺少 %s", m)
		}
		registered := false
		for _, name := range ImplementedMethods() {
			if name == m {
				registered = true
				break
			}
		}
		if !registered {
			t.Fatalf("未注册 %s", m)
		}
	}
}
