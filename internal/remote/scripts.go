package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/remote/wire"
)

var (
	favoritesMu  sync.Mutex
	memFavorites = make(map[*MemoryBackend]map[string][]string)

	// scriptRunSem 限制并发执行脚本的进程数，避免打满宿主系统资源
	scriptRunSem = make(chan struct{}, 4)
)

// maxScriptOutputBytes 是脚本回写进会话的输出上限。
const maxScriptOutputBytes = 64 * 1024

// cappedOutput 是带上限的输出缓冲：写满 limit 后直接丢弃后续内容，只统计丢弃的字节数，
// 避免脚本无限输出（如 `yes`）把桌面端内存吃满。
// 运行期间脚本进程会持续写入，同时 scripts.runStatus 会随时读取，故读写都用 mu 保护。
type cappedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated int
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) <= room {
			w.buf.Write(p)
		} else {
			w.buf.Write(p[:room])
			w.truncated += len(p) - room
		}
	} else {
		w.truncated += len(p)
	}
	return len(p), nil
}

// String 返回截至当前的输出，超限时追加与最终结果一致的截断说明后缀，供运行中查询使用。
func (w *cappedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.buf.String()
	if w.truncated > 0 {
		out += fmt.Sprintf("\n...[truncated %d bytes]", w.truncated)
	}
	return out
}

// maxScriptRunRecords 是内存中保留的脚本运行记录上限，超出后按创建顺序淘汰最旧的。
const maxScriptRunRecords = 50

// 脚本运行状态取值。
const (
	scriptRunStatusRunning  = "running"
	scriptRunStatusFinished = "finished"
	scriptRunStatusRejected = "rejected"
)

// scriptRunRecord 是一次后台脚本运行的当前状态；只存在内存里，由 scripts.runStatus 查询。
type scriptRunRecord struct {
	runID    string
	threadID string
	label    string
	status   string // running / finished / rejected
	exitCode *int
	output   string
	// live 是运行中的输出缓冲，结束后置 nil。
	live      *cappedOutput
	createdAt time.Time
}

// scriptRunStore 按创建顺序保存最近的脚本运行记录，零值可直接使用。
type scriptRunStore struct {
	mu    sync.Mutex
	runs  map[string]*scriptRunRecord
	order []string
}

// put 写入一条新记录，并把超出上限的最旧记录淘汰掉。
func (s *scriptRunStore) put(rec *scriptRunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = make(map[string]*scriptRunRecord)
	}
	s.runs[rec.runID] = rec
	s.order = append(s.order, rec.runID)
	for len(s.order) > maxScriptRunRecords {
		delete(s.runs, s.order[0])
		s.order = s.order[1:]
	}
}

// update 按 runID 更新运行结果；记录已被淘汰时丢弃本次更新。
func (s *scriptRunStore) update(runID, status string, exitCode *int, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.runs[runID]
	if !ok {
		return
	}
	rec.status = status
	rec.exitCode = exitCode
	rec.output = output
	rec.live = nil
}

// setLive 记录运行中的输出缓冲，供 scripts.runStatus 在脚本结束前读到部分输出。
func (s *scriptRunStore) setLive(runID string, w *cappedOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.runs[runID]
	if !ok {
		return
	}
	rec.live = w
}

// get 返回记录的值副本，避免调用方读到后台协程正在修改的字段。
func (s *scriptRunStore) get(runID string) (scriptRunRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.runs[runID]
	if !ok {
		return scriptRunRecord{}, false
	}
	// 锁顺序固定为先 store 锁、后 cappedOutput 锁，避免与写入路径交叉嵌套。
	if rec.live != nil && rec.status == scriptRunStatusRunning {
		out := *rec
		out.output = rec.live.String()
		out.live = nil
		return out, true
	}
	out := *rec
	out.live = nil
	return out, true
}

// detectScriptRunner 根据工作区根目录的包管理器 lockfile 判断脚本运行器（顺序：bun → pnpm → yarn → npm）。
func detectScriptRunner(root string) string {
	if _, err := os.Stat(filepath.Join(root, "bun.lockb")); err == nil {
		return "bun"
	}
	if _, err := os.Stat(filepath.Join(root, "bun.lock")); err == nil {
		return "bun"
	}
	if _, err := os.Stat(filepath.Join(root, "pnpm-lock.yaml")); err == nil {
		return "pnpm"
	}
	if _, err := os.Stat(filepath.Join(root, "yarn.lock")); err == nil {
		return "yarn"
	}
	return "npm"
}

// readPackageScripts 读取工作区根目录下 package.json 中的 scripts 定义。
func readPackageScripts(root string) ([]wire.ScriptsPackageScript, error) {
	pkgPath := filepath.Join(root, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]wire.ScriptsPackageScript, 0), nil
		}
		return nil, err
	}

	var raw struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return make([]wire.ScriptsPackageScript, 0), nil
	}

	// 尝试保持 package.json 中 scripts 键声明顺序
	var orderedKeys []string
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		t, err := dec.Token()
		if err != nil {
			break
		}
		if str, ok := t.(string); ok && str == "scripts" {
			t2, err := dec.Token()
			if err != nil || t2 != json.Delim('{') {
				break
			}
			for dec.More() {
				kTok, err := dec.Token()
				if err != nil {
					break
				}
				if kStr, ok := kTok.(string); ok {
					orderedKeys = append(orderedKeys, kStr)
					var val any
					_ = dec.Decode(&val)
				}
			}
			break
		}
	}

	if len(orderedKeys) == 0 && len(raw.Scripts) > 0 {
		for k := range raw.Scripts {
			orderedKeys = append(orderedKeys, k)
		}
		sort.Strings(orderedKeys)
	}

	var scripts []wire.ScriptsPackageScript
	seen := make(map[string]bool)
	for _, k := range orderedKeys {
		if seen[k] {
			continue
		}
		seen[k] = true
		if cmd, ok := raw.Scripts[k]; ok {
			scripts = append(scripts, wire.ScriptsPackageScript{
				Name:       k,
				Command:    cmd,
				IsFavorite: false,
			})
		}
	}

	if scripts == nil {
		scripts = make([]wire.ScriptsPackageScript, 0)
	}
	return scripts, nil
}

// getFavorites 读取当前工作区已收藏的脚本名称列表。
func getFavorites(b *MemoryBackend, root string) []string {
	favoritesMu.Lock()
	defer favoritesMu.Unlock()

	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()

	if dataDir == "" {
		m, ok := memFavorites[b]
		if !ok {
			return nil
		}
		return m[root]
	}

	favPath := filepath.Join(dataDir, "remote", "script-favorites.json")
	data, err := os.ReadFile(favPath)
	if err != nil {
		return nil
	}
	var stored map[string][]string
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil
	}
	return stored[root]
}

// setFavorites 持久化或内存更新工作区脚本的收藏状态，并返回排序后的收藏列表。
func setFavorites(b *MemoryBackend, root string, name string, isFavorite bool) ([]string, error) {
	favoritesMu.Lock()
	defer favoritesMu.Unlock()

	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()

	if dataDir == "" {
		m, ok := memFavorites[b]
		if !ok {
			m = make(map[string][]string)
			memFavorites[b] = m
		}
		list := m[root]
		var updated []string
		if isFavorite {
			found := false
			for _, item := range list {
				if item == name {
					found = true
					break
				}
			}
			if !found {
				updated = append(list, name)
			} else {
				updated = list
			}
		} else {
			for _, item := range list {
				if item != name {
					updated = append(updated, item)
				}
			}
		}
		m[root] = updated
		res := make([]string, len(updated))
		copy(res, updated)
		sort.Strings(res)
		return res, nil
	}

	remoteDir := filepath.Join(dataDir, "remote")
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		return nil, err
	}
	favPath := filepath.Join(remoteDir, "script-favorites.json")
	stored := make(map[string][]string)
	if data, err := os.ReadFile(favPath); err == nil {
		_ = json.Unmarshal(data, &stored)
	}

	list := stored[root]
	var updated []string
	if isFavorite {
		found := false
		for _, item := range list {
			if item == name {
				found = true
				break
			}
		}
		if !found {
			updated = append(list, name)
		} else {
			updated = list
		}
	} else {
		for _, item := range list {
			if item != name {
				updated = append(updated, item)
			}
		}
	}
	stored[root] = updated

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return nil, err
	}

	tmpFile, err := os.CreateTemp(remoteDir, "script-favorites-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpName)
		return nil, err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpName)
		return nil, err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return nil, err
	}

	if err := os.Rename(tmpName, favPath); err != nil {
		_ = os.Remove(tmpName)
		return nil, err
	}

	res := make([]string, len(updated))
	copy(res, updated)
	sort.Strings(res)
	return res, nil
}

// ScriptsList 获取工作区脚本列表（含 package.json 脚本与项目级脚本）。
func (b *MemoryBackend) ScriptsList(ctx context.Context, p wire.ScriptsListParams) (wire.ScriptsListResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.ScriptsListResult{}, err
	}

	runner := detectScriptRunner(root)
	pkgScripts, err := readPackageScripts(root)
	if err != nil {
		return wire.ScriptsListResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to read scripts: %v", err),
		}
	}

	favs := getFavorites(b, root)
	favMap := make(map[string]bool)
	for _, f := range favs {
		favMap[f] = true
	}

	for i := range pkgScripts {
		if favMap[pkgScripts[i].Name] {
			pkgScripts[i].IsFavorite = true
		}
	}

	projectScripts, err := b.ProjectScripts(p.ThreadID)
	if err != nil {
		return wire.ScriptsListResult{}, err
	}

	return wire.ScriptsListResult{
		Directory:      root,
		Runner:         runner,
		PackageScripts: pkgScripts,
		ProjectScripts: projectScriptsToWire(projectScripts),
		Revision:       projectScriptsRevision(projectScripts),
	}, nil
}

// projectScriptsToWire 把存储层的项目脚本转成 wire 类型，包含 UI 编辑需要的全部字段。
func projectScriptsToWire(scripts []ProjectScript) []wire.ScriptsProjectScript {
	out := make([]wire.ScriptsProjectScript, len(scripts))
	for i, ps := range scripts {
		item := wire.ScriptsProjectScript{
			ID:                  ps.ID,
			Name:                ps.Name,
			Command:             ps.Command,
			RunOnWorktreeCreate: ps.RunOnWorktreeCreate,
		}
		if ps.Symbol != "" {
			symbol := ps.Symbol
			item.Symbol = &symbol
		}
		out[i] = item
	}
	return out
}

// ScriptsSave 整表替换某个项目的项目级脚本（与桌面端保存脚本同一套校验）。
// expectedRevision 非空时做乐观锁，与当前内容版本不一致返回 ErrCodeVersionConflict。
// 返回保存后的项目脚本列表与新的 revision；Directory/Runner/PackageScripts 留空，
// 因为保存只以 projectID 为键，不依赖某个会话的工作区。
func (b *MemoryBackend) ScriptsSave(ctx context.Context, p wire.ScriptsSaveParams) (wire.ScriptsSaveResult, error) {
	scripts := make([]ProjectScript, 0, len(p.Scripts))
	for _, sc := range p.Scripts {
		item := ProjectScript{
			ID:                  sc.ID,
			Name:                sc.Name,
			Command:             sc.Command,
			RunOnWorktreeCreate: sc.RunOnWorktreeCreate,
		}
		if sc.Symbol != nil {
			item.Symbol = *sc.Symbol
		}
		scripts = append(scripts, item)
	}
	expected := ""
	if p.ExpectedRevision != nil {
		expected = strings.TrimSpace(*p.ExpectedRevision)
	}
	saved, err := b.SetProjectScriptsByProjectID(p.ProjectID, scripts, expected)
	if err != nil {
		return wire.ScriptsSaveResult{}, err
	}
	return wire.ScriptsSaveResult{
		ProjectScripts: projectScriptsToWire(saved),
		Revision:       projectScriptsRevision(saved),
	}, nil
}

// ScriptsSetFavorite 设置或取消脚本收藏状态并持久化。
func (b *MemoryBackend) ScriptsSetFavorite(ctx context.Context, p wire.ScriptsSetFavoriteParams) (wire.ScriptsSetFavoriteResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.ScriptsSetFavoriteResult{}, err
	}

	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 256 {
		return wire.ScriptsSetFavoriteResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "script name must not be empty and at most 256 characters",
		}
	}

	favs, err := setFavorites(b, root, name, p.IsFavorite)
	if err != nil {
		return wire.ScriptsSetFavoriteResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to save favorite: %v", err),
		}
	}

	if favs == nil {
		favs = make([]string, 0)
	}

	return wire.ScriptsSetFavoriteResult{
		Favorites: favs,
	}, nil
}

// runScriptAsync 在后台把脚本命令在工作区根目录里执行完，结果只写进内存运行记录，
// 由 scripts.runStatus 查询，不再往会话时间线追加任何消息。
// label 用于描述这次运行（package 脚本为“npm run dev”，项目脚本为脚本名称）。
// 返回本次运行的 runID。
func (b *MemoryBackend) runScriptAsync(threadID, root, label string, build func(ctx context.Context) *exec.Cmd) string {
	runID := uuid.New().String()
	b.scriptRuns.put(&scriptRunRecord{
		runID:     runID,
		threadID:  threadID,
		label:     label,
		status:    scriptRunStatusRunning,
		createdAt: time.Now(),
	})

	go func() {
		select {
		case scriptRunSem <- struct{}{}:
			defer func() { <-scriptRunSem }()
		case <-time.After(10 * time.Second):
			b.scriptRuns.update(runID, scriptRunStatusRejected, nil, "concurrency limit reached")
			return
		}

		runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		cmd := build(runCtx)
		cmd.Dir = root
		// GUI 进程的 PATH 缺少登录 Shell 的目录，脚本需要登录环境才能找到 npm/pnpm 等 runner。
		if cmd.Env == nil {
			cmd.Env = loginenv.Environ()
		}
		combined := &cappedOutput{limit: maxScriptOutputBytes}
		cmd.Stdout = combined
		cmd.Stderr = combined
		b.scriptRuns.setLive(runID, combined)

		err := cmd.Run()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		b.scriptRuns.update(runID, scriptRunStatusFinished, &exitCode, combined.String())
	}()

	return runID
}

// ScriptsRunStatus 返回指定脚本运行的当前状态，供 web 端轮询展示。
// 记录不存在，或记录不属于该会话时，一律返回参数错误，不泄露其他会话的运行信息。
func (b *MemoryBackend) ScriptsRunStatus(ctx context.Context, p wire.ScriptsRunStatusParams) (wire.ScriptsRunStatusResult, error) {
	rec, ok := b.scriptRuns.get(p.RunID)
	if !ok || rec.threadID != p.ThreadID {
		return wire.ScriptsRunStatusResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "run not found",
		}
	}

	return wire.ScriptsRunStatusResult{
		RunID:    rec.runID,
		Label:    rec.label,
		Status:   rec.status,
		ExitCode: rec.exitCode,
		Output:   rec.output,
	}, nil
}

// ScriptsRun 触发指定脚本在工作区后台运行。
func (b *MemoryBackend) ScriptsRun(ctx context.Context, p wire.ScriptsRunParams) (wire.ScriptsRunResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.ScriptsRunResult{}, err
	}

	switch p.Kind {
	case "package":
		if p.Name == nil || strings.TrimSpace(*p.Name) == "" {
			return wire.ScriptsRunResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "name is required for package script",
			}
		}
		scriptName := strings.TrimSpace(*p.Name)
		pkgScripts, err := readPackageScripts(root)
		if err != nil {
			return wire.ScriptsRunResult{}, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to read scripts: %v", err),
			}
		}
		found := false
		for _, s := range pkgScripts {
			if s.Name == scriptName {
				found = true
				break
			}
		}
		if !found {
			return wire.ScriptsRunResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("script %s not found in package.json", scriptName),
			}
		}

		runner := detectScriptRunner(root)
		runID := b.runScriptAsync(p.ThreadID, root, runner+" run "+scriptName, func(runCtx context.Context) *exec.Cmd {
			// 先按登录环境的 PATH 解析 runner 的绝对路径，解析失败时退回原名字。
			bin := runner
			if p, err := loginenv.LookPath(runner); err == nil {
				bin = p
			}
			return procattr.HideWindow(exec.CommandContext(runCtx, bin, "run", scriptName))
		})

		return wire.ScriptsRunResult{OK: true, RunID: runID}, nil

	case "project":
		if p.ScriptID == nil || strings.TrimSpace(*p.ScriptID) == "" {
			return wire.ScriptsRunResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "scriptID is required for project script",
			}
		}
		scriptID := strings.TrimSpace(*p.ScriptID)
		projectScripts, err := b.ProjectScripts(p.ThreadID)
		if err != nil {
			return wire.ScriptsRunResult{}, err
		}
		var target *ProjectScript
		for i := range projectScripts {
			if projectScripts[i].ID == scriptID {
				target = &projectScripts[i]
				break
			}
		}
		if target == nil {
			return wire.ScriptsRunResult{}, &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("project script %s not found", scriptID),
			}
		}

		// 项目脚本直接在工作区根目录用 shell 执行用户填写的命令。
		command := target.Command
		runID := b.runScriptAsync(p.ThreadID, root, target.Name, func(runCtx context.Context) *exec.Cmd {
			return procattr.ShellCommand(runCtx, command)
		})

		return wire.ScriptsRunResult{OK: true, RunID: runID}, nil

	default:
		return wire.ScriptsRunResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("unsupported script kind: %s", p.Kind),
		}
	}
}

// --- Dispatcher Handlers ---

func (d *Dispatcher) handleScriptsList(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ScriptsListParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "scripts operations unavailable")
	}
	res, err := mb.ScriptsList(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleScriptsSetFavorite(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ScriptsSetFavoriteParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 256 {
		return replyErr(wire.InvalidParams, "Invalid params: 'name' must be at most 256 characters")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "scripts operations unavailable")
	}
	res, err := mb.ScriptsSetFavorite(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleScriptsSave(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ScriptsSaveParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(strings.TrimSpace(p.ProjectID)) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}
	if len(p.Scripts) > 500 {
		return replyErr(wire.InvalidParams, "Invalid params: 'scripts' must have at most 500 items")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "scripts operations unavailable")
	}
	res, err := mb.ScriptsSave(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleScriptsRun(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ScriptsRunParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if p.Kind != "package" && p.Kind != "project" {
		return replyErr(wire.InvalidParams, "Invalid params: 'kind' must be 'package' or 'project'")
	}
	if p.Kind == "package" {
		if p.Name == nil || strings.TrimSpace(*p.Name) == "" || len(*p.Name) > 256 {
			return replyErr(wire.InvalidParams, "Invalid params: 'name' must be non-empty and at most 256 characters")
		}
	}
	if p.Kind == "project" {
		if p.ScriptID == nil || !isValidUUID(*p.ScriptID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'scriptID' must be a valid UUID")
		}
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "scripts operations unavailable")
	}
	res, err := mb.ScriptsRun(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleScriptsRunStatus(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ScriptsRunStatusParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.RunID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'runID' must be a valid UUID")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "scripts operations unavailable")
	}
	res, err := mb.ScriptsRunStatus(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}
