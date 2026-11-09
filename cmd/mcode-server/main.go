// Package main 实现了 mcode-server 命令行服务。
//
// 用法示例：
//
//	go run ./cmd/mcode-server -port 9787
//	go run ./cmd/mcode-server -provider claude -workdir 某目录 -mode readonly
//	go run ./cmd/mcode-server -provider codex -workdir 某目录 -mode agent
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/modelsdev"
	"mcode/internal/projctx"
	"mcode/internal/providers/acp"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"mcode/internal/webassets"
)

func envOrDefault(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

// errHeadlessSwarmUnsupported 表示该操作需要桌面端 Client 才能完成，独立服务模式不提供。
var errHeadlessSwarmUnsupported = errors.New("当前服务模式不支持该操作")

// headlessSwarmExtras 是独立服务模式下的 Swarm 远程扩展实现：
// 这里只有 *swarm.Manager，没有桌面端的 Client，所以提问转交与档位配置不在本模式提供。
type headlessSwarmExtras struct {
	mgr *swarm.Manager
	// dataDir 用于读取全局「只规划」配置。
	dataDir string
}

var _ remote.SwarmRemoteExtras = headlessSwarmExtras{}

// PendingBeeQuestion 在独立服务模式下没有提问缓存，始终返回 nil。
func (x headlessSwarmExtras) PendingBeeQuestion(ctx context.Context, beeThreadID string) *wire.SwarmBeeQuestion {
	return nil
}

// ForwardBeeQuestion 需要桌面端 Client 的转交流程，独立服务模式不支持。
func (x headlessSwarmExtras) ForwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string) error {
	return errHeadlessSwarmUnsupported
}

// SwarmMergeDiff 返回 bee 待审查的补丁，并带上该 bee 记录的改动文件列表。
func (x headlessSwarmExtras) SwarmMergeDiff(ctx context.Context, leadThreadID, beeThreadID string) (string, bool, []string, error) {
	patch, err := x.mgr.MergeDiff(ctx, leadThreadID, beeThreadID)
	if err != nil {
		return "", false, nil, err
	}
	files := []string{}
	for _, h := range x.mgr.State(leadThreadID).Bees {
		if h.ThreadID == beeThreadID {
			files = append(files, h.ChangedFiles...)
			break
		}
	}
	return string(patch), false, files, nil
}

// ApproveSwarmMerge 批准并应用 bee 的改动。
func (x headlessSwarmExtras) ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	_, err := x.mgr.ApproveMerge(ctx, leadThreadID, beeThreadID)
	return err
}

// DiscardSwarmMerge 放弃 bee 的改动。
func (x headlessSwarmExtras) DiscardSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	return x.mgr.DiscardMerge(ctx, leadThreadID, beeThreadID)
}

// SetSwarmPlansOnly 设置主会话的「只规划」覆盖；传入 bee 线程时先折算到所属主会话。
func (x headlessSwarmExtras) SetSwarmPlansOnly(ctx context.Context, threadID string, v *bool) error {
	leadID := threadID
	if lead, ok := x.mgr.BeeOwner(threadID); ok {
		leadID = lead
	}
	x.mgr.SetLeadPlansOnly(leadID, v)
	return nil
}

// SwarmPlansOnly 返回主会话当前的「只规划」取值与该值是否来自会话级覆盖。
func (x headlessSwarmExtras) SwarmPlansOnly(leadThreadID string) (bool, bool) {
	if po := x.mgr.State(leadThreadID).PlansOnly; po != nil {
		return *po, true
	}
	return x.globalPlansOnly(), false
}

// globalPlansOnly 读取配置里的全局「只规划」开关，读不到时按 false 处理。
func (x headlessSwarmExtras) globalPlansOnly() bool {
	if x.dataDir == "" {
		return false
	}
	cfg, err := config.Load(x.dataDir)
	if err != nil {
		return false
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg.Orchestration.LeadPlansOnly
}

// SwarmTiers 在独立服务模式下没有档位配置来源，返回空列表。
func (x headlessSwarmExtras) SwarmTiers() []wire.SwarmTier {
	return []wire.SwarmTier{}
}

// SaveSwarmTiers 需要桌面端的配置文件读写，独立服务模式不支持。
func (x headlessSwarmExtras) SaveSwarmTiers(ctx context.Context, tiers []wire.SwarmTier) ([]wire.SwarmTier, error) {
	return nil, errHeadlessSwarmUnsupported
}

func main() {
	defaultDataDir := ""
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("failed to resolve user home directory: %v", err)
	}
	defaultDataDir = filepath.Join(homeDir, ".mcode")
	defaultDataDir = envOrDefault("MCODE_DATA", defaultDataDir)

	defaultWorkDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("failed to resolve current working directory: %v", err)
	}
	defaultWorkDir = envOrDefault("MCODE_WORKDIR", defaultWorkDir)

	defaults := config.Defaults()

	listenFlag := flag.String("listen", defaults.Remote.Listen, "TCP listen address")
	portFlag := flag.Int("port", 0, "TCP port; overrides the port in -listen when > 0")
	dataFlag := flag.String("data", defaultDataDir, "mcode data directory")
	providerFlag := flag.String("provider", defaults.Models.Provider, "agent provider (memory, claude, codex, or enabled API source kind)")
	workdirFlag := flag.String("workdir", defaultWorkDir, "working directory for agent harness")
	modeFlag := flag.String("mode", defaults.Permissions.Mode, "permission mode (readonly, supervised, autoAcceptEdits, auto, fullAccess; agent, full_access still accepted)")
	claudeBinFlag := flag.String("claude-bin", "claude", "path to claude executable")
	codexBinFlag := flag.String("codex-bin", "codex", "path to codex executable")
	tlsListenFlag := flag.String("tls-listen", defaults.Remote.TLSListen, "TLS TCP listen address")
	tlsDomainFlag := flag.String("tls-domain", defaults.Remote.TLSDomain, "TLS domain")
	acmeEmailFlag := flag.String("acme-email", defaults.Remote.ACMEEmail, "ACME account email")
	acmeDirectoryFlag := flag.String("acme-directory", defaults.Remote.ACMEDirectory, "ACME directory URL")
	flag.Parse()

	// 服务端的 worktree 清理只认实际数据目录下的受管目录，避免误删别处的 worktree。
	if strings.TrimSpace(*dataFlag) != "" {
		git.SetWorktreePurgeRoots(filepath.Join(*dataFlag, "worktrees"), filepath.Join(*dataFlag, "swarm-worktrees"), filepath.Join(*dataFlag, "hydra-worktrees"))
	}

	if *portFlag < 0 || *portFlag > 65535 {
		log.Fatalf("invalid port %d: must be between 0 and 65535", *portFlag)
	}

	cfg, err := config.Load(*dataFlag)
	if err != nil {
		log.Printf("failed to load config from %s: %v", config.ConfigPath(*dataFlag), err)
		os.Exit(1)
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	if err := config.Validate(cfg); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	setFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	settings := resolveSettings(cfg, setFlags, flagValues{
		Provider:  *providerFlag,
		Mode:      *modeFlag,
		ClaudeBin: *claudeBinFlag,
		CodexBin:  *codexBinFlag,
		Listen:    *listenFlag,
		Port:      *portFlag,
	})

	listenAddr := settings.ListenAddr

	tlsListen := cfg.Remote.TLSListen
	if setFlags["tls-listen"] {
		tlsListen = *tlsListenFlag
	}
	tlsDomain := cfg.Remote.TLSDomain
	if setFlags["tls-domain"] {
		tlsDomain = *tlsDomainFlag
	}
	acmeEmail := cfg.Remote.ACMEEmail
	if setFlags["acme-email"] {
		acmeEmail = *acmeEmailFlag
	}
	acmeDirectory := cfg.Remote.ACMEDirectory
	if setFlags["acme-directory"] {
		acmeDirectory = *acmeDirectoryFlag
	}

	secrets := config.NewSecretStore("mcode")
	if err := validateProvider(settings.Provider, cfg, secrets); err != nil {
		log.Fatal(err)
	}

	permMode, err := harness.ParseMode(settings.Mode)
	if err != nil {
		log.Fatalf("invalid mode %q: %v", settings.Mode, err)
	}

	workdir, err := filepath.Abs(*workdirFlag)
	if err != nil {
		log.Fatalf("failed to resolve absolute path for workdir %q: %v", *workdirFlag, err)
	}

	if err := os.MkdirAll(*dataFlag, 0o700); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	backend := remote.NewMemoryBackend(hub)
	backend.SetDataDir(*dataFlag)
	modelsdev.Start(*dataFlag)

	dbPath := filepath.Join(*dataFlag, "mcode.db")
	dbStore, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open sqlite store at %s: %v", dbPath, err)
	}
	defer dbStore.Close()

	// 一次性把库切到增量 auto_vacuum，让以后删除/压缩消息能真正归还磁盘空间；失败只记日志，不中断启动。
	if converted, err := dbStore.EnsureIncrementalAutoVacuum(context.Background()); err != nil {
		log.Printf("store: 启用增量 vacuum 失败: %v", err)
	} else if converted {
		log.Printf("store: 已完成一次性数据库整理，后续删除消息会归还磁盘空间")
	}

	if err := backend.AttachStore(context.Background(), dbStore); err != nil {
		log.Fatalf("failed to attach store: %v", err)
	}
	log.Printf("sqlite store opened at %s", dbPath)

	if settings.Provider != "memory" {
		fi, err := os.Stat(workdir)
		if err != nil {
			log.Fatalf("workdir %q does not exist: %v", workdir, err)
		}
		if !fi.IsDir() {
			log.Fatalf("workdir %q is not a directory", workdir)
		}
	}
	switch settings.Provider {
	case "claude":
		if _, err := exec.LookPath(settings.ClaudeBin); err != nil {
			log.Fatalf("claude binary %q not found in PATH: %v (use -claude-bin to specify path)", settings.ClaudeBin, err)
		}
		fi, err := os.Stat(workdir)
		if err != nil {
			log.Fatalf("workdir %q does not exist: %v", workdir, err)
		}
		if !fi.IsDir() {
			log.Fatalf("workdir %q is not a directory", workdir)
		}
		backend.SetHarness(claude.New(claude.WithResident(claude.Options{Binary: settings.ClaudeBin}, cfg.Providers.Claude)), workdir, permMode)
		if _, err := exec.LookPath(settings.CodexBin); err == nil {
			backend.RegisterHarness(codex.New(codex.Options{Binary: settings.CodexBin}))
		}
	case "codex":
		if _, err := exec.LookPath(settings.CodexBin); err != nil {
			log.Fatalf("codex binary %q not found in PATH: %v (use -codex-bin to specify path)", settings.CodexBin, err)
		}
		fi, err := os.Stat(workdir)
		if err != nil {
			log.Fatalf("workdir %q does not exist: %v", workdir, err)
		}
		if !fi.IsDir() {
			log.Fatalf("workdir %q is not a directory", workdir)
		}
		backend.SetHarness(codex.New(codex.Options{Binary: settings.CodexBin}), workdir, permMode)
		if _, err := exec.LookPath(settings.ClaudeBin); err == nil {
			backend.RegisterHarness(claude.New(claude.WithResident(claude.Options{Binary: settings.ClaudeBin}, cfg.Providers.Claude)))
		}
	}

	if settings.Provider == "claude" || settings.Provider == "codex" {
		for _, a := range acp.Agents() {
			if pCfg, ok := settings.ACP[string(a)]; ok && !pCfg.Enabled {
				continue
			}
			bin := ""
			if pCfg, ok := settings.ACP[string(a)]; ok {
				bin = pCfg.Bin
			}
			if bin == "" {
				bin = acp.DefaultBinary(a)
			}
			backend.RegisterHarness(acp.New(acp.Options{
				Agent:  a,
				Binary: bin,
			}))
		}

		for _, err := range config.MigrateLegacySecrets(config.LegacySecretRefs(nil)) {
			log.Printf("legacy secret migration warning: %v", err)
		}

	}
	if settings.Provider != "memory" {
		registerAPIEngines(backend, cfg, secrets, *dataFlag)
		if settings.Provider != "claude" && settings.Provider != "codex" {
			h := backend.RegisteredHarness(settings.Provider)
			if h == nil {
				log.Fatalf("provider %q is not enabled or available", settings.Provider)
			}
			backend.SetHarness(h, workdir, permMode)
		}
	}

	projectName := "演示项目"
	projectPath := filepath.Join(*dataFlag, "demo-project")
	if settings.Provider != "memory" {
		projectName = filepath.Base(workdir)
		projectPath = workdir
	}

	existingProjects, err := backend.ListProjects(context.Background())
	if err != nil {
		log.Fatalf("failed to list projects: %v", err)
	}
	foundProject := false
	for _, p := range existingProjects.Projects {
		if p.Path == projectPath {
			foundProject = true
			break
		}
	}

	if !foundProject {
		projectID := "00000000-0000-4000-8000-000000000001"
		threadID := "00000000-0000-4000-8000-000000000002"
		messageID := "00000000-0000-4000-8000-000000000003"
		nowRFC3339 := time.Now().UTC().Format(time.RFC3339)

		project := wire.ProjectSummary{
			ID:          projectID,
			Name:        projectName,
			Path:        projectPath,
			IsExpanded:  true,
			ThreadCount: 1,
		}
		if settings.Provider != "memory" && settings.Provider != "claude" && settings.Provider != "codex" {
			project.ThreadCount = 0
		}
		backend.AddProject(project)
		if settings.Provider != "memory" && settings.Provider != "claude" && settings.Provider != "codex" {
			goto projectInitialized
		}

		demoProvider := "memory"
		switch settings.Provider {
		case "claude":
			demoProvider = "claude"
		case "codex":
			demoProvider = "codex"
		}

		kindText := "text"
		isFinal := true
		thread := wire.ThreadSummary{
			ID:                threadID,
			ProjectID:         projectID,
			Title:             "演示会话",
			Provider:          demoProvider,
			IsRunning:         false,
			IsArchived:        false,
			IsPinned:          false,
			IsDormant:         false,
			HasUnread:         false,
			Attention:         "normal",
			Section:           "recent",
			OrderKey:          "0",
			UpdatedAt:         nowRFC3339,
			InteractionMode:   "chat",
			FastMode:          false,
			WorktreeRequested: false,
			SwarmEnabled:      false,
		}

		welcomeText := "这是 MCode 内存后端的演示线程，发一条消息试试流式回复"
		switch settings.Provider {
		case "claude":
			welcomeText = fmt.Sprintf("这是 Claude Code 演示线程，消息会交给 Claude Code 在 %s 目录下执行（当前权限模式：%s）。", workdir, permMode)
		case "codex":
			welcomeText = fmt.Sprintf("这是 Codex 演示线程，消息会交给 Codex 在 %s 目录下执行（当前权限模式：%s）。", workdir, permMode)
		}

		welcomeMsg := wire.ThreadMessage{
			ID:              messageID,
			Role:            "assistant",
			Text:            welcomeText,
			CreatedAt:       nowRFC3339,
			TurnIndex:       0,
			Kind:            &kindText,
			IsFinalResponse: &isFinal,
		}
		backend.AddThread(thread, []wire.ThreadMessage{welcomeMsg})
	}

projectInitialized:
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			log.Fatalf("failed to listen on %s: address already in use; pick another port with -port 9787 or -listen 127.0.0.1:9787", listenAddr)
		}
		log.Fatalf("failed to listen on %s: %v", listenAddr, err)
	}
	defer ln.Close()

	var tlsLn net.Listener
	if tlsListen != "" {
		var err error
		tlsLn, err = net.Listen("tcp", tlsListen)
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				log.Fatalf("failed to listen on %s: address already in use", tlsListen)
			}
			log.Fatalf("failed to listen on %s: %v", tlsListen, err)
		}
		defer tlsLn.Close()
	}

	// 配对链接地址在监听就绪后一次算好，闭包只读，避免与配对鉴权互相加锁。
	// 监听在未指定地址（0.0.0.0/::）时，打印 ln.Addr() 会得到手机上打不开的地址，
	// 因此优先取第一个局域网基址，取不到再退回原写法。
	lanBaseURL := "http://" + ln.Addr().String()
	if host, portStr, splitErr := net.SplitHostPort(ln.Addr().String()); splitErr == nil {
		ip := net.ParseIP(host)
		unspecified := host == "" || host == "0.0.0.0" || host == "::" || (ip != nil && ip.IsUnspecified())
		if unspecified {
			if port, convErr := strconv.Atoi(portStr); convErr == nil {
				if urls := remote.LANBaseURLs(port); len(urls) > 0 {
					lanBaseURL = urls[0]
				}
			}
		}
	}

	var tlsBaseURL func() string
	switch {
	case tlsLn != nil:
		base := tlsPairingBaseURL(tlsDomain, tlsLn.Addr())
		if base != "" {
			tlsBaseURL = func() string { return base }
		}
	case strings.TrimSpace(tlsDomain) != "":
		// 域名已配置但 TLS 没起来时，域名仍可走局域网 HTTP 入口。
		if base := domainLANPairingBaseURL(tlsDomain, ln.Addr()); base != "" {
			tlsBaseURL = func() string { return base }
		}
	}

	auth, err := remote.NewPairingAuth(remote.PairingAuthOptions{
		DevicesPath: filepath.Join(*dataFlag, "remote-devices.json"),
		LANBaseURL: func() string {
			return lanBaseURL
		},
		TLSBaseURL: tlsBaseURL,
	})
	if err != nil {
		log.Fatalf("failed to create pairing auth: %v", err)
	}

	dispatcher := remote.NewDispatcher(auth, backend)

	var adminRoutes []http.HandlerFunc

	pushSvc, err := remote.NewWebPushService(remote.WebPushOptions{
		KeyPath:      filepath.Join(*dataFlag, "remote-webpush-vapid.pem"),
		Devices:      auth.Store(),
		VAPIDSubject: remote.VAPIDSubjectFor(tlsDomain, ln),
		ThreadWorkDir: func(threadID string) (string, bool) {
			return backend.ThreadWorkDir(threadID)
		},
	})
	if err != nil {
		log.Printf("failed to initialize webpush service: %v", err)
	} else {
		dispatcher.SetPush(pushSvc)
		if h := backend.Hub(); h != nil {
			pushSvc.ObserveHub(h)
		}
		// 独立 server 设置页的“发送测试推送”走 HTTP 管理接口。
		adminRoutes = append(adminRoutes, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
			sent, err := pushSvc.SendTest(r.Context(), "mcode-server", "测试推送：手机能收到这条通知，说明推送链路已经打通。")
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sent": sent})
		}))
	}

	var gitRunner *git.Runner
	if _, err := exec.LookPath("git"); err == nil {
		gitRunner = git.NewRunner("", 4)
		// 子模块初始化开关与克隆忽略目录开关都是 git 包的进程级全局（默认关闭），headless 模式没有 Client 帮忙回推，
		// 这里按配置推一次，否则「新建工作树时初始化子模块」「新建工作树时克隆依赖目录」在本模式里被静默忽略。
		git.SetInitSubmodules(cfg.SourceControl.InitSubmodulesEnabled())
		git.SetCloneIgnored(cfg.SourceControl.CloneIgnoredOnWorktreeEnabled(), cfg.SourceControl.CloneIgnoredPatternList())
	}

	kanbanSvc := kanban.NewService(kanban.Options{
		Store:       dbStore,
		Git:         gitRunner,
		Driver:      backend,
		Projects:    backend,
		WorktreeDir: filepath.Join(*dataFlag, "worktrees"),
		DataDir:     *dataFlag,
	})
	defer kanbanSvc.Close()

	goalScheduler := kanban.NewGoalScheduler(kanbanSvc, 10*time.Second)
	goalScheduler.Start()
	defer goalScheduler.Stop()

	// 与桌面端一致：看门狗盯住卡住的任务并适时中断，headless 模式同样需要它。
	watchdog := kanban.NewWatchdog(kanbanSvc, 5*time.Second)
	watchdog.Start()
	defer watchdog.Stop()

	dispatcher.SetKanban(kanbanSvc)
	kanbanSvc.SetOnChange(dispatcher.NotifyKanbanChanged)

	if gitRunner != nil {
		swarmMgr := swarm.NewManager(swarm.Options{
			Git:          gitRunner,
			Driver:       backend,
			StopSession:  func(ctx context.Context, id string) error { _, err := backend.InterruptCurrent(ctx, id); return err },
			WorktreeDir:  filepath.Join(*dataFlag, "swarm-worktrees"),
			PollInterval: 2 * time.Second,
			// 只有声明了 skills 的 bee 才带上对应技能的索引块；取不到 home 时不注入。
			SkillsGuidance: func(workDir string, names []string) string {
				home, err := os.UserHomeDir()
				if err != nil {
					return ""
				}
				return projctx.BuildSkillsGuidanceFor(workDir, home, names)
			},
		})
		swarmMgr.Start()
		defer swarmMgr.Stop()
		dispatcher.SetSwarm(swarmMgr)
		// 独立服务模式没有桌面端 Client，用只依赖管理器的实现补齐合并、只规划等扩展 RPC。
		dispatcher.SetSwarmExtras(headlessSwarmExtras{mgr: swarmMgr, dataDir: *dataFlag})
		swarmMgr.SetOnChange(dispatcher.NotifySwarmChanged)
		backend.SetSwarm(true, func(id string) bool { return swarmMgr.State(id).Enabled })
		// 转发会话时让新会话继承源会话的编排开关与只规划覆盖值。
		backend.SetSwarmInheritor(swarmMgr.InheritLead)
	}

	listenHost, _, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		listenHost = ln.Addr().String()
	}

	allowedHosts := []string{listenHost, "localhost"}
	if listenHost == "0.0.0.0" || listenHost == "::" || listenHost == "" {
		allowedHosts = append(allowedHosts, remote.LANIPv4s()...)
	}
	if len(settings.ExtraHosts) > 0 {
		allowedHosts = append(allowedHosts, settings.ExtraHosts...)
	}
	// 配了域名但 TLS 没起来时，配对链接指向 http://<域名>:<端口>，白名单里没这个 Host
	// 点进去就会被拒；TLS 入口直接用同一份白名单，所以这里统一追加。
	if domain := strings.TrimSpace(tlsDomain); domain != "" {
		allowedHosts = append(allowedHosts, domain)
	}

	srv := remote.NewServer(remote.ServerOptions{
		Handler:      dispatcher,
		Assets:       webassets.FS(),
		ListenAddr:   ln.Addr().String(),
		Entry:        remote.EntryLAN,
		AllowedHosts: allowedHosts,
		AdminRoutes:  adminRoutes,
		AnswerHandler: remote.NewAnswerEndpoint(remote.AnswerEndpointOptions{
			Auth:    auth,
			Backend: backend,
			Entry:   remote.EntryLAN,
		}),
	})

	var (
		tlsMgr *remote.TLSManager
		tlsSrv *remote.Server
	)
	if tlsLn != nil {
		secStore := config.NewSecretStore("mcode")
		cfToken, _ := secStore.Get("cloudflare_api_token")

		tlsHosts := make([]string, len(allowedHosts))
		copy(tlsHosts, allowedHosts)

		var err error
		tlsMgr, err = remote.NewTLSManager(remote.TLSOptions{
			Dir:             filepath.Join(*dataFlag, "remote-tls"),
			Hosts:           tlsHosts,
			Domain:          tlsDomain,
			Email:           acmeEmail,
			DirectoryURL:    acmeDirectory,
			CloudflareToken: cfToken,
		})
		if err != nil {
			log.Fatalf("failed to create tls manager: %v", err)
		}
		if err := tlsMgr.Start(context.Background()); err != nil {
			log.Fatalf("failed to start tls manager: %v", err)
		}
		defer tlsMgr.Close()

		tlsSrv = remote.NewServer(remote.ServerOptions{
			Handler:      dispatcher,
			Assets:       webassets.FS(),
			ListenAddr:   tlsLn.Addr().String(),
			Entry:        remote.EntryTLS,
			AllowedHosts: tlsHosts,
			AdminRoutes:  adminRoutes,
			AnswerHandler: remote.NewAnswerEndpoint(remote.AnswerEndpointOptions{
				Auth:    auth,
				Backend: backend,
				Entry:   remote.EntryTLS,
			}),
		})
	}

	pairInfo := auth.CurrentPairingURLs()
	pairingURL := pairInfo.PairingURL
	pairingCode := pairInfo.ShortCode
	if pairingURL == "" {
		newInfo, err := auth.NewPairingCode()
		if err != nil {
			log.Fatalf("failed to generate pairing code: %v", err)
		}
		pairingURL = newInfo.PairingURL
		pairingCode = newInfo.ShortCode
	}

	log.Printf("mcode-server listening on %s", ln.Addr().String())
	if tlsLn != nil {
		log.Printf("mcode-server TLS listening on %s (domain: %s)", tlsLn.Addr().String(), tlsDomain)
	}
	log.Printf("provider: %s, workdir: %s, mode: %s", settings.Provider, workdir, permMode)
	log.Printf("Pairing URL: %s", pairingURL)
	if pairingCode != "" {
		log.Printf("Pairing code: %s", pairingCode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 配对码 5 分钟就过期，CurrentPairingURLs 会自动换发新码；启动时只打印一次的话，
	// 过期后在 headless 模式里再也拿不到可用的配对信息，只能重启进程。这里跟着换码重新打印。
	go func() {
		lastURL, lastCode := pairingURL, pairingCode
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info := auth.CurrentPairingURLs()
				if info.PairingURL == lastURL && info.ShortCode == lastCode {
					continue
				}
				lastURL, lastCode = info.PairingURL, info.ShortCode
				log.Printf("配对码已轮换（旧码已过期）")
				if info.PairingURL != "" {
					log.Printf("Pairing URL: %s", info.PairingURL)
				}
				if info.ShortCode != "" {
					log.Printf("Pairing code: %s", info.ShortCode)
				}
			}
		}
	}()

	serverErrCh := make(chan error, 2)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}
	}()

	if tlsSrv != nil {
		go func() {
			if err := tlsSrv.ServeTLS(tlsLn, tlsMgr.TLSConfig()); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErrCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		log.Println("Shutting down mcode-server...")
	case err := <-serverErrCh:
		if err != nil {
			log.Fatalf("server terminated unexpectedly: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if tlsSrv != nil {
		if err := tlsSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("error during tls server shutdown: %v", err)
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("error during server shutdown: %v", err)
	}

	goalScheduler.Stop()
	kanbanSvc.Close()
	hub.Close()
	backend.Close()
}

// listenPort 从监听地址里取出端口；解析失败或端口无效时返回 0。
func listenPort(addr net.Addr) int {
	if addr == nil {
		return 0
	}
	_, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return 0
	}
	return port
}

// tlsPairingBaseURL 拼出 TLS 入口的对外地址：配置了域名时带上真实监听端口，端口为 443 时省略。
func tlsPairingBaseURL(domain string, addr net.Addr) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		if addr == nil {
			return ""
		}
		return "https://" + addr.String()
	}
	port := listenPort(addr)
	if port <= 0 || port == 443 {
		return "https://" + domain
	}
	return "https://" + net.JoinHostPort(domain, strconv.Itoa(port))
}

// domainLANPairingBaseURL 拼出只配置了域名、TLS 未运行时的 http://<domain>:<局域网端口>；
// 未配置域名或取不到端口时返回空串。
func domainLANPairingBaseURL(domain string, addr net.Addr) string {
	domain = strings.TrimSpace(domain)
	port := listenPort(addr)
	if domain == "" || port <= 0 {
		return ""
	}
	return "http://" + net.JoinHostPort(domain, strconv.Itoa(port))
}
