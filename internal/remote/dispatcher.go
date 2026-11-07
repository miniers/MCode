package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// Dispatcher 负责 JSON-RPC 2.0 请求解析、鉴权校验、参数严格检验与方法路由分发。
type Dispatcher struct {
	auth                  Authenticator
	backend               Backend
	methods               map[string]bool
	kanban                KanbanService
	kanbanActions         *actionLedger
	swarm                 SwarmService
	swarmExtras           SwarmRemoteExtras
	push                  *WebPushService
	removeThreadsListener func()
	swarmNotifyMu         sync.Mutex
	swarmNotifyTimers     map[string]*time.Timer
	swarmNotifyInterval   time.Duration
	titleRegenerator      func(ctx context.Context, threadID string) (string, error)
	authedMu              sync.RWMutex
	authedConns           map[Conn]struct{}
	actionCacheMu         sync.Mutex
	actionCache           map[string]cachedThreadAction
	threadsRevision       atomic.Int64
	threadChangesMu       sync.Mutex
	threadSnapshots       map[string]string
	stopThreadObserver    func()
}

// VAPIDSubjectFor 生成 VAPID 的 sub 声明：有 TLS 域名时用 https://域名，
// 否则退到本机入口地址，保证升级为 https-only 的推送服务仍接受请求。
func VAPIDSubjectFor(tlsDomain string, ln net.Listener) string {
	if d := strings.TrimSpace(tlsDomain); d != "" {
		return "https://" + d
	}
	if ln != nil {
		if addr, ok := ln.Addr().(*net.TCPAddr); ok && !addr.IP.IsUnspecified() {
			return "https://" + net.JoinHostPort(addr.IP.String(), strconv.Itoa(addr.Port))
		}
	}
	return defaultVAPIDSubject
}

// SetPush 设置 Dispatcher 使用的 WebPush 服务实例。
func (d *Dispatcher) SetPush(p *WebPushService) {
	d.push = p
	if p != nil && d.backend != nil {
		// 出错结束的会话只在 threads.changed 里体现，监听它补一条错误推送。
		if listener, ok := d.backend.(interface {
			AddThreadsChangedListener(func(wire.ThreadsChanged)) func()
		}); ok {
			d.removeThreadsListener = listener.AddThreadsChangedListener(func(tc wire.ThreadsChanged) {
				p.HandleThreadsChanged(tc.Threads)
			})
		}
	}
}

// NewDispatcher 构建并初始化 Dispatcher。
func NewDispatcher(auth Authenticator, backend Backend) *Dispatcher {
	// 内置方法与扩展方法都要进路由表，否则扩展方法会先被「Method not implemented」拦掉。
	extensions := extensionMethodNames()
	m := make(map[string]bool, len(implementedMethods)+len(extensions))
	for _, name := range implementedMethods {
		m[name] = true
	}
	for _, name := range extensions {
		m[name] = true
	}
	d := &Dispatcher{
		auth:                auth,
		backend:             backend,
		methods:             m,
		kanbanActions:       newActionLedger(nil),
		swarmNotifyTimers:   make(map[string]*time.Timer),
		swarmNotifyInterval: time.Second,
		authedConns:         make(map[Conn]struct{}),
		actionCache:         make(map[string]cachedThreadAction),
	}
	if ops, ok := backend.(ThreadOpsBackend); ok {
		ops.SetQueueChangedHook(d.notifyQueueChanged)
	}
	if notifier, ok := backend.(interface {
		SetThreadsChangedHook(func(wire.ThreadsChanged))
	}); ok {
		notifier.SetThreadsChangedHook(d.broadcastThreadsChanged)
	}
	return d
}

// IsSubscribed 返回连接当前是否订阅了某个会话（实现 Server 在线判定所需的可选接口）。
func (d *Dispatcher) IsSubscribed(c Conn) bool {
	source, ok := d.backend.(interface{ Hub() *StreamHub })
	if !ok || source.Hub() == nil {
		return false
	}
	return source.Hub().IsSubscribed(c)
}

// listProviders 返回 provider 目录。后端支持项目维度时透传 projectID（用于补齐草稿默认值），
// 否则退回全局列表，保持与未实现该可选接口的后端兼容。
func (d *Dispatcher) listProviders(ctx context.Context, projectID *string) (wire.ProvidersListResult, error) {
	if scoped, ok := d.backend.(interface {
		ListProvidersForProject(context.Context, *string) (wire.ProvidersListResult, error)
	}); ok {
		return scoped.ListProvidersForProject(ctx, projectID)
	}
	return d.backend.ListProviders(ctx)
}

// RemoveConn 通知后端移除指定连接的所有会话订阅并从已认证连接集合中移除。
func (d *Dispatcher) RemoveConn(c Conn) {
	d.authedMu.Lock()
	delete(d.authedConns, c)
	if len(d.authedConns) == 0 && d.stopThreadObserver != nil {
		d.stopThreadObserver()
		d.stopThreadObserver = nil
	}
	d.authedMu.Unlock()
	if d.backend != nil {
		d.backend.RemoveConn(c)
	}
}

func (d *Dispatcher) addAuthedConn(c Conn) {
	if c == nil {
		return
	}
	d.authedMu.Lock()
	d.authedConns[c] = struct{}{}
	if d.stopThreadObserver == nil {
		if source, ok := d.backend.(interface{ Hub() *StreamHub }); ok && source.Hub() != nil {
			d.stopThreadObserver = source.Hub().AddObserver(func(_ string, method string, params any) {
				if summary, ok := params.(wire.ThreadSummary); method == "thread.summary" && ok {
					d.broadcastThreadsChanged(wire.ThreadsChanged{Threads: []wire.ThreadSummary{summary}})
				}
			})
		}
	}
	d.authedMu.Unlock()
}

// broadcastThreadsChanged 向所有已认证连接广播会话变动通知 threads.changed。
// 发送失败的连接将被移除。
func (d *Dispatcher) broadcastThreadsChanged(params wire.ThreadsChanged) {
	d.threadChangesMu.Lock()
	defer d.threadChangesMu.Unlock()
	if d.threadSnapshots == nil {
		d.threadSnapshots = make(map[string]string)
	}
	changed := make([]wire.ThreadSummary, 0, len(params.Threads))
	for _, thread := range params.Threads {
		snapshotThread := thread
		if thread.IsRunning {
			// 流式文本已有 threads.event；列表仅在运行状态或元数据变化时通知。
			snapshotThread.UpdatedAt = ""
			snapshotThread.LastActivityText = nil
		}
		raw, err := json.Marshal(snapshotThread)
		if err != nil {
			continue
		}
		snapshot := string(raw)
		if d.threadSnapshots[thread.ID] != snapshot {
			d.threadSnapshots[thread.ID] = snapshot
			changed = append(changed, thread)
		}
	}
	for _, id := range params.RemovedThreadIDs {
		delete(d.threadSnapshots, id)
	}
	if len(changed) == 0 && len(params.RemovedThreadIDs) == 0 {
		return
	}
	params.Threads = changed
	if params.Threads == nil {
		params.Threads = []wire.ThreadSummary{}
	}
	if params.RemovedThreadIDs == nil {
		params.RemovedThreadIDs = []string{}
	}
	if params.Revision <= 0 {
		params.Revision = int(d.threadsRevision.Add(1))
	}

	d.authedMu.RLock()
	conns := make([]Conn, 0, len(d.authedConns))
	for c := range d.authedConns {
		conns = append(conns, c)
	}
	d.authedMu.RUnlock()

	var failed []Conn
	for _, c := range conns {
		if !c.Info().Authenticated {
			failed = append(failed, c)
			continue
		}
		if err := c.Notify("threads.changed", params); err != nil {
			failed = append(failed, c)
		}
	}
	for _, c := range failed {
		d.RemoveConn(c)
	}
}

// NotifyAuthed 向所有已认证连接广播一条 JSON-RPC 通知。
// 写法与 threads.changed 广播一致：推送失败的连接会从已认证集合里移除。
func (d *Dispatcher) NotifyAuthed(method string, params any) {
	d.authedMu.RLock()
	conns := make([]Conn, 0, len(d.authedConns))
	for c := range d.authedConns {
		conns = append(conns, c)
	}
	d.authedMu.RUnlock()

	var failed []Conn
	for _, c := range conns {
		if !c.Info().Authenticated {
			failed = append(failed, c)
			continue
		}
		if err := c.Notify(method, params); err != nil {
			failed = append(failed, c)
		}
	}
	for _, c := range failed {
		d.RemoveConn(c)
	}
}

// Handle 处理单条或批量 JSON-RPC 2.0 请求，返回序列化后的响应字节流。
// 若所有请求均为 notification（无 id）则返回 nil。
func (d *Dispatcher) Handle(ctx context.Context, c Conn, raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return marshalEnvelope(wire.Response{
			JSONRPC: "2.0",
			Error:   &wire.RPCError{Code: wire.ParseError, Message: "Parse error"},
			ID:      json.RawMessage("null"),
		})
	}

	// 批处理判定：以 '[' 开头
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return marshalEnvelope(wire.Response{
				JSONRPC: "2.0",
				Error:   &wire.RPCError{Code: wire.ParseError, Message: "Parse error"},
				ID:      json.RawMessage("null"),
			})
		}
		if len(batch) == 0 || len(batch) > 64 {
			return marshalEnvelope(wire.Response{
				JSONRPC: "2.0",
				Error:   &wire.RPCError{Code: wire.InvalidRequest, Message: "Invalid Request: Batch must contain 1 to 64 requests"},
				ID:      json.RawMessage("null"),
			})
		}
		var responses []*wire.Response
		for _, item := range batch {
			if resp := d.handleSingle(ctx, c, item); resp != nil {
				responses = append(responses, resp)
			}
		}
		if len(responses) == 0 {
			return nil
		}
		out, _ := json.Marshal(responses)
		return out
	}

	// 单请求判定：以 '{' 开头
	if trimmed[0] == '{' {
		resp := d.handleSingle(ctx, c, trimmed)
		if resp == nil {
			return nil
		}
		return marshalEnvelope(*resp)
	}

	// 既非对象也非数组
	return marshalEnvelope(wire.Response{
		JSONRPC: "2.0",
		Error:   &wire.RPCError{Code: wire.InvalidRequest, Message: "Invalid Request: Expected object or batch array"},
		ID:      json.RawMessage("null"),
	})
}

func (d *Dispatcher) handleSingle(ctx context.Context, c Conn, raw []byte) *wire.Response {
	if !json.Valid(raw) {
		return &wire.Response{
			JSONRPC: "2.0",
			Error:   &wire.RPCError{Code: wire.ParseError, Message: "Parse error"},
			ID:      json.RawMessage("null"),
		}
	}

	var env struct {
		JSONRPC *string          `json:"jsonrpc"`
		Method  *string          `json:"method"`
		Params  json.RawMessage  `json:"params"`
		ID      *json.RawMessage `json:"id"`
	}

	if err := json.Unmarshal(raw, &env); err != nil {
		return &wire.Response{
			JSONRPC: "2.0",
			Error:   &wire.RPCError{Code: wire.InvalidRequest, Message: "Invalid Request: Request must be a JSON object"},
			ID:      json.RawMessage("null"),
		}
	}

	isNotification := env.ID == nil
	var reqID json.RawMessage
	if !isNotification {
		reqID = *env.ID
		trimmedID := bytes.TrimSpace(reqID)
		if len(trimmedID) > 0 && (trimmedID[0] == '{' || trimmedID[0] == '[') {
			return &wire.Response{
				JSONRPC: "2.0",
				Error:   &wire.RPCError{Code: wire.InvalidRequest, Message: "Invalid request id"},
				ID:      json.RawMessage("null"),
			}
		}
	}

	replyErr := func(code int, msg string) *wire.Response {
		if isNotification {
			return nil
		}
		return &wire.Response{
			JSONRPC: "2.0",
			Error:   &wire.RPCError{Code: code, Message: RedactSecrets(msg)},
			ID:      reqID,
		}
	}

	replySuccess := func(result any) *wire.Response {
		if isNotification {
			return nil
		}
		return &wire.Response{
			JSONRPC: "2.0",
			Result:  result,
			ID:      reqID,
		}
	}

	replyWithBackendErr := func(err error) *wire.Response {
		if isNotification {
			return nil
		}
		var rpcErr *wire.RPCError
		if errors.As(err, &rpcErr) {
			redacted := *rpcErr
			redacted.Message = RedactSecrets(redacted.Message)
			return &wire.Response{
				JSONRPC: "2.0",
				Error:   &redacted,
				ID:      reqID,
			}
		}
		return &wire.Response{
			JSONRPC: "2.0",
			Error: &wire.RPCError{
				Code:    wire.InternalError,
				Message: RedactSecrets(err.Error()),
			},
			ID: reqID,
		}
	}

	if env.JSONRPC == nil || *env.JSONRPC != "2.0" {
		return replyErr(wire.InvalidRequest, "Invalid Request: 'jsonrpc' must be '2.0'")
	}
	if env.Method == nil {
		return replyErr(wire.InvalidRequest, "Invalid Request: 'method' must be a string")
	}

	method := *env.Method

	// params 类型校验：若存在，必须为 object 或 array
	trimmedParams := bytes.TrimSpace(env.Params)
	if len(trimmedParams) > 0 && string(trimmedParams) != "null" {
		first := trimmedParams[0]
		if first != '{' && first != '[' {
			return replyErr(wire.InvalidParams, "Params must be an object or array")
		}
	}

	// 认证拦截：未认证连接仅允许调用 session.pair 与 session.authenticate
	if !c.Info().Authenticated {
		if method != "session.pair" && method != "session.authenticate" {
			return replyErr(wire.ErrCodeUnauthorized, "Unauthorized: Authentication required before calling "+method)
		}
	} else {
		d.addAuthedConn(c)
	}

	// 路由存在性校验
	if !d.methods[method] {
		return replyErr(wire.MethodNotFound, "Method not implemented")
	}

	// 方法分发与参数严格校验
	switch method {
	case "session.pair":
		var p wire.SessionPairParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: 'token' string is required")
		}
		if !isValidHex64(p.Token) && !isValidShortCode(p.Token) {
			return replyErr(wire.InvalidParams, "Invalid params: 'token' must be a 64-hex string or 6-digit code")
		}
		var devName string
		if p.DeviceName != nil {
			if len([]rune(*p.DeviceName)) > 64 {
				return replyErr(wire.InvalidParams, "Invalid params: 'deviceName' must be a string of at most 64 characters")
			}
			devName = *p.DeviceName
		}
		res, err := d.auth.Pair(p.Token, devName, c.Info())
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidToken):
				return replyErr(wire.ErrCodeUnauthorized, "Invalid or expired pairing token")
			case errors.Is(err, ErrPairingBlocked):
				return replyErr(wire.ErrCodeUnauthorized, "Too many pairing attempts, try again later")
			}
			var rpcErr *wire.RPCError
			if errors.As(err, &rpcErr) {
				return replyErr(rpcErr.Code, rpcErr.Message)
			}
			// 未认证调用方只给固定文案：内部错误原文（可能含设备库绝对路径）只写日志。
			log.Printf("remote: session.pair failed: %v", err)
			return replyErr(wire.InternalError, "Pairing failed, please retry")
		}
		c.MarkAuthenticated(res.DeviceID, res.Label)
		d.addAuthedConn(c)
		return replySuccess(wire.SessionPairResult{
			SessionToken: res.SessionToken,
			DeviceID:     res.DeviceID,
			Label:        res.Label,
		})

	case "session.authenticate":
		var p wire.SessionAuthenticateParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: 'token' string is required")
		}
		if !isValidHex64(p.Token) {
			return replyErr(wire.InvalidParams, "Invalid params: 'token' must be a 64-hex string")
		}
		deviceID, label, err := d.auth.Authenticate(p.Token, c.Info())
		if err != nil {
			if errors.Is(err, ErrInvalidToken) {
				return replyErr(wire.ErrCodeUnauthorized, "Unauthorized: Invalid or expired session token")
			}
			var rpcErr *wire.RPCError
			if errors.As(err, &rpcErr) {
				return replyErr(rpcErr.Code, rpcErr.Message)
			}
			return replyErr(wire.ErrCodeUnauthorized, "Unauthorized: Invalid or expired session token")
		}
		c.MarkAuthenticated(deviceID, label)
		d.addAuthedConn(c)
		return replySuccess(wire.SessionAuthenticateResult{
			Authenticated: true,
			DeviceID:      deviceID,
			Label:         label,
		})

	case "session.newPairingCode":
		if !checkEmptyParams(env.Params) {
			return replyErr(wire.InvalidParams, "Invalid params: 'session.newPairingCode' takes no parameters")
		}
		info, err := d.auth.NewPairingCode()
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.SessionNewPairingCodeResult{
			Requested:           true,
			PairingURL:          info.PairingURL,
			LANPairingURL:       info.LANPairingURL,
			TailscalePairingURL: info.TailscalePairingURL,
			ExpiresAt:           info.ExpiresAt.UTC().Format(time.RFC3339),
		})

	case "session.revokeDevice":
		var p wire.SessionRevokeDeviceParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.DeviceID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'deviceID' must be a UUID")
		}
		revoked, err := d.auth.RevokeDevice(p.DeviceID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.SessionRevokeDeviceResult{
			DeviceID: p.DeviceID,
			Revoked:  revoked,
		})

	case "session.renameDevice":
		var p wire.SessionRenameDeviceParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		// 名称去空白后必须非空且不超过 64 个字符，不做截断，避免静默改名。
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return replyErr(wire.InvalidParams, "Invalid params: 'name' must not be empty")
		}
		if len([]rune(name)) > 64 {
			return replyErr(wire.InvalidParams, "Invalid params: 'name' must be a string of at most 64 characters")
		}
		// 只允许改当前已认证连接自己的设备：deviceID 取自连接身份，不接受客户端传入。
		deviceID := c.Info().DeviceID
		if err := d.auth.RenameDevice(deviceID, name); err != nil {
			return replyWithBackendErr(err)
		}
		c.MarkAuthenticated(deviceID, name)
		return replySuccess(wire.SessionRenameDeviceResult{
			DeviceID: deviceID,
			Label:    name,
		})

	case "capabilities.get":
		if !checkEmptyParams(env.Params) {
			return replyErr(wire.InvalidParams, "Invalid params: 'capabilities.get' takes no parameters")
		}
		res, err := d.backend.Capabilities(ctx)
		if err != nil {
			return replyWithBackendErr(err)
		}
		if d.push == nil {
			filteredMethods := make([]string, 0, len(res.Methods))
			for _, m := range res.Methods {
				if m != "push.publicKey" && m != "push.subscribe" && m != "push.unsubscribe" {
					filteredMethods = append(filteredMethods, m)
				}
			}
			res.Methods = filteredMethods
			filteredFeatures := make([]string, 0, len(res.Features))
			for _, f := range res.Features {
				if f != "push" && f != "push.offline" {
					filteredFeatures = append(filteredFeatures, f)
				}
			}
			res.Features = filteredFeatures
		}
		return replySuccess(res)

	case "kanban.getBoard":
		return d.handleKanbanGetBoard(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "kanban.createTask":
		return d.handleKanbanCreateTask(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "kanban.moveTask":
		return d.handleKanbanMoveTask(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "kanban.startTask":
		return d.handleKanbanStartTask(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "kanban.reviewTask":
		return d.handleKanbanReviewTask(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "kanban.getDiff":
		return d.handleKanbanGetDiff(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "goals.create":
		return d.handleGoalCreate(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "goals.start":
		return d.handleGoalStart(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "goals.pause":
		return d.handleGoalPause(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "goals.cancel":
		return d.handleGoalCancel(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "goals.replan":
		return d.handleGoalReplan(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "goals.retryTask":
		return d.handleGoalRetryTask(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "goals.skipTask":
		return d.handleGoalSkipTask(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.get", "threads.hydra.get": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmGet(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.set", "threads.hydra.set": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmSet(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.setBeeTiers", "threads.swarm.setHeadTiers", "threads.hydra.setHeadTiers": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmSetBeeTiers(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.stopBee", "threads.swarm.stopHead", "threads.hydra.stopHead": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmStopBee(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.stopAll", "threads.hydra.stopAll": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmStopAll(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.clearFinished", "threads.hydra.clearFinished": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmClearFinished(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.setPlansOnly", "threads.hydra.setPlansOnly": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmSetPlansOnly(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.setMode", "threads.hydra.setMode": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmSetMode(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.forwardQuestion", "threads.hydra.forwardQuestion": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmForwardQuestion(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.mergeDiff", "threads.hydra.mergeDiff": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmMergeDiff(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.approveMerge", "threads.hydra.approveMerge": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmApproveMerge(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.swarm.discardMerge", "threads.hydra.discardMerge": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsSwarmDiscardMerge(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "swarm.tiers.get", "hydra.tiers.get": // 过渡期兼容旧方法名，下个版本移除
		return d.handleSwarmTiersGet(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "swarm.tiers.set", "hydra.tiers.set": // 过渡期兼容旧方法名，下个版本移除
		return d.handleSwarmTiersSet(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.move":
		return d.handleThreadsMove(ctx, env.Params, func(result any) *wire.Response {
			if mr, ok := result.(wire.ThreadsMoveResult); ok {
				threads := []wire.ThreadSummary{mr.Thread}
				if mr.Moved {
					// 移动会重排同项目其他会话的 orderKey，不能只发送目标行。
					if list, err := d.backend.ListThreads(ctx, wire.ThreadListParams{ProjectID: &mr.Thread.ProjectID}); err == nil && len(list.Threads) > 0 {
						threads = list.Threads
					}
				}
				d.broadcastThreadsChanged(wire.ThreadsChanged{
					Threads: threads,
				})
			}
			return replySuccess(result)
		}, replyErr, replyWithBackendErr)

	case "threads.branch":
		return d.handleThreadsBranch(ctx, env.Params, func(result any) *wire.Response {
			if br, ok := result.(wire.ThreadsBranchResult); ok && br.ThreadID != "" {
				if th, err := d.backend.GetThread(ctx, br.ThreadID); err == nil {
					d.broadcastThreadsChanged(wire.ThreadsChanged{
						Threads: []wire.ThreadSummary{th},
					})
				}
			}
			return replySuccess(result)
		}, replyErr, replyWithBackendErr)

	case "threads.continue", "threads.forward": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsContinue(ctx, env.Params, func(result any) *wire.Response {
			if fr, ok := result.(wire.ThreadsContinueResult); ok && fr.ThreadID != "" {
				if th, err := d.backend.GetThread(ctx, fr.ThreadID); err == nil {
					d.broadcastThreadsChanged(wire.ThreadsChanged{
						Threads: []wire.ThreadSummary{th},
					})
				}
			}
			return replySuccess(result)
		}, replyErr, replyWithBackendErr)

	case "threads.commands":
		return d.handleThreadsCommands(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.settleAction":
		return d.handleThreadsSettleAction(ctx, env.Params, func(result any) *wire.Response {
			if sr, ok := result.(wire.ThreadsSettleActionResult); ok && sr.ThreadID != nil && *sr.ThreadID != "" {
				if th, err := d.backend.GetThread(ctx, *sr.ThreadID); err == nil {
					d.broadcastThreadsChanged(wire.ThreadsChanged{
						Threads: []wire.ThreadSummary{th},
					})
				}
			}
			return replySuccess(result)
		}, replyErr, replyWithBackendErr)

	case "threads.retain":
		return d.handleThreadsRetain(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.provisional.release":
		return d.handleThreadsProvisionalRelease(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.queue.list":
		return d.handleThreadsQueueList(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.queue.update":
		return d.handleThreadsQueueUpdate(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.queue.remove":
		return d.handleThreadsQueueRemove(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.queue.sendNow":
		return d.handleThreadsQueueSendNow(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.queue.move":
		return d.handleThreadsQueueMove(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.mcpDisabled.set":
		return d.handleThreadsMCPDisabledSet(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.queue.interruptAndSend":
		return d.handleThreadsQueueInterruptAndSend(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.bulk.preview":
		return d.handleThreadsBulkPreview(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.bulk.archive":
		return d.handleThreadsBulkArchive(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.bulk.markDormant", "threads.bulk.settle": // 过渡期兼容旧方法名，下个版本移除
		return d.handleThreadsBulkMarkDormant(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.bulk.markRead":
		return d.handleThreadsBulkMarkRead(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.bulk.undo":
		return d.handleThreadsBulkUndo(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "files.list":
		return d.handleFilesList(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "files.read":
		return d.handleFilesRead(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "files.search":
		return d.handleFilesSearch(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "git.status":
		return d.handleGitStatus(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "git.branches":
		return d.handleGitBranches(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "scripts.list":
		return d.handleScriptsList(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "scripts.run":
		return d.handleScriptsRun(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "scripts.runStatus":
		return d.handleScriptsRunStatus(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "scripts.setFavorite":
		return d.handleScriptsSetFavorite(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "scripts.save":
		return d.handleScriptsSave(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "composer.quota":
		return d.handleComposerQuota(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "attachments.upload":
		return d.handleAttachmentsUpload(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.media.read":
		return d.handleThreadsMediaRead(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "projects.list":
		if !checkEmptyParams(env.Params) {
			return replyErr(wire.InvalidParams, "Invalid params: 'projects.list' takes no parameters")
		}
		res, err := d.backend.ListProjects(ctx)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "providers.list":
		var p wire.ProvidersListParams
		if len(trimmedParams) > 0 && string(trimmedParams) != "null" {
			if err := decodeStrict(env.Params, &p); err != nil {
				return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
			}
			if p.ProjectID != nil && !isValidUUID(*p.ProjectID) {
				return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a UUID string")
			}
		}
		res, err := d.listProviders(ctx, p.ProjectID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "providers.rememberSelection":
		var p wire.ProvidersRememberSelectionParams
		if len(trimmedParams) > 0 && string(trimmedParams) != "null" {
			if err := decodeStrict(env.Params, &p); err != nil {
				return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
			}
			if p.ProjectID != nil && !isValidUUID(*p.ProjectID) {
				return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a UUID string")
			}
		}
		rememberer, ok := d.backend.(interface {
			RememberSelection(context.Context, wire.ProvidersRememberSelectionParams) error
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		if err := rememberer.RememberSelection(ctx, p); err != nil {
			return replyWithBackendErr(err)
		}
		// 返回与 providers.list 相同的结果：写回后的 draft 已刷新。
		res, err := d.listProviders(ctx, p.ProjectID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.list":
		var p wire.ThreadListParams
		if len(trimmedParams) > 0 && string(trimmedParams) != "null" {
			if err := decodeStrict(env.Params, &p); err != nil {
				return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
			}
		}
		if p.ProjectID != nil && !isValidUUID(*p.ProjectID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a UUID string")
		}
		if p.Limit != nil && *p.Limit <= 0 {
			return replyErr(wire.InvalidParams, "Invalid params: 'limit' must be a positive integer")
		}
		res, err := d.backend.ListThreads(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.get":
		var p wire.GetThreadParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' is required")
		}
		res, err := d.backend.GetThread(ctx, p.ThreadID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.ThreadsGetResult{Thread: res})

	case "threads.history":
		var p wire.HistoryParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		if p.Limit != nil && *p.Limit <= 0 {
			return replyErr(wire.InvalidParams, "Invalid params: 'limit' must be a positive integer")
		}
		res, err := d.backend.ThreadHistory(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.subscribe":
		var p wire.SubscribeParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' is required")
		}
		res, err := d.backend.Subscribe(ctx, c, p.ThreadID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.unsubscribe":
		var p wire.UnsubscribeParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' is required")
		}
		if err := d.backend.Unsubscribe(ctx, c, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.UnsubscribeResult{
			ThreadID:   p.ThreadID,
			Subscribed: false,
		})

	case "threads.sendMessage":
		var p wire.SendMessageParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		if !isValidUUID(p.ActionID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a UUID string")
		}
		if len([]byte(p.Text)) > 64*1024 {
			return replyErr(wire.InvalidParams, "Invalid params: 'text' exceeds maximum length of 64 KiB")
		}
		if p.WhenRunning != "" && p.WhenRunning != "queue" {
			return replyErr(wire.InvalidParams, "Invalid params: 'whenRunning' must be \"queue\"")
		}
		if len(p.Attachments) > 4 {
			return replyErr(wire.InvalidParams, "Invalid params: 'attachments' must contain at most 4 items")
		}
		for _, att := range p.Attachments {
			if !isValidUUID(att) {
				return replyErr(wire.InvalidParams, "Invalid params: attachment ID must be a UUID string")
			}
		}
		if strings.TrimSpace(p.Text) == "" && len(p.Attachments) == 0 {
			return replyErr(wire.InvalidParams, "Invalid params: 'text' or 'attachments' is required")
		}
		res, err := d.backend.SendMessage(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.splitSession":
		var p wire.SplitSessionParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		splitter, ok := d.backend.(interface {
			RequestSplit(threadID string) error
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		if err := splitter.RequestSplit(p.ThreadID); err != nil {
			switch {
			case errors.Is(err, store.ErrNotFound):
				return replyErr(wire.ErrCodeResourceNotFound, "线程不存在")
			case errors.Is(err, ErrSplitRunning):
				return replyErr(wire.ErrCodeOperationRejected, "这一轮还在进行，等它结束再分割")
			case errors.Is(err, ErrSplitEmpty):
				return replyErr(wire.ErrCodeOperationRejected, "会话还没有内容，不需要分割")
			}
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.SplitSessionResult{ThreadID: p.ThreadID, Pending: true})

	case "threads.interrupt":
		var p wire.InterruptParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		if !isValidUUID(p.TurnID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'turnID' must be a UUID string")
		}
		res, err := d.backend.Interrupt(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.killRunningCommand":
		var p wire.KillRunningCommandParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		if p.MessageID != "" && !isValidUUID(p.MessageID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'messageID' must be a UUID string")
		}
		killer, ok := d.backend.(interface {
			KillRunningCommand(ctx context.Context, threadID, messageID string) (int, error)
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		killed, err := killer.KillRunningCommand(ctx, p.ThreadID, p.MessageID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.KillRunningCommandResult{Killed: killed})

	case "threads.queue.edit":
		var p wire.QueueEditParams
		if err := decodeStrict(env.Params, &p); err != nil || !isValidUUID(p.ThreadID) ||
			(p.Action != "begin" && p.Action != "save" && p.Action != "cancel") ||
			(p.Action == "begin" && !isValidUUID(p.FollowUpID)) {
			return replyErr(wire.InvalidParams, "Invalid queue edit params")
		}
		if p.Draft != nil {
			for _, id := range p.Draft.Attachments {
				if !isValidUUID(id) {
					return replyErr(wire.InvalidParams, "Invalid attachment ID")
				}
			}
		}
		editor, ok := d.backend.(interface {
			QueueEdit(context.Context, wire.QueueEditParams) (wire.QueueEditResult, error)
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		res, err := editor.QueueEdit(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.provider.limitText", "threads.handoff.limitText", "threads.compact": // 过渡期兼容旧方法名，下个版本移除
		var p wire.TurnRecallParams
		if err := decodeStrict(env.Params, &p); err != nil || !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid threadID")
		}
		controls, ok := d.backend.(interface {
			LimitHandoffText(context.Context, string) (wire.LimitTextResult, error)
			Compact(context.Context, string) (wire.SendMessageResult, error)
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		if method == "threads.compact" {
			res, err := controls.Compact(ctx, p.ThreadID)
			if err != nil {
				return replyWithBackendErr(err)
			}
			return replySuccess(res)
		}
		res, err := controls.LimitHandoffText(ctx, p.ThreadID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.worktree.preview", "threads.handoff.preview": // 过渡期兼容旧方法名，下个版本移除
		var p wire.ThreadsWorktreePreviewParams
		if err := decodeStrict(env.Params, &p); err != nil || !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid threadID")
		}
		ops, ok := d.backend.(interface {
			WorktreeMovePreview(context.Context, string) (wire.ThreadsWorktreePreviewResult, error)
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		res, err := ops.WorktreeMovePreview(ctx, p.ThreadID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.worktree.execute", "threads.handoff.execute": // 过渡期兼容旧方法名，下个版本移除
		var p wire.ThreadsWorktreeExecuteParams
		if err := decodeStrict(env.Params, &p); err != nil || !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params")
		}
		ops, ok := d.backend.(interface {
			WorktreeMoveExecute(context.Context, wire.ThreadsWorktreeExecuteParams) (wire.ThreadsWorktreeExecuteResult, error)
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		res, err := ops.WorktreeMoveExecute(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.turn.recall", "threads.turn.takeBack": // 过渡期兼容旧方法名，下个版本移除
		var p wire.TurnRecallParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		taker, ok := d.backend.(interface {
			RecallTurn(ctx context.Context, threadID string) (wire.TurnRecallResult, error)
		})
		if !ok {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		res, err := taker.RecallTurn(ctx, p.ThreadID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.create":
		var p wire.ThreadCreateParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ProjectID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a UUID string")
		}
		if !isValidUUID(p.ActionID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a UUID string")
		}
		res, err := d.backend.CreateThread(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		// 新建会话要求开启编排时需在首轮之前真正开启，否则首轮会按普通会话执行、不派子代理。
		// 只处理 true：新建会话没有“关闭”语义，重复开启幂等。
		if d.swarm != nil && p.Options != nil && p.Options.SwarmEnabled != nil && *p.Options.SwarmEnabled {
			d.swarm.SetEnabled(res.ThreadID, *p.Options.SwarmEnabled)
			d.NotifySwarmChanged(res.ThreadID)
		}
		if th, err := d.backend.GetThread(ctx, res.ThreadID); err == nil {
			d.broadcastThreadsChanged(wire.ThreadsChanged{
				Threads: []wire.ThreadSummary{th},
			})
		}
		return replySuccess(res)

	case "threads.title.regenerate":
		return d.handleThreadsTitleRegenerate(ctx, env.Params, replySuccess, replyErr, replyWithBackendErr)

	case "threads.update":
		var p wire.ThreadUpdateParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		var (
			res   wire.ThreadUpdateResult
			token string
			err   error
		)
		if undo, ok := d.undoThreads(); ok {
			res, token, err = undo.UpdateThreadWithUndo(ctx, p)
		} else {
			res, err = d.backend.UpdateThread(ctx, p)
		}
		if err != nil {
			return replyWithBackendErr(err)
		}
		if token != "" {
			res.UndoToken = &token
		}
		d.broadcastThreadsChanged(wire.ThreadsChanged{
			Threads: []wire.ThreadSummary{res.Thread},
		})
		return replySuccess(res)

	case "threads.actionStatus":
		var p wire.ActionStatusParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ActionID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a UUID string")
		}
		res, err := d.backend.ActionStatus(ctx, p.ActionID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "requests.list":
		var p wire.RequestsListParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		res, err := d.backend.ListRequests(ctx, p.ThreadID)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "requests.respond":
		var p wire.RequestsRespondParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a UUID string")
		}
		if strings.TrimSpace(p.RunID) == "" {
			return replyErr(wire.InvalidParams, "Invalid params: 'runID' is required")
		}
		if strings.TrimSpace(p.RequestID) == "" {
			return replyErr(wire.InvalidParams, "Invalid params: 'requestID' is required")
		}
		var count int
		if p.OptionID != nil && strings.TrimSpace(*p.OptionID) != "" {
			count++
		}
		// 计划驳回可在 deny 上附带反馈，具体请求类型及答案由 backend 校验。
		if len(p.Answers) > 0 && !bytes.Equal(bytes.TrimSpace(p.Answers), []byte("null")) && (p.OptionID == nil || *p.OptionID != "deny") {
			count++
		}
		if p.Skip != nil && *p.Skip {
			count++
		}
		if count != 1 {
			return replyErr(wire.InvalidParams, "Invalid params: exactly one of 'optionID', 'answers', or 'skip' must be provided")
		}
		if !c.Info().Entry.Secure() {
			reqs, err := d.backend.ListRequests(ctx, p.ThreadID)
			if err != nil {
				return replyWithBackendErr(err)
			}
			for _, req := range reqs.Requests {
				if req.RequestID == p.RequestID {
					for _, q := range req.Questions {
						if q.IsSecret {
							return replyErr(wire.ErrCodeRequiresHTTPS, "Secret answers require the HTTPS entry")
						}
					}
					break
				}
			}
		}
		// 网页端直接答复：来源记为 remote；确认总管建议走 ConfirmApprovalSuggestion，会再覆盖为 confirm。
		ctx = WithAnswerSource(ctx, AnswerSourceRemote)
		res, err := d.backend.RespondRequest(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "threads.getDiff":
		var p wire.ThreadsGetDiffParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if !isValidUUID(p.ThreadID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'threadID' is required")
		}
		if p.TurnID != nil && !isValidUUID(*p.TurnID) {
			return replyErr(wire.InvalidParams, "Invalid params: 'turnID' must be a UUID string")
		}
		res, err := d.backend.GetDiff(ctx, p)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)

	case "push.publicKey":
		if d.push == nil {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		if !checkEmptyParams(env.Params) {
			return replyErr(wire.InvalidParams, "Invalid params: 'push.publicKey' takes no parameters")
		}
		return replySuccess(wire.PushPublicKeyResult{
			PublicKey: d.push.PublicKey(),
		})

	case "push.subscribe":
		if d.push == nil {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		var p wire.PushSubscribeParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if strings.TrimSpace(p.Endpoint) == "" || len(p.Endpoint) > 2048 {
			return replyErr(wire.InvalidParams, "Invalid params: 'endpoint' is required and must not exceed 2048 characters")
		}
		if strings.TrimSpace(p.P256dh) == "" || len(p.P256dh) > 256 {
			return replyErr(wire.InvalidParams, "Invalid params: 'p256dh' is required and must not exceed 256 characters")
		}
		if strings.TrimSpace(p.Auth) == "" || len(p.Auth) > 256 {
			return replyErr(wire.InvalidParams, "Invalid params: 'auth' is required and must not exceed 256 characters")
		}
		deviceID := c.Info().DeviceID
		if err := d.push.Subscribe(deviceID, p.Endpoint, p.P256dh, p.Auth); err != nil {
			var rpcErr *wire.RPCError
			if errors.As(err, &rpcErr) {
				return replyErr(rpcErr.Code, rpcErr.Message)
			}
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.PushSubscribeResult{
			Subscribed: true,
		})

	case "push.unsubscribe":
		if d.push == nil {
			return replyErr(wire.MethodNotFound, "Method not implemented")
		}
		var p wire.PushUnsubscribeParams
		if err := decodeStrict(env.Params, &p); err != nil {
			return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
		}
		if strings.TrimSpace(p.Endpoint) == "" || len(p.Endpoint) > 2048 {
			return replyErr(wire.InvalidParams, "Invalid params: 'endpoint' is required and must not exceed 2048 characters")
		}
		deviceID := c.Info().DeviceID
		unsubscribed, err := d.push.Unsubscribe(deviceID, p.Endpoint)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(wire.PushUnsubscribeResult{
			Unsubscribed: unsubscribed,
		})
	}

	// 内置方法都没有命中时查扩展注册表：扩展方法按需注册，不必改动上面的 switch。
	if handler, ok := lookupExtension(method); ok {
		result, err := handler(ctx, d, c, env.Params)
		if err != nil {
			var extErr *ExtError
			if errors.As(err, &extErr) {
				return replyErr(extErr.Code, extErr.Message)
			}
			return replyWithBackendErr(err)
		}
		return replySuccess(result)
	}

	return replyErr(wire.MethodNotFound, "Method not implemented")
}

func marshalEnvelope(r wire.Response) []byte {
	out, _ := json.Marshal(r)
	return out
}

func decodeStrict(data []byte, v any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return errors.New("empty params")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data in params")
	}
	return nil
}

func checkEmptyParams(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return true
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return false
	}
	return len(m) == 0
}

func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

func isValidHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < 64; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func isValidShortCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
