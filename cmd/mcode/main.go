package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updaternative "github.com/egoist/mygo/plugins/updater/native"

	"mcode/internal/agentconf"
	"mcode/internal/app"
	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/mcp"
	"mcode/internal/providers/loginenv"
	"mcode/internal/startuptrace"
	mcodeui "mcode/internal/ui"
)

func envOrDefault(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

// pathListSeparator 返回 goos 的 PATH 分隔符：Windows 用分号，其余平台用冒号。
// 生产环境 goos 就是 runtime.GOOS，取值与 os.PathListSeparator 等价；按 goos 单独判断
// 是为了让测试能在任意宿主上验证 Windows 分支（宿主分隔符与 goos 不一致时仍然正确）。
func pathListSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return ":"
}

// extraPathDirs 返回要追加到 PATH 末尾的目录：Unix 补 Homebrew 等固定目录，
// Windows 补 npm、bun、cargo 的用户级 bin 目录（Windows 的 PATH 里本来就有系统目录）。
func extraPathDirs(home, goos string) []string {
	if goos == "windows" {
		dirs := make([]string, 0, 4)
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".local", "bin"))
		}
		if appData := os.Getenv("APPDATA"); appData != "" {
			dirs = append(dirs, filepath.Join(appData, "npm"))
		}
		if home != "" {
			dirs = append(dirs,
				filepath.Join(home, ".bun", "bin"),
				filepath.Join(home, ".cargo", "bin"),
			)
		}
		return dirs
	}

	dirs := []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/go/bin"}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local/bin"),
			filepath.Join(home, ".bun/bin"),
			filepath.Join(home, ".cargo/bin"),
			filepath.Join(home, ".npm-global/bin"),
			filepath.Join(home, ".volta/bin"),
			filepath.Join(home, ".version-fox/sdks/nodejs/bin"),
			filepath.Join(home, ".version-fox/sdks/golang/bin"),
			filepath.Join(home, ".version-fox/shims"),
			filepath.Join(home, ".local/share/mise/shims"),
			filepath.Join(home, ".asdf/shims"),
		)
	}
	return append(dirs, "/usr/bin", "/bin", "/usr/sbin", "/sbin")
}

// augmentedPath 补全 PATH：从 Finder 或 Dock 启动时 PATH 只有系统目录，会找不到 claude、codex 等 CLI。
// goos 决定分隔符与补充目录：Windows 的 PATH 用分号分隔、路径本身带盘符冒号，按冒号拆会拆坏，
// Unix 的 Homebrew 目录在 Windows 上也没有意义。
func augmentedPath(path, home, goos string) string {
	sep := pathListSeparator(goos)
	parts := make([]string, 0, 32)
	seen := make(map[string]bool)
	for _, p := range strings.Split(path, sep) {
		if p == "" {
			continue
		}
		parts = append(parts, p)
		seen[p] = true
	}

	for _, p := range extraPathDirs(home, goos) {
		if p == "" || seen[p] {
			continue
		}
		parts = append(parts, p)
		seen[p] = true
	}
	return strings.Join(parts, sep)
}

// useSingleInstanceLock 报告该平台是否需要 mygo 的单实例锁：macOS 由 LaunchServices
// 保证只有一个实例，Windows/Linux 需要应用自己抢锁。
func useSingleInstanceLock(goos string) bool {
	return goos != "darwin"
}

// trayKeepsAppAlive 报告托盘是否足以让应用在无窗口时还能被唤回：macOS 有 Dock 图标，
// 不依赖托盘；其他平台只有托盘真的建起来了才算（配置里开启但创建失败不算）。
func trayKeepsAppAlive(goos string, trayActive bool) bool {
	return goos == "darwin" || trayActive
}

// shouldQuitOnAllWindowsClosed 报告最后一个窗口关闭时是否直接退出：macOS 保持后台常驻
// （点 Dock 图标重开窗口）；其他平台只有在没有托盘可唤回时才退出，否则会留下一个
// 看不见、也无法唤回的进程。
func shouldQuitOnAllWindowsClosed(goos string, trayActive bool) bool {
	return goos != "darwin" && !trayActive
}

// injectAppVersion 用打包产物里的版本号补齐「关于」页与更新检查读取的版本。
// mygo build 会把 mygo.json 的 version 注入包信息；go run / mygo dev 拿不到时保持 dev。
func injectAppVersion() {
	if app.AppVersion != "dev" {
		return
	}
	if version := mygo.App.Version(); version != "" {
		app.AppVersion = version
	}
}

// openResult 是后台 app.Open 的结果：opened 为 nil 时 err 说明失败原因。
type openResult struct {
	opened *app.App
	err    error
}

func main() {
	startuptrace.Mark("main entered")
	// 主 goroutine 的 panic 先记录日志、弹窗（Windows），再重新抛出交给 SetCrashOutput
	// 写入 crash.log。放在最前面注册，保证它最后执行，也能接住其他 defer 里的 panic。
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("MCode 崩溃：%v\n%s", r, debug.Stack())
			log.Print(msg)
			showFatal(msg)
			panic(r)
		}
	}()

	// 单实例：macOS 由 LaunchServices 保证，Windows/Linux 靠 mygo 的 socket 锁。必须在
	// config.Load 与 app.Open 之前抢锁，否则第二个实例会去争同一个 sqlite 库。抢不到锁时
	// mygo 已把命令行转交给已有实例，这里直接正常退出，不弹错误框。
	if useSingleInstanceLock(runtime.GOOS) && !mygo.App.RequestSingleInstanceLock() {
		log.Printf("MCode 已在运行，本次启动已把命令行交给已有实例，退出")
		return
	}

	home, _ := os.UserHomeDir()
	_ = os.Setenv("PATH", augmentedPath(os.Getenv("PATH"), home, runtime.GOOS))
	// 预热登录环境：首次读取会同步执行登录 shell（首次约 200～430ms），放后台与数据库打开并行；
	// 后面的 claude 登录检测和启动 CLI 都复用这份缓存，loginenv 内部已做好并发保护。
	go func() { _ = loginenv.Environ() }()

	defaultDataDir := ""

	if homeDir, err := os.UserHomeDir(); err == nil {
		defaultDataDir = filepath.Join(homeDir, ".mcode")
	}
	defaultDataDir = envOrDefault("MCODE_DATA", defaultDataDir)

	defaultWorkDir, err := os.Getwd()
	if err != nil {
		defaultWorkDir = "."
	}
	// 从 Finder 启动时 cwd 是根目录，用 home 作为默认工作目录更合理。
	if defaultWorkDir == "/" && home != "" {
		defaultWorkDir = home
	}
	defaultWorkDir = envOrDefault("MCODE_WORKDIR", defaultWorkDir)

	defaults := config.Defaults()

	dataFlag := flag.String("data", defaultDataDir, "mcode data directory")
	providerFlag := flag.String("provider", defaults.Models.Provider, "agent provider (memory, claude, codex)")
	workdirFlag := flag.String("workdir", defaultWorkDir, "working directory for agent harness")
	modeFlag := flag.String("mode", defaults.Permissions.Mode, "permission mode (readonly, supervised, autoAcceptEdits, auto, fullAccess; agent, full_access still accepted)")
	claudeBinFlag := flag.String("claude-bin", "claude", "path to claude executable")
	codexBinFlag := flag.String("codex-bin", "codex", "path to codex executable")
	flag.Parse()

	// GUI 程序没有控制台：先把日志接到 startup.log，再把崩溃输出接到 crash.log，
	// 之后任何启动失败（包括 panic）都能在文件里查到。放在 flag.Parse 之后，
	// 日志才能与 mcode.log 一起落在数据目录下的 logs。
	initStartupLog(*dataFlag)
	startPprofFromEnv()

	autoOpen, err := mcodeui.PrepareOnboarding(*dataFlag)
	if err != nil {
		log.Printf("准备新用户引导失败: %v", err)
		autoOpen = false
	}

	cfg, err := config.Load(*dataFlag)
	if err != nil {
		fatalExit("failed to load config from %s: %v", config.ConfigPath(*dataFlag), err)
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	if err := config.Validate(cfg); err != nil {
		fatalExit("invalid config: %v", err)
	}
	startuptrace.Mark("config loaded")

	setFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	lockedKeys := make(map[string]bool)
	if setFlags["provider"] {
		lockedKeys["provider"] = true
	}
	if setFlags["mode"] {
		lockedKeys["mode"] = true
	}
	if setFlags["claude-bin"] {
		lockedKeys["claude-bin"] = true
	}
	if setFlags["codex-bin"] {
		lockedKeys["codex-bin"] = true
	}

	if _, ok := os.LookupEnv("MCODE_PROVIDER"); ok {
		lockedKeys["provider"] = true
	}
	if _, ok := os.LookupEnv("MCODE_MODE"); ok {
		lockedKeys["mode"] = true
	}
	if _, ok := os.LookupEnv("MCODE_CLAUDE_BIN"); ok {
		lockedKeys["claude-bin"] = true
	}
	if _, ok := os.LookupEnv("MCODE_CODEX_BIN"); ok {
		lockedKeys["codex-bin"] = true
	}

	settings := resolveSettings(cfg, setFlags, flagValues{
		Provider:  *providerFlag,
		Mode:      *modeFlag,
		ClaudeBin: *claudeBinFlag,
		CodexBin:  *codexBinFlag,
	})

	permMode, err := harness.ParseMode(settings.Mode)
	if err != nil {
		fatalExit("invalid permission mode: %v", err)
	}

	provider := settings.Provider
	switch provider {
	case "claude":
		if !providerBinaryAvailable(settings.ClaudeBin, "claude") {
			if providerBinaryAvailable(settings.CodexBin, "codex") {
				log.Printf("warning: claude binary %q not found; falling back to codex provider", settings.ClaudeBin)
				provider = "codex"
			} else {
				log.Printf("warning: claude binary %q not found; keeping claude provider", settings.ClaudeBin)
			}
		}
	case "codex":
		if !providerBinaryAvailable(settings.CodexBin, "codex") {
			if providerBinaryAvailable(settings.ClaudeBin, "claude") {
				log.Printf("warning: codex binary %q not found; falling back to claude provider", settings.CodexBin)
				provider = "claude"
			} else {
				log.Printf("warning: codex binary %q not found; keeping codex provider", settings.CodexBin)
			}
		}
	case "memory":
		// memory 是内置的假 provider：只有命令行或环境变量显式指定时才保留；
		// config.json 里的遗留值改为优先使用本机可用的真实 provider。
		if !lockedKeys["provider"] {
			if providerBinaryAvailable(settings.ClaudeBin, "claude") {
				log.Printf("warning: provider %q is the built-in test provider; using claude instead", provider)
				provider = "claude"
			} else if providerBinaryAvailable(settings.CodexBin, "codex") {
				log.Printf("warning: provider %q is the built-in test provider; using codex instead", provider)
				provider = "codex"
			} else {
				log.Printf("warning: provider %q is the built-in test provider and no claude/codex binary was found; keeping it", provider)
			}
		}
	}
	startuptrace.Mark("provider binary detected")

	absWorkDir, err := filepath.Abs(*workdirFlag)
	if err != nil {
		fatalExit("failed to resolve absolute path for workdir %q: %v", *workdirFlag, err)
	}

	for _, err := range config.MigrateLegacySecrets(config.LegacySecretRefs(nil)) {
		log.Printf("legacy secret migration warning: %v", err)
	}

	// MCP 工具策略文件与设置页共用同一数据目录，供 MCP 代理层过滤禁用工具。
	if *dataFlag != "" {
		mcp.SetToolPolicyPath(agentconf.MCPToolPolicyPath(*dataFlag))
		// 受管 worktree 基目录跟随数据目录，批量清理只允许动这些目录下的 worktree。
		if dataDir, err := filepath.Abs(*dataFlag); err == nil {
			git.SetWorktreePurgeRoots(filepath.Join(dataDir, "worktrees"), filepath.Join(dataDir, "swarm-worktrees"), filepath.Join(dataDir, "hydra-worktrees"))
		}
	}

	// app.Open 会构造更新检查器并读取 app.AppVersion，所以必须在它之前补齐版本号。
	injectAppVersion()

	// app.Open 只装配后端（数据库、StreamHub、Provider、种子数据），不触碰 AppKit；
	// 放到后台 goroutine 与框架启动并行，主线程照旧先注册生命周期回调再 Run，
	// WhenReady 里再取结果。通道缓冲为 1：没人取结果时 goroutine 也能写完退出。
	appOpen := make(chan openResult, 1)
	// 整理数据库的进度由 app.Open 的回调从后台 goroutine 写入：最近进度存原子变量，
	// 加载窗口可能还没建或已经关掉，所以窗口指针也用原子指针保护。
	var compactDone, compactTotal atomic.Int64
	var loadingWindow atomic.Pointer[mcodeui.StartupLoadingWindow]
	compactStarted := make(chan struct{})
	var compactStartedOnce sync.Once
	onStartupProgress := func(p app.StartupProgress) {
		if p.Stage != app.StartupStageCompactDB {
			return
		}
		compactStartedOnce.Do(func() { close(compactStarted) })
		compactDone.Store(p.Done)
		compactTotal.Store(p.Total)
		if w := loadingWindow.Load(); w != nil {
			w.SetProgress(p.Done, p.Total)
		}
	}
	go func() {
		res := openResult{}
		// 后台 goroutine 的 panic 会直接终止进程，用户什么提示都看不到；这里转成错误，
		// 交给 WhenReady 的致命错误路径记录日志并弹窗。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("app.Open panic: %v\n%s", r, debug.Stack())
				res = openResult{err: fmt.Errorf("app.Open panic: %v", r)}
			}
			appOpen <- res
		}()
		opened, openErr := app.Open(app.Options{
			DataDir:           *dataFlag,
			Provider:          provider,
			WorkDir:           absWorkDir,
			Mode:              string(permMode),
			ClaudeBin:         settings.ClaudeBin,
			CodexBin:          settings.CodexBin,
			OnStartupProgress: onStartupProgress,
		})
		if openErr == nil {
			startuptrace.Mark("app.Open done")
		}
		res = openResult{opened: opened, err: openErr}
	}()

	// a 与 openAwaited 只在主线程读写：WhenReady 里的加载窗口回调、生命周期回调与这里的 defer 都在主线程执行。
	var a *app.App
	openAwaited := false
	// openExit 收一份 app.Open 结果的副本：结果处理走 mygo.RunOnMain，事件循环关闭后投递会被丢弃，
	// 退出路径不能靠它唤醒，只能读这个缓冲为 1 的通道（只写一次，写完不会被阻塞）。
	openExit := make(chan openResult, 1)
	awaitAppOpen := func() error {
		if openAwaited {
			return nil
		}
		res := <-openExit
		if res.err != nil {
			return res.err
		}
		a = res.opened
		return nil
	}
	defer func() {
		// Run 返回后释放后端资源；app.Open 还没取过结果时先等它完成，避免泄漏。
		if openErr := awaitAppOpen(); openErr != nil {
			log.Printf("failed to open app: %v", openErr)
			return
		}
		if a != nil {
			_ = a.Close()
		}
	}()

	var cleanupNative func()
	// trayActive 由 WhenReady 里的 setupNative 写入，之后只被主线程上的生命周期回调读取。
	var trayActive bool
	// sendOwnerNotification 是总管汇报的通知发送函数，同样由 setupNative 写入。
	var sendOwnerNotification func(title, body, threadID string) error
	// sendAutomationNotification 是自动化运行成功的通知发送函数，同样由 setupNative 写入。
	var sendAutomationNotification func(title, body, threadID string) error
	defer func() {
		if cleanupNative != nil {
			cleanupNative()
		}
	}()

	title := fmt.Sprintf("MCode - %s", filepath.Base(absWorkDir))

	// 在主线程、App.Run 之前应用主题；放进 UI 构造会在无事件循环的测试里死锁
	if cfg.Appearance.Theme != "" {
		mygo.Theme.SetSource(mygo.ThemeSource(cfg.Appearance.Theme))
	}
	startuptrace.Mark("theme set")

	mygo.Use(updaternative.New(updater.Options{
		Language: "zh-Hans",
	}))
	startuptrace.Mark("updater registered")

	// openChatWindow 没有可用的聊天窗口时重开主窗口；newThread 为 true 的直接进入新建会话流程
	// （Dock 点击、应用重新激活），否则只恢复主窗口（通知点击、托盘、全局快捷键）。
	openChatWindow := func(newThread bool) {
		mygo.RunOnMain(func() {
			// 后端还没就绪（理论上早于 WhenReady 的系统事件）：直接忽略这次开窗。
			if a == nil {
				return
			}
			if !mcodeui.HasChatWindow() {
				if newThread {
					mcodeui.OpenNewThreadWindow(a.Client(), title, mcodeui.WithSettings(*dataFlag, lockedKeys), mcodeui.WithLaunchMode(string(permMode)), mcodeui.WithUIStatePersistence())
				} else {
					mcodeui.OpenThreadWindow(a.Client(), "", title, mcodeui.WithSettings(*dataFlag, lockedKeys), mcodeui.WithLaunchMode(string(permMode)), mcodeui.WithUIStatePersistence())
				}
			} else {
				win := mygo.FocusedWindow()
				if win == nil {
					if windows := mygo.Windows(); len(windows) > 0 {
						win = windows[0]
					}
				}
				if win != nil {
					if win.IsMinimized() {
						win.Restore()
					}
					win.Show()
				}
			}
			mygo.App.Show()
			mygo.App.Focus()
		})
	}

	// 第二个实例启动时唤回已有窗口。mygo 在接收 goroutine 里只把消息 post 到主线程，回调
	// 快照是在主线程执行时才取的，所以抢锁之后、App.Run 之前注册不会漏掉早到的消息。
	if useSingleInstanceLock(runtime.GOOS) {
		defer mygo.App.OnSecondInstance(func(args []string, workingDir string) {
			openChatWindow(false)
		})()
	}

	showWindow := func() { openChatWindow(false) }

	var forceQuit bool
	onForceQuit := func() {
		forceQuit = true
		mygo.App.Quit()
	}

	// 退出生命周期控制：QuitAction 为 minimize_to_tray 且托盘可用时，阻止 ⌘Q 并隐藏窗口；
	// 托盘显式退出不受阻。托盘不可用时不能只隐藏窗口：Windows 上那会让进程既看不见也唤不回。
	defer mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if forceQuit {
			return
		}
		currentCfg, err := config.Load(*dataFlag)
		if err != nil {
			currentCfg = cfg
		}
		if currentCfg.Startup.QuitAction == config.QuitActionMinimizeToTray && currentCfg.Startup.ShowTrayIcon && trayKeepsAppAlive(runtime.GOOS, trayActive) {
			e.PreventDefault()
			mygo.RunOnMain(func() {
				for _, w := range mygo.Windows() {
					w.Hide()
				}
				mygo.App.Hide()
			})
		}
	})()

	// 保留后台会话与系统入口；显式“退出”仍走正常退出流程。
	// 非 macOS 平台没有托盘可唤回时不能常驻：最后一个窗口关闭后退出，否则进程再也找不回来。
	defer mygo.App.OnWindowAllClosed(func() {
		if shouldQuitOnAllWindowsClosed(runtime.GOOS, trayActive) {
			mygo.App.Quit()
		}
	})()
	defer mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if a == nil {
			return
		}
		if !hasVisibleWindows {
			// Dock 图标点击 / 应用重新激活：没有窗口时先开一个聊天窗口并进入新建会话。
			openChatWindow(true)
		}
	})()

	// finishStartup 在主线程完成 app.Open 之后的启动收尾：装菜单、开主窗口、准备托盘与通知、
	// 必要时提示数据库损坏。只在 app.Open 结果到手后调用一次。
	finishStartup := func() {
		// 安装应用菜单（macOS 为顶栏菜单）。
		mcodeui.InstallAppMenu()
		startuptrace.Mark("app menu installed")
		// 主窗口开启界面状态持久化：启动时恢复侧边栏宽度与显隐，变化时写回 ui-state.json。
		mcodeui.OpenThreadWindow(a.Client(), "", title, mcodeui.WithSettings(*dataFlag, lockedKeys), mcodeui.WithLaunchMode(string(permMode)), mcodeui.WithUIStatePersistence(), mcodeui.WithOnboarding(autoOpen))
		startuptrace.Mark("window created")
		cleanupNative, trayActive, sendOwnerNotification, sendAutomationNotification = setupNative(a.Hub(), a.Client(), *dataFlag, cfg, showWindow, mcodeui.ActivateThread, onForceQuit)
		// 总管汇报走同一个通知器：同一时段的多条通知合并成一条；未接入时 notify_owner 会返回明确错误。
		a.SetOwnerNotifier(func(title, body, threadID string) error {
			if sendOwnerNotification == nil {
				return nil
			}
			if err := sendOwnerNotification(title, body, threadID); err != nil {
				// 抑制（前台 / 安静时段）由 notify_owner 转成给总管的提示，不当作错误刷日志。
				if !errors.Is(err, app.ErrOwnerNotifySuppressed) {
					log.Printf("failed to send steward notification: %v", err)
				}
				return err
			}
			return nil
		})
		// 自动化运行成功也发一条桌面通知：用户没在看该会话且开着「完成后通知」时才真正弹出。
		a.SetAutomationNotifier(func(title, body, threadID string) {
			if sendAutomationNotification == nil {
				return
			}
			if err := sendAutomationNotification(title, body, threadID); err != nil {
				log.Printf("failed to send automation notification: %v", err)
			}
		})
		startuptrace.Mark("native setup done")

		// 011：store 自愈时把损坏的原库改名备份后以空库启动，必须显式告知用户备份位置，
		// 否则用户只看到「数据全没了」。mygo.Dialog.Message 阻塞，放到后台 goroutine 里弹（与托盘更新提示同做法）。
		if backupPath := a.RecoveredStoreBackupPath(); backupPath != "" {
			absBackup, err := filepath.Abs(backupPath)
			if err != nil {
				absBackup = backupPath
			}
			log.Printf("数据库已损坏，已备份到 %s 并以空库启动；可退出 MCode 后用该备份恢复", absBackup)
			go func(path string) {
				if _, err := mygo.Dialog.Message(mygo.MessageOptions{
					Type:    mygo.MessageWarning,
					Title:   "数据库已损坏",
					Message: "数据库已损坏，已备份到 " + path + " 并以空库启动",
					Detail:  "可退出 MCode 后用该备份文件恢复。",
					Buttons: []string{"好"},
				}); err != nil {
					log.Printf("failed to show store recovery notice: %v", err)
				}
			}(absBackup)
		}
	}

	mygo.App.WhenReady(func() {
		startuptrace.Mark("mygo ready")
		// 只有 app.Open 真的开始整理数据库（首次切增量 auto_vacuum）时才弹加载窗口，
		// 普通启动直接等结果开主窗口。
		waitOpenWithLoading(appOpen, compactStarted, func() {
			mygo.RunOnMain(func() {
				w := mcodeui.OpenStartupLoadingWindow()
				loadingWindow.Store(w)
				// 窗口建起来之前 app.Open 可能已经报过进度，这里补刷一次最新值。
				w.SetProgress(compactDone.Load(), compactTotal.Load())
			})
		}, func(res openResult) {
			// 先把结果副本交给退出路径：下面的 RunOnMain 在事件循环关闭后会被丢弃。
			openExit <- res
			mygo.RunOnMain(func() {
				openAwaited = true
				startuptrace.Mark("app.Open awaited")
				if res.err != nil {
					fatalExit("failed to open app: %v", res.err)
				}
				a = res.opened
				// 先开主窗口再关加载窗口：加载窗口这时是唯一窗口，先关会同步触发
				// OnWindowAllClosed，在 Windows/Linux 上会直接把应用退出。
				finishStartup()
				if w := loadingWindow.Swap(nil); w != nil {
					w.Close()
				}
			})
		})
	})

	startuptrace.Mark("before App.Run")
	// Run 返回错误通常意味着 GUI 初始化失败（例如 Windows 上缺少 WebView2）；
	// 以前这里被忽略，双击后只会一闪而过、什么都不出现。
	if runErr := mygo.App.Run(); runErr != nil {
		fatalExit("failed to run app: %v", runErr)
	}
}
