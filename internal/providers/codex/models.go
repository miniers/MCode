package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/modelcache"
	"mcode/internal/startuptrace"
)

// Model 表示 Codex 提供者导出的模型配置与推理能力信息。
type Model struct {
	ID            string
	DisplayName   string
	Efforts       []string
	DefaultEffort string
	SupportsFast  bool
	// FastTierID 快速档位的协议 ID（如 "priority"），无快速档位时为空。
	FastTierID string
}

var modelCache = &modelcache.Cache[Model]{}

// catalogFetch 是后台探测入口；单独抽成变量便于测试替换，避免真的启动本机 CLI。
var catalogFetch = fetchCachedModels

// catalogSnapshot 是 CatalogNotice 读取探测状态的入口；单独抽成变量便于测试注入。
var catalogSnapshot = CachedCatalog

// loginPresent 报告本机是否有 Codex 登录账号；单独抽成变量便于测试替换。
var loginPresent = cachedLoginPresent

var (
	loginPresentOnce  sync.Once
	loginPresentValue bool
)

// codexConfigHome 返回 Codex 的配置目录：优先 CODEX_HOME，未设置时用 ~/.codex。
func codexConfigHome() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userHome, ".codex")
}

// cachedLoginPresent 按进程缓存登录检测结果，避免模型选择器反复读凭据。
func cachedLoginPresent() bool {
	loginPresentOnce.Do(func() {
		loginPresentValue = ReadAccountEmail(codexConfigHome()) != ""
	})
	return loginPresentValue
}

// FetchModels 启动 <binary> app-server 子进程，通过 JSON-RPC 2.0 先完成 initialize 握手，
// 再循环调用 model/list 抓取全部模型信息（最多 10 页），解析推理强度与加速支持。
// 整体超时 15 秒，结束后确保完全关闭并回收子进程。
func FetchModels(ctx context.Context, binary string) ([]Model, error) {
	if binary == "" {
		binary = "codex"
	}
	binPath, err := loginenv.LookPath(binary)
	if err != nil {
		binPath, err = exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("codex: binary %q not found: %w", binary, errors.Join(modelcache.ErrNotInstalled, err))
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	args := []string{"app-server", "--stdio"}
	cmd := procattr.HideWindow(exec.CommandContext(ctx, binPath, args...))
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGTERM)
	}
	cmd.WaitDelay = 2 * time.Second

	tempDir := os.TempDir()
	cmd.Dir = tempDir
	cmd.Env = buildEnv(loginenv.Environ(), tempDir, nil)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("codex: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("codex: start %s: %w", binPath, err)
	}

	// 退出时确保彻底关闭 stdin 并安全等待/杀死进程组，防止僵尸进程
	defer func() {
		_ = stdin.Close()
		waitDone := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(500 * time.Millisecond):
			_ = killProcessGroup(cmd, syscall.SIGKILL)
			<-waitDone
		}
	}()

	client := newAppClient(stdin)

	// 启动标准输出监听循环：分发响应报文；遇到服务端审批等反向请求直接 decline 避免挂起
	go func() {
		defer close(client.readerDoneCh)
		reader := bufio.NewReader(stdout)

		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				line = bytes.TrimSpace(line)
				if len(line) > 0 {
					var rawMsg JSONRPCMessage
					if err := json.Unmarshal(line, &rawMsg); err == nil {
						switch {
						case rawMsg.IsResponse():
							idStr := rawMsg.RawID()
							client.mu.Lock()
							ch, ok := client.pending[idStr]
							client.mu.Unlock()
							if ok && ch != nil {
								ch <- &rawMsg
							}
						case rawMsg.IsRequest():
							go func(reqMsg JSONRPCMessage) {
								reply := &JSONRPCMessage{
									JSONRPC: "2.0",
									ID:      reqMsg.ID,
									Result:  json.RawMessage(`{"decision":"decline"}`),
								}
								_ = client.send(reply)
							}(rawMsg)
						}
					}
				}
			}
			if readErr != nil {
				break
			}
		}

		client.mu.Lock()
		for idStr, ch := range client.pending {
			close(ch)
			delete(client.pending, idStr)
		}
		client.mu.Unlock()
	}()

	// initialize 握手流程
	initParams := map[string]any{
		"clientInfo": map[string]any{
			"name":    "mcode",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}
	if _, err := client.call(ctx, "initialize", initParams); err != nil {
		return nil, fmt.Errorf("codex: initialize failed: %w", err)
	}
	_ = client.notify("initialized", nil)

	// 循环调用 model/list 分页拉取，最多 10 页
	var models []Model
	var cursor any
	for page := 0; page < 10; page++ {
		params := make(map[string]any)
		if cursor != nil && cursor != "" {
			params["cursor"] = cursor
		}
		resRaw, err := client.call(ctx, "model/list", params)
		if err != nil {
			return nil, fmt.Errorf("codex: model/list (page %d): %w", page, err)
		}

		pageModels, nextCursor, err := parseModelListPage(resRaw)
		if err != nil {
			return nil, fmt.Errorf("codex: parse model/list (page %d): %w", page, err)
		}
		models = append(models, pageModels...)

		if nextCursor == nil || nextCursor == "" || nextCursor == cursor {
			break
		}
		cursor = nextCursor
	}

	return models, nil
}

// parseModelListPage 解析 model/list 单页返回结果。
// 字段名同时兼容 camelCase 与 snake_case。
func parseModelListPage(raw json.RawMessage) ([]Model, any, error) {
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, err
	}

	var rawData []any
	if d, ok := resp["data"].([]any); ok {
		rawData = d
	}

	var models []Model
	for _, item := range rawData {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		// 过滤隐藏模型
		if getBoolField(m, "hidden", "is_hidden", "isHidden") {
			continue
		}

		id := getStringField(m, "id")
		if id == "" {
			continue
		}

		displayName := getStringField(m, "displayName", "display_name", "name")
		if displayName == "" {
			displayName = id
		}

		// 提取 supportedReasoningEfforts，支持对象列表与字符串列表
		var efforts []string
		rawEfforts := getSliceField(m, "supportedReasoningEfforts", "supported_reasoning_efforts", "efforts", "reasoning_efforts", "reasoningEfforts")
		for _, eff := range rawEfforts {
			if s, ok := eff.(string); ok && s != "" {
				efforts = append(efforts, s)
			} else if effMap, ok := eff.(map[string]any); ok {
				name := getStringField(effMap, "reasoningEffort", "reasoning_effort", "effort", "name", "id")
				if name != "" {
					efforts = append(efforts, name)
				}
			}
		}

		// 默认推理强度
		defaultEffort := getStringField(m, "defaultReasoningEffort", "default_reasoning_effort", "defaultEffort", "default_effort")

		// 快速响应档位判定：serviceTiers 含有 fast / priority，或显式支持标志；
		// 同时记录该档位的协议 ID。
		supportsFast := getBoolField(m, "supportsFast", "supports_fast")
		fastTierID := ""
		if !supportsFast {
			rawTiers := getSliceField(m, "serviceTiers", "service_tiers")
			for _, tier := range rawTiers {
				if tierMap, ok := tier.(map[string]any); ok {
					tierID := getStringField(tierMap, "id")
					tierName := getStringField(tierMap, "name")
					label := strings.ToLower(tierID + " " + tierName)
					if strings.Contains(label, "fast") || strings.Contains(label, "priority") {
						supportsFast = true
						fastTierID = tierID
						break
					}
				}
			}
		}

		models = append(models, Model{
			ID:            id,
			DisplayName:   displayName,
			Efforts:       efforts,
			DefaultEffort: defaultEffort,
			SupportsFast:  supportsFast,
			FastTierID:    fastTierID,
		})
	}

	// 下一页游标
	var nextCursor any
	if v, ok := resp["nextCursor"]; ok && v != nil {
		nextCursor = v
	} else if v, ok := resp["next_cursor"]; ok && v != nil {
		nextCursor = v
	}
	if s, ok := nextCursor.(string); ok && s == "" {
		nextCursor = nil
	}

	return models, nextCursor, nil
}

// CachedCatalog 返回模型目录与最近一次探测状态；尚未拿到时在后台启动一次探测。
func CachedCatalog() modelcache.Snapshot[Model] {
	snap := modelCache.Peek(catalogFetch)
	if len(snap.Models) > 0 {
		// 首次拿到目录时打点：启动路径只读缓存，子进程在后台跑。
		startuptrace.Mark("providers: codex models ready")
		snap.Models = cloneModels(snap.Models)
	}
	return snap
}

// CachedModels 返回缓存的 Codex 模型列表。
// 未完成或失败时返回 nil，成功后返回缓存；失败后 10 分钟内不再重试。
func CachedModels() []Model {
	return CachedCatalog().Models
}

// ResetCatalogCacheForTest 把进程级模型目录缓存重置为「从未探测」，并把后台探测换成必然失败的桩，
// 返回的函数用于恢复原状态。仅供测试使用：同进程里其他用例可能已经写入缓存或触发真实探测，
// 不重置时读到的目录会随执行顺序变化。
func ResetCatalogCacheForTest() func() {
	prevCache, prevFetch := modelCache, catalogFetch
	modelCache = &modelcache.Cache[Model]{}
	catalogFetch = func(context.Context) ([]Model, error) {
		return nil, errors.Join(modelcache.ErrNotInstalled, errors.New("测试环境已禁用模型探测"))
	}
	return func() {
		modelCache, catalogFetch = prevCache, prevFetch
	}
}

// CatalogNotice 返回模型目录为空时的原因文案；目录可用时返回空串。
func CatalogNotice() string {
	snap := catalogSnapshot()
	if len(snap.Models) > 0 {
		return ""
	}
	if snap.Probing {
		return "正在探测 Codex 模型…"
	}
	if errors.Is(snap.Err, modelcache.ErrNotInstalled) {
		return "未安装 Codex CLI"
	}
	if snap.Err != nil && !loginPresent() {
		return "未登录 Codex"
	}
	return "探测 Codex 模型失败"
}

// RefreshModels 强制重新读取 Codex CLI 的模型目录。
func RefreshModels(ctx context.Context, binary string) error {
	return modelCache.Refresh(ctx, func(ctx context.Context) ([]Model, error) {
		return FetchModels(ctx, binary)
	})
}

func fetchCachedModels(ctx context.Context) ([]Model, error) {
	// 二进制路径解析与 codex.go 保持一致，当为空时默认寻找 "codex"
	binary := "codex"
	binPath, err := loginenv.LookPath(binary)
	if err != nil {
		binPath, err = exec.LookPath(binary)
	}
	if err != nil {
		return nil, fmt.Errorf("codex: 未找到 %s: %w", binary, errors.Join(modelcache.ErrNotInstalled, err))
	}
	return FetchModels(ctx, binPath)
}

func cloneModels(src []Model) []Model {
	res := make([]Model, len(src))
	for i, m := range src {
		effortsCopy := make([]string, len(m.Efforts))
		copy(effortsCopy, m.Efforts)
		res[i] = Model{
			ID:            m.ID,
			DisplayName:   m.DisplayName,
			Efforts:       effortsCopy,
			DefaultEffort: m.DefaultEffort,
			SupportsFast:  m.SupportsFast,
			FastTierID:    m.FastTierID,
		}
	}
	return res
}

func getStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func getBoolField(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

func getSliceField(m map[string]any, keys ...string) []any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.([]any); ok {
				return s
			}
		}
	}
	return nil
}
