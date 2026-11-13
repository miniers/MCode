package agentconf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mcode/internal/config"
	"mcode/internal/fsutil"
)

// MCPToolInfo 表示 MCP 服务提供的工具描述信息。
type MCPToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToolListerFunc 是工具探测函数的函数类型。
type ToolListerFunc func(ctx context.Context, srv MCPServer) ([]MCPToolInfo, error)

// DefaultToolLister 是包级工具探测钩子，外部可注入真实的 MCP 协议探测实现。
var DefaultToolLister ToolListerFunc

// ListMCPTools 获取指定 MCP 服务器提供的工具列表。
// 拿不到工具列表或探测失败时返回错误，供 UI 展示「连接后可见」。
func ListMCPTools(ctx context.Context, srv MCPServer) ([]MCPToolInfo, error) {
	// 仓库内配置在用户信任前不得拉起命令，探测同样要拦住。
	if srv.Untrusted {
		return nil, errors.New("该 MCP 服务来自仓库配置，需在设置中启用以信任后才能使用")
	}
	if DefaultToolLister != nil {
		return DefaultToolLister(ctx, srv)
	}
	return nil, errors.New("工具列表连接后可见")
}

// SecretStore 定义凭据存储接口，兼容 config.SecretStore。
type SecretStore interface {
	Get(account string) (string, error)
	Set(account, value string) error
	Delete(account string) error
}

var mcpSecretStore SecretStore

func init() {
	mcpSecretStore = config.NewSecretStore("mcode.mcp")
}

// SetSecretStore 用于单元测试或外部替换凭据存储实现。
func SetSecretStore(s SecretStore) {
	mcpSecretStore = s
}

func isSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "TOKEN") ||
		strings.Contains(upper, "SECRET") ||
		strings.Contains(upper, "KEY") ||
		strings.Contains(upper, "PASSWORD") ||
		strings.Contains(upper, "AUTH")
}

// isSecretHeaderKey 判断请求头取值是否属于凭据：Authorization 一律加密，其余按名字推断。
func isSecretHeaderKey(key string) bool {
	return strings.EqualFold(strings.TrimSpace(key), "Authorization") || isSecretKey(key)
}

type customMCPServerRecord struct {
	Transport        string            `json:"transport"`
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	URL              string            `json:"url,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	SecretEnvKeys    []string          `json:"secretEnvKeys,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	SecretHeaderKeys []string          `json:"secretHeaderKeys,omitempty"`
	Disabled         bool              `json:"disabled,omitempty"`
	// Auth 只在目录条目声明后写入；旧配置没有该字段时读出 nil，仍可正常使用。
	Auth *MCPCatalogAuth `json:"auth,omitempty"`
}

type customMCPConfigFile struct {
	MCPServers      map[string]customMCPServerRecord `json:"mcpServers,omitempty"`
	DisabledServers []string                         `json:"disabledServers,omitempty"`
	// NewThreadDisabled 记录新会话默认停用的服务键（agent:scope:name）。
	// 空表示全部默认启用；旧文件没有该字段时读出 nil，行为不变。
	NewThreadDisabled []string `json:"newThreadDisabled,omitempty"`
	// TrustedProjectServers 记录用户已信任的仓库内服务指纹，只存在于全局配置里。
	TrustedProjectServers []string `json:"trustedProjectServers,omitempty"`
}

// projectMCPFingerprint 计算仓库内 MCP 服务的信任指纹。
// 项目路径、来源、名称、命令、参数、URL，以及环境变量与请求头的键值任一变化都会改变指纹，
// 需要用户重新确认信任；否则仓库把 env 改成 NODE_OPTIONS=--require ./evil.js 之类的取值，
// 已信任的服务仍被执行，等价于任意代码执行。
func projectMCPFingerprint(projectDir string, srv MCPServer) string {
	parts := make([]string, 0, 6+len(srv.Args))
	parts = append(parts, projectDir, srv.Agent, srv.Source, srv.Name, srv.Command)
	parts = append(parts, srv.Args...)
	parts = append(parts, srv.URL)
	// 键值都参与计算：只算键名拦不住改取值，且按键排序保证 map 遍历顺序不影响结果。
	parts = append(parts, sortedKeyValues("env", srv.Env)...)
	parts = append(parts, sortedKeyValues("header", srv.Headers)...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// sortedKeyValues 把 map 拍平成 kind\x01key\x01value 形式的片段并按 key 排序。
// 片段带前缀且键值用 \x01 分隔，避免 env 与 header 之间、键与值之间拼接出歧义。
func sortedKeyValues(kind string, values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, kind+"\x01"+k+"\x01"+values[k])
	}
	return parts
}

// scanMCPServerWithCredentials 重新扫描同一份配置并取出带凭据的目标服务。
// opts 一般来自设置页（不读凭据），而信任指纹必须包含 Env/Headers，所以要重扫一次；
// 重新扫描找不到目标时回退到传入的 srv，至少不会因为查不到就跳过记录信任。
func scanMCPServerWithCredentials(opts Options, target MCPServer) MCPServer {
	credOpts := opts
	credOpts.WithCredentials = true
	servers, err := ScanMCP(credOpts)
	if err != nil {
		return target
	}
	for _, srv := range servers {
		if srv.Agent == target.Agent && srv.Name == target.Name && sameFilePath(srv.Source, target.Source) {
			return srv
		}
	}
	return target
}

// mcpSourceInProjectDir 判断配置来源文件是否位于项目目录内。
// 仓库内的配置可能来自 clone 的不可信仓库；用户主目录下的配置由用户自己编写，视为可信。
func mcpSourceInProjectDir(source, projectDir string) bool {
	if source == "" || projectDir == "" {
		return false
	}
	rel, err := filepath.Rel(projectDir, source)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sameFilePath 判断两个路径是否指向同一个文件。
func sameFilePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil {
		a = absA
	}
	if errB == nil {
		b = absB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// isUserOwnedMCPSource 判断来源文件是否属于用户自己的全局配置（主目录下的 claude/codex/mcode 配置）。
// 这些文件由用户亲手编写、不随仓库分发，不参与仓库内命令的信任门控。
func isUserOwnedMCPSource(source string, opts Options) bool {
	if source == "" {
		return false
	}
	candidates := []string{getMCPConfigPath(opts, "global")}
	if home, _, err := resolveOptions(opts); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".claude.json"),
			filepath.Join(home, ".codex", "config.toml"),
		)
	}
	for _, candidate := range candidates {
		if sameFilePath(source, candidate) {
			return true
		}
	}
	return false
}

// isRepoOwnedMCPSource 判断来源是否为仓库内、需要用户确认信任的配置文件。
func isRepoOwnedMCPSource(source, projectDir string, opts Options) bool {
	return !isUserOwnedMCPSource(source, opts) && mcpSourceInProjectDir(source, projectDir)
}

// loadTrustedProjectFingerprints 读取全局配置里已信任的仓库内服务指纹（按指纹查表）。
func loadTrustedProjectFingerprints(path string) map[string]bool {
	result := make(map[string]bool)
	cfg, err := loadCustomMCPConfig(path)
	if err != nil {
		return result
	}
	for _, fp := range cfg.TrustedProjectServers {
		result[fp] = true
	}
	return result
}

// trustProjectMCPServer 把仓库内服务的指纹写入全局信任列表（去重）。
// 指纹包含 Env/Headers，调用方传入的服务可能已被脱敏（设置页扫描不开 WithCredentials），
// 这里先按 Agent+来源+名称重新扫描一次拿到带凭据的版本再算。
// 旧版本指纹不含 Env/Headers，升级后已有信任记录全部失效，需要用户重新确认信任。
func trustProjectMCPServer(opts Options, srv MCPServer) error {
	_, projectDir, err := resolveOptions(opts)
	if err != nil || projectDir == "" {
		return nil
	}
	srv = scanMCPServerWithCredentials(opts, srv)
	fp := projectMCPFingerprint(projectDir, srv)
	path := getMCPConfigPath(opts, "global")
	cfg, err := loadCustomMCPConfig(path)
	if err != nil {
		return fmt.Errorf("读取全局 MCP 配置失败，无法记录信任：%w", err)
	}
	for _, existing := range cfg.TrustedProjectServers {
		if existing == fp {
			return nil
		}
	}
	cfg.TrustedProjectServers = append(cfg.TrustedProjectServers, fp)
	sort.Strings(cfg.TrustedProjectServers)
	return saveCustomMCPConfig(path, cfg)
}

// trustScannedProjectServer 把扫描结果中同名且位于仓库内的服务标记为已信任。
// only 非空时只信任与它同一 Agent、同一来源文件的那一份，不连带信任其它来源的同名服务。
// 指纹包含 Env/Headers，而这里的扫描结果可能已经被脱敏，所以统一交给
// trustProjectMCPServer 内部重新扫描一次，保证两种扫描模式下指纹一致。
func trustScannedProjectServer(opts Options, serverName string, only *MCPServer) error {
	_, projectDir, err := resolveOptions(opts)
	if err != nil || projectDir == "" {
		return nil
	}
	// 扫描本身不读凭据，这里的 Env/Headers 只用于匹配目标服务。
	servers, err := ScanMCP(Options{Home: opts.Home, ProjectDir: opts.ProjectDir, DataDir: opts.DataDir})
	if err != nil {
		return nil
	}
	for _, srv := range servers {
		if srv.Name != serverName || !isRepoOwnedMCPSource(srv.Source, projectDir, opts) {
			continue
		}
		if only != nil && (srv.Agent != only.Agent || !sameFilePath(srv.Source, only.Source)) {
			continue
		}
		if err := trustProjectMCPServer(opts, srv); err != nil {
			return err
		}
	}
	return nil
}

func getMCPConfigPath(opts Options, scope string) string {
	if strings.EqualFold(scope, "project") && opts.ProjectDir != "" {
		return filepath.Join(opts.ProjectDir, ".mcode", "mcp.json")
	}
	dataDir := resolveDataDir(opts)
	if dataDir == "" {
		home, _, _ := resolveOptions(opts)
		dataDir = filepath.Join(home, ".mcode")
	}
	return filepath.Join(dataDir, "mcp.json")
}

func loadCustomMCPConfig(filePath string) (customMCPConfigFile, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return customMCPConfigFile{MCPServers: make(map[string]customMCPServerRecord)}, nil
		}
		return customMCPConfigFile{}, err
	}
	var cfg customMCPConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return customMCPConfigFile{}, err
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]customMCPServerRecord)
	}
	return cfg, nil
}

func saveCustomMCPConfig(filePath string, cfg customMCPConfigFile) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filePath, data, 0600)
}

// NewThreadDisabledServers 读取全局配置里「新会话默认停用」的服务键列表。
// 键格式为 agent:scope:name，由调用方生成；agentconf 不认识具体服务。
// 缺少文件或解析失败时返回 nil，表示全部默认启用。
func NewThreadDisabledServers(opts Options) []string {
	cfg, err := loadCustomMCPConfig(getMCPConfigPath(opts, "global"))
	if err != nil || len(cfg.NewThreadDisabled) == 0 {
		return nil
	}
	keys := make([]string, len(cfg.NewThreadDisabled))
	copy(keys, cfg.NewThreadDisabled)
	return keys
}

// SetNewThreadDisabledServers 覆盖全局配置里的「新会话默认停用」列表。
// 读改写同一份文件，其余字段（disabledServers、trustedProjectServers、mcpServers）原样保留。
func SetNewThreadDisabledServers(opts Options, keys []string) error {
	filePath := getMCPConfigPath(opts, "global")
	// 读失败直接报错，不拿空配置覆盖全局服务与信任列表。
	cfg, err := loadCustomMCPConfig(filePath)
	if err != nil {
		return fmt.Errorf("读取 MCP 配置文件 %s 失败：%w", filePath, err)
	}
	// 去重并排序：同一份名单写回稳定，空串不是合法键，直接丢弃。
	seen := make(map[string]bool, len(keys))
	cleaned := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		cleaned = append(cleaned, k)
	}
	sort.Strings(cleaned)
	if len(cleaned) == 0 {
		// 空名单与旧文件的 nil 等价，omitempty 不会写出该字段。
		cleaned = nil
	}
	cfg.NewThreadDisabled = cleaned
	if err := saveCustomMCPConfig(filePath, cfg); err != nil {
		return fmt.Errorf("写入 MCP 配置文件 %s 失败：%w", filePath, err)
	}
	return nil
}

// writeFileAtomic 先写同目录临时文件再 rename：写盘中断或并发写只会留下半截临时文件，
// 目标配置不会变成非法 JSON。
func writeFileAtomic(filePath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filePath, data, perm)
}

// SaveMCPServer 持久化自定义 MCP 服务配置。敏感变量自动存入 SecretStore。
func SaveMCPServer(opts Options, srv MCPServer) error {
	if srv.ReadOnly {
		return errors.New("只读来源不可修改")
	}
	if strings.TrimSpace(srv.Name) == "" {
		return errors.New("MCP 服务名称不能为空")
	}
	// 非法登录方式不落盘，旧配置里的字段值为空时仍按未声明处理。
	if err := validateMCPAuth(srv.Auth); err != nil {
		return err
	}
	filePath := getMCPConfigPath(opts, srv.Scope)
	// 读不出来时不能当成空配置写回，否则其余服务会被整体覆盖掉。
	cfg, err := loadCustomMCPConfig(filePath)
	if err != nil {
		return fmt.Errorf("读取 MCP 配置文件 %s 失败：%w", filePath, err)
	}

	// 列表扫描不带凭据，编辑表单回填的敏感值可能为空或整组缺失；
	// 先把同名旧记录读出来，空值一律沿用凭据库里的旧值。
	oldRec := cfg.MCPServers[srv.Name]

	// 显式声明的敏感变量优先，其次按变量名推断（TOKEN/KEY/SECRET 等）。
	explicitEnvSecrets := make(map[string]bool, len(srv.SecretEnvKeys))
	for _, k := range srv.SecretEnvKeys {
		explicitEnvSecrets[k] = true
	}

	var secretEnvKeys []string
	plainEnv := make(map[string]string)
	for k, v := range srv.Env {
		if !(explicitEnvSecrets[k] || isSecretKey(k)) || mcpSecretStore == nil {
			plainEnv[k] = v
			continue
		}
		// 空值表示本次没有提交新凭据，不调 Set，凭据库里的旧值原封不动。
		if v != "" {
			if err := mcpSecretStore.Set("mcp."+srv.Name+"."+k, v); err != nil {
				return fmt.Errorf("保存 MCP 服务 %s 的敏感环境变量 %s 失败，请检查系统凭据库", srv.Name, k)
			}
		}
		secretEnvKeys = append(secretEnvKeys, k)
	}
	sort.Strings(secretEnvKeys)

	// 请求头同样区分明文与明文外的凭据，Authorization 等取值不再落盘。
	var secretHeaderKeys []string
	plainHeaders := make(map[string]string)
	submittedHeaders := make(map[string]bool, len(srv.Headers))
	for k, v := range srv.Headers {
		submittedHeaders[k] = true
		if !isSecretHeaderKey(k) || mcpSecretStore == nil {
			plainHeaders[k] = v
			continue
		}
		// 同环境变量：空值不覆盖凭据库。
		if v != "" {
			if err := mcpSecretStore.Set("mcp."+srv.Name+".header."+k, v); err != nil {
				return fmt.Errorf("保存 MCP 服务 %s 的敏感请求头 %s 失败，请检查系统凭据库", srv.Name, k)
			}
		}
		secretHeaderKeys = append(secretHeaderKeys, k)
	}
	// 敏感头本次完全没提交（典型是编辑表单没回填 Authorization）时，旧凭据仍需保留。
	for _, k := range oldRec.SecretHeaderKeys {
		if submittedHeaders[k] {
			continue
		}
		secretHeaderKeys = append(secretHeaderKeys, k)
	}
	sort.Strings(secretHeaderKeys)

	trans := srv.Transport
	if trans == "" {
		if srv.URL != "" {
			trans = "http"
		} else {
			trans = "stdio"
		}
	}

	rec := customMCPServerRecord{
		Transport:        trans,
		Command:          srv.Command,
		Args:             srv.Args,
		URL:              srv.URL,
		Env:              plainEnv,
		SecretEnvKeys:    secretEnvKeys,
		Headers:          plainHeaders,
		SecretHeaderKeys: secretHeaderKeys,
		Disabled:         srv.Disabled,
		Auth:             cloneCatalogAuth(srv.Auth),
	}
	cfg.MCPServers[srv.Name] = rec
	if err := saveCustomMCPConfig(filePath, cfg); err != nil {
		return err
	}

	// 项目级配置是用户在当前仓库界面亲手创建的，同时记入信任列表，
	// 否则下次扫描会把它当成仓库自带的未信任服务。
	if strings.EqualFold(srv.Scope, "project") {
		absSource, err := filepath.Abs(filePath)
		if err != nil {
			absSource = filePath
		}
		trustedSrv := srv
		trustedSrv.Agent = "custom"
		trustedSrv.Source = absSource
		trustedSrv.Command = rec.Command
		trustedSrv.Args = rec.Args
		trustedSrv.URL = rec.URL
		if err := trustProjectMCPServer(opts, trustedSrv); err != nil {
			return err
		}
	}
	return nil
}

// deleteMCPSecrets 清除某个服务名下的全部凭据条目。
func deleteMCPSecrets(serverName string, rec customMCPServerRecord) {
	if mcpSecretStore == nil {
		return
	}
	for _, secKey := range rec.SecretEnvKeys {
		_ = mcpSecretStore.Delete("mcp." + serverName + "." + secKey)
	}
	for _, secKey := range rec.SecretHeaderKeys {
		_ = mcpSecretStore.Delete("mcp." + serverName + ".header." + secKey)
	}
}

// deleteMCPServerAtPath 从单个配置文件里删除服务记录、凭据与停用记录。
// 文件不存在时视为没有该服务；其余读盘或写盘失败都要报错，
// 否则界面会显示删除成功而配置原封不动。
// dropSecrets 为 false 时只删记录（凭据按服务名存放，另一个作用域还在用同名服务）。
func deleteMCPServerAtPath(path, serverName string, dropSecrets bool) error {
	cfg, err := loadCustomMCPConfig(path)
	if err != nil {
		return fmt.Errorf("读取 MCP 配置文件 %s 失败：%w", path, err)
	}
	changed := false
	if rec, ok := cfg.MCPServers[serverName]; ok {
		if dropSecrets {
			deleteMCPSecrets(serverName, rec)
		}
		delete(cfg.MCPServers, serverName)
		changed = true
	}
	if len(cfg.DisabledServers) > 0 {
		kept := make([]string, 0, len(cfg.DisabledServers))
		for _, d := range cfg.DisabledServers {
			if d != serverName {
				kept = append(kept, d)
			}
		}
		if len(kept) != len(cfg.DisabledServers) {
			cfg.DisabledServers = kept
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := saveCustomMCPConfig(path, cfg); err != nil {
		return fmt.Errorf("写入 MCP 配置文件 %s 失败：%w", path, err)
	}
	return nil
}

// otherMCPScope 返回另一个作用域的名字。
func otherMCPScope(scope string) string {
	if strings.EqualFold(scope, "project") {
		return "global"
	}
	return "project"
}

// DeleteMCPServerInScope 只删除指定作用域（global/project）下的同名服务，
// 另一个作用域的同名服务保持不变。作用域直接取自列表里该服务的 Scope。
func DeleteMCPServerInScope(opts Options, scope, serverName string) error {
	if strings.TrimSpace(serverName) == "" {
		return errors.New("serverName 不能为空")
	}
	filePath := getMCPConfigPath(opts, scope)
	// 凭据库里按「服务名.键」存放，两个作用域同名时会共用，另一边还在就不能删。
	dropSecrets := true
	if otherPath := getMCPConfigPath(opts, otherMCPScope(scope)); otherPath != filePath {
		if otherCfg, err := loadCustomMCPConfig(otherPath); err == nil {
			if _, ok := otherCfg.MCPServers[serverName]; ok {
				dropSecrets = false
			}
		}
	}
	if err := deleteMCPServerAtPath(filePath, serverName, dropSecrets); err != nil {
		return err
	}
	if !strings.EqualFold(scope, "project") {
		return nil
	}
	// 全局配置的 DisabledServers 对 ScanMCP 里的所有同名服务生效，而删项目级那份时
	// 只清理了项目文件自己的列表，全局条目会残留，之后新建同名服务会莫名处于停用状态。
	// 所以重新扫描一遍（不带凭据）：只有任何 Agent、任何作用域都不再有同名服务时才清理，
	// 还有同名服务时保留条目——那是它们自己的停用状态。
	servers, err := ScanMCP(Options{Home: opts.Home, ProjectDir: opts.ProjectDir, DataDir: opts.DataDir})
	if err != nil {
		// 扫描失败时不动全局停用列表：删除本身已成功，凭猜测清理可能误清同名服务的状态。
		return nil
	}
	for _, s := range servers {
		if s.Name == serverName {
			return nil
		}
	}
	globalPath := getMCPConfigPath(opts, "global")
	cfg, err := loadCustomMCPConfig(globalPath)
	if err != nil {
		return fmt.Errorf("读取 MCP 配置文件 %s 失败：%w", globalPath, err)
	}
	kept := make([]string, 0, len(cfg.DisabledServers))
	for _, d := range cfg.DisabledServers {
		if d != serverName {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(cfg.DisabledServers) {
		return nil
	}
	cfg.DisabledServers = kept
	if err := saveCustomMCPConfig(globalPath, cfg); err != nil {
		return fmt.Errorf("写入 MCP 配置文件 %s 失败：%w", globalPath, err)
	}
	return nil
}

// DeleteMCPServer 删除全局与项目配置中的同名服务及其凭据（两个作用域都删）。
func DeleteMCPServer(opts Options, serverName string) error {
	if strings.TrimSpace(serverName) == "" {
		return errors.New("serverName 不能为空")
	}
	paths := []string{getMCPConfigPath(opts, "global")}
	if opts.ProjectDir != "" {
		paths = append(paths, getMCPConfigPath(opts, "project"))
	}
	for _, p := range paths {
		if err := deleteMCPServerAtPath(p, serverName, true); err != nil {
			return err
		}
	}
	return nil
}

// copyMCPSecretKeys 把旧服务名下缺失的凭据复制到新服务名，返回合并后的键列表。
// prefix 为空表示环境变量凭据，header. 表示请求头凭据。
func copyMCPSecretKeys(oldName, newName string, oldKeys, newKeys []string, prefix string) ([]string, error) {
	merged := make([]string, 0, len(oldKeys)+len(newKeys))
	seen := make(map[string]bool, len(oldKeys)+len(newKeys))
	addKeys := func(keys []string) {
		for _, k := range keys {
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			merged = append(merged, k)
		}
	}
	addKeys(oldKeys)
	addKeys(newKeys)
	// 新记录上可能已经列了键（表单提交了空值），但凭据库里没有新名下的值，
	// 这种键同样要从旧名迁移，所以按键逐个判断而不是只看键列表。
	if mcpSecretStore != nil {
		for _, k := range merged {
			newAccount := "mcp." + newName + "." + prefix + k
			// 新名下已有值：本次提交了新凭据，不能被旧值覆盖。
			if _, err := mcpSecretStore.Get(newAccount); err == nil {
				continue
			}
			val, err := mcpSecretStore.Get("mcp." + oldName + "." + prefix + k)
			if err != nil {
				continue
			}
			if setErr := mcpSecretStore.Set(newAccount, val); setErr != nil {
				return nil, fmt.Errorf("复制 MCP 服务 %s 的凭据 %s 失败，请检查系统凭据库", newName, k)
			}
		}
	}
	sort.Strings(merged)
	return merged, nil
}

// RenameMCPServer 把同一作用域下 oldName 的记录改名为 newName，并迁移凭据库里的条目。
// 编辑表单回填的敏感值可能为空，新记录上就缺这些凭据键；这时先把旧名的凭据复制到新名，
// 再删旧名。复制或写盘任一步失败都保留旧记录，不会把凭据删掉却拿不回原配置。
func RenameMCPServer(opts Options, scope, oldName, newName string) error {
	if strings.TrimSpace(oldName) == "" || strings.TrimSpace(newName) == "" {
		return errors.New("改名需要同时给出新旧服务名")
	}
	if oldName == newName {
		return nil
	}
	filePath := getMCPConfigPath(opts, scope)
	cfg, err := loadCustomMCPConfig(filePath)
	if err != nil {
		return fmt.Errorf("读取 MCP 配置文件 %s 失败：%w", filePath, err)
	}
	oldRec, ok := cfg.MCPServers[oldName]
	if !ok {
		// 旧记录不在这个配置文件里（例如来自 Agent 自带配置），没有可迁移的内容。
		return nil
	}
	newRec, hasNew := cfg.MCPServers[newName]
	if !hasNew {
		// 调用方还没写新记录时直接沿用旧记录内容，只换名字。
		newRec = oldRec
	}
	envKeys, err := copyMCPSecretKeys(oldName, newName, oldRec.SecretEnvKeys, newRec.SecretEnvKeys, "")
	if err != nil {
		return err
	}
	headerKeys, err := copyMCPSecretKeys(oldName, newName, oldRec.SecretHeaderKeys, newRec.SecretHeaderKeys, "header.")
	if err != nil {
		return err
	}
	newRec.SecretEnvKeys = envKeys
	newRec.SecretHeaderKeys = headerKeys

	cfg.MCPServers[newName] = newRec
	delete(cfg.MCPServers, oldName)
	// 停用列表按服务名记录，改名后同步替换，否则旧名残留、改名后的服务还会被停用。
	for i, d := range cfg.DisabledServers {
		if d == oldName {
			cfg.DisabledServers[i] = newName
		}
	}
	if err := saveCustomMCPConfig(filePath, cfg); err != nil {
		return fmt.Errorf("写入 MCP 配置文件 %s 失败：%w", filePath, err)
	}
	// 配置已经指向新名，最后才清旧凭据；清不掉只是残留，不影响使用。
	deleteMCPSecrets(oldName, oldRec)
	return nil
}

// SetMCPServerEnabled 设置 MCP 服务的启用/禁用状态。
// 启用时会信任所有同名的仓库内服务；界面上针对某一条服务操作时用 SetMCPServerEnabledFor。
func SetMCPServerEnabled(opts Options, serverName string, enabled bool) error {
	return setMCPServerEnabled(opts, serverName, enabled, nil)
}

// SetMCPServerEnabledFor 按扫描到的某一条服务设置启用状态：启用时只信任这一条
// （同一 Agent、同一来源文件），其它 Agent 配置里的同名仓库服务仍需各自确认。
func SetMCPServerEnabledFor(opts Options, srv MCPServer, enabled bool) error {
	if srv.ReadOnly {
		return errors.New("只读来源不可修改")
	}
	return setMCPServerEnabled(opts, srv.Name, enabled, &srv)
}

func setMCPServerEnabled(opts Options, serverName string, enabled bool, only *MCPServer) error {
	if strings.TrimSpace(serverName) == "" {
		return errors.New("serverName 不能为空")
	}
	// 启用仓库内来源的服务等于用户确认信任它的命令，先把指纹记进全局信任列表；
	// 停用时不移除信任，重新启用无需再确认。
	if enabled && strings.TrimSpace(opts.ProjectDir) != "" {
		if err := trustScannedProjectServer(opts, serverName, only); err != nil {
			return err
		}
	}
	paths := []string{getMCPConfigPath(opts, "global")}
	if opts.ProjectDir != "" {
		paths = append(paths, getMCPConfigPath(opts, "project"))
	}
	for _, p := range paths {
		cfg, err := loadCustomMCPConfig(p)
		if err != nil {
			return fmt.Errorf("读取 MCP 配置文件 %s 失败：%w", p, err)
		}
		if rec, ok := cfg.MCPServers[serverName]; ok {
			rec.Disabled = !enabled
			cfg.MCPServers[serverName] = rec
			if err := saveCustomMCPConfig(p, cfg); err != nil {
				return fmt.Errorf("写入 MCP 配置文件 %s 失败：%w", p, err)
			}
		}
	}

	targetPath := getMCPConfigPath(opts, "global")
	// 同 SaveMCPServer：读失败直接报错，不拿空配置覆盖全局停用列表。
	cfg, err := loadCustomMCPConfig(targetPath)
	if err != nil {
		return fmt.Errorf("读取 MCP 配置文件 %s 失败：%w", targetPath, err)
	}
	disabledMap := make(map[string]bool)
	for _, d := range cfg.DisabledServers {
		disabledMap[d] = true
	}
	if enabled {
		delete(disabledMap, serverName)
	} else {
		disabledMap[serverName] = true
	}
	var newDisabled []string
	for k := range disabledMap {
		newDisabled = append(newDisabled, k)
	}
	sort.Strings(newDisabled)
	cfg.DisabledServers = newDisabled
	return saveCustomMCPConfig(targetPath, cfg)
}

type claudeProjectConfigJSON struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	// DisabledMcpServers 是用户在该目录用 /mcp 关掉的服务名，会盖住全局、项目与插件带来的同名服务。
	DisabledMcpServers []string `json:"disabledMcpServers"`
	// DisabledMcpjsonServers 是用户在该目录停用的 .mcp.json 服务名。
	DisabledMcpjsonServers []string `json:"disabledMcpjsonServers"`
}

type claudeConfigFileJSON struct {
	MCPServers map[string]json.RawMessage         `json:"mcpServers"`
	Projects   map[string]claudeProjectConfigJSON `json:"projects"`
}

// claudeProjectKeyCandidates 返回用于匹配 ~/.claude.json projects 键的候选路径。
// 原路径优先，再补 EvalSymlinks 后的真实路径：macOS 上 /tmp 指向 /private/tmp、
// /var 指向 /private/var，Claude Code 记录的是真实路径，而调用方传入的可能是链接路径。
func claudeProjectKeyCandidates(projectDir string) []string {
	if projectDir == "" {
		return nil
	}
	candidates := []string{projectDir}
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil && resolved != projectDir {
		candidates = append(candidates, resolved)
	}
	return candidates
}

// claudeEngineDisabledSets 从 ~/.claude.json 的项目条目里取出引擎侧停用名单。
// 原路径与真实路径两个键都命中时取并集，避免符号链接导致漏判。
func claudeEngineDisabledSets(cfg claudeConfigFileJSON, projectDir string) (disabled, disabledMcpjson map[string]bool) {
	disabled = make(map[string]bool)
	disabledMcpjson = make(map[string]bool)
	for _, key := range claudeProjectKeyCandidates(projectDir) {
		proj, ok := cfg.Projects[key]
		if !ok {
			continue
		}
		for _, name := range proj.DisabledMcpServers {
			disabled[name] = true
		}
		for _, name := range proj.DisabledMcpjsonServers {
			disabledMcpjson[name] = true
		}
	}
	return disabled, disabledMcpjson
}

// markEngineDisabledBySets 在引擎侧停用名单命中时打展示标记，不改 Disabled：
// mcode 的开关与引擎侧状态分开，注入行为仍由 mcode 自己决定。
func markEngineDisabledBySets(srv *MCPServer, sets ...map[string]bool) {
	for _, set := range sets {
		if set[srv.Name] {
			srv.DisabledByEngine = true
			return
		}
	}
}

type mcpConfigFileJSON struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

type rawClaudeMCPServerItem struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	URL     string   `json:"url"`
	// ServerURL 是 agy 等外部 Agent 配置里远程服务使用的地址字段名，与 url 等价。
	ServerURL string            `json:"serverUrl"`
	Env       map[string]any    `json:"env"`
	Headers   map[string]string `json:"headers"`
}

// ScanMCP 扫描 claude、codex 与外部 Agent（agy、pi）的 MCP 配置。
// agy（~/.gemini/config/mcp_config.json）与 pi（~/.pi/agent/mcp.json）的条目只做只读展示：
// 标记 ReadOnly，不参与停用表与未信任判定，也不会被注入会话（见 internal/mcp/session.go）。
func ScanMCP(opts Options) ([]MCPServer, error) {
	home, projectDir, homeErr := resolveOptions(opts)

	var attemptedSources int
	var readErrors []error

	var servers []MCPServer
	// projectFingerprints 与 servers 同序，只对仓库内来源的服务写指纹，其余留空。
	// 指纹必须包含 Env/Headers，而列表扫描默认会把它们脱敏，所以在这里、清空凭据之前算好；
	// 设置页拿到的服务是脱敏的，信任记录也用这份指纹，两种扫描模式才能对得上。
	var projectFingerprints []string
	seen := make(map[string]bool) // key: agent + ":" + scope + ":" + name

	addServer := func(s MCPServer) {
		key := s.Agent + ":" + s.Scope + ":" + s.Name
		if seen[key] {
			return
		}
		seen[key] = true
		var fp string
		// 只读展示的外部配置不属于仓库内来源，不参与信任门控。
		if !s.ReadOnly && projectDir != "" && isRepoOwnedMCPSource(s.Source, projectDir, opts) {
			fp = projectMCPFingerprint(projectDir, s)
		}
		if !opts.WithCredentials {
			s.Env = nil
			s.Headers = nil
		}
		servers = append(servers, s)
		projectFingerprints = append(projectFingerprints, fp)
	}

	// 1. Claude 全局与项目：Home/.claude.json
	// 引擎侧停用名单只用于展示标记，不参与 mcode 的停用判定。
	var claudeEngineDisabled, claudeEngineDisabledMcpjson map[string]bool
	if homeErr == nil && home != "" {
		claudePath := filepath.Join(home, ".claude.json")
		attemptedSources++
		data, err := os.ReadFile(claudePath)
		if err != nil {
			if !os.IsNotExist(err) {
				readErrors = append(readErrors, err)
			}
		} else {
			absPath, _ := filepath.Abs(claudePath)
			var cfg claudeConfigFileJSON
			if err := json.Unmarshal(data, &cfg); err == nil {
				claudeEngineDisabled, claudeEngineDisabledMcpjson = claudeEngineDisabledSets(cfg, projectDir)
				// a) claude 全局
				for name, raw := range cfg.MCPServers {
					srv, err := parseClaudeItem(name, "global", absPath, raw)
					if err == nil {
						markEngineDisabledBySets(&srv, claudeEngineDisabled)
						addServer(srv)
					}
				}
				// b) claude 项目 (Home/.claude.json projects 段)
				for _, key := range claudeProjectKeyCandidates(projectDir) {
					projCfg, ok := cfg.Projects[key]
					if !ok {
						continue
					}
					for name, raw := range projCfg.MCPServers {
						srv, err := parseClaudeItem(name, "project", absPath, raw)
						if err == nil {
							markEngineDisabledBySets(&srv, claudeEngineDisabled, claudeEngineDisabledMcpjson)
							addServer(srv)
						}
					}
				}
			}
		}
	} else if homeErr != nil && opts.Home == "" {
		attemptedSources++
		readErrors = append(readErrors, homeErr)
	}

	// 2. Claude 项目：ProjectDir/.mcp.json
	if projectDir != "" {
		mcpPath := filepath.Join(projectDir, ".mcp.json")
		attemptedSources++
		data, err := os.ReadFile(mcpPath)
		if err != nil {
			if !os.IsNotExist(err) {
				readErrors = append(readErrors, err)
			}
		} else {
			absPath, _ := filepath.Abs(mcpPath)
			var cfg mcpConfigFileJSON
			if err := json.Unmarshal(data, &cfg); err == nil {
				for name, raw := range cfg.MCPServers {
					srv, err := parseClaudeItem(name, "project", absPath, raw)
					if err == nil {
						// .mcp.json 的服务受 disabledMcpjsonServers 与 disabledMcpServers 两个名单约束。
						markEngineDisabledBySets(&srv, claudeEngineDisabledMcpjson, claudeEngineDisabled)
						addServer(srv)
					}
				}
			}
		}
	}

	// 3. Codex 全局：Home/.codex/config.toml
	if homeErr == nil && home != "" {
		codexGlobalPath := filepath.Join(home, ".codex", "config.toml")
		attemptedSources++
		data, err := os.ReadFile(codexGlobalPath)
		if err != nil {
			if !os.IsNotExist(err) {
				readErrors = append(readErrors, err)
			}
		} else {
			absPath, _ := filepath.Abs(codexGlobalPath)
			list := parseCodexTOML(string(data), "codex", "global", absPath)
			for _, srv := range list {
				addServer(srv)
			}
		}
	} else if homeErr != nil && opts.Home == "" {
		attemptedSources++
		readErrors = append(readErrors, homeErr)
	}

	// 4. Codex 项目：ProjectDir/.codex/config.toml
	if projectDir != "" {
		codexProjPath := filepath.Join(projectDir, ".codex", "config.toml")
		attemptedSources++
		data, err := os.ReadFile(codexProjPath)
		if err != nil {
			if !os.IsNotExist(err) {
				readErrors = append(readErrors, err)
			}
		} else {
			absPath, _ := filepath.Abs(codexProjPath)
			list := parseCodexTOML(string(data), "codex", "project", absPath)
			for _, srv := range list {
				addServer(srv)
			}
		}
	}

	// 4.5 Claude Code 已启用插件自带的 MCP 服务（claude 以 --strict-mcp-config 启动时不会再自行加载它们）
	if homeErr == nil && home != "" {
		for _, srv := range scanPluginMCP(home) {
			addServer(srv)
		}
	}

	// 4.6 外部 Agent 配置：agy 与 pi。仅做只读展示，不参与注入与启停。
	if homeErr == nil && home != "" {
		externalSources := []struct {
			agent string
			path  string
		}{
			{agent: "agy", path: filepath.Join(home, ".gemini", "config", "mcp_config.json")},
			{agent: "pi", path: filepath.Join(home, ".pi", "agent", "mcp.json")},
		}
		for _, ext := range externalSources {
			for _, srv := range scanReadOnlyMCPFile(ext.path, ext.agent) {
				addServer(srv)
			}
		}
	}

	// 5. mcode 自定义全局与项目 MCP 配置
	customSources := []struct {
		scope string
		path  string
	}{
		{scope: "global", path: getMCPConfigPath(opts, "global")},
	}
	if projectDir != "" {
		customSources = append(customSources, struct {
			scope string
			path  string
		}{scope: "project", path: getMCPConfigPath(opts, "project")})
	}

	disabledSet := make(map[string]bool)
	for _, cSrc := range customSources {
		cfg, err := loadCustomMCPConfig(cSrc.path)
		if err != nil {
			continue
		}
		for _, d := range cfg.DisabledServers {
			disabledSet[d] = true
		}
		absPath, _ := filepath.Abs(cSrc.path)
		for name, rec := range cfg.MCPServers {
			var envKeys []string
			seenKeys := make(map[string]bool)
			for k := range rec.Env {
				if !seenKeys[k] {
					seenKeys[k] = true
					envKeys = append(envKeys, k)
				}
			}
			for _, k := range rec.SecretEnvKeys {
				if !seenKeys[k] {
					seenKeys[k] = true
					envKeys = append(envKeys, k)
				}
			}
			sort.Strings(envKeys)

			srv := MCPServer{
				Agent:     "custom",
				Scope:     cSrc.scope,
				Name:      name,
				Transport: rec.Transport,
				Command:   rec.Command,
				Args:      rec.Args,
				URL:       rec.URL,
				EnvKeys:   envKeys,
				Source:    absPath,
				Disabled:  rec.Disabled || disabledSet[name],
				Auth:      cloneCatalogAuth(rec.Auth),
			}
			if opts.WithCredentials {
				srv.Env = make(map[string]string)
				for k, v := range rec.Env {
					srv.Env[k] = v
				}
				for _, k := range rec.SecretEnvKeys {
					if mcpSecretStore != nil {
						if val, err := mcpSecretStore.Get("mcp." + name + "." + k); err == nil {
							srv.Env[k] = val
						}
					}
				}
				if len(rec.Headers) > 0 || len(rec.SecretHeaderKeys) > 0 {
					srv.Headers = make(map[string]string, len(rec.Headers)+len(rec.SecretHeaderKeys))
					for k, v := range rec.Headers {
						srv.Headers[k] = v
					}
					for _, k := range rec.SecretHeaderKeys {
						if mcpSecretStore != nil {
							if val, err := mcpSecretStore.Get("mcp." + name + ".header." + k); err == nil {
								srv.Headers[k] = val
							}
						}
					}
				}
			}
			addServer(srv)
		}
	}

	// 停用表按名字跨 Agent 生效，只读来源不可停用，这里直接跳过。
	for i := range servers {
		if servers[i].ReadOnly {
			continue
		}
		if disabledSet[servers[i].Name] {
			servers[i].Disabled = true
		}
	}

	// 仓库内来源的配置可能随 clone 一起进来：未在全局配置里信任的服务一律禁用且不执行命令。
	// 这里用 addServer 阶段预先算好的指纹（含 Env/Headers）；本段在 sortMCPServers 之前，与 servers 同序。
	if projectDir != "" {
		trusted := loadTrustedProjectFingerprints(getMCPConfigPath(opts, "global"))
		for i := range servers {
			if projectFingerprints[i] == "" {
				continue
			}
			if !trusted[projectFingerprints[i]] {
				servers[i].Untrusted = true
				servers[i].Disabled = true
			}
		}
	}

	if attemptedSources > 0 && len(readErrors) == attemptedSources {
		return nil, errors.Join(readErrors...)
	}

	sortMCPServers(servers)
	if servers == nil {
		servers = []MCPServer{}
	}
	return servers, nil
}

// scanReadOnlyMCPFile 读取外部 Agent（agy、pi）的 MCP 配置文件，返回只读展示用的服务列表。
// 文件不存在或解不动一律静默返回 nil：这些外部配置不是 mcode 自己的配置，不能因为读失败就报错。
func scanReadOnlyMCPFile(path, agent string) []MCPServer {
	var cfg mcpConfigFileJSON
	if !readJSONFile(path, &cfg) {
		return nil
	}
	absPath, _ := filepath.Abs(path)
	servers := make([]MCPServer, 0, len(cfg.MCPServers))
	for name, raw := range cfg.MCPServers {
		srv, err := parseClaudeItem(name, "global", absPath, raw)
		if err != nil {
			continue
		}
		srv.Agent = agent
		srv.Scope = "global"
		srv.Source = absPath
		srv.ReadOnly = true
		// 只读来源仅作展示，凭据一律不展开：Env 值可能含文件内明文 token，
		// Headers 同理，只保留键名让用户知道配了什么。
		srv.Env = nil
		srv.SecretEnvKeys = nil
		srv.Headers = nil
		servers = append(servers, srv)
	}
	return servers
}

func parseClaudeItem(name, scope, source string, raw json.RawMessage) (MCPServer, error) {
	var item rawClaudeMCPServerItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return MCPServer{}, err
	}

	url := item.URL
	if url == "" && item.ServerURL != "" {
		url = item.ServerURL
	}

	transport := item.Type
	if transport == "" {
		if url != "" {
			transport = "http"
		} else {
			transport = "stdio"
		}
	}

	var envKeys []string
	env := make(map[string]string, len(item.Env))
	if len(item.Env) > 0 {
		envKeys = make([]string, 0, len(item.Env))
		for k := range item.Env {
			envKeys = append(envKeys, k)
			if value, ok := item.Env[k].(string); ok {
				env[k] = os.ExpandEnv(value)
			}
		}
		sort.Strings(envKeys)
	}

	return MCPServer{
		Agent:     "claude",
		Scope:     scope,
		Name:      name,
		Transport: transport,
		Command:   item.Command,
		Args:      item.Args,
		URL:       url,
		EnvKeys:   envKeys,
		Source:    source,
		Env:       env,
		Headers:   expandValues(item.Headers),
	}, nil
}

type tomlServerEntry struct {
	server  MCPServer
	envKeys map[string]bool
}

// parseCodexTOML 手写最小 TOML 解析。
func parseCodexTOML(content, agent, scope, source string) []MCPServer {
	lines := strings.Split(content, "\n")

	var order []string
	entries := make(map[string]*tomlServerEntry)

	currentServer := ""
	inEnvSubtable := false
	ignoreSubtable := false
	subtable := ""

	getOrCreate := func(name string) *tomlServerEntry {
		if ent, ok := entries[name]; ok {
			return ent
		}
		order = append(order, name)
		ent := &tomlServerEntry{
			server: MCPServer{
				Agent:  agent,
				Scope:  scope,
				Name:   name,
				Source: source,
			},
			envKeys: make(map[string]bool),
		}
		entries[name] = ent
		return ent
	}

	for lineIdx := 0; lineIdx < len(lines); lineIdx++ {
		line := stripComment(lines[lineIdx])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// 表头判断
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && !strings.HasPrefix(trimmed, "[[") {
			header := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if !strings.HasPrefix(header, "mcp_servers.") {
				currentServer = ""
				inEnvSubtable = false
				subtable = ""
				continue
			}

			parts := splitTOMLDottedKey(strings.TrimPrefix(header, "mcp_servers."))
			currentServer = parts[0]
			getOrCreate(currentServer)
			subtable = ""
			if len(parts) == 2 {
				switch parts[1] {
				case "env", "http_headers", "env_http_headers":
					subtable = parts[1]
				}
			}
			inEnvSubtable = subtable != ""
			ignoreSubtable = len(parts) > 1 && !inEnvSubtable
			continue
		}

		if currentServer == "" || ignoreSubtable {
			continue
		}

		entry := entries[currentServer]

		if inEnvSubtable {
			// 解析 .env 子表下的各个 key
			eqIdx := strings.Index(trimmed, "=")
			if eqIdx != -1 {
				k := strings.TrimSpace(trimmed[:eqIdx])
				k = parseTOMLTableName(k)
				if k != "" {
					setTOMLMapValue(entry, subtable, k, parseTOMLString(strings.TrimSpace(trimmed[eqIdx+1:])))
				}
			}
			continue
		}

		// 解析 [mcp_servers.NAME] 主表
		eqIdx := strings.Index(trimmed, "=")
		if eqIdx == -1 {
			continue
		}

		k := strings.TrimSpace(trimmed[:eqIdx])
		val := strings.TrimSpace(trimmed[eqIdx+1:])

		switch k {
		case "enabled":
			entry.server.Disabled = val == "false"
		case "bearer_token_env_var":
			if token := os.Getenv(parseTOMLString(val)); token != "" {
				setTOMLMapValue(entry, "http_headers", "Authorization", "Bearer "+token)
			}
		case "command":
			entry.server.Command = parseTOMLString(val)
		case "url":
			entry.server.URL = parseTOMLString(val)
		case "args":
			arrayStr := val
			bracketCount := countBrackets(arrayStr)
			for bracketCount > 0 && lineIdx+1 < len(lines) {
				lineIdx++
				nextLine := stripComment(lines[lineIdx])
				arrayStr += "\n" + nextLine
				bracketCount = countBrackets(arrayStr)
			}
			entry.server.Args = parseStringArray(arrayStr)
		case "env", "http_headers", "env_http_headers":
			for key, value := range parseInlineStringMap(val) {
				setTOMLMapValue(entry, k, key, value)
			}
		}
	}

	var result []MCPServer
	for _, name := range order {
		ent := entries[name]
		if ent.server.URL != "" {
			ent.server.Transport = "http"
		} else {
			ent.server.Transport = "stdio"
		}

		if len(ent.envKeys) > 0 {
			var envKeys []string
			for k := range ent.envKeys {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			ent.server.EnvKeys = envKeys
		}
		result = append(result, ent.server)
	}

	return result
}

// splitTOMLDottedKey 按引号外的点拆分表名。
func splitTOMLDottedKey(s string) []string {
	var parts []string
	start, quote, escaped := 0, byte(0), false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 {
			if ch == '\\' && quote == '"' {
				escaped = true
			} else if ch == quote {
				quote = 0
			}
		} else if ch == '"' || ch == '\'' {
			quote = ch
		} else if ch == '.' {
			parts = append(parts, parseTOMLTableName(s[start:i]))
			start = i + 1
		}
	}
	return append(parts, parseTOMLTableName(s[start:]))
}

func expandValues(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = os.ExpandEnv(value)
	}
	return result
}

func setTOMLMapValue(entry *tomlServerEntry, table, key, value string) {
	if table == "env" {
		entry.envKeys[key] = true
		if entry.server.Env == nil {
			entry.server.Env = make(map[string]string)
		}
		entry.server.Env[key] = value
		return
	}
	if entry.server.Headers == nil {
		entry.server.Headers = make(map[string]string)
	}
	if table == "env_http_headers" {
		value = os.Getenv(value)
		if value == "" {
			return
		}
	}
	entry.server.Headers[key] = value
}

// 内联表只在不位于字符串内的逗号处分割，保留值中的逗号和转义。
func parseInlineStringMap(value string) map[string]string {
	result := make(map[string]string)
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "{") || !strings.HasSuffix(value, "}") {
		return result
	}
	value = value[1 : len(value)-1]
	start, quote, escaped := 0, byte(0), false
	for i := 0; i <= len(value); i++ {
		if i == len(value) || (value[i] == ',' && quote == 0) {
			part := value[start:i]
			if eq := strings.Index(part, "="); eq >= 0 {
				result[parseTOMLTableName(strings.TrimSpace(part[:eq]))] = parseTOMLString(strings.TrimSpace(part[eq+1:]))
			}
			start = i + 1
			continue
		}
		ch := value[i]
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 {
			if ch == '\\' && quote == '"' {
				escaped = true
			} else if ch == quote {
				quote = 0
			}
		} else if ch == '"' || ch == '\'' {
			quote = ch
		}
	}
	return result
}

// stripComment 剔除行尾注释（# 开头，注意字符串内的 # 不是注释）。
func stripComment(line string) string {
	inQuote := false
	quoteChar := byte(0)
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inQuote {
			if escaped {
				escaped = false
			} else if c == '\\' && quoteChar == '"' {
				escaped = true
			} else if c == quoteChar {
				inQuote = false
			}
		} else {
			if c == '"' || c == '\'' {
				inQuote = true
				quoteChar = c
			} else if c == '#' {
				return line[:i]
			}
		}
	}
	return line
}

// parseTOMLTableName 去除表名可能的引号。
func parseTOMLTableName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			if unq, err := strconv.Unquote(s); err == nil {
				return unq
			}
			return s[1 : len(s)-1]
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseTOMLString 解析字符串字面量。
func parseTOMLString(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			if unq, err := strconv.Unquote(s); err == nil {
				return unq
			}
			return s[1 : len(s)-1]
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// countBrackets 计算字符串中的方括号平衡度（忽略字符串内的括号）。
func countBrackets(s string) int {
	count := 0
	inQuote := false
	quoteChar := byte(0)
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if escaped {
				escaped = false
			} else if c == '\\' && quoteChar == '"' {
				escaped = true
			} else if c == quoteChar {
				inQuote = false
			}
		} else {
			if c == '"' || c == '\'' {
				inQuote = true
				quoteChar = c
			} else if c == '[' {
				count++
			} else if c == ']' {
				count--
			}
		}
	}
	return count
}

// parseStringArray 解析可能跨多行的字符串数组。
func parseStringArray(s string) []string {
	var result []string
	inQuote := false
	quoteChar := byte(0)
	escaped := false
	var current strings.Builder

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if escaped {
				escaped = false
				current.WriteByte(c)
			} else if c == '\\' && quoteChar == '"' {
				escaped = true
				current.WriteByte(c)
			} else if c == quoteChar {
				inQuote = false
				lit := current.String()
				if quoteChar == '"' {
					if unq, err := strconv.Unquote("\"" + lit + "\""); err == nil {
						lit = unq
					}
				}
				result = append(result, lit)
				current.Reset()
			} else {
				current.WriteByte(c)
			}
		} else {
			if c == '"' || c == '\'' {
				inQuote = true
				quoteChar = c
				current.Reset()
			}
		}
	}
	return result
}
