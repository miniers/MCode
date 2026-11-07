package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/remote/wire"
)

// ThreadOpsBackend 定义 Dispatcher 所需的高级会话与队列操作后端接口。
type ThreadOpsBackend interface {
	MoveThread(ctx context.Context, threadID, direction string) (wire.ThreadsMoveResult, error)
	BranchThread(ctx context.Context, p wire.ThreadsBranchParams) (wire.ThreadsBranchResult, error)
	ForwardThread(ctx context.Context, p wire.ThreadsContinueParams) (wire.ThreadsContinueResult, error)
	CommandScope(ctx context.Context, threadID, projectID, provider string) (projectDir string, resolvedProvider string, err error)
	SettleAction(ctx context.Context, p wire.ThreadsSettleActionParams) (wire.ThreadsSettleActionResult, error)
	QueueList(ctx context.Context, threadID string) (wire.ThreadsQueueSnapshot, error)
	QueueUpdate(ctx context.Context, p wire.ThreadsQueueUpdateParams) (wire.ThreadsQueueUpdateResult, error)
	QueueRemove(ctx context.Context, p wire.ThreadsQueueRemoveParams) (wire.ThreadsQueueRemoveResult, error)
	QueueSendNow(ctx context.Context, p wire.ThreadsQueueSendNowParams) (wire.ThreadsQueueSendNowResult, error)
	QueueMove(ctx context.Context, p wire.ThreadsQueueMoveParams) (wire.ThreadsQueueMoveResult, error)
	SetThreadMCPDisabled(ctx context.Context, threadID string, keys []string) (wire.ThreadSummary, error)
	QueueInterruptAndSend(ctx context.Context, p wire.ThreadsQueueInterruptAndSendParams) (wire.ThreadsQueueInterruptAndSendResult, error)
	SetQueueChangedHook(fn func(wire.ThreadsQueueSnapshot))
}

// BulkThreadsBackend 定义 Dispatcher 所需的批量整理后端接口。
type BulkThreadsBackend interface {
	BulkPreview(ctx context.Context, projectID string, before *time.Time, includePinned, includeDormant bool) (threadIDs []string, skippedRunning int, err error)
	BulkArchive(ctx context.Context, projectID string, threadIDs []string) (token string, skippedRunning int, err error)
	BulkMarkDormant(ctx context.Context, projectID string, threadIDs []string) (token string, skippedRunning int, err error)
	BulkMarkRead(ctx context.Context, projectID string, threadIDs []string) (token string, err error)
	UndoBulkOperation(ctx context.Context, projectID, token string) error
}

// bulkThreads 对 d.backend 进行类型断言，获取 BulkThreadsBackend 接口。
func (d *Dispatcher) bulkThreads() (BulkThreadsBackend, bool) {
	if d.backend == nil {
		return nil, false
	}
	ops, ok := d.backend.(BulkThreadsBackend)
	return ops, ok
}

// threadOps 对 d.backend 进行类型断言，获取 ThreadOpsBackend 接口。
func (d *Dispatcher) threadOps() (ThreadOpsBackend, bool) {
	if d.backend == nil {
		return nil, false
	}
	ops, ok := d.backend.(ThreadOpsBackend)
	return ops, ok
}

// cachedThreadAction 保存 actionID 对应的已执行操作及成功结果。
type cachedThreadAction struct {
	method  string
	payload string
	result  any
	at      time.Time
}

const (
	actionCacheTTL = 10 * time.Minute
	actionCacheMax = 256
)

// checkActionCache 查询 actionID 对应的幂等缓存。
// 返回 (result, conflict, hit)：
// - conflict: 同一 actionID 存在有效记录但 method 或 payload 不一致
// - hit: 完全匹配，直接复用缓存结果
func (d *Dispatcher) checkActionCache(actionID, method, payload string) (any, bool, bool) {
	d.actionCacheMu.Lock()
	defer d.actionCacheMu.Unlock()

	entry, exists := d.actionCache[actionID]
	if !exists {
		return nil, false, false
	}
	if time.Since(entry.at) > actionCacheTTL {
		delete(d.actionCache, actionID)
		return nil, false, false
	}
	if entry.method != method || entry.payload != payload {
		return nil, true, false
	}
	return entry.result, false, true
}

// saveActionCache 将成功的操作结果存入幂等缓存，条目超出上限时淘汰最旧条目。
func (d *Dispatcher) saveActionCache(actionID, method, payload string, result any) {
	d.actionCacheMu.Lock()
	defer d.actionCacheMu.Unlock()

	if len(d.actionCache) >= actionCacheMax {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, v := range d.actionCache {
			if first || v.at.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.at
				first = false
			}
		}
		if oldestKey != "" {
			delete(d.actionCache, oldestKey)
		}
	}

	d.actionCache[actionID] = cachedThreadAction{
		method:  method,
		payload: payload,
		result:  result,
		at:      time.Now(),
	}
}

// notifyQueueChanged 广播会话跟进队列变更通知给所有已认证连接。
func (d *Dispatcher) notifyQueueChanged(snap wire.ThreadsQueueSnapshot) {
	d.authedMu.RLock()
	conns := make([]Conn, 0, len(d.authedConns))
	for c := range d.authedConns {
		conns = append(conns, c)
	}
	d.authedMu.RUnlock()

	for _, c := range conns {
		_ = c.Notify("threads.queue.changed", snap)
	}
}

// scanSkills 是技能扫描函数的包级注入点，便于测试 Mock。
var scanSkills = agentconf.ScanSkills

// agentConfDataDirProvider 是可选接口：后端实现它时，技能扫描会带上数据目录，
// 从而读到与设置页同一份技能停用配置。
type agentConfDataDirProvider interface {
	AgentConfDataDir() string
}

func (d *Dispatcher) handleThreadsMove(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsMoveParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if p.Direction != "up" && p.Direction != "down" {
		return replyErr(wire.InvalidParams, "Invalid params: 'direction' must be 'up' or 'down'")
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.MoveThread(ctx, p.ThreadID, p.Direction)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsBranch(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsBranchParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.MessageID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'messageID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.branch", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.BranchThread(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	d.saveActionCache(p.ActionID, "threads.branch", payload, res)
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsContinue(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsContinueParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.continue", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.ForwardThread(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	d.saveActionCache(p.ActionID, "threads.continue", payload, res)
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsCommands(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsCommandsParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}

	hasThread := p.ThreadID != nil && strings.TrimSpace(*p.ThreadID) != ""
	hasProject := p.ProjectID != nil && strings.TrimSpace(*p.ProjectID) != ""
	hasProvider := p.Provider != nil && strings.TrimSpace(*p.Provider) != ""

	if hasThread {
		if hasProject || hasProvider {
			return replyErr(wire.InvalidParams, "Invalid params: provide either 'threadID' or ('projectID' and 'provider'), not both")
		}
		if !isValidUUID(*p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
		}
	} else {
		if !hasProject || !hasProvider {
			return replyErr(wire.InvalidParams, "Invalid params: must provide either 'threadID' or both 'projectID' and 'provider'")
		}
		if !isValidUUID(*p.ProjectID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
		}
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}

	res, err := ThreadCommandsFromOps(ctx, ops, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

// ThreadCommands 查询指定会话或项目的可用命令列表（内置命令与技能）。
func (b *MemoryBackend) ThreadCommands(ctx context.Context, p wire.ThreadsCommandsParams) (wire.ThreadsCommandsResult, error) {
	return ThreadCommandsFromOps(ctx, b, p)
}

// slashCatalogEntry 定义内置斜杠命令目录项。
type slashCatalogEntry struct {
	Name      string
	Detail    string
	Providers []string // 为空表示适用于所有 provider
}

// builtinSlashCatalog 内置斜杠命令包级目录。
// 每项包含 Name、Detail（英文短说明）、适用的 provider 集合（为空表示全部），
// Kind 统一为 "builtin"、IsBuiltIn 为 true。
var builtinSlashCatalog = []slashCatalogEntry{
	{Name: "mode", Detail: "Toggle plan mode", Providers: nil},
	{Name: "plan", Detail: "Turn plan mode on or off", Providers: nil},
	{Name: "compact", Detail: "Summarize the conversation to free up context", Providers: []string{"claude", "codex"}},
	{Name: "clear", Detail: "Clear conversation history", Providers: nil},
	{Name: "model", Detail: "Switch the active language model", Providers: nil},
	{Name: "effort", Detail: "Set reasoning effort", Providers: nil},
	{Name: "fast", Detail: "Toggle fast mode", Providers: nil},
	{Name: "pair", Detail: "Configure orchestration pair", Providers: nil},
	{Name: "export", Detail: "Export current conversation transcript", Providers: nil},
}

// builtinCommandsForProvider 根据 provider 过滤内置命令目录并按目录顺序返回。
func builtinCommandsForProvider(provider string) []wire.RemoteCommand {
	var cmds []wire.RemoteCommand
	for _, entry := range builtinSlashCatalog {
		if len(entry.Providers) > 0 {
			matched := false
			for _, p := range entry.Providers {
				if p == provider {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		cmds = append(cmds, wire.RemoteCommand{
			Name:      entry.Name,
			Detail:    entry.Detail,
			IsBuiltIn: true,
			Kind:      "builtin",
		})
	}
	return cmds
}

// providerSlashCommands 返回特定 provider 专有的斜杠命令列表。
// 注意：docs/remote-control.schema.json 中 RemoteCommand.kind 枚举仅允许 "builtin"、"command"、"skill"，
// 不支持 "provider"，因此 Kind 采用 schema 合法的 "command"，IsBuiltIn 设为 false。
func providerSlashCommands(provider string) []wire.RemoteCommand {
	switch provider {
	case "claude":
		return []wire.RemoteCommand{
			{Name: "review", Detail: "Review working tree diff and code changes", IsBuiltIn: false, Kind: "command"},
			{Name: "pr_comments", Detail: "Review pull request comments", IsBuiltIn: false, Kind: "command"},
			{Name: "bug", Detail: "Report and fix a bug", IsBuiltIn: false, Kind: "command"},
			{Name: "init", Detail: "Initialize project configuration", IsBuiltIn: false, Kind: "command"},
			{Name: "doctor", Detail: "Check and diagnose system health", IsBuiltIn: false, Kind: "command"},
		}
	case "agy", "antigravity":
		// plan 与内置目录同名，去重后实际输出仍为 builtin 版本，此处仅与 provider 目录对齐。
		return []wire.RemoteCommand{
			{Name: "plan", Detail: "Plan an implementation, breakdown, or architecture", IsBuiltIn: false, Kind: "command"},
			{Name: "goal", Detail: "Define high-level objectives and constraints", IsBuiltIn: false, Kind: "command"},
			{Name: "browser", Detail: "Navigate or interact with web resources", IsBuiltIn: false, Kind: "command"},
			{Name: "grill-me", Detail: "Stress-test assumptions and critique implementation proposals", IsBuiltIn: false, Kind: "command"},
			{Name: "boost", Detail: "Boost reasoning depth and exhaustive verification", IsBuiltIn: false, Kind: "command"},
			{Name: "learn", Detail: "Extract learnings, conventions, and workspace guidelines", IsBuiltIn: false, Kind: "command"},
			{Name: "schedule", Detail: "Schedule recurring or delayed tasks", IsBuiltIn: false, Kind: "command"},
			{Name: "teamwork-preview", Detail: "Preview team of autonomous agents working together", IsBuiltIn: false, Kind: "command"},
			{Name: "help", Detail: "Display available Antigravity slash commands and guidance", IsBuiltIn: false, Kind: "command"},
		}
	case "cursor":
		return []wire.RemoteCommand{
			{Name: "review", Detail: "Review working tree diff and git status", IsBuiltIn: false, Kind: "command"},
			{Name: "commit", Detail: "Draft a commit message for current changes", IsBuiltIn: false, Kind: "command"},
			{Name: "test", Detail: "Run automated tests for the project", IsBuiltIn: false, Kind: "command"},
			{Name: "copy-request-id", Detail: "Copy the current request ID", IsBuiltIn: false, Kind: "command"},
		}
	case "opencode", "opencodeGo":
		return []wire.RemoteCommand{
			{Name: "review", Detail: "Review uncommitted changes in current repository", IsBuiltIn: false, Kind: "command"},
			{Name: "commit", Detail: "Draft and generate a commit message for staged changes", IsBuiltIn: false, Kind: "command"},
			{Name: "test", Detail: "Run project tests", IsBuiltIn: false, Kind: "command"},
			{Name: "fix", Detail: "Fix issues or compiler errors", IsBuiltIn: false, Kind: "command"},
			{Name: "docs", Detail: "Generate code documentation", IsBuiltIn: false, Kind: "command"},
			{Name: "init", Detail: "Initialize project configuration", IsBuiltIn: false, Kind: "command"},
		}
	case "pi", "mu":
		// model、export 与内置目录同名，去重后实际输出仍为 builtin 版本；compact 仅 Pi 独有，会实际生效。
		return []wire.RemoteCommand{
			{Name: "model", Detail: "Switch the active language model", IsBuiltIn: false, Kind: "command"},
			{Name: "compact", Detail: "Compact the active conversation", IsBuiltIn: false, Kind: "command"},
			{Name: "export", Detail: "Export current conversation transcript", IsBuiltIn: false, Kind: "command"},
			{Name: "reload", Detail: "Reload Pi extensions and skills", IsBuiltIn: false, Kind: "command"},
			{Name: "login", Detail: "Log in to Pi or providers", IsBuiltIn: false, Kind: "command"},
			{Name: "logout", Detail: "Log out of active Pi account", IsBuiltIn: false, Kind: "command"},
			{Name: "help", Detail: "Show Pi help and commands", IsBuiltIn: false, Kind: "command"},
		}
	case "grok":
		return []wire.RemoteCommand{
			{Name: "think", Detail: "Engage deep reasoning on complex problems", IsBuiltIn: false, Kind: "command"},
			{Name: "review", Detail: "Review working tree and branch changes", IsBuiltIn: false, Kind: "command"},
			{Name: "test", Detail: "Execute test suite", IsBuiltIn: false, Kind: "command"},
		}
	case "commandcode":
		return []wire.RemoteCommand{
			{Name: "review", Detail: "Review working tree diff", IsBuiltIn: false, Kind: "command"},
			{Name: "commit", Detail: "Generate commit for working tree changes", IsBuiltIn: false, Kind: "command"},
			{Name: "test", Detail: "Run test suite", IsBuiltIn: false, Kind: "command"},
		}
	case "cline":
		return []wire.RemoteCommand{
			{Name: "review", Detail: "Review uncommitted changes", IsBuiltIn: false, Kind: "command"},
			{Name: "test", Detail: "Run tests", IsBuiltIn: false, Kind: "command"},
			{Name: "commit", Detail: "Draft git commit", IsBuiltIn: false, Kind: "command"},
		}
	case "devin":
		return []wire.RemoteCommand{
			{Name: "review", Detail: "Review current changes", IsBuiltIn: false, Kind: "command"},
			{Name: "test", Detail: "Run tests", IsBuiltIn: false, Kind: "command"},
		}
	case "codex":
		return nil
	default:
		return nil
	}
}

// isPluginSkill 判断技能是否为 Claude Code 插件技能：只有 claude 侧才有插件，
// 命令名按 Claude Code 的斜杠格式带插件前缀（插件名:技能名）。
func isPluginSkill(resolvedProvider string, sk agentconf.Skill) bool {
	return resolvedProvider == "claude" && sk.Scope == "external" && sk.Plugin != ""
}

// ThreadCommandsFromOps 根据 ThreadOpsBackend 查询并组装可用命令列表（内置命令、Provider 专有命令与技能）。
func ThreadCommandsFromOps(ctx context.Context, ops ThreadOpsBackend, p wire.ThreadsCommandsParams) (wire.ThreadsCommandsResult, error) {
	var thID, prjID, prov string
	if p.ThreadID != nil {
		thID = *p.ThreadID
	}
	if p.ProjectID != nil {
		prjID = *p.ProjectID
	}
	if p.Provider != nil {
		prov = *p.Provider
	}

	projectDir, resolvedProvider, err := ops.CommandScope(ctx, thID, prjID, prov)
	if err != nil {
		return wire.ThreadsCommandsResult{}, err
	}

	// 1. 内置命令（根据 resolvedProvider 过滤，按目录顺序输出）
	builtin := builtinCommandsForProvider(resolvedProvider)

	// 2. Provider 专有命令
	providerCmdsRaw := providerSlashCommands(resolvedProvider)

	seenNames := make(map[string]bool)
	for _, b := range builtin {
		seenNames[b.Name] = true
	}

	var providerCmds []wire.RemoteCommand
	for _, pc := range providerCmdsRaw {
		if !seenNames[pc.Name] {
			seenNames[pc.Name] = true
			providerCmds = append(providerCmds, pc)
		}
	}

	baseCmds := append(builtin, providerCmds...)

	// 技能停用状态与设置页同源：数据目录由后端提供（可选接口），Home 取当前用户目录。
	scanOpts := agentconf.Options{ProjectDir: projectDir}
	if dataDirProvider, ok := ops.(agentConfDataDirProvider); ok {
		scanOpts.DataDir = dataDirProvider.AgentConfDataDir()
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		scanOpts.Home = home
	}

	skills, scanErr := scanSkills(scanOpts)
	if scanErr != nil {
		errMsg := "命令列表加载失败，已显示内置命令"
		return wire.ThreadsCommandsResult{
			Commands: baseCmds,
			Recent:   []string{},
			Loading:  false,
			Error:    &errMsg,
		}, nil
	}

	// 按命令名去重（project 作用域优先于 global，内置命令与 provider 专有命令优先于 skill）。
	// Claude Code 插件技能的命令名是「插件名:技能名」，与普通技能同处一个命名空间。
	skillsByName := make(map[string]agentconf.Skill)
	for _, sk := range skills {
		if sk.Disabled {
			continue
		}
		cmdName := sk.Name
		pluginSkill := isPluginSkill(resolvedProvider, sk)
		if pluginSkill {
			cmdName = sk.Plugin + ":" + sk.Name
		} else if sk.Agent != resolvedProvider {
			continue
		}
		if seenNames[cmdName] {
			continue
		}
		existing, exists := skillsByName[cmdName]
		if !exists {
			skillsByName[cmdName] = sk
		} else if !pluginSkill && existing.Scope != "project" && sk.Scope == "project" {
			skillsByName[cmdName] = sk
		}
	}

	skillCmds := make([]wire.RemoteCommand, 0, len(skillsByName))
	for cmdName, sk := range skillsByName {
		desc := sk.Description
		r := []rune(desc)
		if len(r) > 300 {
			desc = string(r[:300])
		}
		skillCmds = append(skillCmds, wire.RemoteCommand{
			Name:      cmdName,
			Detail:    desc,
			IsBuiltIn: false,
			Kind:      "skill",
		})
	}

	// skill 按名称排序
	sort.Slice(skillCmds, func(i, j int) bool {
		return skillCmds[i].Name < skillCmds[j].Name
	})

	// 组合顺序：内置命令 -> provider 专有命令 -> 技能
	finalCommands := append(baseCmds, skillCmds...)

	return wire.ThreadsCommandsResult{
		Commands: finalCommands,
		Recent:   []string{},
		Loading:  false,
		Error:    nil,
	}, nil
}

func (d *Dispatcher) handleThreadsSettleAction(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsSettleActionParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionJournalID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionJournalID' cannot be empty")
	}
	hasProj := p.ProjectID != nil && strings.TrimSpace(*p.ProjectID) != ""
	hasThread := p.ThreadID != nil && strings.TrimSpace(*p.ThreadID) != ""
	if !hasProj && !hasThread {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' or 'threadID' is required")
	}
	if hasProj && !isValidUUID(*p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}
	if hasThread && !isValidUUID(*p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.SettleAction(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsQueueList(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsQueueListParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.QueueList(ctx, p.ThreadID)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsQueueUpdate(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsQueueUpdateParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.FollowUpID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'followUpID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}
	if len(p.Attachments) > 0 {
		return replyErr(wire.ErrCodeOperationRejected, "attachments are not supported")
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.queue.update", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.QueueUpdate(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	d.saveActionCache(p.ActionID, "threads.queue.update", payload, res)
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsQueueRemove(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsQueueRemoveParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.FollowUpID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'followUpID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.queue.remove", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.QueueRemove(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	d.saveActionCache(p.ActionID, "threads.queue.remove", payload, res)
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsQueueSendNow(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsQueueSendNowParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.FollowUpID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'followUpID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.queue.sendNow", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.QueueSendNow(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	d.saveActionCache(p.ActionID, "threads.queue.sendNow", payload, res)
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsQueueMove(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsQueueMoveParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.FollowUpID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'followUpID' must be a valid UUID")
	}
	if p.BeforeFollowUpID != "" && !isValidUUID(p.BeforeFollowUpID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'beforeFollowUpID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.queue.move", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.QueueMove(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	d.saveActionCache(p.ActionID, "threads.queue.move", payload, res)
	return replySuccess(res)
}

// handleThreadsMCPDisabledSet 保存会话级外部 MCP 禁用名单，并在成功后广播会话摘要。
func (d *Dispatcher) handleThreadsMCPDisabledSet(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsMCPDisabledSetParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}
	for _, key := range p.Keys {
		if strings.TrimSpace(key) == "" {
			return replyErr(wire.InvalidParams, "Invalid params: 'keys' must not contain empty values")
		}
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.mcpDisabled.set", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	summary, err := ops.SetThreadMCPDisabled(ctx, p.ThreadID, p.Keys)
	if err != nil {
		return replyWithBackendErr(err)
	}
	res := wire.ThreadsMCPDisabledSetResult{ThreadID: p.ThreadID, Keys: summary.MCPDisabled}
	d.saveActionCache(p.ActionID, "threads.mcpDisabled.set", payload, res)
	d.broadcastThreadsChanged(wire.ThreadsChanged{Threads: []wire.ThreadSummary{summary}})
	return replySuccess(res)
}

func (d *Dispatcher) handleThreadsQueueInterruptAndSend(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsQueueInterruptAndSendParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}
	hasText := p.Text != nil && strings.TrimSpace(*p.Text) != ""
	hasAttachments := len(p.Attachments) > 0
	if !hasText && !hasAttachments {
		return replyErr(wire.InvalidParams, "Invalid params: 'text' or 'attachments' is required")
	}
	if hasAttachments {
		return replyErr(wire.ErrCodeOperationRejected, "attachments are not supported")
	}

	payloadBytes, _ := json.Marshal(p)
	payload := string(payloadBytes)
	if cached, conflict, hit := d.checkActionCache(p.ActionID, "threads.queue.interruptAndSend", payload); conflict {
		return replyErr(wire.ErrCodeOperationRejected, "actionID reused with different payload")
	} else if hit {
		return replySuccess(cached)
	}

	ops, ok := d.threadOps()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "thread operations unavailable")
	}
	res, err := ops.QueueInterruptAndSend(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	d.saveActionCache(p.ActionID, "threads.queue.interruptAndSend", payload, res)
	return replySuccess(res)
}

func mapBulkBackendErr(err error, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	if errors.Is(err, ErrBulkUndoExpired) {
		return replyErr(wire.InvalidParams, "批量操作撤销已超时")
	}
	if errors.Is(err, ErrBulkUndoNotFound) {
		return replyErr(wire.InvalidParams, "批量操作撤销记录不存在或项目不匹配")
	}
	return replyWithBackendErr(err)
}

func (d *Dispatcher) handleThreadsBulkPreview(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsBulkPreviewParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if strings.TrimSpace(p.ProjectID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' is required")
	}
	if !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}

	ops, ok := d.bulkThreads()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "bulk operations unavailable")
	}
	threadIDs, skipped, err := ops.BulkPreview(ctx, p.ProjectID, p.Before, p.IncludePinned, p.IncludeDormant)
	if err != nil {
		return mapBulkBackendErr(err, replyErr, replyWithBackendErr)
	}
	return replySuccess(wire.ThreadsBulkPreviewResult{
		ThreadIDs:      threadIDs,
		SkippedRunning: skipped,
	})
}

func (d *Dispatcher) handleThreadsBulkArchive(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsBulkArchiveParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if strings.TrimSpace(p.ProjectID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' is required")
	}
	if !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}

	ops, ok := d.bulkThreads()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "bulk operations unavailable")
	}
	token, skipped, err := ops.BulkArchive(ctx, p.ProjectID, p.ThreadIDs)
	if err != nil {
		return mapBulkBackendErr(err, replyErr, replyWithBackendErr)
	}
	return replySuccess(wire.ThreadsBulkArchiveResult{
		UndoToken:      token,
		SkippedRunning: skipped,
		ThreadIDs:      p.ThreadIDs,
	})
}

func (d *Dispatcher) handleThreadsBulkMarkDormant(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsBulkMarkDormantParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if strings.TrimSpace(p.ProjectID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' is required")
	}
	if !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}

	ops, ok := d.bulkThreads()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "bulk operations unavailable")
	}
	token, skipped, err := ops.BulkMarkDormant(ctx, p.ProjectID, p.ThreadIDs)
	if err != nil {
		return mapBulkBackendErr(err, replyErr, replyWithBackendErr)
	}
	return replySuccess(wire.ThreadsBulkMarkDormantResult{
		UndoToken:      token,
		SkippedRunning: skipped,
		ThreadIDs:      p.ThreadIDs,
	})
}

func (d *Dispatcher) handleThreadsBulkMarkRead(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsBulkMarkReadParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if strings.TrimSpace(p.ProjectID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' is required")
	}
	if !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}

	ops, ok := d.bulkThreads()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "bulk operations unavailable")
	}
	token, err := ops.BulkMarkRead(ctx, p.ProjectID, p.ThreadIDs)
	if err != nil {
		return mapBulkBackendErr(err, replyErr, replyWithBackendErr)
	}
	return replySuccess(wire.ThreadsBulkMarkReadResult{
		UndoToken: token,
		ThreadIDs: p.ThreadIDs,
	})
}

func (d *Dispatcher) handleThreadsBulkUndo(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadsBulkUndoParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if strings.TrimSpace(p.ProjectID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' is required")
	}
	if !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}
	if strings.TrimSpace(p.Token) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'token' cannot be empty")
	}

	ops, ok := d.bulkThreads()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "bulk operations unavailable")
	}
	if err := ops.UndoBulkOperation(ctx, p.ProjectID, p.Token); err != nil {
		return mapBulkBackendErr(err, replyErr, replyWithBackendErr)
	}
	return replySuccess(wire.ThreadsBulkUndoResult{
		OK: true,
	})
}

// TitleRegenerator 重新生成会话标题的回调，返回写回后的新标题。
type TitleRegenerator func(ctx context.Context, threadID string) (string, error)

// DefaultTitleRegenerator 是未通过 SetTitleRegenerator 注入时的全局回退，由 app 层在启动时设置。
var DefaultTitleRegenerator TitleRegenerator

// SetTitleRegenerator 注入 threads.title.regenerate 使用的标题重生成回调。
func (d *Dispatcher) SetTitleRegenerator(fn func(ctx context.Context, threadID string) (string, error)) {
	d.titleRegenerator = fn
}

type threadsTitleRegenerateParams struct {
	ThreadID string `json:"threadId"`
}

type threadsTitleRegenerateResult struct {
	Title string `json:"title"`
}

// handleThreadsTitleRegenerate 处理 threads.title.regenerate。
func (d *Dispatcher) handleThreadsTitleRegenerate(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p threadsTitleRegenerateParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if strings.TrimSpace(p.ThreadID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadId' is required")
	}
	fn := d.titleRegenerator
	if fn == nil {
		fn = DefaultTitleRegenerator
	}
	if fn == nil {
		return replyErr(wire.ErrCodeOperationRejected, "title regeneration unavailable")
	}
	title, err := fn(ctx, p.ThreadID)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(threadsTitleRegenerateResult{Title: title})
}
