package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/agentconf"
	"mcode/internal/apisource"
	"mcode/internal/attention"
	"mcode/internal/automation"
	"mcode/internal/backup"
	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/harness/policy"
	"mcode/internal/kanban"
	"mcode/internal/localmem"
	"mcode/internal/magpiecfg"
	"mcode/internal/mcp"
	"mcode/internal/modelsdev"
	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/providers/acp"
	"mcode/internal/providers/agy"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/commandcode"
	"mcode/internal/providers/copilot"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/omp"
	"mcode/internal/providers/pi"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/startuptrace"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"mcode/internal/textgen"
	mcodeui "mcode/internal/ui"
)

var _ agy.Router = (*accounts.Manager)(nil)

// Options 配置 App 启动所需的参数。
type Options struct {
	DataDir            string
	Provider           string
	WorkDir            string
	Mode               string
	ClaudeBin          string
	CodexBin           string
	AgyAccountsOptions accounts.Options
	// OnStartupProgress 接收启动阶段的可量化进度；可为 nil。回调可能来自后台 goroutine。
	OnStartupProgress func(p StartupProgress)
}

// StartupProgress 是启动阶段的一次进度上报，Total 为 0 时表示总量未知。
type StartupProgress struct {
	Stage string
	Done  int64
	Total int64
}

// StartupStageCompactDB 是首次把数据库切到增量 auto_vacuum 的预整理阶段。
const StartupStageCompactDB = "compact_db"

const backgroundShutdownTimeout = 5 * time.Second

// providerMCP 是注入各 Provider 的内置与外部 MCP 接入方式。
// 运行时重建 harness 的路径（设置页、自定义端点）复用同一份实例。
type providerMCP struct {
	Builtin  harness.BuiltinMCP
	External func(workDir, threadID string) []harness.MCPEndpoint
	// Blocked 返回仓库内未信任、需强行关闭 MCP 服务的名字（目前仅 codex 使用）。
	Blocked func(workDir string) []string
}

// App 封装本地桌面应用的后端核心组件。
type App struct {
	client        *Client
	hub           *remote.StreamHub
	backend       *remote.MemoryBackend
	store         *store.Store
	kanban        *kanban.Service
	scheduler     *kanban.GoalScheduler
	automation    *automation.Service
	swarm         *swarm.Manager
	swarmPipeline *swarm.Pipeline
	mcp           *mcp.Server
	mcpHub        *mcp.Hub
	mcpProxy      *mcp.Proxy
	remote        *RemoteService
	quotaCancel   context.CancelFunc
	quotaDone     <-chan struct{}
	modelsCancel  context.CancelFunc
	modelsDone    <-chan struct{}
	updateCancel  context.CancelFunc
	// inboxMigrateCancel / inboxMigrateDone 是启动后旧收件箱迁移后台任务的取消函数与退出信号：
	// 迁移会写 store，关闭前必须先取消并等它退出。
	inboxMigrateCancel context.CancelFunc
	inboxMigrateDone   chan struct{}
	// attention 是待处理事件（收件箱）服务，attentionSrc 是四类事件源的接线。
	attention    *attention.Service
	attentionSrc *attentionWiring
	// ownerNotify 是向用户发桌面通知的实现（由桌面端接入）。
	// 它返回哨兵错误 ErrOwnerNotifySuppressed 表示通知被前台或安静时段抑制、没有弹出。
	ownerNotify func(title, body, threadID string) error
	notifyMu    sync.RWMutex
	// dataDir 是 mcode 数据目录。
	dataDir string
	// retention 是 bee 消息保留任务的后台调度器：启动 2 分钟后跑第一次，之后每 24 小时一次。
	retention *messageRetentionScheduler
	// automationSleep 是自动化防休眠守卫：有自动化运行中或即将到期时阻止电脑休眠。
	automationSleep *remote.SleepGuard
}

// Client 获取界面层 Client 适配器实例。
func (a *App) Client() *Client {
	return a.client
}

// Automation 获取自动化服务实例。
func (a *App) Automation() *automation.Service {
	return a.automation
}

// Hub 获取 StreamHub 实例。
func (a *App) Hub() *remote.StreamHub {
	return a.hub
}

// RecoveredStoreBackupPath 返回本次打开时因数据库损坏而被改名备份的原库路径；
// 未发生自愈恢复时返回空字符串。
func (a *App) RecoveredStoreBackupPath() string {
	if a.store == nil {
		return ""
	}
	return a.store.RecoveredBackupPath()
}

// Close 关闭应用资源，关闭顺序为 bee 消息保留 -> 旧收件箱迁移 -> 待处理事件接线 -> mcpProxy.Close -> mcpHub.Close -> mcp.Close -> automationSleep.Stop -> automation.Stop -> scheduler.Stop -> kanban.Close -> swarmPipeline.Stop -> swarm.Stop -> remote.Stop -> hub.Close -> backend.Close -> store.Close。
func (a *App) Close() error {
	if a.quotaCancel != nil {
		a.quotaCancel()
	}
	if a.modelsCancel != nil {
		a.modelsCancel()
	}
	waitBackground(a.quotaDone)
	waitBackground(a.modelsDone)
	if a.updateCancel != nil {
		a.updateCancel()
	}
	if a.client != nil {
		a.client.stopQuestionAutoForward()
	}
	// bee 消息保留会写库，必须等它退出后再关闭 store。
	if a.retention != nil {
		a.retention.Stop()
	}
	// 旧收件箱迁移会写库，先取消并等它退出，再继续关闭 store。
	if a.inboxMigrateCancel != nil {
		a.inboxMigrateCancel()
	}
	if a.inboxMigrateDone != nil {
		<-a.inboxMigrateDone
	}
	if a.attentionSrc != nil {
		a.attentionSrc.Stop()
	}
	if a.mcpProxy != nil {
		_ = a.mcpProxy.Close()
	}
	if a.mcpHub != nil {
		_ = a.mcpHub.Close()
	}
	if a.mcp != nil {
		_ = a.mcp.Close()
	}
	if a.automationSleep != nil {
		a.automationSleep.Stop()
	}
	if a.automation != nil {
		a.automation.Stop()
	}
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
	if a.kanban != nil {
		a.kanban.Close()
	}
	if a.swarmPipeline != nil {
		a.swarmPipeline.Stop()
	}
	if a.swarm != nil {
		a.swarm.Stop()
	}
	if a.remote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.remote.Stop(ctx)
	}
	if a.hub != nil {
		a.hub.Close()
	}
	if a.backend != nil {
		a.backend.Close()
	}
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}

func waitBackground(done <-chan struct{}) {
	if done == nil {
		return
	}
	timer := time.NewTimer(backgroundShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Print("后台任务关闭超时")
	}
}

// claudeProbeResult 是 claude 登录检测（后台 goroutine）的结果。
type claudeProbeResult struct {
	lookPathErr error
	authErr     error
}

var (
	// claudeProbeProcessEnv 提供当前进程环境（main 已把常用目录补进 PATH），供快路径使用；单测可替换。
	claudeProbeProcessEnv = os.Environ
	// claudeProbeLoginEnv 提供登录 Shell 环境（loginenv 首次调用执行登录 Shell 后缓存），单测可替换。
	claudeProbeLoginEnv = loginenv.Environ
	// claudeProbeCheck 执行带缓存与并发去重的登录态检测（AuthChecker.Check），单测可替换。
	claudeProbeCheck = func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
		return claude.NewAuthChecker(binary, env).Check(ctx)
	}
	// claudeProbeReadStatus 执行无缓存的登录态检测（ReadAuthStatus），供登录环境复核使用；单测可替换。
	claudeProbeReadStatus = claude.ReadAuthStatus
)

// probeClaudeBinary 检查 claude 二进制是否存在，存在时再判断是否已登录，逻辑与原同步实现一致。
// claude 可用 = 二进制存在且已登录：本地走 API key / 自定义网关 / Bedrock / Vertex 时
// 不需要 claude.ai 登录，跳过检测；检测未上报（超时、命令不存在、输出非 JSON）时保持
// 「二进制存在即可用」的既有行为，且最多只等 AuthProbeBudget。
//
// 判断顺序：绝大多数用户已经用 claude.ai 登录，登录凭证在配置目录里、进程环境就能读到，
// 所以先拿当前进程环境跑一次快路径，命中「已登录」就直接返回，省掉 loginenv.Environ()
// 首次调用要等登录 Shell（zsh -l -i，约 200ms）的开销。只有「已登录」会提前采纳：
// 快路径说未登录、未上报或超时，都回到原来的完整逻辑（登录环境 + 再次检测），
// 因此只在登录 Shell 里才有 API key / 网关令牌 / Bedrock / Vertex 变量的用户，判断结果与原来一致。
// 已知边界：在 .zshrc 里设置的 CLAUDE_CONFIG_DIR 只在登录环境可见，快路径可能读到另一个配置目录。
func probeClaudeBinary(claudeBin string) (lookPathErr, authErr error) {
	if _, lookPathErr = exec.LookPath(claudeBin); lookPathErr != nil {
		return lookPathErr, nil
	}
	if claudeProbeFastPathLoggedIn(claudeBin) {
		startuptrace.Mark("claude auth fast path")
		return nil, nil
	}
	startuptrace.Mark("claude auth login-env path")
	// 检测子进程使用与真正启动 CLI 相同的登录环境（loginenv 首次调用执行登录 Shell 后缓存）；
	// 配了 API key / 网关令牌 / Bedrock / Vertex 时本地不需要 claude.ai 登录，跳过检测。
	claudeEnv := claudeProbeLoginEnv()
	if claude.UsesThirdPartyAPI(claudeEnv) {
		return nil, nil
	}
	// AuthProbeBudget 从真正开始检测时计时，与原来的同步实现一致。
	authCtx, authCancel := context.WithTimeout(context.Background(), claude.AuthProbeBudget)
	defer authCancel()
	// AuthChecker.Check 的结果按 binary + CLAUDE_CONFIG_DIR 缓存 60 秒，快路径已用进程环境写过同一个键，
	// 这里再走 Check 只会命中缓存、拿不到登录环境的结论，改用无缓存的 ReadAuthStatus 重新检测。
	if status, known := claudeProbeReadStatus(authCtx, claudeBin, claudeEnv); known && !status.LoggedIn {
		authErr = claude.ErrNotLoggedIn
	}
	return nil, authErr
}

// claudeProbeFastPathLoggedIn 报告进程环境是否足以判定 claude 可用：
// 进程环境配了 API key / 网关令牌 / Bedrock / Vertex 时本地不需要 claude.ai 登录；
// 否则在 AuthProbeBudget 内查一次登录态，只有「已上报且已登录」才算命中。
func claudeProbeFastPathLoggedIn(claudeBin string) bool {
	env := claudeProbeProcessEnv()
	if claude.UsesThirdPartyAPI(env) {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), claude.AuthProbeBudget)
	defer cancel()
	status, known := claudeProbeCheck(ctx, claudeBin, env)
	return known && status.LoggedIn
}

// Open 初始化数据库、StreamHub、MemoryBackend 并按配置装配 Provider 和种子数据。
func Open(opts Options) (*App, error) {
	setAppDataDir(opts.DataDir)
	// 日志同时写 stderr 与磁盘文件，整体先经过 redactWriter 脱敏；
	// 日志文件打不开时只退回 stderr，不影响启动。
	var logOut io.Writer = redactWriter{os.Stderr}
	if logFile, _, err := openLogFile(opts.DataDir); err == nil {
		logOut = redactWriter{io.MultiWriter(os.Stderr, logFile)}
	}
	log.SetOutput(logOut)
	startuptrace.Flush()

	workdir := opts.WorkDir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve current working directory: %w", err)
		}
	}
	absWorkDir, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path for workdir %q: %w", workdir, err)
	}

	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	if _, err := backup.ApplyPending(opts.DataDir); err != nil {
		return nil, fmt.Errorf("apply pending restore: %w", err)
	}

	// 配置在打开数据库之前先读：claude 二进制路径要尽早确定，登录检测才能和后面的
	// 数据库打开、swarm 恢复等启动步骤并行；此处还没有需要清理的资源，读配置失败直接返回。
	cfg, err := config.Load(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	config.ApplyEnv(&cfg, os.LookupEnv)

	claudeBin := opts.ClaudeBin
	if claudeBin == "" {
		claudeBin = cfg.Providers.Claude.Bin
	}
	if claudeBin == "" {
		claudeBin = "claude"
	}
	// 检测整体丢到后台，与后续启动并行，结果在原处等待。通道缓冲为 1：Open 在等待前
	// 因错误提前返回时 goroutine 仍能写完退出，不会泄漏。goroutine 只读入参、只写通道。
	claudeProbe := make(chan claudeProbeResult, 1)
	go func() {
		lookPathErr, authErr := probeClaudeBinary(claudeBin)
		startuptrace.Mark("claude auth probe finished (bg)")
		claudeProbe <- claudeProbeResult{lookPathErr: lookPathErr, authErr: authErr}
	}()

	dbPath := filepath.Join(opts.DataDir, "mcode.db")
	// 首次切增量 auto_vacuum 要整库重写一次，打开前先用 VACUUM INTO 完成并上报字节进度；
	// 失败只记日志，打开后 EnsureIncrementalAutoVacuum 还会原地 VACUUM 兜底。
	var compactProgress func(done, total int64)
	if opts.OnStartupProgress != nil {
		compactProgress = func(done, total int64) {
			opts.OnStartupProgress(StartupProgress{Stage: StartupStageCompactDB, Done: done, Total: total})
		}
	}
	if converted, err := store.CompactForIncrementalVacuum(context.Background(), dbPath, compactProgress); err != nil {
		log.Printf("store: 预整理数据库失败，改用原地整理: %v", err)
	} else if converted {
		log.Printf("store: 已完成数据库预整理")
	}
	dbStore, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store at %s: %w", dbPath, err)
	}
	startuptrace.Mark("store opened")
	if _, err := backup.CompletePending(context.Background(), opts.DataDir, dbStore); err != nil {
		_ = dbStore.Close()
		return nil, fmt.Errorf("complete pending restore: %w", err)
	}
	// 一次性把库切到增量 auto_vacuum，让以后删除/压缩消息能真正归还磁盘空间；失败只记日志，不中断启动。
	if converted, err := dbStore.EnsureIncrementalAutoVacuum(context.Background()); err != nil {
		log.Printf("store: 启用增量 vacuum 失败: %v", err)
	} else if converted {
		log.Printf("store: 已完成一次性数据库整理，后续删除消息会归还磁盘空间")
	}
	// 旧收件箱迁移会写库，放到后台与后续启动并行；取消函数与退出信号在 App 构造后交给 App.Close。
	// 构造 App 前就返回的路径由下面的 defer 取消并等它退出，不会留下仍写已关闭 store 的 goroutine。
	migrateCtx, migrateCancel := context.WithCancel(context.Background())
	migrateDone := make(chan struct{})
	go func() {
		defer close(migrateDone)
		migrateLegacyInboxOnStartup(migrateCtx, dbStore)
	}()
	migrateOwned := true
	defer func() {
		if migrateOwned {
			migrateCancel()
			<-migrateDone
		}
	}()

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	backend := remote.NewMemoryBackend(hub)
	backend.SetDataDir(opts.DataDir)
	// 远程能力声明里带桌面端版本号，与「关于」页同一来源。
	backend.SetAppVersion(AppVersion)
	// 自研引擎请求的 UA 与「关于」页同一版本来源。
	llm.UserAgent = "mcode-app/" + AppVersion
	modelsCtx, modelsCancel := context.WithCancel(context.Background())
	modelsDone := modelsdev.Start(modelsCtx, opts.DataDir)
	modelsOwned := true
	defer func() {
		if modelsOwned {
			modelsCancel()
			waitBackground(modelsDone)
		}
	}()
	startuptrace.Mark("modelsdev started")

	if err := backend.AttachStore(context.Background(), dbStore); err != nil {
		_ = dbStore.Close()
		return nil, fmt.Errorf("attach store: %w", err)
	}
	startuptrace.Mark("AttachStore done")

	var gitRunner *git.Runner
	if _, err := exec.LookPath("git"); err == nil {
		gitRunner = git.NewRunner("", 4)
	}

	var swarmPipeline *swarm.Pipeline
	var swarmMgr *swarm.Manager
	var swarmRestored bool
	secrets := config.NewSecretStore("mcode")
	if gitRunner != nil {
		describe := func(ctx context.Context, diff string) (string, error) {
			cfg, err := config.Load(opts.DataDir)
			if err != nil {
				return "", err
			}
			config.ApplyEnv(&cfg, os.LookupEnv)
			if !cfg.SourceControl.GenerateCommitMessageEnabled() {
				return "", fmt.Errorf("未启用")
			}
			provider, model, effort := textgenModelSelection(cfg)
			prompt := textgen.CommitMessagePrompt("", diff, cfg.SourceControl.CommitInstructions)
			ctx = withTextgenGlobalSelection(ctx, cfg)
			out, err := generateText(ctx, cfg, secrets, provider, model, effort, prompt)
			if err != nil {
				return "", err
			}
			cleaned := textgen.CleanCommitMessage(out)
			if cleaned == "" {
				return "", fmt.Errorf("生成提交信息为空")
			}
			return cleaned, nil
		}
		beeTimeout := swarm.DefaultBeeTimeout
		if c, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&c, os.LookupEnv)
			beeTimeout = c.Orchestration.BeeTimeoutDuration()
		}
		// 影子仓库只对 swarm 生效，避免界面共用 Runner 将非 git 目录误识别为 git 仓库。
		swarmGitRunner := git.NewRunner("", 4)
		swarmGitRunner.ShadowRoot = filepath.Join(opts.DataDir, "shadow-git")
		swarmMgr = swarm.NewManager(swarm.Options{
			Git:       swarmGitRunner,
			Describe:  describe,
			Driver:    backend,
			ReadPaths: backend.ThreadReadPaths,
			NormalizeBee: func(provider, model string) (string, string) {
				cfg, err := config.Load(opts.DataDir)
				if err != nil {
					return provider, model
				}
				config.ApplyEnv(&cfg, os.LookupEnv)
				provider, model, _ = apisource.NormalizeBeeModel(cfg, config.NewSecretStore("mcode"), provider, model)
				return provider, model
			},
			DefaultBeeSource: func(lead swarm.ThreadContext) (string, string, error) {
				cfg, err := config.Load(opts.DataDir)
				if err != nil {
					return "", "", err
				}
				config.ApplyEnv(&cfg, os.LookupEnv)
				return defaultBeeSource(cfg, config.NewSecretStore("mcode"), lead)
			},
			ThreadContext: func(leadThreadID string) swarm.ThreadContext {
				msg, pairID, tiers, beeProvider, beeModel, beeEffort := backend.SwarmThreadContext(leadThreadID)
				tc := swarmThreadContextFrom(msg, pairID, tiers, beeProvider, beeModel, beeEffort, backend.SwarmProviderEnabled)
				if lead, err := backend.GetThread(context.Background(), leadThreadID); err == nil {
					tc.Provider = lead.Provider
					if lead.Model != nil {
						tc.Model = *lead.Model
					}
				}
				return tc
			},
			StopSession:        func(ctx context.Context, id string) error { _, err := backend.InterruptCurrent(ctx, id); return err },
			WorktreeDir:        filepath.Join(opts.DataDir, "swarm-worktrees"),
			PollInterval:       2 * time.Second,
			NoEditStallTimeout: beeTimeout,
			Persister:          swarmStoreAdapter{s: dbStore},
			ResolveProject:     resolveSwarmProject(backend),
			LeadTouchedPaths:   backend.CurrentTurnTouchedPaths,
			// 只有声明了 skills 的 bee 才带上对应技能的索引块；取不到 home 时不注入。
			SkillsGuidance: func(workDir string, names []string) string {
				home, err := os.UserHomeDir()
				if err != nil {
					return ""
				}
				return projctx.BuildSkillsGuidanceFor(workDir, home, names)
			},
			OnReport: func(lead, msg string) error {
				return backend.SendSwarmReport(context.Background(), lead, msg)
			},
			// 汇报全文存档：消息里的超长总结被截断后，swarm_report_get 与界面展开都从这里读。
			ArchiveReport: dbStore.SaveSwarmReport,
			OnBeesIdle: func(ctx context.Context, leadThreadID string) {
				if err := backend.DrainQueuedFollowUps(ctx, leadThreadID); err != nil {
					log.Printf("swarm: 主代理 %s 空闲时推进排队汇报失败: %v", leadThreadID, err)
				}
			},
			OnBatchLanded: func(ctx context.Context, b swarm.LandedBatch) {
				if swarmPipeline != nil {
					swarmPipeline.OnBatchLanded(ctx, b)
				}
			},
		})
		// 自动合并流水线：默认只做本地提交，AutoPush 开启后才推送与创建/合入 PR/MR。
		// Cleanup 保持为空，让已完成的 bee 继续显示在团队面板中。
		swarmPipeline = swarm.NewPipeline(swarm.PipelineOptions{
			Git:      gitRunner,
			Describe: describe,
			Settings: swarmMgr.Settings,
			Notify:   func(lead, msg string) { _ = backend.AppendNotice(lead, msg) },
			// 合并进度回写到各 bee：应用退出后仍能看出合并做到哪一步，并在重启后续做。
			OnResult: func(b swarm.LandedBatch, r swarm.BatchResult) {
				ids := make([]string, 0, len(b.Bees))
				for _, h := range b.Bees {
					ids = append(ids, h.ThreadID)
				}
				swarmMgr.RecordAutoMerge(b.LeadThreadID, ids, r)
			},
		})
		swarmPipeline.Start()
		swarmMgr.SetSettingsFunc(func() swarm.Settings {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return SwarmSettingsFromConfig(c.Orchestration)
			}
			return SwarmSettingsFromConfig(config.OrchestrationConfig{})
		})
		if err := swarmMgr.Restore(context.Background()); err != nil {
			log.Printf("swarm restore error: %v", err)
		} else {
			swarmRestored = true
			// 上次退出时没做完的自动合并接着做：已本地提交的批次从远端阶段继续，不重复提交。
			for _, pending := range swarmMgr.PendingAutoMergeBatches() {
				swarmPipeline.ResumeBatch(context.Background(), pending.Batch, pending.SHA, swarm.MergeStageFromString(pending.Stage))
			}
			go func() {
				removed, err := swarmMgr.SweepOrphanCopies(context.Background())
				if len(removed) > 0 {
					log.Printf("swarm: 已清理 %d 个孤儿副本", len(removed))
				}
				if err != nil {
					log.Printf("swarm: 清理孤儿副本出错: %v", err)
				}
			}()
		}
		startuptrace.Mark("swarm restored")
		backend.SetSwarm(true, func(id string) bool { return swarmMgr.State(id).Enabled })
		// 新建会话的编排开关为 true（显式传入或从记忆补齐）时在创建时就启用，
		// 与 threads.swarm.set 走同一个管理器，重复启用幂等。
		backend.SetSwarmEnabler(func(id string, enabled bool) { swarmMgr.SetEnabled(id, enabled) })
		// 转发会话时让新会话继承源会话的编排开关与只规划覆盖值。
		backend.SetSwarmInheritor(swarmMgr.InheritLead)
		// 撤销会还原工作区文件：同一工作区还有子代理在跑（包括共享工作区模式直接在主工作区工作的 bee）时
		// 拒绝撤销，避免撤销与自动合并回来的改动互相覆盖。
		backend.SetRevertBusyCheck(func(_ string, root string) string {
			const busyReason = "这个工作区还有子代理在运行，改动会合并回来，等它们结束再撤销"
			for leadID, bees := range swarmMgr.RunningBees() {
				if workDir, ok := backend.ThreadWorkDir(leadID); ok && workDir != "" && dirWithinRoot(workDir, root) {
					return busyReason
				}
				for _, bee := range bees {
					if bee.WorktreePath != "" && dirWithinRoot(bee.WorktreePath, root) {
						return busyReason
					}
				}
			}
			return ""
		})
	}

	var kanbanLeader *kanban.Leader
	var kanbanLeaderHeld func() bool
	if opts.DataDir != "" {
		kanbanLeader = kanban.NewLeader(filepath.Join(opts.DataDir, "kanban-scheduler.lock"))
		kanbanLeaderHeld = kanbanLeader.Held
	}
	kanbanOpts := kanban.Options{
		Store:       dbStore,
		Git:         gitRunner,
		Driver:      backend,
		StopSession: func(ctx context.Context, id string) error { _, err := backend.InterruptCurrent(ctx, id); return err },
		Projects:    backend,
		WorktreeDir: filepath.Join(opts.DataDir, "worktrees"),
		DataDir:     opts.DataDir,
		Leader:      kanbanLeaderHeld,
		AutoMerge: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.Workbench.AutoMergeTaskWorktree()
			}
			return true
		},
		AutoCleanupMergedWorktrees: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.SourceControl.AutoCleanupMergedWorktreesEnabled()
			}
			return true
		},
		AutoReview: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.Workbench.KanbanAutoReviewEnabled()
			}
			return true
		},
		AutoStartOnCreate: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.Workbench.KanbanAutoStartOnCreate
			}
			return false
		},
		AutoRescueStuck: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.Workbench.AutoRescueStuckEnabled()
			}
			return true
		},
		RunInWorktree: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.Workbench.KanbanRunInWorktreeEnabled()
			}
			return true
		},
		StewardTasks: func() kanban.StewardTaskDefaults {
			c, err := config.Load(opts.DataDir)
			if err != nil {
				return kanban.StewardTaskDefaults{}
			}
			config.ApplyEnv(&c, os.LookupEnv)
			return stewardTaskDefaults(c)
		},
		StuckNudgeTimeout: func() time.Duration {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return time.Duration(c.Workbench.StuckNudgeTimeout) * time.Second
			}
			return time.Duration(config.DefaultStuckNudgeTimeout) * time.Second
		},
		StuckInterruptTimeout: func() time.Duration {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return time.Duration(c.Workbench.StuckInterruptTimeout) * time.Second
			}
			return time.Duration(config.DefaultStuckInterruptTimeout) * time.Second
		},
	}
	if swarmMgr != nil {
		kanbanOpts.DelegatingBees = func(id string) bool { return len(swarmMgr.RunningBees()[id]) > 0 }
	}
	kanbanSvc := kanban.NewService(kanbanOpts)

	goalScheduler := kanban.NewGoalScheduler(kanbanSvc, 10*time.Second)
	goalScheduler.Start()

	watchdog := kanban.NewWatchdog(kanbanSvc, 5*time.Second)
	watchdog.Start()

	var mcpProxy *mcp.Proxy
	var mcpHub *mcp.Hub
	var mcpSrv *mcp.Server
	var autoSvc *automation.Service
	// autoSleep 是自动化防休眠守卫，跟随 autoSvc 一起启动与停止。
	var autoSleep *remote.SleepGuard
	// attentionSvc 与 attentionSrc 在 client 装配之后才构造：自动化结算回调通过闭包延迟引用，
	// 构造阶段为空时回调也不会出现空指针。
	var attentionSvc *attention.Service
	var attentionSrc *attentionWiring
	var appInstance *App
	var appInstanceMu sync.RWMutex
	cleanup := func() {
		if attentionSrc != nil {
			attentionSrc.Stop()
		}
		if mcpProxy != nil {
			_ = mcpProxy.Close()
		}
		if mcpHub != nil {
			_ = mcpHub.Close()
		}
		if swarmMgr != nil {
			swarmMgr.Stop()
		}
		if mcpSrv != nil {
			_ = mcpSrv.Close()
		}
		if autoSleep != nil {
			autoSleep.Stop()
		}
		if autoSvc != nil {
			autoSvc.Stop()
		}
		watchdog.Stop()
		goalScheduler.Stop()
		kanbanSvc.Close()
		if kanbanLeader != nil {
			if err := kanbanLeader.Close(); err != nil {
				log.Printf("kanban: 释放调度锁失败: %v", err)
			}
		}
		hub.Close()
		backend.Close()
		_ = dbStore.Close()
	}

	oauthMgr := mcp.NewOAuthManager(mcp.OAuthOptions{
		Secrets: config.NewSecretStore("mcode.mcp-oauth"),
		OpenBrowser: func(authURL string) error {
			switch runtime.GOOS {
			case "darwin":
				return exec.Command("open", authURL).Start()
			case "windows":
				return exec.Command("rundll32", "url.dll,FileProtocolHandler", authURL).Start()
			default:
				return exec.Command("xdg-open", authURL).Start()
			}
		},
	})

	mcpHub = mcp.NewHub(oauthMgr)

	mcpProxy = mcp.NewProxy(mcpHub)
	if err := mcpProxy.Start(context.Background()); err != nil {
		cleanup()
		return nil, fmt.Errorf("start mcp proxy: %w", err)
	}
	startuptrace.Mark("mcp proxy started")

	// Nowledge Mem 客户端在这里创建：内置 MCP 的记忆工具需要它，记忆注入与会话同步稍后接入。
	// 拿不到客户端（未安装/未配置）时保持 nil，记忆工具视为未配置。
	nowledgeClient, nowledgeErr := nmem.Default()
	if nowledgeErr != nil {
		log.Printf("初始化 Nowledge Mem 客户端失败，跳过记忆注入与会话同步: %v", nowledgeErr)
	}
	memoryCfg := cfg.Memory
	if c, err := config.Load(opts.DataDir); err == nil {
		config.ApplyEnv(&c, os.LookupEnv)
		memoryCfg = c.Memory
	}
	memoryMode := resolveMemoryBackend(memoryCfg.BackendMode(), nowledgeClient, nowledgeErr)
	setMemoryBackend(memoryMode)
	var localDocs *localmem.Docs
	if memoryMode == config.MemoryBackendLocal {
		nowledgeClient = nil
		localDocs = localmem.New("")
	}
	mcpOpts := mcp.Options{
		Store:       dbStore,
		Kanban:      kanbanSvc,
		WorktreeDir: filepath.Join(opts.DataDir, "worktrees"),
		KanbanEnabled: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.Workbench.KanbanMCPToolsEnabled()
			}
			return true
		},
		KanbanEntryOrdinary: func() bool {
			if c, err := config.Load(opts.DataDir); err == nil {
				config.ApplyEnv(&c, os.LookupEnv)
				return c.Workbench.KanbanEntryOrdinarySessions()
			}
			return true
		},
		// CapabilityEnabled 留空即全部开启；后续按项目裁剪 MCP 工具能力时可在此接入（例如读配置或按项目设置）。
		ResolveProjectID: func(ctx context.Context, workDir string) (string, bool) {
			if swarmMgr == nil {
				return "", false
			}
			return swarmMgr.ProjectIDForWorkDir(workDir)
		},
		// SwarmBees 返回本主会话的 bee 快照，供 swarm_report_get 的列表模式使用。
		SwarmBees: func(leadThreadID string) []mcp.SwarmBeeSummary {
			if swarmMgr == nil {
				return nil
			}
			bees := swarmMgr.State(leadThreadID).Bees
			out := make([]mcp.SwarmBeeSummary, 0, len(bees))
			for _, bee := range bees {
				out = append(out, mcp.SwarmBeeSummary{
					ID:             bee.ThreadID,
					Name:           bee.Name,
					Task:           bee.Task,
					Tier:           bee.Tier,
					Status:         string(bee.Status),
					StartedAt:      bee.StartedAt,
					FinishedAt:     bee.FinishedAt,
					LastProgressAt: bee.LastProgressAt,
				})
			}
			return out
		},
	}
	// 记忆工具只读项目空间与全局空间；用户明确要求记住时 memory_propose 经 MemoryCommit 直接写入。
	if localDocs != nil {
		mem := &localMemoryTools{docs: localDocs, threads: localmem.Threads{Store: dbStore}, store: dbStore}
		mcpOpts.MemorySearch = mem.search
		mcpOpts.MemoryGet = mem.get
		mcpOpts.MemoryForget = mem.forget
		mcpOpts.ThreadSearch = mem.searchThreads
		mcpOpts.ThreadFetch = mem.fetchThread
		mcpOpts.MemoryCommit = mem.commit
	} else if nowledgeClient != nil {
		mem := &nowledgeTools{client: nowledgeClient, dataDir: opts.DataDir, store: dbStore}
		mcpOpts.MemorySearch = mem.search
		mcpOpts.MemoryGet = mem.get
		mcpOpts.MemoryForget = mem.forget
		mcpOpts.ThreadSearch = mem.searchThreads
		mcpOpts.ThreadFetch = mem.fetchThread
		mcpOpts.MemoryCommit = mem.commit
	}
	mcpSrv = mcp.New(mcpOpts)
	if err := mcpSrv.Start(context.Background()); err != nil {
		cleanup()
		return nil, fmt.Errorf("start mcp server: %w", err)
	}
	startuptrace.Mark("mcp server started")

	if mcpProxy != nil && mcpSrv != nil {
		mcpProxy.SetBuiltin(mcpSrv)
	}

	// 内置 MCP 端点：代理已启动时优先按会话路由，否则退回按 workdir 头识别项目的共享端点。
	builtinMCP := harness.BuiltinMCP{
		URL:   mcpSrv.URL(),
		Token: mcpSrv.Token(),
	}
	if mcpProxy != nil {
		builtinMCP.ThreadBase = mcpProxy.BuiltinBase()
		builtinMCP.ThreadToken = mcpProxy.Token()
	}

	mcpRouter := &mcp.SessionRouter{Hub: mcpHub, Proxy: mcpProxy}
	// externalMCP 注入外部 MCP 端点；会话级禁用名单从后端会话摘要里取，threadID 为空时沿用全局设置。
	externalMCP := func(workDir, threadID string) []harness.MCPEndpoint {
		projectDir := workDir
		if project, err := mcpSrv.ProjectForWorkDir(context.Background(), workDir); err == nil {
			projectDir = project.Path
		}
		var disabled map[string]bool
		if keys := backend.ThreadMCPDisabled(threadID); len(keys) > 0 {
			disabled = make(map[string]bool, len(keys))
			for _, key := range keys {
				disabled[key] = true
			}
		}
		return mcpRouter.Endpoints(projectDir, workDir, disabled)
	}
	// 仓库内未信任的服务仍可能被 codex 从工作目录配置拉起，需要在线程配置里强行关闭。
	blockedMCP := func(workDir string) []string {
		projectDir := workDir
		if project, err := mcpSrv.ProjectForWorkDir(context.Background(), workDir); err == nil {
			projectDir = project.Path
		}
		return mcpRouter.UntrustedServerNames(projectDir, "codex")
	}
	// 与启动时的 Provider 构造、运行时重建共用同一份 MCP 注入配置。
	mcpInjection := providerMCP{Builtin: builtinMCP, External: externalMCP, Blocked: blockedMCP}

	var permMode harness.PermissionMode
	if opts.Mode == "" {
		permMode = harness.ModeFullAccess
	} else {
		var err error
		permMode, err = harness.ParseMode(opts.Mode)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("parse permission mode %q: %w", opts.Mode, err)
		}
	}

	if err := migrateLegacyMagpieEndpoints(opts.DataDir, &cfg, config.NewSecretStore("mcode")); err != nil {
		log.Printf("failed to migrate legacy magpie endpoints: %v", err)
	}
	if err := migrateMagpieRoutingV1(opts.DataDir, &cfg); err != nil {
		log.Printf("failed to migrate magpie routing: %v", err)
	}
	// 掉电可能把被接管的配置文件写成整份 0x00：长度在、数据没落盘。codex 这类
	// agent 会因此直接起不来，启动时按 applied 记录补回 mcode 管理的字段。
	if repaired, err := magpiecfg.RepairZeroed(opts.DataDir); err != nil {
		log.Printf("failed to repair zeroed agent config: %v", err)
	} else {
		for _, path := range repaired {
			log.Printf("repaired zeroed agent config %s", path)
		}
	}
	if err := migrateLegacySwarmProfiles(opts.DataDir, &cfg); err != nil {
		log.Printf("failed to migrate legacy swarm profiles: %v", err)
	}
	// 全局 Swarm 开关：会话模式为「跟随设置」时用它们推导实际生效的模式。
	swarmGlobalMode := func() swarm.GlobalMode {
		g := swarm.GlobalMode{
			AlwaysBees:    cfg.Orchestration.AlwaysBees,
			LeadPlansOnly: cfg.Orchestration.LeadPlansOnly,
		}
		if c, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&c, os.LookupEnv)
			g.AlwaysBees = c.Orchestration.AlwaysBees
			g.LeadPlansOnly = c.Orchestration.LeadPlansOnly
		}
		return g
	}
	// swarmModeResolver 返回会话当前生效的 Swarm 工作模式：会话级设置优先，「跟随设置」时
	// 按全局开关推导；Swarm 面板的会话级「只规划」开关作为全局值的会话覆盖保留。
	swarmModeResolver := func(threadID string) string {
		threadMode := ""
		if t, err := backend.GetThread(context.Background(), threadID); err == nil {
			threadMode = t.SwarmMode
		}
		global := swarmGlobalMode()
		if swarm.NormalizeMode(threadMode) == swarm.ModeInherited && swarmMgr != nil {
			if po := swarmMgr.State(threadID).PlansOnly; po != nil {
				global.LeadPlansOnly = *po
			}
		}
		return swarm.EffectiveMode(threadMode, global)
	}
	kanbanPreface := func(threadID string) string {
		return kanbanSvc.CoordinatorPreface(context.Background(), threadID)
	}
	if swarmMgr == nil {
		// Swarm 未接线（缺少 git）时不会走 wireSwarm，单独安装只做 preface 前置的装饰器；
		// 看板段经 harness/policy 渲染，前缀始终带版本标记。
		backend.SetPromptDecorator(prefaceDecorator(func(threadID string) string {
			return policy.Render(hostPolicyCaps(), policy.Sections{
				Kanban: kanbanPreface(threadID),
			})
		}, nil))
	} else {
		wireSwarmWithModes(context.Background(), swarmMgr, backend, mcpSrv, kanbanPreface, swarmModeResolver)
	}

	// 普通会话的「可以交给总管」提示只在首轮注入一次，挂在已有的首轮指导词机制上，
	// 不再每轮前置，避免随历史累积与每轮读磁盘。bee 和前台会话不带。
	backend.SetFirstTurnExtra(func(threadID string) string {
		if swarmMgr != nil {
			if _, isBee := swarmMgr.BeeOwner(threadID); isBee {
				return ""
			}
		}
		if id, _, err := dbStore.GetSetting(context.Background(), "steward.feed_thread_id"); err == nil && id == threadID {
			return ""
		}
		return frontdeskHandoffSection(opts.DataDir, threadID)
	})

	// Nowledge Mem 接入：客户端已在上方创建，拿到后首轮 Working Memory 注入与会话同步才生效。
	if nowledgeClient != nil {
		backend.SetNowledge(nowledgeClient)
	}
	if localDocs != nil {
		backend.SetLocalMemory(localDocs.GlobalGuidance)
	}

	backend.SetGeneralSettingsFunc(func() config.Config {
		if c, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&c, os.LookupEnv)
			return c
		}
		return cfg
	})
	backend.SetWorkbenchSettingsFunc(func() config.WorkbenchConfig {
		if c, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&c, os.LookupEnv)
			return c.Workbench
		}
		return cfg.Workbench
	})

	backend.SetNowledgeSettingsFunc(func() config.MemoryConfig {
		if c, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&c, os.LookupEnv)
			return c.Memory
		}
		return cfg.Memory
	})

	// 上次退出时没传完的会话记在 app_settings 里（此时 AttachStore 与 SetNowledge 都已完成），
	// 这里在后台补传，不等待网络；未启用同步或未注入客户端时只保留标记。
	backend.ResumeNowledgeSync()

	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		if c, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&c, os.LookupEnv)
			return c.Orchestration
		}
		return cfg.Orchestration
	})

	backend.SetProviderEnabledFunc(func() map[string]bool {
		c := cfg
		if loaded, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&loaded, os.LookupEnv)
			c = loaded
		}
		return mcodeui.ProviderEnabledMap(c)
	})

	// 远程端新建会话的默认草稿沿用桌面的模型记忆与编排选择。
	backend.SetDraftDefaultsFunc(func() wire.ComposerDraftDefaults {
		c := cfg
		if loaded, err := config.Load(opts.DataDir); err == nil {
			config.ApplyEnv(&loaded, os.LookupEnv)
			c = loaded
		}
		return remoteDraftDefaults(c)
	})

	// 用户在远程端主动确认的模型与编排选择写回同一份模型记忆，桌面下次新建会话据此初始化。
	backend.SetRememberSelectionFunc(rememberRemoteSelection)

	wireAutoTitle(backend, opts.DataDir)
	// Dispatcher 在 remote_service.go / cmd 中构造，这里通过包级回退注入标题重生成回调。
	remote.DefaultTitleRegenerator = func(ctx context.Context, threadID string) (string, error) {
		return RegenerateTitle(ctx, backend, opts.DataDir, threadID)
	}
	codexBin := opts.CodexBin
	if codexBin == "" {
		codexBin = cfg.Providers.Codex.Bin
	}
	if codexBin == "" {
		codexBin = "codex"
	}
	resolvedCodex, codexErr := loginenv.LookPath(codexBin)
	if codexErr == nil {
		resolvedCodex, codexErr = filepath.Abs(resolvedCodex)
		if codexErr == nil {
			codexBin = resolvedCodex
		}
	}

	probe := <-claudeProbe
	claudeErr, claudeAuthErr := probe.lookPathErr, probe.authErr
	startuptrace.Mark("claude auth probed")
	claudeAvailable := claudeErr == nil && claudeAuthErr == nil

	// provider CLI 缺失或未登录时不再阻止启动：记一条警告后继续，界面里可以在设置中配置 CLI 路径或安装后重启。
	if opts.Provider == "claude" && claudeErr != nil {
		log.Printf("warning: claude binary %q not found in PATH: %v; MCode 将继续启动，可在设置中配置 CLI 路径或安装后重启", claudeBin, claudeErr)
	}
	if opts.Provider == "claude" && claudeAuthErr != nil {
		log.Printf("warning: %v; MCode 将继续启动，可在设置中配置 CLI 路径或安装后重启", claudeAuthErr)
	}
	if opts.Provider == "codex" && codexErr != nil {
		log.Printf("warning: codex binary %q not found in PATH: %v; MCode 将继续启动，可在设置中配置 CLI 路径或安装后重启", codexBin, codexErr)
	}

	var primaryHarness harness.Harness
	var otherHarness harness.Harness
	var agyAccountsMgr *accounts.Manager
	var agyHarness harness.Harness

	if opts.Provider != "memory" {
		fi, err := os.Stat(absWorkDir)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("workdir %q does not exist: %w", absWorkDir, err)
		}
		if !fi.IsDir() {
			cleanup()
			return nil, fmt.Errorf("workdir %q is not a directory", absWorkDir)
		}

		if claudeAvailable || codexErr == nil {
			if opts.Provider == "codex" {
				if codexErr == nil {
					primaryHarness = codex.New(codex.Options{
						MagpieRoutes: codexMagpieRoutes,
						Binary:       codexBin,
						BuiltinMCP:   builtinMCP,
						ExternalMCP:  externalMCP,
						BlockedMCP:   blockedMCP,
					})
				}
				if claudeAvailable {
					// 线程空闲期间下一轮复用同一个 claude 进程，省掉冷启动（时长见 providers.claude.residentIdleMinutes）。
					otherHarness = claude.New(claude.WithResident(claude.Options{
						Binary:       claudeBin,
						BuiltinMCP:   builtinMCP,
						ExternalMCP:  externalMCP,
						MagpieRoutes: claudeMagpieRoutes,
					}, cfg.Providers.Claude))
				}
			} else {
				if claudeAvailable {
					// 线程空闲期间下一轮复用同一个 claude 进程，省掉冷启动（时长见 providers.claude.residentIdleMinutes）。
					primaryHarness = claude.New(claude.WithResident(claude.Options{
						Binary:       claudeBin,
						BuiltinMCP:   builtinMCP,
						ExternalMCP:  externalMCP,
						MagpieRoutes: claudeMagpieRoutes,
					}, cfg.Providers.Claude))
					if codexErr == nil {
						otherHarness = codex.New(codex.Options{
							MagpieRoutes: codexMagpieRoutes,
							Binary:       codexBin,
							BuiltinMCP:   builtinMCP,
							ExternalMCP:  externalMCP,
							BlockedMCP:   blockedMCP,
						})
					}
				} else if codexErr == nil {
					primaryHarness = codex.New(codex.Options{
						MagpieRoutes: codexMagpieRoutes,
						Binary:       codexBin,
						BuiltinMCP:   builtinMCP,
						ExternalMCP:  externalMCP,
						BlockedMCP:   blockedMCP,
					})
				}
			}

			if primaryHarness != nil {
				backend.SetHarness(primaryHarness, absWorkDir, permMode)
			}
			if otherHarness != nil {
				backend.RegisterHarness(otherHarness)
			}
		}

		agyAccountsMgr, agyHarness = registerExtraProviders(backend, cfg, opts.DataDir, opts.AgyAccountsOptions, mcpInjection)
		startuptrace.Mark("extra providers registered")

		// 用户显式选择 agy 时把它设为默认 provider（含工作目录与权限模式），新线程默认走 agy；
		// agy 未注册（providers.agy.enabled=false）时只记警告，不阻止启动。
		if opts.Provider == "agy" {
			if agyHarness != nil {
				backend.SetHarness(agyHarness, absWorkDir, permMode)
			} else {
				log.Printf("warning: provider agy is not available (providers.agy.enabled=%v); MCode 将继续启动，可在设置中配置 CLI 路径或安装后重启", cfg.Providers.Agy.Enabled)
			}
		}
	}

	autoSvc = automation.New(automation.Options{
		Store:     dbStore,
		Driver:    backend,
		Projects:  backend,
		Evaluator: &automation.Evaluator{Harness: primaryHarness},
		// 跨进程调度锁落在 <DataDir>/automation/scheduler.lock，保证多个实例只有一个负责触发。
		DataDir: opts.DataDir,
		// 运行结算只在这里挂一次：attention 服务此时可能尚未构造，闭包内延迟引用并判空。
		OnRunSettled: func(def automation.Definition, run store.AutomationRun) {
			if attentionSrc != nil {
				attentionSrc.HandleAutomationRun(def, run)
			}
		},
	})
	autoSvc.Start()
	// 自动化防休眠：开关开启时，运行中的自动化或 5 分钟内将到期的自动化会阻止电脑休眠，
	// 保证定时任务按时触发。配置每次现读，设置里改完开关即时生效。
	autoSleep = remote.NewSleepGuard(func() bool {
		enabled := true
		if c, err := config.Load(opts.DataDir); err == nil {
			enabled = c.Automation.PreventSleepEnabled()
		}
		if !enabled {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return automationKeepAwake(enabled, autoSvc.HasRunningRun(), func() bool {
			return autoSvc.NextRunWithin(ctx, 5*time.Minute)
		})
	}, 20*time.Second)
	autoSleep.Start()
	startuptrace.Mark("automation started")
	mcpSrv.SetAutomation(autoSvc)

	projectName := filepath.Base(absWorkDir)
	projectPath := absWorkDir

	existingProjects, err := backend.ListProjects(context.Background())
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	foundProject := false
	for _, p := range existingProjects.Projects {
		if p.Path == projectPath {
			foundProject = true
			break
		}
	}

	// 演示项目只在首次启动时种一次：用户移除后重启不再复活。
	const demoSeededKey = "demo_seeded"
	// 读取失败时按已种入处理，避免重复创建演示项目。
	demoSeeded := true
	if _, ok, err := dbStore.GetSetting(context.Background(), demoSeededKey); err != nil {
		log.Printf("failed to read %s flag: %v", demoSeededKey, err)
	} else {
		demoSeeded = ok
	}

	if !foundProject && !demoSeeded {
		projectID := newUUID()
		threadID := newUUID()
		messageID := newUUID()
		nowRFC3339 := time.Now().UTC().Format(time.RFC3339)

		project := wire.ProjectSummary{
			ID:          projectID,
			Name:        projectName,
			Path:        projectPath,
			IsExpanded:  true,
			ThreadCount: 1,
		}
		backend.AddProject(project)

		demoProvider := "memory"
		switch opts.Provider {
		case "claude":
			demoProvider = "claude"
		case "codex":
			demoProvider = "codex"
		case "agy":
			demoProvider = "agy"
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
			InteractionMode:   "build",
			FastMode:          false,
			WorktreeRequested: false,
			SwarmEnabled:      false,
		}

		welcomeText := "这是 MCode 内存后端的演示线程，发一条消息试试流式回复"
		switch opts.Provider {
		case "claude":
			welcomeText = fmt.Sprintf("这是 Claude Code 演示线程，消息会交给 Claude Code 在 %s 目录下执行（当前权限模式：%s）。", absWorkDir, permMode)
		case "codex":
			welcomeText = fmt.Sprintf("这是 Codex 演示线程，消息会交给 Codex 在 %s 目录下执行（当前权限模式：%s）。", absWorkDir, permMode)
		case "agy":
			welcomeText = fmt.Sprintf("这是 Antigravity（agy）演示线程，消息会交给 agy 在 %s 目录下执行（当前权限模式：%s）。", absWorkDir, permMode)
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

	if !demoSeeded {
		if err := dbStore.SetSetting(context.Background(), demoSeededKey, "1"); err != nil {
			log.Printf("failed to write %s flag: %v", demoSeededKey, err)
		}
	}

	client := NewClient(backend)
	client.updateChecker = &UpdateChecker{
		FeedURL: UpdateFeedURL, CurrentVersion: AppVersion,
		Enabled: func() bool {
			loaded, err := config.Load(opts.DataDir)
			return err == nil && loaded.AutoCheckUpdateEnabled()
		},
		OnChecked: client.notifyConfigChanged,
	}
	updateCtx, updateCancel := context.WithCancel(context.Background())
	client.updateChecker.Start(updateCtx, 6*time.Hour)
	// 待处理事件（收件箱）：会话、看板、自动化、bee 四类事件源都在这里接入，
	// 界面层用 client.(mcodeui.AttentionClient) 读取，未实现该接口时不显示收件箱。
	attentionSvc = attention.New(dbStore, attention.Options{})
	client.SetAttention(attentionSvc)
	attentionSrc = newAttentionWiring(attentionWiringOptions{
		Service:              attentionSvc,
		Hub:                  hub,
		Kanban:               kanbanSvc,
		Swarm:                swarmMgr,
		Client:               client,
		Store:                dbStore,
		Sessions:             backend,
		DataDir:              opts.DataDir,
		AppendStewardMessage: backend.AppendStewardMessage,
		Notify: func(title, body, threadID string) {
			appInstanceMu.RLock()
			instance := appInstance
			appInstanceMu.RUnlock()
			if instance != nil {
				instance.notifyAttention(title, body, threadID)
			}
		},
	})
	attentionSrc.Start()
	rs := NewRemoteService(opts.DataDir, backend)
	client.SetRemoteService(rs)
	if swarmMgr != nil {
		// 远程 Dispatcher 与桌面端共用同一份 Swarm 管理器与 Client 能力，web 端才能看到合并、提问与档位配置。
		rs.SetSwarmWiring(swarmMgr, remoteSwarmExtras{client: client})
		// 管理器的 OnChange 已被 Client.SetSwarmManager 独占，这里订阅 Client 的变更分发，
		// 让桌面端的进度与状态变化也能推 threads.swarm.changed。
		rs.SetSwarmWatcher(client.WatchSwarm)
	}
	if attentionSvc != nil {
		// 待处理事件变更推 attention.changed，让 web 端的收件箱角标与列表跟着刷新。
		rs.SetAttentionWatcher(client.WatchAttention, client.AttentionUnresolvedCount)
	}
	client.SetProviderStore(opts.DataDir, secrets)
	// 装配层与 Client 共用同一个 Git 执行器：Client 自己懒建一个会让全局并发上限从 4 变 8，
	// 而「新建工作树时初始化子模块」的回推也挂在 SetGit 上，没有被调用就永远不会同步。
	// 必须在 SetProviderStore 之后调，SetGit 要读数据目录下的配置才能回推开关。
	if gitRunner != nil {
		client.SetGit(gitRunner)
	}
	client.SetProviderMCP(mcpInjection)
	// pi 的 Magpie 注入按最新配置生成：这里只记录数据目录与凭据存储，
	// 端点与勾选模型在每次 pi 启动、取模型目录时重新读取，设置改完下一次就生效。
	// 取目录的注入源无条件设上（不限于启动时已启用 pi）：运行期在设置里启用 pi 时，
	// 取到的模型目录也能直接带上这些 Magpie 模型，不用重启。
	setPiMagpieRuntime(opts.DataDir, secrets)
	remote.SetPiMagpieInjection(piMagpieInjection)
	remote.SetPiStaleModel(piStaleModel)
	// claude 的 Magpie 模型由运行时按前缀路由，模型目录同样按最新配置追加这些带前缀的行。
	remote.SetMagpieCatalog(magpieCatalogRows)
	// Codex 额度来源由 Codex 自己的配置决定（直连或 Magpie）：远程端用同一个读取器，
	// 才能与桌面端共享二进制、会话工作目录与 CODEX_HOME，避免两边来源判定不一致。
	remote.SetCodexQuotaReader(client.codexQuotaLimits)
	// web 端额度条：pi 线程的模型 ID 第一段是 Magpie 端点 provider id，端点与 key 在本层，
	// 所以额度的读取（含在用卡片标记）在这里注入，与桌面端 ProviderQuota 走同一条路。
	remote.SetPiQuotaReader(func(ctx context.Context, providerID string) (remote.PiQuotaSource, bool) {
		reading, err := client.piMagpieQuotaReadingFor(ctx, providerID)
		if err != nil || !reading.Found || len(reading.Windows) == 0 {
			return remote.PiQuotaSource{}, false
		}
		windows := make([]remote.PiQuotaWindow, 0, len(reading.Windows))
		for _, w := range reading.Windows {
			windows = append(windows, remote.PiQuotaWindow{
				Account:     w.Account,
				ID:          w.ID,
				Label:       w.Label,
				Period:      w.Period,
				UsedPercent: w.UsedPercent,
				ResetsAt:    w.ResetsAt,
				Active:      w.Active,
				Plan:        w.Plan,
				Display:     w.Display,
				Text:        w.Text,
				Unlimited:   w.Unlimited,
				Balance:     w.Balance,
				Unit:        w.Unit,
				Disabled:    w.Disabled,
				Source:      w.Source,
				ReadAt:      w.ReadAt,
			})
		}
		return remote.PiQuotaSource{Name: reading.Name, Windows: windows}, true
	})
	// 人设切换：每轮运行前按线程的 provider/model 读本机 magpie 的 persona-rules 人设，
	// 在 prompt 最前面加 [[persona:ID]]，由插件注入对应人设正文。非 Magpie 线程返回空串，
	// 不加标签；和 Magpie 注入一样每次重新读配置，设置改完下一轮就生效。
	backend.SetPersonaTagResolver(client.personaTag)
	if cfg.Remote.Enabled {
		if err := rs.Start(cfg.Remote.Listen, cfg.Remote.ExtraHosts); err != nil {
			log.Printf("failed to start remote service: %v", err)
		}
		startuptrace.Mark("remote started")
	}
	if kanbanSvc != nil {
		client.SetKanban(kanbanSvc)
		kanbanSvc.SetOnChange(client.notifyKanbanChanged)
	}
	if autoSvc != nil {
		client.SetAutomation(autoSvc)
		autoSvc.SetOnChange(client.notifyAutomationChanged)
	}
	if swarmMgr != nil {
		client.SetSwarmManager(swarmMgr)
	}
	if swarmPipeline != nil {
		client.SetSwarmPipeline(swarmPipeline)
	}
	// 首次启动清理 Swarm 改名前遗留的 hydra 副本：只删已合并且干净的副本，未合并的留在
	// 设置页由用户手动处理。放到 swarm 恢复之后的后台 goroutine，既不阻塞启动，也能让
	// inUse 看到正在运行的 bee 工作目录；已归档会话绑定的副本由内部会话列表合并保护。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), legacyHydraCleanupTimeout)
		defer cancel()
		if err := runLegacyHydraCleanupOnce(ctx, dbStore, client.gitRunner(), filepath.Join(opts.DataDir, "hydra-worktrees"), client.worktreeRepoRoots, client.worktreeInUseFunc()); err != nil {
			log.Printf("旧副本清理未完成: %v", err)
		}
	}()

	client.SetMCPOAuth(oauthMgr)
	client.SetAgyAccounts(agyAccountsMgr)
	backend.SetAgyAccounts(agyAccountsMgr)
	// 额度弹窗靠线程上落盘的 provider 会话 ID 找当前会话绑定的 agy 账号：
	// 线程还没跑过一轮（SessionID 为空）或尚未绑定时，弹窗退回新会话的目标账号。
	client.SetAgySessionLookup(func(ctx context.Context, threadID string) string {
		th, err := dbStore.GetThread(ctx, threadID)
		if err != nil {
			return ""
		}
		return th.SessionID
	})
	if swarmMgr != nil {
		swarmMgr.Start()
		if swarmRestored {
			// 编排接线、provider 注册与账号配置均已完成，驱动此时才能续接原会话。
			go swarmMgr.ResumeAllInterrupted(context.Background())
		}
	}

	appInstanceMu.Lock()
	appInstance = &App{
		client:        client,
		dataDir:       opts.DataDir,
		hub:           hub,
		backend:       backend,
		store:         dbStore,
		kanban:        kanbanSvc,
		scheduler:     goalScheduler,
		automation:    autoSvc,
		swarm:         swarmMgr,
		swarmPipeline: swarmPipeline,
		mcp:           mcpSrv,
		mcpHub:        mcpHub,
		mcpProxy:      mcpProxy,
		remote:        rs,
		updateCancel:  updateCancel,
		modelsCancel:  modelsCancel,
		modelsDone:    modelsDone,
		attention:     attentionSvc,
		attentionSrc:  attentionSrc,
		// 旧收件箱迁移的取消函数与退出信号：App.Close 里先取消并等待，再关闭 store。
		inboxMigrateCancel: migrateCancel,
		inboxMigrateDone:   migrateDone,
		// automationSleep 与 autoSvc 同时创建，这里只做保存，清理时统一 Stop。
		automationSleep: autoSleep,
	}
	appInstanceMu.Unlock()
	migrateOwned = false
	modelsOwned = false
	if agyAccountsMgr != nil {
		ctx, cancel := context.WithCancel(context.Background())
		appInstance.quotaCancel = cancel
		appInstance.quotaDone = agyAccountsMgr.StartQuotaPoller(ctx)
	}

	// 总管接线：待处理事件服务与 MCP 服务都已就绪，钩子直接引用 App 的通知方法，
	// 所以放在 App 实例构造之后；总管自己的会话随后被排除在待处理事件之外。
	wireFrontdesk(appInstance, opts.DataDir)

	// bee 消息保留：后台每天压缩一次已结束满 30 天的 bee 会话消息。
	// 开关每次现读配置，读不到配置时跳过，不动数据。
	if dbStore != nil {
		appInstance.retention = startMessageRetentionScheduler(
			func(ctx context.Context) error {
				if _, err := dbStore.RunBeeMessageRetention(ctx, store.BeeRetentionConfig{}); err != nil {
					return err
				}
				// 冷归档：把长期无消息的会话压进冷存储，开关每次现读配置，读不到就跳过这一步。
				var coldErr error
				if cfg, err := config.Load(opts.DataDir); err != nil {
					log.Printf("冷归档: 读取配置失败，跳过本轮: %v", err)
				} else if cfg.Retention.ColdArchiveEnabled() {
					report, err := dbStore.RunColdArchive(ctx, store.ColdArchiveConfig{})
					if err != nil {
						log.Printf("冷归档: 执行失败: %v", err)
						coldErr = err
					} else {
						log.Printf("冷归档: 归档会话 %d 个、消息 %d 条，原始 %d 字节 -> 载荷 %d 字节",
							report.ThreadsArchived, report.MessagesArchived, report.RawBytes, report.PayloadBytes)
					}
				}
				// 压缩/删除消息只腾出 freelist，接着把空闲页还给文件系统，文件才会真的变小。
				reclaimed, err := dbStore.ReclaimFreePages(ctx, 0, 0)
				if err != nil && coldErr == nil {
					coldErr = err
				}
				if reclaimed > 0 {
					log.Printf("bee 消息保留: 回收空闲页 %d 页", reclaimed)
				}
				return coldErr
			},
			func() bool {
				cfg, err := config.Load(opts.DataDir)
				if err != nil {
					log.Printf("bee 消息保留: 读取配置失败，跳过本轮: %v", err)
					return false
				}
				return cfg.Retention.BeeMessagesEnabled()
			},
			messageRetentionFirstDelay,
			messageRetentionInterval,
		)
	}

	// 远程扩展接线：此时 backend 的各项 SetXxx 与 Swarm、看板、目标调度、自动化、Nowledge、
	// 账号管理器均已构造完成，a.client/a.backend/a.store/a.swarm/a.kanban/a.automation/a.mcp
	// 等字段都可用，扩展只需把服务注入 backend 即可，无需改动 New 本身。
	wireRemoteExtensions(appInstance)

	startuptrace.Mark("app.Open end")
	return appInstance, nil
}

// gateExtension 是某个 Provider 门禁扩展的安装结果：path 非空才可用，err 非空表示装不上。
type gateExtension struct {
	path string
	err  error
}

var (
	appDataDirMu sync.RWMutex
	appDataDir   string
)

func setAppDataDir(dir string) {
	if dir == "" {
		return
	}
	appDataDirMu.Lock()
	appDataDir = dir
	appDataDirMu.Unlock()
}

func getAppDataDir() string {
	appDataDirMu.RLock()
	defer appDataDirMu.RUnlock()
	if appDataDir != "" {
		return appDataDir
	}
	return os.Getenv("MCODE_DATA")
}

// gateExtensions 缓存启动时写出的门禁扩展。omp 与 pi 都只在加载了门禁扩展（`-e`）
// 时才把工具调用以门禁形式回到适配器审批，所以启动适配器时必须带上它。
var gateExtensions struct {
	once sync.Once
	omp  gateExtension
	pi   gateExtension
}

// loadGateExtensions 把内嵌的门禁扩展写到临时目录，返回 omp 与 pi 各自的安装结果。
// 一个进程一份临时目录；写不出来时把错误留在结果里，调用方必须据此拒绝启动对应 Provider——
// 没有门禁的 omp 会以 `--approval-mode yolo` 裸跑，界面上的只读/受监督档位等于全放行。
func loadGateExtensions() (ompExt, piExt gateExtension) {
	gateExtensions.once.Do(func() {
		dir, err := os.MkdirTemp("", "mcode-gates-")
		if err != nil {
			log.Printf("failed to create gate extension directory: %v", err)
			gateExtensions.omp.err = err
			gateExtensions.pi.err = err
			return
		}
		if path, err := omp.InstallGate(filepath.Join(dir, "omp")); err != nil {
			log.Printf("failed to install omp gate extension: %v", err)
			gateExtensions.omp.err = err
		} else {
			gateExtensions.omp.path = path
		}
		if path, err := pi.InstallGate(filepath.Join(dir, "pi")); err != nil {
			log.Printf("failed to install pi gate extension: %v", err)
			gateExtensions.pi.err = err
		} else {
			gateExtensions.pi.path = path
		}
	})
	return gateExtensions.omp, gateExtensions.pi
}

// piSkillFallbackLogOnce 保证「退回 pi 原生技能发现」的日志每个进程只打印一次。
var piSkillFallbackLogOnce sync.Once

// piSkillManifest 返回本轮要显式传给 pi 的技能目录，以及是否走「完整清单」模式。
//
// pi 自己会自动发现这几类技能：<agentDir>/skills（默认 ~/.pi/agent/skills，可被
// PI_CODING_AGENT_DIR 覆盖）、~/.agents/skills、项目 .pi/skills，以及工作目录向上
// 到 git 根各级目录的 .agents/skills；settings.json 的 skills 条目与 pi packages
// 带的技能也走同一条发现链路。mcode 固定传 --no-approve，pi 会跳过项目里的
// .agents/skills 与 .pi/skills，所以完整清单模式用 --no-skills 关掉上述全部来源，
// 再用 --skill 逐个加载 mcode 里未停用的技能：只有这样，用户在 mcode 里停用的技能
// 才不会被 pi 悄悄重新发现。
//
// settings.json 里的 skills 条目（支持 glob）无法逐条映射成 mcode 的停用状态，一旦
// 用户配了它就整体退回旧行为（不传 --no-skills，交给 pi 自己发现），宁可多加载也不
// 悄悄丢掉用户没停用的技能；扫描失败时同样退回旧行为。packages 不同：piPackageSkillRoots
// 能解析出每个包声明的技能根目录，解析不了才退回。
func piSkillManifest(workDir string) ([]string, bool) {
	if workDir == "" {
		return nil, false
	}
	home, _ := os.UserHomeDir()
	agentDir := pi.ConfigDir(nil, home)
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}
	pkgSkillRoots, ok := piPackageSkillRoots(agentDir, absWorkDir)
	if !ok {
		// 每次解析技能清单都会走到这里，日志只打一次，避免刷屏。
		piSkillFallbackLogOnce.Do(func() {
			log.Printf("pi: settings.json 的 packages 无法逐条解析，退回 pi 原生技能发现")
		})
		return nil, false
	}
	if piSettingsDeclareSkills(agentDir, absWorkDir) {
		piSkillFallbackLogOnce.Do(func() {
			log.Printf("pi: settings.json 里配置了 skills，退回 pi 原生技能发现")
		})
		return nil, false
	}
	scanOpts := agentconf.Options{
		Home:       home,
		ProjectDir: absWorkDir,
		DataDir:    getAppDataDir(),
	}
	skills, err := agentconf.ScanSkills(scanOpts)
	if err != nil {
		log.Printf("pi: 扫描技能失败，退回 pi 原生技能发现：%v", err)
		return nil, false
	}

	roots := piSkillRoots(home, agentDir, absWorkDir)
	rootPaths := make([]string, 0, len(roots))
	for _, root := range roots {
		rootPaths = append(rootPaths, root.path)
	}
	seen := make(map[string]bool)
	known := make(map[string]bool)
	result := make([]string, 0, len(skills))
	add := func(path string) {
		abs := piAbsClean(path)
		if abs == "" || seen[abs] {
			return
		}
		seen[abs] = true
		result = append(result, abs)
	}
	for _, sk := range skills {
		// 只取 pi 会发现的来源：~/.agents/skills 与项目 .agents/skills 在 agentconf
		// 里记在 codex 名下，所以按路径落在哪个来源目录下来认，不看 Agent 字段。
		skillDir := sk.Path
		if filepath.Base(skillDir) == "SKILL.md" {
			skillDir = filepath.Dir(skillDir)
		}
		known[piAbsClean(skillDir)] = true
		if !sk.Enabled || sk.Disabled {
			continue
		}
		if !piUnderAnyRoot(skillDir, rootPaths) {
			continue
		}
		add(skillDir)
	}
	// 包自带的技能：pi 会把 --skill 目录递归展开，所以传声明里的根目录即可。
	// mcode 没有这些技能的停用记录，行为与 pi 原生发现一致（全部加载）。
	for _, dir := range pkgSkillRoots {
		add(dir)
	}
	// 补扫：agentconf 只按来源目录的一层子目录建清单，pi 却会递归找 SKILL.md，
	// 并把 <agentDir>/skills 与项目 .pi/skills 根级的 *.md 也当技能；祖先目录下
	// 的 .agents/skills 更是完全不在 agentconf 的扫描范围里（它只扫当前项目）。
	// 这些路径没被 agentconf 列出来，所以按同名停用记录再筛一道（agentconf 会把
	// 同名技能合并成一条，停用那一条要连带生效），剩下的都当作启用。
	for _, root := range roots {
		for _, path := range piSkillPathsIn(root.path, root.rootFiles) {
			if known[piAbsClean(path)] {
				continue
			}
			name := filepath.Base(path)
			if strings.HasSuffix(name, ".md") {
				name = strings.TrimSuffix(name, ".md")
			}
			if !agentconf.IsSkillActive(scanOpts, agentconf.Skill{Agent: root.agent, Scope: root.scope, Name: name}) {
				continue
			}
			add(path)
		}
	}
	sort.Strings(result)
	return result, true
}

// piSkillRoot 是 pi 会自动发现的一个技能来源目录。
// rootFiles 为 true 表示根级 *.md 也算技能（pi 的 <agentDir>/skills 与项目 .pi/skills
// 是这种布局；.agents/skills 只认子目录里的 SKILL.md）。
// agent 与 scope 是 agentconf 里对应来源的身份，用于按名称查停用记录。
type piSkillRoot struct {
	path      string
	rootFiles bool
	agent     string
	scope     string
}

// piSkillRoots 返回 pi 会自动发现的全部技能来源目录（与 pi 的 package-manager 一致），
// 含工作目录向上到 git 根各级目录的 .agents/skills。
func piSkillRoots(home, agentDir, workDir string) []piSkillRoot {
	roots := []piSkillRoot{
		{path: filepath.Join(agentDir, "skills"), rootFiles: true, agent: "pi", scope: "global"},
		{path: filepath.Join(home, ".agents", "skills"), agent: "codex", scope: "global"},
		{path: filepath.Join(workDir, ".pi", "skills"), rootFiles: true, agent: "pi", scope: "project"},
		{path: filepath.Join(workDir, ".agents", "skills"), agent: "codex", scope: "project"},
	}
	for _, dir := range piAncestorAgentsSkillDirs(workDir, home) {
		roots = append(roots, piSkillRoot{path: dir, agent: "codex", scope: "project"})
	}
	return roots
}

// piAncestorAgentsSkillDirs 返回工作目录之上各级目录的 .agents/skills，到 git 根（含）为止，
// 不含工作目录自己与用户级 ~/.agents/skills（后者已在全局来源里按停用状态处理过）。
func piAncestorAgentsSkillDirs(workDir, home string) []string {
	userAgentsSkills := filepath.Clean(filepath.Join(home, ".agents", "skills"))
	gitRoot := piGitRoot(workDir)
	var dirs []string
	for dir := filepath.Dir(filepath.Clean(workDir)); ; dir = filepath.Dir(dir) {
		root := filepath.Join(dir, ".agents", "skills")
		if filepath.Clean(root) != userAgentsSkills {
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				dirs = append(dirs, root)
			}
		}
		parent := filepath.Dir(dir)
		if dir == gitRoot || parent == dir {
			break
		}
	}
	return dirs
}

// piGitRoot 返回 workDir 或其最近祖先里带 .git 的目录；找不到时返回空串。
func piGitRoot(workDir string) string {
	for dir := filepath.Clean(workDir); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if parent := filepath.Dir(dir); parent == dir {
			return ""
		}
	}
}

// piSkillPathsIn 递归列出某个来源目录下 pi 会当作技能的路径：含 SKILL.md 的目录，
// 以及（includeRootFiles 时）根级的 *.md 文件；层级规则与 pi 一致，一个目录有自己的
// SKILL.md 就不再往里找。
func piSkillPathsIn(root string, includeRootFiles bool) []string {
	var paths []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 0 {
			if md, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !md.IsDir() {
				paths = append(paths, dir)
				return
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			// 与 pi 一致：跳过隐藏目录与 node_modules。
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if info.IsDir() {
				walk(path, depth+1)
				continue
			}
			if depth == 0 && includeRootFiles && strings.HasSuffix(entry.Name(), ".md") {
				paths = append(paths, path)
			}
		}
	}
	walk(root, 0)
	return paths
}

// piAbsClean 返回技能路径的绝对形式；绝对化失败时退回清理后的原路径。
func piAbsClean(path string) string {
	if resolved, err := filepath.Abs(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// piUnderAnyRoot 判断技能目录是否落在 pi 的某个自动发现目录下。
func piUnderAnyRoot(path string, roots []string) bool {
	abs := filepath.Clean(path)
	if resolved, err := filepath.Abs(path); err == nil {
		abs = resolved
	}
	for _, root := range roots {
		rel, err := filepath.Rel(filepath.Clean(root), abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

// dirWithinRoot 判断 dir 是否等于 root 或位于 root 之内。
// 两侧都先按符号链接解析：git 返回的仓库根目录已是真实路径，而会话工作目录可能仍是符号链接写法。
func dirWithinRoot(dir, root string) bool {
	rel, err := filepath.Rel(resolveForCompare(root), resolveForCompare(dir))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveForCompare 返回用于路径比较的形式：能解析符号链接就用真实路径，否则退回清理后的原值。
func resolveForCompare(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// piSettingsDeclareSkills 判断 pi 的 settings.json（全局与项目级）里是否配了 skills 条目。
// 这些条目的启停由 pi 自己解析（路径可含 glob），mcode 无法映射成停用状态，所以一旦配了
// 就得退回旧行为。文件读不出来或解析失败时同样按「配了」处理（保守退回）。
// packages 不在这里判定：它们声明的技能根目录由 piPackageSkillRoots 解析后显式传入。
func piSettingsDeclareSkills(agentDir, workDir string) bool {
	paths := []string{
		filepath.Join(agentDir, "settings.json"),
		filepath.Join(workDir, ".pi", "settings.json"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return true
		}
		var cfg struct {
			Skills json.RawMessage `json:"skills"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return true
		}
		if len(cfg.Skills) == 0 || string(cfg.Skills) == "null" {
			continue
		}
		// skills 既可以是路径字符串数组，也可以是带 customDirectories 的对象。
		var list []string
		if err := json.Unmarshal(cfg.Skills, &list); err == nil {
			if len(list) > 0 {
				return true
			}
			continue
		}
		var obj struct {
			CustomDirectories []string `json:"customDirectories"`
		}
		if err := json.Unmarshal(cfg.Skills, &obj); err == nil && len(obj.CustomDirectories) > 0 {
			return true
		}
	}
	return false
}

// buildProviderHarness 按配置构造单个额外 Provider 的 harness；未启用或 ID 未知时返回 false。
// 覆盖 CLI 家族（copilot/pi/omp/commandcode）与 OpenAI 兼容预设（deepseek/zai/meta/magpie1..4）；
// ACP Agent 不在此处构造（由 registerExtraProviders 的 ACP 循环负责）。
// omp 不接 MCP。
func buildProviderHarness(cfg config.Config, secrets config.SecretStore, id string, mcpInjection providerMCP) (harness.Harness, bool) {
	switch id {
	case "openai":
		if !cfg.Providers.OpenAI.Enabled {
			return nil, false
		}
		return newSourceEngineHarness("openai", cfg, secrets, mcodeRuntimeDataDir(), mcpInjection), true
	case "copilot":
		if !cfg.Providers.Copilot.Enabled {
			return nil, false
		}
		bin := cfg.Providers.Copilot.Bin
		if bin == "" {
			bin = copilot.DefaultBinary()
		}
		return copilot.New(copilot.Options{Binary: bin, BuiltinMCP: mcpInjection.Builtin, ExternalMCP: mcpInjection.External}), true
	case "pi":
		if !cfg.Providers.Pi.Enabled {
			return nil, false
		}
		bin := cfg.Providers.Pi.Bin
		if bin == "" {
			bin = pi.DefaultBinary
		}
		_, piGate := loadGateExtensions()
		// Magpie 注入只在设置里有勾选模型的 Magpie 端点时生效（MagpieEnv 返回空串即不注入）。
		return pi.New(pi.Options{
			Binary:         bin,
			Extension:      piGate.path,
			MagpieBridge:   installPiMagpieBridge(),
			MagpieEnv:      currentPiMagpieEnv,
			InjectedModel:  piUnregisteredModel,
			OnMagpieMember: recordPiMagpieMember,
			BuiltinMCP:     mcpInjection.Builtin,
			ExternalMCP:    mcpInjection.External,
			Skills:         piSkillManifest,
			// 线程空闲十分钟内下一轮复用同一个 pi 进程，省掉 Node 冷启动。
			IdleTTL: 10 * time.Minute,
			// 设置里开启长缓存或填了视觉模型时，经环境变量交给 pi 子进程。
			Env: piTuningEnv(cfg.Providers.Pi),
		}), true
	case "omp":
		if !cfg.Providers.Omp.Enabled {
			return nil, false
		}
		bin := cfg.Providers.Omp.Bin
		if bin == "" {
			bin = omp.DefaultBinary
		}
		ompGate, _ := loadGateExtensions()
		if ompGate.err != nil {
			// 门禁装不上就不注册 omp：带空扩展启动等于以 yolo 放行全部工具，
			// 界面却仍显示只读/受监督。宁可让这个 Provider 不可用。
			log.Printf("omp 门禁扩展安装失败，已跳过 omp Provider：%v", ompGate.err)
			return nil, false
		}
		return omp.New(omp.Options{Binary: bin, Extension: ompGate.path}), true
	case "commandcode":
		if !cfg.Providers.CommandCode.Enabled {
			return nil, false
		}
		bin := cfg.Providers.CommandCode.Bin
		if bin == "" {
			bin = commandcode.DefaultBinary
		}
		return commandcode.New(commandcode.Options{Binary: bin, BuiltinMCP: mcpInjection.Builtin, ExternalMCP: mcpInjection.External}), true
	}

	pc, ok := cfg.Providers.Presets[id]
	if !ok || !pc.Enabled {
		return nil, false
	}
	_, ok = apisource.PresetByID(id)
	if !ok {
		return nil, false
	}
	return newSourceEngineHarness(harness.Kind(id), cfg, secrets, mcodeRuntimeDataDir(), mcpInjection), true
}

// buildExtraHarnesses 按配置构造除 ACP 与 agy 之外的全部额外 Provider harness 列表。
// 顺序固定：copilot、pi、omp、commandcode，然后是 deepseek、zai、meta；
// Magpie 现由自定义端点注册。
func buildExtraHarnesses(cfg config.Config, secrets config.SecretStore, mcpInjection providerMCP) []harness.Harness {
	ids := []string{"copilot", "pi", "omp", "commandcode", "deepseek", "zai", "meta"}

	var res []harness.Harness
	for _, id := range ids {
		if h, ok := buildProviderHarness(cfg, secrets, id, mcpInjection); ok {
			res = append(res, h)
		}
	}
	return res
}

// registerExtraProviders 注册 ACP 体系的各个 Agent、OpenAI 以及 Agy Provider。
// 返回 agy 的账号管理器与 harness（未启用或初始化失败时为 nil），供调用方按需把 agy 设为默认 provider。
func registerExtraProviders(backend *remote.MemoryBackend, cfg config.Config, dataDir string, agyOpts accounts.Options, mcpInjection providerMCP) (*accounts.Manager, harness.Harness) {
	setAppDataDir(dataDir)
	secrets := config.NewSecretStore("mcode")

	for _, a := range acp.Agents() {
		pCfg, ok := cfg.Providers.ACP[string(a)]
		if ok && !pCfg.Enabled {
			continue
		}
		bin := pCfg.Bin
		if bin == "" {
			bin = acp.DefaultBinary(a)
		}
		opts := acp.Options{
			Agent:       a,
			Binary:      bin,
			BuiltinMCP:  mcpInjection.Builtin,
			ExternalMCP: mcpInjection.External,
		}
		if a == acp.AgentDroid {
			// droid 认证：配置为空视为 browser（沿用 Droid 自带登录）；
			// apiKey 模式注入 keyring 中的 droid.apiKey，browser 模式移除 FACTORY_API_KEY。
			mode := acp.DroidAuthWeb
			if strings.EqualFold(strings.TrimSpace(pCfg.Auth), string(acp.DroidAuthAPIKey)) {
				mode = acp.DroidAuthAPIKey
			}
			opts.DroidAuth = mode
			opts.DroidAPIKey = func() string {
				v, _ := secrets.Get(droidAPIKeyAccount)
				return v
			}
		}
		backend.RegisterHarness(acp.New(opts))
	}

	if h, ok := buildProviderHarness(cfg, secrets, "openai", mcpInjection); ok {
		backend.RegisterHarness(h)
	}

	for _, h := range buildExtraHarnesses(cfg, secrets, mcpInjection) {
		backend.RegisterHarness(h)
	}
	// Anthropic 来源没有开关：凭据可用（含 ANTHROPIC_API_KEY）时注册。
	if _, ok := currentAnthropicSource(cfg, secrets); ok {
		backend.RegisterHarness(newAnthropicEngineHarness(cfg, secrets, dataDir, mcpInjection))
	}
	registerMcodeTextGenerator(cfg, secrets)

	for _, ep := range cfg.Providers.Custom {
		if ep.Enabled {
			backend.RegisterHarness(newCustomEndpointHarness(ep, secrets, dataDir, mcpInjection))
		}
	}

	var agyMgr *accounts.Manager
	var agyHarness harness.Harness
	if cfg.Providers.Agy.Enabled {
		agyOpts.DataDir = dataDir
		mgr, err := accounts.NewManager(agyOpts)
		if err != nil {
			log.Printf("failed to initialize agy accounts manager: %v", err)
		} else {
			agyMgr = mgr
		}

		bin := cfg.Providers.Agy.Bin
		if bin == "" {
			bin = agy.DefaultBinary
		}

		var r agy.Router
		if agyMgr != nil {
			r = agyMgr
		}

		agyHarness = agy.New(agy.Options{
			Binary:      bin,
			Router:      r,
			BuiltinMCP:  mcpInjection.Builtin,
			ExternalMCP: mcpInjection.External,
		})
		backend.RegisterHarness(agyHarness)
	}

	// 兼容旧线程，最后注册以免覆盖其他来源的默认顺序。
	backend.RegisterHarness(buildMcodeHarness(cfg, secrets, dataDir, mcpInjection))
	return agyMgr, agyHarness
}

// stewardTaskDefaults 读取总管下发任务的模型设置：选了仍存在的编排配对时按配对两侧模型下发并开启编排，
// 否则用单个任务模型、不开编排。
func stewardTaskDefaults(c config.Config) kanban.StewardTaskDefaults {
	st := c.Orchestration.Steward
	p, ok := c.Orchestration.Pair(st.TaskPairID)
	if st.TaskPairID == "" || !ok {
		return kanban.StewardTaskDefaults{Provider: st.TaskProvider, Model: st.TaskModel}
	}
	return kanban.StewardTaskDefaults{
		Provider:     p.LeadProvider,
		Model:        p.LeadModel,
		Effort:       derefString(p.LeadEffort),
		FastMode:     p.LeadFastMode,
		BeesProvider: p.WorkerProvider,
		BeesModel:    p.WorkerModel,
		BeesEffort:   derefString(p.WorkerEffort),
		PairID:       p.ID,
	}
}

// swarmThreadContextFrom 从原始用户消息、配对 ID、档位 JSON 与会话选定的子线程模型（含推理强度）构建 swarm.ThreadContext。
// 当 tiersJSON 非空时反序列化为 map[string]config.SwarmTierOverride 并通过 swarmBeeTiersFromConfig 转换；
// 解析出错时记录日志并置为 nil。
func swarmThreadContextFrom(msg, pairID, tiersJSON, beeProvider, beeModel, beeEffort string, enabled func(string) bool) swarm.ThreadContext {
	var beeTiers map[string]swarm.TierOverride
	if tiersJSON != "" {
		var raw map[string]config.SwarmTierOverride
		if err := json.Unmarshal([]byte(tiersJSON), &raw); err != nil {
			log.Printf("swarm: unmarshal bee tiers json failed: %v", err)
			beeTiers = nil
		} else {
			beeTiers = swarmBeeTiersFromConfig(raw)
		}
	}
	return swarm.ThreadContext{
		UserMessage:     msg,
		PairID:          pairID,
		BeeTiers:        beeTiers,
		BeeProvider:     beeProvider,
		BeeModel:        beeModel,
		BeeEffort:       beeEffort,
		ProviderEnabled: enabled,
	}
}

// remoteDraftDefaults 构造 providers.list 的草稿默认值，复刻桌面
// internal/ui 的 newThreadOptionsForProject 优先级：
// 1. 上次在模型选择器里确认的选择（provider 与模型仍在目录里时沿用，含推理强度与快速模式）；
// 2. provider 模型列表的第一个模型（DefaultModel）。
// RuntimeMode 留空，由 remote.MemoryBackend 用后端当前的默认权限模式补齐；
// 编排开关受全局总开关约束，工作区模式沿用设置里的新会话默认值。
func remoteDraftDefaults(c config.Config) wire.ComposerDraftDefaults {
	provider := strings.TrimSpace(c.Models.Provider)
	if !c.Models.ProviderSelected || strings.EqualFold(provider, config.ProviderMcode) {
		provider = ""
	}
	model, effort := "", ""
	fast := false
	enabled := mcodeui.ProviderEnabledMap(c)
	if last, ok := mcodeui.LoadLastModelSelection(); ok && last.Provider != "" && draftProviderEnabled(enabled, last.Provider) && draftModelAllowed(c, last.Provider, last.Model) {
		provider, model, effort, fast = last.Provider, last.Model, last.Effort, last.Fast
	}
	if model == "" {
		model = remote.DefaultModel(provider)
	}

	beesProvider, beesModel, beesEffort := "", "", ""
	lastSwarm := mcodeui.LoadLastSwarmSelection()
	if h := lastSwarm.Bees; h != nil {
		followsLead := h.Provider == "" && h.Model == ""
		if followsLead || (draftProviderEnabled(enabled, h.Provider) && draftModelAllowed(c, h.Provider, h.Model)) {
			beesProvider, beesModel, beesEffort = h.Provider, h.Model, h.Effort
		}
	}
	swarmEnabled := lastSwarm.Enabled != nil && *lastSwarm.Enabled
	workspaceMode := c.SourceControl.DefaultWorkspaceMode
	if workspaceMode != config.WorkspaceModeWorktree {
		workspaceMode = config.WorkspaceModeLocal
	}
	return wire.ComposerDraftDefaults{
		Provider:          provider,
		Model:             optionalDraftString(model),
		Effort:            optionalDraftString(effort),
		FastMode:          fast,
		InteractionMode:   "build",
		WorkspaceMode:     workspaceMode,
		SwarmEnabled:      swarmEnabled && c.Orchestration.IsEnabled(),
		SwarmBeesProvider: optionalDraftString(beesProvider),
		SwarmBeesModel:    optionalDraftString(beesModel),
		SwarmBeesEffort:   optionalDraftString(beesEffort),
	}
}

// rememberRemoteSelection 把用户在远程端主动确认的模型与编排选择写回模型记忆，
// 字段与桌面新建会话时的写回（internal/ui 的 rememberModelSelection 与 rememberSwarm*）一致。
// 主导 provider 与 model 都非空时才写主导记忆（effort 为空表示默认档位）；
// 子代理三字段任一非 nil 时整组写入（空串表示跟随主导）；编排开关非 nil 时写开关。
func rememberRemoteSelection(p wire.ProvidersRememberSelectionParams) {
	if provider, model := derefDraftString(p.Provider), derefDraftString(p.Model); provider != "" && model != "" {
		mcodeui.SaveLastModelSelection(mcodeui.ModelSelection{
			Provider: provider,
			Model:    model,
			Effort:   derefDraftString(p.Effort),
			Fast:     p.FastMode != nil && *p.FastMode,
		})
	}
	if p.SwarmBeesProvider != nil || p.SwarmBeesModel != nil || p.SwarmBeesEffort != nil {
		mcodeui.RememberSwarmBees(
			derefDraftString(p.SwarmBeesProvider),
			derefDraftString(p.SwarmBeesModel),
			derefDraftString(p.SwarmBeesEffort),
		)
	}
	if p.SwarmEnabled != nil {
		mcodeui.RememberSwarmEnabled(*p.SwarmEnabled)
	}
}

// draftProviderEnabled 判断 provider 是否在设置里启用；没有启用状态信息的 provider 视为启用。
func draftProviderEnabled(enabled map[string]bool, provider string) bool {
	if strings.EqualFold(strings.TrimSpace(provider), config.ProviderMcode) {
		return false
	}
	on, known := enabled[provider]
	return !known || on
}

// draftModelAllowed 判断记忆里的模型是否仍可用：空模型表示跟随 provider 默认，视为可用；
// 目录之外的模型只要已加入用户固定列表（自定义模型 ID）也视为可用。
func draftModelAllowed(cfg config.Config, provider, model string) bool {
	if provider == "" {
		return false
	}
	if model == "" {
		return true
	}
	return remote.IsValidModel(provider, model) || cfg.Models.HasModelPin(provider, model)
}

// optionalDraftString 返回空串时为 nil 的字符串指针，避免把空值当成显式选择下发。
func optionalDraftString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// derefDraftString 解引用可空字符串，nil 时返回空串。
func derefDraftString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
