package pi

// 本文件读模型目录：
// providerOf / modelOf / supportedThinkingLevels / defaultThinkingLevel /
// fetchedModels（models-store.json）以及 listModels 的一次性 RPC 抓取。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// piThinkingLevels 是 Pi 的 --thinking 等级。
var piThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// piModelArgs 是一次性取模型目录时 pi 的固定参数。
var piModelArgs = []string{
	"--mode", "rpc",
	"--no-session",
	"--no-extensions",
	"--no-skills",
	"--no-prompt-templates",
	"--no-themes",
	"--no-context-files",
}

// listModelsTimeout 是一次性 RPC 取模型的上限（30 秒）。
const listModelsTimeout = 30 * time.Second

// execCommand 是启动子进程的钩子，测试里可以替换成回放 fixture 的假进程。
var execCommand = exec.CommandContext

// Model 是 Pi 目录里的一个可选模型。
type Model struct {
	// ID 是 <provider>/<model id> 形式的完整 id（Pi 的 id 本身可能已带 provider 前缀）。
	ID string
	// Provider 是 Pi 的 --provider 与 set_model 接受的第一段。
	Provider string
	// Name 是展示名，模型没报名字时退化为 Pi 的 id。
	Name string
	// ContextWindow 是模型的上下文窗口上限（tokens），取不到时为 0。
	ContextWindow int
	// ThinkingLevels 是该模型真正接受的思考等级，模型不支持思考时为空。
	ThinkingLevels []string
	// DefaultThinking 是 Pi 自己会用的启动等级，取不到时为空。
	DefaultThinking string
	// IsDefault 表示这是当前会话正在用的模型。
	IsDefault bool
}

// ProviderOf 返回完整 id 里的 provider 段，没有斜杠时 ok 为 false。
func ProviderOf(id string) (provider string, ok bool) {
	if index := strings.IndexByte(id, '/'); index > 0 {
		return id[:index], true
	}
	return "", false
}

// ModelOf 返回 Pi 的 --model / set_model 接受的模型 id：斜杠之后的一切
// （Pi 允许模型 id 本身含斜杠），为空时 ok 为 false。
func ModelOf(id string) (model string, ok bool) {
	index := strings.IndexByte(id, '/')
	if index < 0 {
		return "", false
	}
	model = id[index+1:]
	if model == "" {
		return "", false
	}
	return model, true
}

// ParseFetchedModels 解析 Pi 自己 refresh 写下的 models-store.json，得到
// "provider/model" → 条目 的表；无法解析时返回空表。
func ParseFetchedModels(raw []byte) map[string]json.RawMessage {
	models := map[string]json.RawMessage{}
	root := piRawObject(raw)
	if root == nil {
		return models
	}
	for providerKey, providerValue := range root {
		provider := piRawObject(providerValue)
		if provider == nil {
			continue
		}
		for _, entry := range piRawArray(provider["models"]) {
			object := piRawObject(entry)
			if object == nil {
				continue
			}
			id := piString(object, "id")
			if id == "" {
				continue
			}
			name := piString(object, "provider")
			if name == "" {
				name = providerKey
			}
			models[name+"/"+id] = entry
		}
	}
	return models
}

// FetchedModels 读 <config>/models-store.json；文件缺失或无法解析时返回空表。
func FetchedModels(env map[string]string, home string) map[string]json.RawMessage {
	file := filepath.Join(ConfigDir(env, home), "models-store.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return map[string]json.RawMessage{}
	}
	return ParseFetchedModels(data)
}

// ParseModels 解析 Pi RPC 返回的模型列表，raw 兼容 {data:{models:[…]}}、
// {models:[…]} 与裸数组三种写法；当 raw 顶层带 state（ListModels 会把 get_state 的
// data 并进来）时，据此标出当前模型。
//
// fetched 是 models-store.json 的表（可为 nil，等价于查不到），settings 是
// settings.json 的顶层对象（可为 nil）。
func ParseModels(raw []byte, fetched map[string]json.RawMessage, settings map[string]json.RawMessage) ([]Model, error) {
	items := piModelItems(raw)
	if items == nil {
		return nil, fmt.Errorf("pi: 无法识别的模型列表载荷")
	}
	currentProvider, currentID := piCurrentModel(raw)
	models := make([]Model, 0, len(items))
	for _, item := range items {
		object := piRawObject(item)
		if object == nil {
			continue
		}
		provider := piString(object, "provider")
		id := piString(object, "id")
		// Pi 报的 id 里已经带了 provider 前缀，完整 id 因此形如 provider/provider/model。
		if provider == "" || id == "" {
			continue
		}
		fullID := provider + "/" + id
		var entry map[string]json.RawMessage
		if fetched != nil {
			if rawEntry, ok := fetched[fullID]; ok {
				entry = piRawObject(rawEntry)
			}
		}
		// 上下文窗口优先用模型自报的值，没有时回落到 models-store.json 的同一模型。
		contextWindow := 0
		if size := piNumber(object, "contextWindow"); size != nil && *size > 0 {
			contextWindow = int(*size)
		} else if size := piNumber(entry, "contextWindow"); size != nil && *size > 0 {
			contextWindow = int(*size)
		}
		levels := piSupportedThinkingLevels(object, entry)
		wanted := piDefaultThinkingLevel(fullID, settings)
		defaultThinking := ""
		if len(levels) > 0 {
			if piContains(levels, wanted) {
				defaultThinking = wanted
			} else {
				defaultThinking = levels[0]
			}
		}
		name := piString(object, "name")
		if name == "" {
			name = id
		}
		models = append(models, Model{
			ID:              fullID,
			Provider:        provider,
			Name:            name,
			ContextWindow:   contextWindow,
			ThinkingLevels:  levels,
			DefaultThinking: defaultThinking,
			IsDefault:       piIsCurrentModel(provider, id, fullID, currentProvider, currentID),
		})
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		return models[i].Name < models[j].Name
	})
	return models, nil
}

// ListModels 用一次性的 `pi --mode rpc` 进程取模型目录：
// 写入 get_state 与 get_available_models 两条请求，读到对应响应后结束进程。
// binary 是 pi 可执行文件（必填，解析交给调用方）；env 为 nil 时用进程环境。
// 不注入 Magpie 端点，等价于 ListModelsWith 的 bridge 与 magpieEnv 都为空。
func ListModels(ctx context.Context, binary string, env map[string]string, home string) ([]Model, error) {
	return ListModelsWith(ctx, binary, env, home, "", "")
}

// ListModelsWith 在 ListModels 的基础上按需加载 Magpie 账号桥：bridge 与 magpieEnv 都非空时
// 在 piModelArgs 之后追加 `-e bridge` 并在子进程环境里设置 MCODE_PI_MAGPIE，
// 这样取到的目录里会包含 mcode 设置里配置的 Magpie 端点模型。
//
// 保留 piModelArgs 里的 --no-extensions：它只关掉扩展发现与内置扩展，
// 显式 -e 路径仍然会加载（pi 的 --help 原文与 resource-loader 的 noExtensions 分支）。
func ListModelsWith(ctx context.Context, binary string, env map[string]string, home string, bridge, magpieEnv string) ([]Model, error) {
	if strings.TrimSpace(binary) == "" {
		return nil, fmt.Errorf("pi: 未指定 pi 可执行文件")
	}
	runCtx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()

	args := piModelArgs
	magpieEnabled := strings.TrimSpace(bridge) != "" && strings.TrimSpace(magpieEnv) != ""
	if magpieEnabled {
		args = make([]string, 0, len(piModelArgs)+2)
		args = append(args, piModelArgs...)
		args = append(args, "-e", bridge)
	}

	cmd := procattr.HideWindow(execCommand(runCtx, binary, args...))
	cmd.Dir = piHome(env, home)
	cmd.Env = piLaunchEnv(env)
	if magpieEnabled {
		cmd.Env = piSetEnv(cmd.Env, MagpieEnvVar, magpieEnv)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pi: 打开 stdin 管道: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("pi: 打开 stdout 管道: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("pi: 启动 %s: %w", binary, err)
	}
	// 收尾：关掉 stdin 并等进程退出；上下文取消会终止子进程。
	defer func() {
		_ = stdin.Close()
		cancel()
		_ = cmd.Wait()
	}()

	// 两条请求的顺序固定；写失败不单独报错，下面的读取会以缺响应收场。
	_, _ = io.WriteString(stdin, `{"type":"get_state","id":"state"}`+"\n")
	_, _ = io.WriteString(stdin, `{"type":"get_available_models","id":"models"}`+"\n")

	var stateRaw, modelsRaw json.RawMessage
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(line, &message); err != nil {
			continue
		}
		switch piString(message, "id") {
		case "state":
			stateRaw = append(json.RawMessage(nil), line...)
		case "models":
			modelsRaw = append(json.RawMessage(nil), line...)
		}
		if stateRaw != nil && modelsRaw != nil {
			break
		}
	}
	if modelsRaw == nil {
		return nil, fmt.Errorf("pi: %s 未返回模型列表", binary)
	}

	// 把 get_state 的 data 与模型数组并成一份载荷，交给 ParseModels 一次解析。
	payload := struct {
		Models json.RawMessage `json:"models"`
		State  json.RawMessage `json:"state,omitempty"`
	}{}
	if items := piModelItems(modelsRaw); items != nil {
		payload.Models, _ = json.Marshal(items)
	}
	if stateData := piStateData(stateRaw); stateData != nil {
		payload.State = stateData
	}
	merged, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("pi: 组装模型载荷: %w", err)
	}

	models, err := ParseModels(merged, FetchedModels(env, home), Settings(env, home))
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("pi: 没列出任何模型")
	}
	return models, nil
}

// BinaryAvailable 在登录 shell 的 PATH 中查找 pi 可执行文件；找不到时返回空路径与 false。
// 对应状态监控里的“Pi 是否安装”。
func BinaryAvailable(binary string) (path string, ok bool) {
	name := strings.TrimSpace(binary)
	if name == "" {
		return "", false
	}
	resolved, err := loginenv.LookPath(name)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// piSupportedThinkingLevels 读模型自己报的
// thinkingLevelMap 里映射为 null 的等级被隐藏，xhigh / max 只有模型显式映射时才出现，
// 其余等级默认可用。传入了 models-store.json 的条目（含空对象）时它整份取代模型自报的
// thinkingLevelMap，但 reasoning 只在 fetched 自己有这个布尔值时以它为准，否则回落到模型。
func piSupportedThinkingLevels(model map[string]json.RawMessage, fetched map[string]json.RawMessage) []string {
	// fetched 非 nil（哪怕是空 map）就只认它的 thinkingLevelMap。
	source := model
	if fetched != nil {
		source = fetched
	}
	// 按“键是否存在且为 bool”判断：fetched 没有可用的 reasoning 时回落到 model。
	reasoning := piBool(fetched, "reasoning")
	if reasoning == nil {
		reasoning = piBool(model, "reasoning")
	}
	if reasoning == nil || !*reasoning {
		return nil
	}
	var mapped map[string]json.RawMessage
	if raw, ok := source["thinkingLevelMap"]; ok {
		mapped = piRawObject(raw)
	}
	levels := make([]string, 0, len(piThinkingLevels))
	for _, level := range piThinkingLevels {
		if raw, ok := mapped[level]; ok {
			if isJSONNull(raw) {
				continue
			}
			levels = append(levels, level)
			continue
		}
		if level == "xhigh" || level == "max" {
			continue
		}
		levels = append(levels, level)
	}
	return levels
}

// piDefaultThinkingLevel 取默认思考等级：先看 settings 里该模型的
// 等级，再看全局默认，最后是 Pi 自己的回退值 medium。
func piDefaultThinkingLevel(model string, settings map[string]json.RawMessage) string {
	trimmed := strings.TrimSpace(model)
	if perModel := piRawObject(settings["modelThinkingLevels"]); perModel != nil {
		if level := piString(perModel, trimmed); level != "" {
			return level
		}
	}
	if global := piString(settings, "defaultThinkingLevel"); global != "" {
		return global
	}
	return "medium"
}

// piIsCurrentModel 判断当前模型 id：当前模型 id 可能是完整 id、
// 裸模型 id，或以 "/<裸 id>" 结尾的形状。
func piIsCurrentModel(provider, id, fullID, currentProvider, currentID string) bool {
	if currentProvider == "" || currentID == "" || provider != currentProvider {
		return false
	}
	return currentID == fullID || currentID == id || strings.HasSuffix(currentID, "/"+id)
}

// piModelItems 从模型列表载荷里取出条目数组，兼容 {data:{models:[…]}}、{models:[…]}
// 与裸数组三种写法；识别不了时返回 nil。
func piModelItems(raw []byte) []json.RawMessage {
	if items := piRawArray(raw); items != nil {
		return items
	}
	root := piRawObject(raw)
	if root == nil {
		return nil
	}
	if data := piRawObject(root["data"]); data != nil {
		if items := piRawArray(data["models"]); items != nil {
			return items
		}
	}
	return piRawArray(root["models"])
}

// piCurrentModel 从（可能并入了 state 的）载荷里读出当前模型的 provider 与 id。
func piCurrentModel(raw []byte) (provider, id string) {
	state := piRawObject(raw)
	if state == nil {
		return "", ""
	}
	state = piStateData2(state)
	if state == nil {
		return "", ""
	}
	model := piRawObject(state["model"])
	if model == nil {
		return "", ""
	}
	return piString(model, "provider"), piString(model, "id")
}

// piStateData 取 get_state 响应的 data 字段（缺失时退回整条响应）。
func piStateData(raw []byte) json.RawMessage {
	message := piRawObject(raw)
	if message == nil {
		return nil
	}
	if data := piRawObject(message["data"]); data != nil {
		return message["data"]
	}
	return raw
}

// piStateData2 在已解开的载荷里找 state（顶层 state 或 data.state）。
func piStateData2(root map[string]json.RawMessage) map[string]json.RawMessage {
	if state := piRawObject(root["state"]); state != nil {
		return state
	}
	if data := piRawObject(root["data"]); data != nil {
		return piRawObject(data["state"])
	}
	return nil
}

// piLaunchEnv 组装子进程环境：env 为 nil 时用登录 shell 环境，否则用给定的表，
// 并统一加上 PI_SKIP_VERSION_CHECK=1。
func piLaunchEnv(env map[string]string) []string {
	var result []string
	if env == nil {
		result = append(result, loginenv.Environ()...)
	} else {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			result = append(result, key+"="+env[key])
		}
	}
	return piSetEnv(result, "PI_SKIP_VERSION_CHECK", "1")
}

// piSetEnv 在 KEY=VALUE 列表里设置一项，已有同名项时替换。
func piSetEnv(environ []string, key, value string) []string {
	prefix := key + "="
	entry := prefix + value
	for i, item := range environ {
		if strings.HasPrefix(item, prefix) {
			if environ[i] == entry {
				return environ
			}
			replaced := make([]string, len(environ))
			copy(replaced, environ)
			replaced[i] = entry
			return replaced
		}
	}
	return append(environ, entry)
}

func piContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// piRawObject 把一段 JSON 解成对象；不是对象（含 null、数组、标量）时返回 nil。
func piRawObject(raw []byte) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	return object
}

// piRawArray 把一段 JSON 解成数组；不是数组时返回 nil。
func piRawArray(raw []byte) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err != nil {
		return nil
	}
	return array
}

// piString 读字符串字段；缺失、null 或类型不符时返回空串。
func piString(object map[string]json.RawMessage, key string) string {
	if object == nil {
		return ""
	}
	var text string
	if err := json.Unmarshal(object[key], &text); err != nil {
		return ""
	}
	return text
}

// piBool 读布尔字段；缺失、null 或类型不符时返回 nil。
func piBool(object map[string]json.RawMessage, key string) *bool {
	if object == nil || isJSONNull(object[key]) {
		return nil
	}
	var value bool
	if err := json.Unmarshal(object[key], &value); err != nil {
		return nil
	}
	return &value
}

// piNumber 读数值字段，兼容数字与数字字符串；缺失或非法时返回 nil。
func piNumber(object map[string]json.RawMessage, key string) *float64 {
	if object == nil {
		return nil
	}
	return piNumberRaw(object[key])
}

func piNumberRaw(raw json.RawMessage) *float64 {
	if isJSONNull(raw) {
		return nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err == nil {
		return &value
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return &parsed
		}
	}
	return nil
}

// isJSONNull 判断一段 JSON 是否是 null（含空片段）。
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null"
}
