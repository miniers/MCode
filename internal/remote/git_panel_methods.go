package remote

import (
	"context"
	"encoding/json"
	"mcode/internal/git"
	"mcode/internal/remote/wire"
	"time"
)

// gitPanelService 提供 Git 面板的远程操作。
type gitPanelService interface {
	GitStatusEntries(context.Context, string) ([]git.StatusEntry, error)
	GitIndexDiff(context.Context, string, string, bool) ([]git.FileDiff, error)
	GitLog(context.Context, string, git.LogOptions) ([]git.LogCommit, error)
	GitShowCommit(context.Context, string, string) (git.CommitDetail, error)
	GitBlame(context.Context, string, string, string) ([]git.BlameLine, error)
	GitBranches(context.Context, string) ([]git.BranchInfo, error)
	GitTags(context.Context, string) ([]git.TagInfo, error)
	GitRemotes(context.Context, string) ([]git.RemoteInfo, error)
	GitOpState(context.Context, string) (git.OpState, error)
	GitStageFiles(context.Context, string, []string) error
	GitUnstageFiles(context.Context, string, []string) error
	GitCommitIndex(context.Context, string, string, bool) (string, error)
	GitPushForceWithLease(context.Context, string) error
	GitBranchRename(context.Context, string, string, string) error
	GitBranchDelete(context.Context, string, string, bool) error
	GitTagCreate(context.Context, string, string, string, string) error
	GitTagDelete(context.Context, string, string) error
	GitContinueOp(context.Context, string) error
	GitSkipOp(context.Context, string) error
	GitAbortOp(context.Context, string) error
	GitCherryPick(context.Context, string, string) error
	GitRevertCommit(context.Context, string, string) error
	GitReset(context.Context, string, string, string) error
}
type panelStatus struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath"`
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
}
type panelLine struct {
	Kind  byte   `json:"kind"`
	Text  string `json:"text"`
	OldNo int    `json:"oldNo"`
	NewNo int    `json:"newNo"`
}
type panelHunk struct {
	OldStart int         `json:"oldStart"`
	OldLines int         `json:"oldLines"`
	NewStart int         `json:"newStart"`
	NewLines int         `json:"newLines"`
	Header   string      `json:"header"`
	Lines    []panelLine `json:"lines"`
}
type panelDiff struct {
	OldPath       string      `json:"oldPath"`
	NewPath       string      `json:"newPath"`
	Status        string      `json:"status"`
	Binary        bool        `json:"binary"`
	Hunks         []panelHunk `json:"hunks"`
	IsDeferred    bool        `json:"isDeferred"`
	DeferredPatch string      `json:"deferredPatch"`
	Additions     int         `json:"additions"`
	Deletions     int         `json:"deletions"`
}
type panelRef struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type panelLog struct {
	SHA        string     `json:"sha"`
	Parents    []string   `json:"parents"`
	Author     string     `json:"author"`
	Email      string     `json:"email"`
	AuthorTime time.Time  `json:"authorTime"`
	Subject    string     `json:"subject"`
	Refs       []panelRef `json:"refs"`
	Unpushed   bool       `json:"unpushed"`
}
type panelCommitFile struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath"`
	Status   string `json:"status"`
	Added    int    `json:"added"`
	Deleted  int    `json:"deleted"`
	Binary   bool   `json:"binary"`
}
type panelDetail struct {
	panelLog
	Body       string            `json:"body"`
	Committer  string            `json:"committer"`
	CommitTime time.Time         `json:"commitTime"`
	Files      []panelCommitFile `json:"files"`
}
type panelBlame struct {
	Line       int       `json:"line"`
	SHA        string    `json:"sha"`
	Author     string    `json:"author"`
	AuthorTime time.Time `json:"authorTime"`
	Summary    string    `json:"summary"`
	Text       string    `json:"text"`
}
type panelBranch struct {
	Name         string    `json:"name"`
	FullRef      string    `json:"fullRef"`
	Remote       bool      `json:"remote"`
	Head         bool      `json:"head"`
	Upstream     string    `json:"upstream"`
	Ahead        int       `json:"ahead"`
	Behind       int       `json:"behind"`
	UpstreamGone bool      `json:"upstreamGone"`
	WorktreePath string    `json:"worktreePath"`
	CommitTime   time.Time `json:"commitTime"`
	Subject      string    `json:"subject"`
}
type panelTag struct {
	Name      string    `json:"name"`
	SHA       string    `json:"sha"`
	Subject   string    `json:"subject"`
	Annotated bool      `json:"annotated"`
	Time      time.Time `json:"time"`
}
type panelRemote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetchUrl"`
	PushURL  string `json:"pushUrl"`
}
type panelConflict struct {
	Path string `json:"path"`
	Code string `json:"code"`
}
type panelState struct {
	Kind        string          `json:"kind"`
	Branch      string          `json:"branch"`
	Onto        string          `json:"onto"`
	Step        int             `json:"step"`
	Total       int             `json:"total"`
	Conflicts   []panelConflict `json:"conflicts"`
	OursLabel   string          `json:"oursLabel"`
	TheirsLabel string          `json:"theirsLabel"`
}

func panelConvert[T any](v any) (T, error) {
	var out T
	data, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}
func init() {
	RegisterMethod("git.status.entries", handlePanel0)
	RegisterMethod("git.diff.file", handlePanel1)
	RegisterMethod("git.log", handlePanel2)
	RegisterMethod("git.show", handlePanel3)
	RegisterMethod("git.blame", handlePanel4)
	RegisterMethod("git.branch.list", handlePanel5)
	RegisterMethod("git.tag.list", handlePanel6)
	RegisterMethod("git.remote.list", handlePanel7)
	RegisterMethod("git.op.state", handlePanel8)
	RegisterMethod("git.stage", handlePanel9)
	RegisterMethod("git.unstage", handlePanel10)
	RegisterMethod("git.commit", handlePanel11)
	RegisterMethod("git.push.forceWithLease", handlePanel12)
	RegisterMethod("git.branch.rename", handlePanel13)
	RegisterMethod("git.branch.delete", handlePanel14)
	RegisterMethod("git.tag.create", handlePanel15)
	RegisterMethod("git.tag.delete", handlePanel16)
	RegisterMethod("git.op.continue", handlePanel17)
	RegisterMethod("git.op.skip", handlePanel18)
	RegisterMethod("git.op.abort", handlePanel19)
	RegisterMethod("git.cherryPick", handlePanel20)
	RegisterMethod("git.revert", handlePanel21)
	RegisterMethod("git.reset", handlePanel22)
}
func handlePanel0(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitStatusEntries(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	out := make([]panelStatus, 0, len(value))
	for _, v := range value {
		out = append(out, panelStatus{v.Path, v.OrigPath, string(v.Index), string(v.Worktree)})
	}
	return out, nil
}
func handlePanel1(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Path     string `json:"path"`
		Staged   *bool  `json:"staged"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Staged == nil {
		return nil, ExtInvalidParams("staged 不能为空")
	}
	if p.Path == "" {
		return nil, ExtInvalidParams("path 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitIndexDiff(ctx, p.ThreadID, p.Path, *p.Staged)
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[[]panelDiff](value)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []panelDiff{}
	}
	return struct {
		Files []panelDiff `json:"files"`
	}{out}, nil
}
func handlePanel2(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Ref      string `json:"ref"`
		All      bool   `json:"all"`
		Path     string `json:"path"`
		Query    string `json:"query"`
		Skip     int    `json:"skip"`
		Limit    int    `json:"limit"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	if p.Limit > 500 {
		p.Limit = 500
	}
	if p.Skip < 0 {
		return nil, ExtInvalidParams("skip 不能为负数")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitLog(ctx, p.ThreadID, git.LogOptions{Ref: p.Ref, All: p.All, Path: p.Path, Query: p.Query, Skip: p.Skip, Limit: p.Limit})
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[[]panelLog](value)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []panelLog{}
	}
	return out, nil
}
func handlePanel3(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		SHA      string `json:"sha"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.SHA == "" {
		return nil, ExtInvalidParams("sha 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitShowCommit(ctx, p.ThreadID, p.SHA)
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[panelDetail](value)
	if err != nil {
		return nil, err
	}
	return out, nil
}
func handlePanel4(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Path     string `json:"path"`
		Rev      string `json:"rev"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, ExtInvalidParams("path 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitBlame(ctx, p.ThreadID, p.Path, p.Rev)
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[[]panelBlame](value)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []panelBlame{}
	}
	return out, nil
}
func handlePanel5(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitBranches(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[[]panelBranch](value)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []panelBranch{}
	}
	return out, nil
}
func handlePanel6(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitTags(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[[]panelTag](value)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []panelTag{}
	}
	return out, nil
}
func handlePanel7(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitRemotes(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[[]panelRemote](value)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []panelRemote{}
	}
	return out, nil
}
func handlePanel8(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	value, err := svc.GitOpState(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	out, err := panelConvert[panelState](value)
	if err != nil {
		return nil, err
	}
	return out, nil
}
func handlePanel9(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string   `json:"threadID"`
		Paths    []string `json:"paths"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if len(p.Paths) == 0 {
		return nil, ExtInvalidParams("路径不能为空")
	}
	for _, path := range p.Paths {
		if path == "" {
			return nil, ExtInvalidParams("路径不能为空")
		}
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitStageFiles(ctx, p.ThreadID, p.Paths); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel10(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string   `json:"threadID"`
		Paths    []string `json:"paths"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if len(p.Paths) == 0 {
		return nil, ExtInvalidParams("路径不能为空")
	}
	for _, path := range p.Paths {
		if path == "" {
			return nil, ExtInvalidParams("路径不能为空")
		}
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitUnstageFiles(ctx, p.ThreadID, p.Paths); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel11(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Message  string `json:"message"`
		Amend    bool   `json:"amend"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Message == "" {
		return nil, ExtInvalidParams("message 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	value, err := svc.GitCommitIndex(ctx, p.ThreadID, p.Message, p.Amend)
	if err != nil {
		return nil, err
	}
	return struct {
		SHA string `json:"sha"`
	}{value}, nil
}
func handlePanel12(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtNetworkTimeout)
	defer cancel()
	if err := svc.GitPushForceWithLease(ctx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel13(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		OldName  string `json:"oldName"`
		NewName  string `json:"newName"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.OldName == "" {
		return nil, ExtInvalidParams("oldName 不能为空")
	}
	if p.NewName == "" {
		return nil, ExtInvalidParams("newName 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitBranchRename(ctx, p.ThreadID, p.OldName, p.NewName); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel14(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Name     string `json:"name"`
		Force    bool   `json:"force"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, ExtInvalidParams("name 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitBranchDelete(ctx, p.ThreadID, p.Name, p.Force); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel15(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Name     string `json:"name"`
		Rev      string `json:"rev"`
		Message  string `json:"message"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, ExtInvalidParams("name 不能为空")
	}
	if p.Rev == "" {
		p.Rev = "HEAD"
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitTagCreate(ctx, p.ThreadID, p.Name, p.Rev, p.Message); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel16(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Name     string `json:"name"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, ExtInvalidParams("name 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitTagDelete(ctx, p.ThreadID, p.Name); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel17(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitContinueOp(ctx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel18(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitSkipOp(ctx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel19(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitAbortOp(ctx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel20(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		SHA      string `json:"sha"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.SHA == "" {
		return nil, ExtInvalidParams("sha 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitCherryPick(ctx, p.ThreadID, p.SHA); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel21(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		SHA      string `json:"sha"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.SHA == "" {
		return nil, ExtInvalidParams("sha 不能为空")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitRevertCommit(ctx, p.ThreadID, p.SHA); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
func handlePanel22(ctx context.Context, d *Dispatcher, c Conn, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadID"`
		Rev      string `json:"rev"`
		Mode     string `json:"mode"`
	}
	if err := decodeExtParams(raw, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Rev == "" {
		return nil, ExtInvalidParams("rev 不能为空")
	}
	if p.Mode == "" {
		return nil, ExtInvalidParams("mode 不能为空")
	}
	if p.Mode != "soft" && p.Mode != "mixed" && p.Mode != "hard" {
		return nil, ExtInvalidParams("无效的重置模式")
	}
	svc, ok := backendService[gitPanelService](d.backend, GitExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	ctx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitReset(ctx, p.ThreadID, p.Rev, p.Mode); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}
