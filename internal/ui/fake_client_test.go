package mcodeui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/projctx"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/multiaccount"
)

type uploadAttachmentCall struct {
	Name     string
	MimeType string
	Data     []byte
}

type sentWithAttachmentsCall struct {
	ThreadID      string
	Text          string
	AttachmentIDs []string
}

type sentCall struct {
	ThreadID string
	Text     string
}

type respondCall struct {
	Approval Approval
	OptionID string
}

type respondAnswersCall struct {
	Approval Approval
	OptionID string
	Answers  map[string][]string
	Skip     bool
}

type watchEntry struct {
	id          int
	threadID    string
	onUpdate    func(ThreadUpdate)
	onApprovals func([]Approval)
	stopped     bool
}

type createCallInfo struct {
	ProjectID string
	Provider  string
}

type setRuntimeModeCall struct {
	ThreadID string
	Mode     string
}

// composerPrefsCall 记录 SetThreadComposerPrefs 调用参数。
type composerPrefsCall struct {
	ThreadID string
	Prefs    ComposerPrefs
}

// FileOpCall 记录 FakeClient 的一次文件写操作调用。
// Op 取值 create / rename / trash / duplicate。
type FileOpCall struct {
	Op       string
	ThreadID string
	Path     string
	NewPath  string
	Dir      bool
}

// FastModeCall 记录 SetThreadFastMode 调用参数。
type FastModeCall struct {
	ThreadID  string
	FastMode  bool
	SpeedTier string
}

// SwarmModeCall 记录 SetThreadSwarmMode 调用参数。
type SwarmModeCall struct {
	ThreadID string
	Mode     string
}

// BeeTiersCall 记录 SetThreadBeeTiers 调用参数。
type BeeTiersCall struct {
	ThreadID string
	BeeTiers string
}

// SwarmPairIDCall 记录 SetThreadSwarmPairID 调用参数。
type SwarmPairIDCall struct {
	ThreadID string
	PairID   string
}

// ForwardQuestionCall 记录 ForwardBeeQuestion 调用参数。
type ForwardQuestionCall struct {
	LeadThreadID string
	BeeThreadID  string
	RequestID    string
}

// fakeQueueCall 记录队列操作调用参数。
type fakeQueueCall struct {
	ThreadID         string
	FollowUpID       string
	BeforeFollowUpID string
	Text             string
	AttachmentIDs    []string
}

// fakeMCPDisabledCall 记录 SetThreadMCPDisabled 调用参数。
type fakeMCPDisabledCall struct {
	ThreadID string
	Keys     []string
}

type SetAgentCall struct {
	ThreadID string
	Provider string
	Model    string
}

// MoveTaskCall 记录 MoveTask 调用参数。
type MoveTaskCall struct {
	TaskID   string
	Status   string
	Revision int
}

// ReviewCall 记录 ReviewTask 调用参数。
type ReviewCall struct {
	TaskID   string
	Decision string
	Feedback string
	Revision int
}

// GoalCall 记录 Goal 操作调用参数。
type GoalCall struct {
	GoalID   string
	Action   string
	Feedback string
	Revision int
}

// BulkMarkReadCall 记录 BulkMarkRead 调用参数。
type BulkMarkReadCall struct {
	ProjectID string
	ThreadIDs []string
}

// BulkArchiveCall 记录 BulkArchive 调用参数。
type BulkArchiveCall struct {
	ProjectID string
	ThreadIDs []string
}

// UndoBulkOpCall 记录 UndoBulkOperation 调用参数。
type UndoBulkOpCall struct {
	ProjectID string
	Token     string
}

// gitSelectedCommitCall 记录一次 GitCommitSelected 的调用参数。
type gitSelectedCommitCall struct {
	Paths   []string
	Message string
}

// FakeClient 是一个并发安全的内存假 Client，支持手动触发 Watch 回调并记录各项调用。
type FakeClient struct {
	mu sync.Mutex

	projects          []Project
	providers         []ProviderInfo
	threads           map[string][]Thread
	threadsCalls      map[string]int // 按项目统计 Threads 调用次数，用于断言列表重拉的次数
	archivedThreads   []Thread
	messages          map[string][]Message
	forwardedContexts map[string]ForwardedContext
	approvals         map[string][]Approval
	usage             map[string]ThreadUsage
	diffs             map[string]ThreadDiff

	kanbanBoard         KanbanBoard
	kanbanErr           error
	moveTaskErr         error
	kanbanBoardCalls    int
	kanbanWatchers      map[int]func()
	nextKanbanWatcherID int

	moveTaskCalls   []MoveTaskCall
	startTaskCalls  []string
	reviewTaskCalls []ReviewCall
	createTaskCalls []KanbanTask
	goalCalls       []GoalCall
	reviewTaskErr   error
	historyCalls    []string
	diffCalls       []string

	automations               []Automation
	automationRuns            map[string][]AutomationRun
	createAutomationCalls     []AutomationInput
	updateAutomationCalls     []AutomationInput
	updateAutomationRevisions []int
	automationWatchers        map[int]func()
	runAutomationCalls        []string
	runAutomationErr          error
	deleteAutomationCalls     []string
	schedulerStatus           AutomationSchedulerStatus
	schedulerStatusErr        error
	schedulerStatusCalls      int

	swarmStates       map[string]SwarmState
	swarmErr          error
	swarmMergeDiffs   map[string]ThreadDiff
	swarmMergeDiffErr error
	// swarmReports 按 bee ID 保存模拟的存档汇报全文。
	swarmReports          map[string]string
	swarmWatchers         map[int]func(string)
	nextSwarmWatcherID    int
	stoppedBees           []string
	stopAllCalls          []string
	clearCalls            []string
	approveMergeCalls     []string
	discardMergeCalls     []string
	forwardQuestionCalls  []ForwardQuestionCall
	gitGenCommitMsgResult string
	gitGenCommitMsgErr    error
	setSwarmEnabledCalls  []struct {
		ThreadID string
		Enabled  bool
	}

	nextWatcherID int
	nextThreadSeq int
	watchers      map[string][]*watchEntry

	createCalls         []string
	createCallInfos     []createCallInfo
	lastNewThreadOpts   NewThreadOptions
	sendCalls           []sentCall
	respondCalls        []respondCall
	respondAnswersCalls []respondAnswersCall
	interruptCalls      []string
	// killRunningCommandCalls 记录 KillRunningCommand 的调用（格式为「threadID/messageID」）。
	killRunningCommandCalls []string
	// killRunningCommandKilled 让 FakeClient.KillRunningCommand 模拟后端返回的终止进程数。
	killRunningCommandKilled int
	// interruptAlreadyStopped 让 FakeClient.Interrupt 模拟后端返回 already_stopped。
	interruptAlreadyStopped bool
	// sendDelegated 让 FakeClient.Send/SendWithAttachments 模拟消息被交给子线程。
	sendDelegated       bool
	addProjectCalls     []string
	setRuntimeModeCalls []setRuntimeModeCall
	SetAgentCalls       []SetAgentCall
	composerPrefsCalls  []composerPrefsCall
	composerPrefsHook   func(string, ComposerPrefs) error
	fastModeCalls       []FastModeCall
	beeTiersCalls       []BeeTiersCall
	swarmPairIDCalls    []SwarmPairIDCall
	swarmModeCalls      []SwarmModeCall

	queue              map[string][]QueuedFollowUp
	enqueueCalls       []fakeQueueCall
	removeQueuedCalls  []fakeQueueCall
	sendQueuedNowCalls []fakeQueueCall
	moveQueuedCalls    []fakeQueueCall
	mcpDisabledCalls   []fakeMCPDisabledCall
	queueWatchers      map[int]func(string, []QueuedFollowUp)
	nextQueueWatcherID int

	summaryWatchers      map[int]func(Thread)
	nextSummaryWatcherID int

	mcpSignedIn    map[string]bool
	mcpSignInErr   error
	mcpSignOutErr  error
	mcpSignInCalls []string
	// mcpSignInAuth 记录最近一次登录收到的目录 Auth 声明，nil 表示未声明。
	mcpSignInAuth       *agentconf.MCPCatalogAuth
	mcpSignOutCalls     []string
	commands            []SlashCommand
	files               []FileMatch
	fileTree            map[string][]FileEntry
	fileContents        map[string]FileContent
	filesErr            error
	fileOpCalls         []FileOpCall
	fileOpsErr          error
	threadWorkDir       string
	scripts             []Script
	scriptsErr          error
	projectScripts      map[string][]ProjectScript
	uploadCalls         []uploadAttachmentCall
	nextAttachmentSeq   int
	sendWithAttachCalls []sentWithAttachmentsCall

	// agy 账号与路由模拟字段与调用记录
	agyAccounts           []accounts.Account
	agyAccountsErr        error
	agyDefaultAccountID   string
	agyLoginURL           string
	agyLoginStartErr      error
	agyLoginSubmitErr     error
	agyLoginSubmitCalls   []string
	agyLoginWaitAccount   accounts.Account
	agyLoginWaitErr       error
	agyLoginWaitDone      chan struct{}
	agyLoginCancelCalls   int
	agyTerminalID         string
	agyTerminalEnv        []string
	agyTerminalArgs       []string
	agyTerminalLoginErr   error
	agyAdoptAccount       accounts.Account
	agyAdoptErr           error
	agyAdoptCalls         []string
	agyRemoveErr          error
	agyRemovedIDs         []string
	agyBoundConversations map[string][]string
	agySetDefaultErr      error
	agySetDefaultIDs      []string
	agyUpdateErr          error
	agyUpdateCalls        []struct {
		ID    string
		Patch accounts.UpdatePatch
	}
	agyRoutingConfig    multiaccount.Config
	agyRoutingErr       error
	agySetRoutingErr    error
	agySetRoutingCalls  []multiaccount.Config
	agyRefreshQuotaErr  error
	agyRefreshCalls     []string
	agyWarmupErr        error
	agyWarmupCalls      []string
	agyAutoWarmup       bool
	agyAutoWarmupErr    error
	agySetAutoWarmupErr error

	renameCalls []renameCall

	// splitCalls 记录 SplitSession 请求的会话 ID；splitErr 非 nil 时该调用直接返回它。
	splitCalls []string
	splitErr   error

	// Git 模拟字段与调用记录
	gitState          GitState
	gitErr            error
	gitCheckoutCalls  []string
	gitWorktreeCalls  []string
	gitCommitMessages []string
	gitPushCalls      int
	// gitChangedFiles 是 GitChangedFiles 返回的改动文件，gitCommitSelectedCalls 记录按路径提交的调用。
	gitChangedFiles        []GitChangedFile
	gitCommitSelectedCalls []gitSelectedCommitCall
	// gitOpCalls 记录 Git 菜单中同步类操作的调用：键为 GitFetch/GitPull/GitStash/GitStashPop 或
	// "GitFastForwardInto:<branch>"/"GitMergeFrom:<branch>"。
	gitOpCalls []string

	// 会话分支模拟字段与调用记录：BranchCalls 每项为 {threadID, messageID}
	BranchCalls [][2]string
	branchID    string
	branchErr   error

	contextDocs map[string]ContextDoc
	// contextDocProjects 记录文档绑定的项目路径（为空表示对所有项目可见）。
	contextDocProjects   map[string]string
	contextWatchers      map[int]func()
	nextContextWatcherID int
	contextWriteCalls    int

	// memoryProposals / memoryItems 是记忆面板（Nowledge）的测试数据。
	memoryBackend   string
	memoryProposals []MemoryProposal
	memoryItems     []MemoryItem
	memoryWorking   string
	// memorySpaces 是设置页「项目记忆空间」可选的 Nowledge 空间列表。
	memorySpaces       []MemorySpace
	memoryWorkingErr   error
	memoryProposalsErr error
	memoryUpdateErr    error
	memoryAcceptErr    error
	memoryRejectErr    error
	memoryCreateErr    error
	memoryDistillCount int
	memoryDistillErr   error
	memorySearchErr    error
	memoryUpdateCalls  []MemoryProposal
	memoryAcceptCalls  []string
	memoryRejectCalls  []string
	memoryCreateCalls  [][4]string
	memoryDistillCalls [][2]string
	memorySearchCalls  [][2]string
	memoryTargetCalls  [][2]string
	// memoryForget* / memoryExport* 是 ForgetMemory 与 ExportMemories 的预设结果。
	memoryForgetDeleted bool
	memoryForgetErr     error
	memoryForgetCalls   [][2]string
	memoryExportBody    string
	memoryExportErr     error
	memoryExportCalls   []string
	// memoryCondense* 是 CondenseMemory 的预设结果。
	memoryCondenseCalls [][4]string
	memoryCondenseTitle string
	memoryCondenseBody  string
	memoryCondenseErr   error
	memoryCondenseSet   bool

	remoteStatus          RemoteStatus
	remotePairing         RemotePairing
	remoteErr             error
	setRemoteEnabledCalls []bool
	remotePairingCalls    []bool
	remoteDevices         []RemoteDevice
	remoteDevicesErr      error
	revokeDeviceCalls     []string

	renameDeviceCalls     [][2]string
	revokeAllDevicesCalls int
	restartRemoteCalls    int
	remoteTLSStatus       RemoteTLSStatus
	remoteTLSErr          error
	remoteTLSStatusCalls  int
	saveRemoteTokenCalls  []string
	issueRemoteTLSCalls   int
	importRemoteTLSCalls  [][2]string
	removeRemoteTLSCalls  int
	remoteTailscaleStatus RemoteTailscaleStatus
	remoteTailscaleErr    error
	// remoteTailscaleGate 非 nil 时，RemoteTailscaleStatus 先拍快照再等它关闭才返回，
	// 用来把一次刷新卡在写回之前，观察过期的在途结果会不会被丢弃。
	remoteTailscaleGate chan struct{}
	// setRemoteTailscaleCalls 记录每次调用 SetRemoteTailscaleEnabled 的 enabled 参数（取最后一次判断）。
	setRemoteTailscaleCalls   []bool
	resetRemoteTailscaleCalls int

	customEndpoints           []CustomEndpoint
	piMagpieEndpoints         []PiMagpieEndpoint
	saveCustomEndpointCalls   []CustomEndpoint
	saveCustomEndpointKeys    []string
	saveCustomEndpointWebKeys []string
	deleteCustomEndpointCalls []string
	testCustomEndpointCalls   []CustomEndpoint
	testCustomEndpointResult  CustomEndpointTestResult
	customEndpointErr         error

	providerSettings         []ProviderSetting
	saveProviderSettingCalls []ProviderSetting
	saveProviderSettingKeys  []string
	providerSettingsErr      error

	// 会话休眠模拟调用记录
	setThreadDormantCalls []setThreadDormantCall

	// 会话转发模拟字段与调用记录
	ForwardCalls []string
	forwardID    string
	forwardErr   error

	// Git 新建分支模拟调用记录
	gitCreateBranchCalls [][2]string

	// 模型与 Swarm 配置
	modelList        []ModelPin
	modelPreferences map[string]ModelPreference
	swarmPairs       []SwarmPair

	// 调用记录与模拟字段
	moveToWorktreeCalls []struct {
		ThreadID      string
		Branch        string
		TransferFiles bool
	}
	moveToWorktreeErr error

	bulkMarkReadCalls []BulkMarkReadCall
	bulkMarkReadErr   error

	// 单条归档/休眠的撤销令牌模拟字段与调用记录
	archiveThreadWithUndoToken string
	archiveThreadWithUndoErr   error
	archiveThreadWithUndoCalls []string
	markDormantWithUndoToken   string
	markDormantWithUndoErr     error
	markDormantWithUndoCalls   []string

	bulkArchiveCalls []BulkArchiveCall
	bulkArchiveToken string
	bulkArchiveErr   error

	undoBulkOpCalls []UndoBulkOpCall
	undoBulkOpErr   error

	recallTurnCalls []string
	recallTurnText  string
	recallTurnErr   error

	previewRevertCalls []struct {
		ThreadID string
		TurnID   string
	}
	previewRevertResult *RevertPreview
	previewRevertErr    error

	executeRevertCalls []struct {
		ThreadID string
		TurnID   string
	}
	executeRevertErr error

	scanWorktreesCalls  int
	scanWorktreesResult []WorktreeInfo
	scanWorktreesErr    error

	cleanSafeWorktreesCalls  int
	cleanSafeWorktreesResult int
	cleanSafeWorktreesErr    error

	triggerAutoMergeCalls []string
	triggerAutoMergeErr   error

	providerVersionCalls  []string
	providerVersionResult ProviderVersionInfo
	providerVersionErr    error

	checkProviderUpdateCalls  []string
	checkProviderUpdateResult ProviderVersionInfo
	checkProviderUpdateErr    error

	providerAccountCalls  []string
	providerAccountResult ProviderAccountInfo
	providerAccountErr    error

	providerQuotaCalls  []string
	providerQuotaResult []ProviderQuotaWindow
	providerQuotaErr    error

	refreshProviderQuotaCalls []string
	refreshProviderQuotaErr   error
	// refreshProviderQuotaGate 非 nil 时 RefreshProviderQuota 等它关闭才返回，用来观察刷新中的按钮。
	refreshProviderQuotaGate chan struct{}

	refreshThreadQuotaCalls []threadQuotaRefreshCall

	providerCreditsCalls  []string
	providerCreditsResult map[string]ProviderCredits
	providerCreditsErr    error

	// magpiePersonas 是 MagpiePersonas 返回的假数据，nil 表示空列表。
	magpiePersonas []MagpiePersona

	// testMagpiePersonasResult 与 testMagpiePersonasCalls 是 TestMagpiePersonas 的假结果与调用记录。
	testMagpiePersonasResult PersonaSourceTestResult
	testMagpiePersonasCalls  []testMagpiePersonasCall

	magpieQuotaProviders      []string
	magpieQuotaProvidersErr   error
	magpieQuotaProvidersCalls int

	// agentMagpie 是 AgentMagpie 返回的假数据，按 agent 索引；未预置时返回默认设置。
	agentMagpie               map[string]AgentMagpieSetting
	agentMagpieErr            error
	agentMagpieCalls          []string
	setAgentMagpieModelsCalls []setAgentMagpieModelsCall
	setAgentMagpieModelsErr   error
	setAgentMagpieModeCalls   []setAgentMagpieModeCall
	setAgentMagpieModeErr     error

	setAgentMagpieDefaultModelCalls []setAgentMagpieDefaultModelCall
	setAgentMagpieDefaultModelErr   error

	agentMagpieGlobalActionCalls []agentMagpieGlobalActionCall
	agentMagpieGlobalActionErr   error
}

type testMagpiePersonasCall struct {
	ep     CustomEndpoint
	webKey string
}

type setAgentMagpieModelsCall struct {
	Agent      string
	EndpointID string
	ModelIDs   []string
}

type setAgentMagpieModeCall struct {
	Agent string
	Mode  string
}

// setAgentMagpieDefaultModelCall 是一次默认模型保存请求的参数。
type setAgentMagpieDefaultModelCall struct {
	Agent      string
	EndpointID string
	ModelID    string
}

type agentMagpieGlobalActionCall struct {
	Agent  string
	Action string
}

type setThreadDormantCall struct {
	ThreadID string
	Dormant  bool
}

type renameCall struct {
	ThreadID string
	Title    string
}

// SplitSession 记录分割请求；配置了 splitErr 时按错误返回，方便用例验证失败提示。
func (f *FakeClient) SplitSession(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.splitCalls = append(f.splitCalls, threadID)
	return f.splitErr
}

// splitSessionCalls 在锁内复制一份分割请求记录。
func (f *FakeClient) splitSessionCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.splitCalls...)
}

func NewFakeClient() *FakeClient {
	now := time.Now()
	p := Project{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		UpdatedAt: now,
	}

	return &FakeClient{
		projects:              []Project{p},
		threads:               map[string][]Thread{"p1": {th}},
		messages:              make(map[string][]Message),
		approvals:             make(map[string][]Approval),
		watchers:              make(map[string][]*watchEntry),
		usage:                 make(map[string]ThreadUsage),
		diffs:                 make(map[string]ThreadDiff),
		kanbanWatchers:        make(map[int]func()),
		automationRuns:        make(map[string][]AutomationRun),
		swarmStates:           make(map[string]SwarmState),
		swarmMergeDiffs:       make(map[string]ThreadDiff),
		swarmWatchers:         make(map[int]func(string)),
		mcpSignedIn:           make(map[string]bool),
		agyRoutingConfig:      multiaccount.DefaultConfig(),
		agyDefaultAccountID:   accounts.SystemAccountID,
		agyBoundConversations: make(map[string][]string),
		contextDocs:           make(map[string]ContextDoc),
		contextDocProjects:    make(map[string]string),
		contextWatchers:       make(map[int]func()),
		modelPreferences:      make(map[string]ModelPreference),
	}
}

func (f *FakeClient) SetThreadUsage(threadID string, u ThreadUsage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usage[threadID] = u
}

func (f *FakeClient) Usage(ctx context.Context, threadID string) (ThreadUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage[threadID], nil
}

func (f *FakeClient) Projects(ctx context.Context) ([]Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]Project, len(f.projects))
	copy(res, f.projects)
	return res, nil
}

func (f *FakeClient) AddProject(ctx context.Context, path string) (Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cleanPath := filepath.Clean(path)
	f.addProjectCalls = append(f.addProjectCalls, cleanPath)

	name := filepath.Base(cleanPath)
	newID := fmt.Sprintf("p%d", len(f.projects)+1)
	p := Project{
		ID:   newID,
		Name: name,
		Path: cleanPath,
	}
	f.projects = append(f.projects, p)
	return p, nil
}

func (f *FakeClient) SetProjectsAndThreads(projects []Project, threads map[string][]Thread) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects = projects
	f.threads = threads
}

func (f *FakeClient) Threads(ctx context.Context, projectID string) ([]Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.threadsCalls == nil {
		f.threadsCalls = make(map[string]int)
	}
	f.threadsCalls[projectID]++
	list := f.threads[projectID]
	res := make([]Thread, len(list))
	copy(res, list)
	return res, nil
}

// ThreadsCalls 返回某个项目上 Threads 被调用的次数。
func (f *FakeClient) ThreadsCalls(projectID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.threadsCalls[projectID]
}

func (f *FakeClient) History(ctx context.Context, threadID string) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.historyCalls = append(f.historyCalls, threadID)
	list := f.messages[threadID]
	res := make([]Message, len(list))
	copy(res, list)
	return res, nil
}

func (f *FakeClient) HistoryCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.historyCalls))
	copy(res, f.historyCalls)
	return res
}

func (f *FakeClient) SetProviders(providers []ProviderInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providers = providers
}

func (f *FakeClient) Providers(ctx context.Context) ([]ProviderInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]ProviderInfo, len(f.providers))
	copy(res, f.providers)
	return res, nil
}

func (f *FakeClient) RefreshProviders(ctx context.Context) ([]ProviderInfo, error) {
	return f.Providers(ctx)
}

func (f *FakeClient) CreateThreadWithOptions(ctx context.Context, projectID string, opts NewThreadOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, projectID)
	f.createCallInfos = append(f.createCallInfos, createCallInfo{ProjectID: projectID, Provider: opts.Provider})
	f.lastNewThreadOpts = opts

	f.nextThreadSeq++
	newID := fmt.Sprintf("t-new-%d", f.nextThreadSeq)
	newThread := Thread{
		ID:           newID,
		ProjectID:    projectID,
		Title:        fmt.Sprintf("新会话 %d", len(f.threads[projectID])+1),
		Provider:     opts.Provider,
		Model:        opts.Model,
		BeesProvider: opts.BeesProvider,
		BeesModel:    opts.BeesModel,
		UpdatedAt:    time.Now(),
	}
	f.threads[projectID] = append(f.threads[projectID], newThread)
	return newID, nil
}

func (f *FakeClient) CreateThread(ctx context.Context, projectID, provider string) (string, error) {
	return f.CreateThreadWithOptions(ctx, projectID, NewThreadOptions{Provider: provider})
}

func (f *FakeClient) Send(ctx context.Context, threadID, text string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls = append(f.sendCalls, sentCall{ThreadID: threadID, Text: text})
	return f.sendDelegated, nil
}

func (f *FakeClient) SetCommands(commands []SlashCommand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append([]SlashCommand(nil), commands...)
}

func (f *FakeClient) Commands(ctx context.Context, threadID string) ([]SlashCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]SlashCommand, len(f.commands))
	copy(res, f.commands)
	return res, nil
}

func (f *FakeClient) SetScripts(scripts []Script) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append([]Script(nil), scripts...)
}

func (f *FakeClient) SetScriptsErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scriptsErr = err
}

func (f *FakeClient) Scripts(ctx context.Context, threadID string) ([]Script, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scriptsErr != nil {
		return nil, f.scriptsErr
	}
	// 项目脚本排在前面（与 app.Client 一致）。
	stored := f.projectScripts[threadID]
	res := make([]Script, 0, len(stored)+len(f.scripts))
	for _, s := range stored {
		res = append(res, Script{
			Name:                s.Name,
			Command:             s.Command,
			Kind:                "project",
			ID:                  s.ID,
			Symbol:              s.Symbol,
			RunOnWorktreeCreate: s.RunOnWorktreeCreate,
		})
	}
	res = append(res, f.scripts...)
	return res, nil
}

// SetProjectScripts 按会话写入项目脚本，并在缺 ID 时补一个稳定 ID。
func (f *FakeClient) SetProjectScripts(ctx context.Context, threadID string, scripts []ProjectScript) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.projectScripts == nil {
		f.projectScripts = make(map[string][]ProjectScript)
	}
	stored := make([]ProjectScript, len(scripts))
	copy(stored, scripts)
	for i := range stored {
		if stored[i].ID == "" {
			stored[i].ID = fmt.Sprintf("project-script-%d", i+1)
		}
	}
	f.projectScripts[threadID] = stored
	return nil
}

// ProjectScripts 读取 FakeClient 里按会话存放的项目脚本。
func (f *FakeClient) ProjectScripts(ctx context.Context, threadID string) ([]ProjectScript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored := f.projectScripts[threadID]
	res := make([]ProjectScript, len(stored))
	copy(res, stored)
	return res, nil
}

func (f *FakeClient) ThreadWorkDir(threadID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.threadWorkDir == "" {
		return "", false
	}
	return f.threadWorkDir, true
}

// SetThreadWorkDir 设置 ThreadWorkDir 的返回值。
func (f *FakeClient) SetThreadWorkDir(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threadWorkDir = dir
}

// SetFileTree 设置 FilesList 返回的目录树，键为目录相对路径，根目录用空字符串。
func (f *FakeClient) SetFileTree(tree map[string][]FileEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileTree = tree
}

// SetFileContents 设置 FilesRead 返回的文件内容，键为文件相对路径。
func (f *FakeClient) SetFileContents(contents map[string]FileContent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileContents = contents
}

// SetFilesError 设置后 FilesList/FilesRead 均返回该错误。
func (f *FakeClient) SetFilesError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filesErr = err
}

func (f *FakeClient) FilesList(ctx context.Context, threadID, subpath string) (FileListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.filesErr != nil {
		return FileListing{}, f.filesErr
	}
	entries := f.fileTree[subpath]
	res := make([]FileEntry, len(entries))
	copy(res, entries)
	return FileListing{Path: subpath, Entries: res}, nil
}

func (f *FakeClient) FilesRead(ctx context.Context, threadID, path string, offset, length int64) (FileContent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.filesErr != nil {
		return FileContent{}, f.filesErr
	}
	content, ok := f.fileContents[path]
	if !ok {
		return FileContent{}, fmt.Errorf("fake: file content not set: %s", path)
	}
	return content, nil
}

func (f *FakeClient) FilesDuplicate(ctx context.Context, threadID, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileOpCalls = append(f.fileOpCalls, FileOpCall{Op: "duplicate", ThreadID: threadID, Path: path})
	if f.fileOpsErr != nil {
		return "", f.fileOpsErr
	}
	return path + " copy", nil
}

// FileOpCalls 返回文件写操作的调用记录副本。
func (f *FakeClient) FileOpCalls() []FileOpCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FileOpCall(nil), f.fileOpCalls...)
}

// SetFileOpsError 设置后四个文件写操作均返回该错误。
func (f *FakeClient) SetFileOpsError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileOpsErr = err
}

func (f *FakeClient) FilesCreate(ctx context.Context, threadID, path string, dir bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileOpCalls = append(f.fileOpCalls, FileOpCall{Op: "create", ThreadID: threadID, Path: path, Dir: dir})
	if f.fileOpsErr != nil {
		return "", f.fileOpsErr
	}
	return path, nil
}

func (f *FakeClient) FilesRename(ctx context.Context, threadID, path, newPath string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileOpCalls = append(f.fileOpCalls, FileOpCall{Op: "rename", ThreadID: threadID, Path: path, NewPath: newPath})
	if f.fileOpsErr != nil {
		return "", f.fileOpsErr
	}
	return newPath, nil
}

func (f *FakeClient) FilesTrash(ctx context.Context, threadID, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileOpCalls = append(f.fileOpCalls, FileOpCall{Op: "trash", ThreadID: threadID, Path: path})
	return f.fileOpsErr
}

func (f *FakeClient) SetFiles(files []FileMatch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = append([]FileMatch(nil), files...)
}

func (f *FakeClient) SearchFiles(ctx context.Context, threadID, query string) ([]FileMatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if query == "" {
		res := make([]FileMatch, len(f.files))
		copy(res, f.files)
		return res, nil
	}
	q := strings.ToLower(query)
	var res []FileMatch
	for _, m := range f.files {
		if strings.Contains(strings.ToLower(m.Path), q) || strings.Contains(strings.ToLower(m.Name), q) {
			res = append(res, m)
		}
	}
	return res, nil
}

func (f *FakeClient) UploadAttachment(ctx context.Context, name, mimeType string, data []byte) (Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadCalls = append(f.uploadCalls, uploadAttachmentCall{
		Name:     name,
		MimeType: mimeType,
		Data:     data,
	})
	f.nextAttachmentSeq++
	id := fmt.Sprintf("att-%d", f.nextAttachmentSeq)
	return Attachment{
		ID:        id,
		Name:      name,
		MimeType:  mimeType,
		ByteCount: len(data),
	}, nil
}

func (f *FakeClient) SendWithAttachments(ctx context.Context, threadID, text string, attachmentIDs []string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendWithAttachCalls = append(f.sendWithAttachCalls, sentWithAttachmentsCall{
		ThreadID:      threadID,
		Text:          text,
		AttachmentIDs: append([]string(nil), attachmentIDs...),
	})
	return f.sendDelegated, nil
}

func (f *FakeClient) Approvals(ctx context.Context, threadID string) ([]Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.approvals[threadID]
	res := make([]Approval, len(list))
	copy(res, list)
	return res, nil
}

func (f *FakeClient) Respond(ctx context.Context, a Approval, optionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respondCalls = append(f.respondCalls, respondCall{Approval: a, OptionID: optionID})
	return nil
}

func (f *FakeClient) RespondAnswers(ctx context.Context, a Approval, optionID string, answers map[string][]string, skip bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respondAnswersCalls = append(f.respondAnswersCalls, respondAnswersCall{Approval: a, OptionID: optionID, Answers: answers, Skip: skip})
	return nil
}

func (f *FakeClient) Interrupt(ctx context.Context, threadID string) (bool, error) {
	f.mu.Lock()
	f.interruptCalls = append(f.interruptCalls, threadID)
	alreadyStopped := f.interruptAlreadyStopped
	f.mu.Unlock()
	return alreadyStopped, nil
}

func (f *FakeClient) KillRunningCommand(ctx context.Context, threadID, messageID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killRunningCommandCalls = append(f.killRunningCommandCalls, threadID+"/"+messageID)
	return f.killRunningCommandKilled, nil
}

func (f *FakeClient) Watch(threadID string, onUpdate func(ThreadUpdate), onApprovals func([]Approval)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nextWatcherID++
	entry := &watchEntry{
		id:          f.nextWatcherID,
		threadID:    threadID,
		onUpdate:    onUpdate,
		onApprovals: onApprovals,
	}
	f.watchers[threadID] = append(f.watchers[threadID], entry)

	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		entry.stopped = true
	}
}

// TriggerUpdate 手动触发指定会话的消息与状态增量更新。
func (f *FakeClient) TriggerUpdate(update ThreadUpdate) {
	f.mu.Lock()
	watchers := make([]*watchEntry, 0, len(f.watchers[update.ThreadID]))
	for _, w := range f.watchers[update.ThreadID] {
		if !w.stopped {
			watchers = append(watchers, w)
		}
	}
	f.mu.Unlock()

	for _, w := range watchers {
		w.onUpdate(update)
	}
}

// TriggerApprovals 手动触发指定会话的审批事件。
func (f *FakeClient) TriggerApprovals(threadID string, approvals []Approval) {
	f.mu.Lock()
	// 与真实后端一致：推送的列表就是该会话当前的待审批，之后 Approvals() 也能拉到。
	if f.approvals == nil {
		f.approvals = make(map[string][]Approval)
	}
	f.approvals[threadID] = append([]Approval(nil), approvals...)
	watchers := make([]*watchEntry, 0, len(f.watchers[threadID]))
	for _, w := range f.watchers[threadID] {
		if !w.stopped {
			watchers = append(watchers, w)
		}
	}
	f.mu.Unlock()

	for _, w := range watchers {
		w.onApprovals(approvals)
	}
}

func (f *FakeClient) CreateThreadCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.createCalls)
}

func (f *FakeClient) SendCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sendCalls)
}

func (f *FakeClient) LastSentText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendCalls) == 0 {
		return ""
	}
	return f.sendCalls[len(f.sendCalls)-1].Text
}

func (f *FakeClient) SetMessages(threadID string, msgs []Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]Message, len(msgs))
	copy(res, msgs)
	f.messages[threadID] = res
}

func (f *FakeClient) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = nil
	f.createCallInfos = nil
	f.lastNewThreadOpts = NewThreadOptions{}
	f.sendCalls = nil
	f.respondCalls = nil
	f.respondAnswersCalls = nil
	f.interruptCalls = nil
	f.killRunningCommandCalls = nil
	f.SetAgentCalls = nil
	f.composerPrefsCalls = nil
	f.fastModeCalls = nil
	f.swarmModeCalls = nil
	f.stoppedBees = nil
	f.stopAllCalls = nil
	f.clearCalls = nil
	f.approveMergeCalls = nil
	f.discardMergeCalls = nil
	f.setSwarmEnabledCalls = nil
	f.setThreadDormantCalls = nil
	f.ForwardCalls = nil
	f.gitCreateBranchCalls = nil
}

func (f *FakeClient) LastCreatedProvider() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createCallInfos) == 0 {
		return ""
	}
	return f.createCallInfos[len(f.createCallInfos)-1].Provider
}

func (f *FakeClient) LastNewThreadOptions() NewThreadOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastNewThreadOpts
}

func (f *FakeClient) InterruptCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.interruptCalls)
}

func (f *FakeClient) LastInterruptThreadID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.interruptCalls) == 0 {
		return ""
	}
	return f.interruptCalls[len(f.interruptCalls)-1]
}

// KillRunningCommandCalls 返回 KillRunningCommand 的调用次数。
func (f *FakeClient) KillRunningCommandCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.killRunningCommandCalls)
}

// LastKillRunningCommandMessageID 返回最后一次 KillRunningCommand 的目标消息 ID。
func (f *FakeClient) LastKillRunningCommandMessageID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.killRunningCommandCalls) == 0 {
		return ""
	}
	call := f.killRunningCommandCalls[len(f.killRunningCommandCalls)-1]
	if idx := strings.LastIndexByte(call, '/'); idx >= 0 {
		return call[idx+1:]
	}
	return call
}

// TriggerRunningUpdate 手动触发指定会话运行状态更新。
func (f *FakeClient) TriggerRunningUpdate(threadID string, isRunning bool) {
	f.mu.Lock()
	var thread Thread
	found := false
	for _, list := range f.threads {
		for i, th := range list {
			if th.ID == threadID {
				list[i].IsRunning = isRunning
				thread = list[i]
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		thread = Thread{ID: threadID, IsRunning: isRunning}
	}
	f.mu.Unlock()

	f.TriggerUpdate(ThreadUpdate{
		ThreadID: threadID,
		Thread:   thread,
	})
}

func (f *FakeClient) LastRespondOptionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.respondCalls) == 0 {
		return ""
	}
	return f.respondCalls[len(f.respondCalls)-1].OptionID
}

func (f *FakeClient) AddProjectCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.addProjectCalls))
	copy(res, f.addProjectCalls)
	return res
}

func (f *FakeClient) HasAddProjectCall(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	clean := filepath.Clean(path)
	for _, p := range f.addProjectCalls {
		if filepath.Clean(p) == clean {
			return true
		}
	}
	return false
}

func (f *FakeClient) LastCreatedProjectID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createCalls) == 0 {
		return ""
	}
	return f.createCalls[len(f.createCalls)-1]
}

func (f *FakeClient) SetThreadRuntimeMode(threadID, mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setRuntimeModeCalls = append(f.setRuntimeModeCalls, setRuntimeModeCall{ThreadID: threadID, Mode: mode})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].RuntimeMode = mode
			}
		}
	}
	return nil
}

func (f *FakeClient) SetThreadAgent(threadID, provider, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SetAgentCalls = append(f.SetAgentCalls, SetAgentCall{ThreadID: threadID, Provider: provider, Model: model})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				if provider != "" {
					list[i].Provider = provider
				}
				list[i].Model = model
			}
		}
	}
	return nil
}

func (f *FakeClient) RenameThread(threadID, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameCalls = append(f.renameCalls, renameCall{ThreadID: threadID, Title: title})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].Title = title
			}
		}
	}
	return nil
}

func (f *FakeClient) SetThreadPinned(threadID string, pinned bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].Pinned = pinned
			}
		}
	}
	return nil
}

func (f *FakeClient) SetThreadArchived(threadID string, archived bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if archived {
		// 从活跃线程移动到归档列表
		for pid, list := range f.threads {
			var filtered []Thread
			for _, th := range list {
				if th.ID == threadID {
					f.archivedThreads = append(f.archivedThreads, th)
				} else {
					filtered = append(filtered, th)
				}
			}
			f.threads[pid] = filtered
		}
	} else {
		// 从归档列表恢复到活跃线程
		var remaining []Thread
		for _, th := range f.archivedThreads {
			if th.ID == threadID {
				pid := th.ProjectID
				f.threads[pid] = append(f.threads[pid], th)
			} else {
				remaining = append(remaining, th)
			}
		}
		f.archivedThreads = remaining
	}
	return nil
}

// ArchivedThreads 返回当前假客户端中的所有已归档会话。
func (f *FakeClient) ArchivedThreads(ctx context.Context) ([]Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]Thread, len(f.archivedThreads))
	copy(res, f.archivedThreads)
	return res, nil
}

func (f *FakeClient) SetThreadDormant(threadID string, dormant bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setThreadDormantCalls = append(f.setThreadDormantCalls, setThreadDormantCall{ThreadID: threadID, Dormant: dormant})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].Dormant = dormant
			}
		}
	}
	return nil
}

func (f *FakeClient) SetThreadDormantCalls() []setThreadDormantCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]setThreadDormantCall, len(f.setThreadDormantCalls))
	copy(res, f.setThreadDormantCalls)
	return res
}

// ArchiveThreadWithUndo 记录调用并返回可配置的撤销令牌；不改变状态。
func (f *FakeClient) ArchiveThreadWithUndo(threadID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archiveThreadWithUndoCalls = append(f.archiveThreadWithUndoCalls, threadID)
	return f.archiveThreadWithUndoToken, f.archiveThreadWithUndoErr
}

// MarkDormantWithUndo 记录调用并返回可配置的撤销令牌；不改变状态。
func (f *FakeClient) MarkDormantWithUndo(threadID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markDormantWithUndoCalls = append(f.markDormantWithUndoCalls, threadID)
	return f.markDormantWithUndoToken, f.markDormantWithUndoErr
}

// ArchiveThreadWithUndoCalls 获取所有 ArchiveThreadWithUndo 调用的会话 ID。
func (f *FakeClient) ArchiveThreadWithUndoCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.archiveThreadWithUndoCalls))
	copy(res, f.archiveThreadWithUndoCalls)
	return res
}

// MarkDormantWithUndoCalls 获取所有 MarkDormantWithUndo 调用的会话 ID。
func (f *FakeClient) MarkDormantWithUndoCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.markDormantWithUndoCalls))
	copy(res, f.markDormantWithUndoCalls)
	return res
}

// RetainThread 空实现：不改变状态。
func (f *FakeClient) RetainThread(threadID string) error {
	return nil
}

// ReleaseProvisionalThread 空实现：不改变状态。
func (f *FakeClient) ReleaseProvisionalThread(threadID string) error {
	return nil
}

func (f *FakeClient) DeleteThread(threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for pid, list := range f.threads {
		var filtered []Thread
		for _, th := range list {
			if th.ID != threadID {
				filtered = append(filtered, th)
			}
		}
		f.threads[pid] = filtered
	}
	var remArchived []Thread
	for _, th := range f.archivedThreads {
		if th.ID != threadID {
			remArchived = append(remArchived, th)
		}
	}
	f.archivedThreads = remArchived
	delete(f.messages, threadID)
	return nil
}

func (f *FakeClient) RemoveProject(projectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var filtered []Project
	for _, p := range f.projects {
		if p.ID != projectID {
			filtered = append(filtered, p)
		}
	}
	f.projects = filtered
	return nil
}

// LastRespondAnswers 返回最近一次 RespondAnswers 调用的参数，没有调用时 ok 为 false。
func (f *FakeClient) LastRespondAnswers() (respondAnswersCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.respondAnswersCalls) == 0 {
		return respondAnswersCall{}, false
	}
	return f.respondAnswersCalls[len(f.respondAnswersCalls)-1], true
}

func (f *FakeClient) RespondCalls() []respondCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]respondCall, len(f.respondCalls))
	copy(res, f.respondCalls)
	return res
}

func (f *FakeClient) SetThreadDiff(threadID, turnID string, d ThreadDiff) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.diffs == nil {
		f.diffs = make(map[string]ThreadDiff)
	}
	f.diffs[threadID+"|"+turnID] = d
}

func (f *FakeClient) Diff(ctx context.Context, threadID, turnID string) (ThreadDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffCalls = append(f.diffCalls, threadID)
	if f.diffs == nil {
		return ThreadDiff{}, nil
	}
	return f.diffs[threadID+"|"+turnID], nil
}

func (f *FakeClient) DiffCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.diffCalls))
	copy(res, f.diffCalls)
	return res
}

var _ Client = (*FakeClient)(nil)

func (f *FakeClient) WatchAutomations(onChange func()) func() {
	f.mu.Lock()
	f.nextKanbanWatcherID++
	id := f.nextKanbanWatcherID
	if f.automationWatchers == nil {
		f.automationWatchers = make(map[int]func())
	}
	f.automationWatchers[id] = onChange
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.automationWatchers, id)
		f.mu.Unlock()
	}
}

func (f *FakeClient) TriggerAutomationChange() {
	f.mu.Lock()
	var callbacks []func()
	for _, fn := range f.automationWatchers {
		callbacks = append(callbacks, fn)
	}
	f.mu.Unlock()
	for _, fn := range callbacks {
		fn()
	}
}

// SetKanbanBoard 设置假客户端的看板数据。
func (f *FakeClient) SetKanbanBoard(b KanbanBoard) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kanbanBoard = b
}

// SetKanbanError 设置 KanbanBoard 调用时返回的模拟错误。
func (f *FakeClient) SetKanbanError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kanbanErr = err
}

// SetMoveTaskError 设置 MoveTask 调用时返回的模拟错误。
func (f *FakeClient) SetMoveTaskError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moveTaskErr = err
}

// MoveTaskCalls 获取所有 MoveTask 调用记录。
func (f *FakeClient) MoveTaskCalls() []MoveTaskCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]MoveTaskCall, len(f.moveTaskCalls))
	copy(res, f.moveTaskCalls)
	return res
}

// StartTaskCalls 获取所有 StartTask 调用的任务 ID。
func (f *FakeClient) StartTaskCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.startTaskCalls))
	copy(res, f.startTaskCalls)
	return res
}

// ReviewTaskCalls 获取所有 ReviewTask 调用记录。
func (f *FakeClient) ReviewTaskCalls() []ReviewCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]ReviewCall, len(f.reviewTaskCalls))
	copy(res, f.reviewTaskCalls)
	return res
}

// CreateTaskCalls 获取所有 CreateTask 调用记录（包含 ProjectID, Title, Notes）。
func (f *FakeClient) CreateTaskCalls() []KanbanTask {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]KanbanTask, len(f.createTaskCalls))
	copy(res, f.createTaskCalls)
	return res
}

// GoalCalls 获取所有目标操作调用记录。
func (f *FakeClient) GoalCalls() []GoalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]GoalCall, len(f.goalCalls))
	copy(res, f.goalCalls)
	return res
}

// KanbanBoardCalls 获取 KanbanBoard 被调用的总次数。
func (f *FakeClient) KanbanBoardCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kanbanBoardCalls
}

// TriggerKanbanChange 手动触发所有 WatchKanban 注册的回调。
func (f *FakeClient) TriggerKanbanChange() {
	f.mu.Lock()
	var list []func()
	for _, fn := range f.kanbanWatchers {
		if fn != nil {
			list = append(list, fn)
		}
	}
	f.mu.Unlock()

	for _, fn := range list {
		fn()
	}
}

// SetAutomations 设置假客户端的自动化配置列表。
func (f *FakeClient) SetAutomations(list []Automation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.automations = make([]Automation, len(list))
	copy(f.automations, list)
}

// SetAutomationRuns 设置指定自动化的运行记录列表。
func (f *FakeClient) SetAutomationRuns(id string, runs []AutomationRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.automationRuns == nil {
		f.automationRuns = make(map[string][]AutomationRun)
	}
	r := make([]AutomationRun, len(runs))
	copy(r, runs)
	f.automationRuns[id] = r
}

// CreateAutomationCalls 获取所有 CreateAutomation 调用记录。
func (f *FakeClient) CreateAutomationCalls() []AutomationInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]AutomationInput, len(f.createAutomationCalls))
	copy(res, f.createAutomationCalls)
	return res
}

// UpdateAutomationCalls 获取所有 UpdateAutomation 调用记录。
func (f *FakeClient) UpdateAutomationCalls() []AutomationInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]AutomationInput, len(f.updateAutomationCalls))
	copy(res, f.updateAutomationCalls)
	return res
}

// RunAutomationCalls 获取所有 RunAutomation 调用的自动化 ID。
func (f *FakeClient) RunAutomationCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.runAutomationCalls))
	copy(res, f.runAutomationCalls)
	return res
}

// DeleteAutomationCalls 获取所有 DeleteAutomation 调用的自动化 ID。
func (f *FakeClient) DeleteAutomationCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.deleteAutomationCalls))
	copy(res, f.deleteAutomationCalls)
	return res
}

// SetAutomationSchedulerStatus 设置假客户端返回的调度锁状态。
func (f *FakeClient) SetAutomationSchedulerStatus(st AutomationSchedulerStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedulerStatus = st
}

// SetAutomationSchedulerStatusError 设置调度锁状态查询返回的错误。
func (f *FakeClient) SetAutomationSchedulerStatusError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedulerStatusErr = err
}

// SetAutomationRunError 设置 RunAutomation 返回的错误。
func (f *FakeClient) SetAutomationRunError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runAutomationErr = err
}

// AutomationSchedulerStatusCalls 获取 AutomationSchedulerStatus 的调用次数。
func (f *FakeClient) AutomationSchedulerStatusCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.schedulerStatusCalls
}

// KanbanBoard 返回已设置看板中按 projectID 过滤后的副本。
func (f *FakeClient) KanbanBoard(ctx context.Context, projectID string) (KanbanBoard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kanbanBoardCalls++
	if f.kanbanErr != nil {
		return KanbanBoard{}, f.kanbanErr
	}
	var tasks []KanbanTask
	for _, t := range f.kanbanBoard.Tasks {
		if projectID == "" || t.ProjectID == projectID {
			tasks = append(tasks, t)
		}
	}
	var goals []KanbanGoal
	for _, g := range f.kanbanBoard.Goals {
		if projectID == "" || g.ProjectID == projectID {
			goals = append(goals, g)
		}
	}
	return KanbanBoard{
		Revision: f.kanbanBoard.Revision,
		Tasks:    tasks,
		Goals:    goals,
	}, nil
}

// CreateTask 往看板追加一个 Column 为 todo、Revision 为 1、CanStart 和 CanMove 为 true 的任务。
func (f *FakeClient) CreateTask(ctx context.Context, projectID, title, notes string) (KanbanTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	taskID := fmt.Sprintf("task-%d", len(f.kanbanBoard.Tasks)+1)
	task := KanbanTask{
		ID:        taskID,
		ProjectID: projectID,
		Title:     title,
		Notes:     notes,
		Status:    "todo",
		Column:    "todo",
		Revision:  1,
		CanStart:  true,
		CanMove:   true,
		UpdatedAt: time.Now(),
	}
	f.createTaskCalls = append(f.createTaskCalls, KanbanTask{
		ProjectID: projectID,
		Title:     title,
		Notes:     notes,
	})
	f.kanbanBoard.Tasks = append(f.kanbanBoard.Tasks, task)
	return task, nil
}

// MoveTask 移动任务状态，有 moveTaskErr 时只记录调用并返回该错误，否则把看板中对应任务的 Status 和 Column 改成 status，Revision 加 1。
func (f *FakeClient) MoveTask(ctx context.Context, taskID, status string, revision int) (KanbanTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moveTaskCalls = append(f.moveTaskCalls, MoveTaskCall{
		TaskID:   taskID,
		Status:   status,
		Revision: revision,
	})
	if f.moveTaskErr != nil {
		return KanbanTask{}, f.moveTaskErr
	}
	for i := range f.kanbanBoard.Tasks {
		if f.kanbanBoard.Tasks[i].ID == taskID {
			f.kanbanBoard.Tasks[i].Status = status
			f.kanbanBoard.Tasks[i].Column = status
			f.kanbanBoard.Tasks[i].Revision++
			return f.kanbanBoard.Tasks[i], nil
		}
	}
	return KanbanTask{ID: taskID, Status: status, Column: status, Revision: revision + 1}, nil
}

// StartTask 开始执行指定任务。
func (f *FakeClient) StartTask(ctx context.Context, taskID string, revision int) (KanbanTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startTaskCalls = append(f.startTaskCalls, taskID)
	for i := range f.kanbanBoard.Tasks {
		if f.kanbanBoard.Tasks[i].ID == taskID {
			f.kanbanBoard.Tasks[i].Status = "in_progress"
			f.kanbanBoard.Tasks[i].Column = "in_progress"
			f.kanbanBoard.Tasks[i].Revision++
			return f.kanbanBoard.Tasks[i], nil
		}
	}
	return KanbanTask{ID: taskID, Status: "in_progress", Column: "in_progress", Revision: revision + 1}, nil
}

// SetReviewTaskError 设置 ReviewTask 返回的模拟错误。
func (f *FakeClient) SetReviewTaskError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewTaskErr = err
}

// ReviewTask 评审任务并记录调用。
func (f *FakeClient) ReviewTask(ctx context.Context, taskID, decision, feedback string, revision int) (KanbanTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewTaskCalls = append(f.reviewTaskCalls, ReviewCall{
		TaskID:   taskID,
		Decision: decision,
		Feedback: feedback,
		Revision: revision,
	})
	if f.reviewTaskErr != nil {
		return KanbanTask{}, f.reviewTaskErr
	}
	status := "done"
	if decision == "reject" {
		status = "todo"
	}
	for i := range f.kanbanBoard.Tasks {
		if f.kanbanBoard.Tasks[i].ID == taskID {
			f.kanbanBoard.Tasks[i].Status = status
			f.kanbanBoard.Tasks[i].Column = status
			f.kanbanBoard.Tasks[i].Revision++
			return f.kanbanBoard.Tasks[i], nil
		}
	}
	return KanbanTask{ID: taskID, Status: status, Column: status, Revision: revision + 1}, nil
}

// CreateGoal 在指定项目下创建新目标。
func (f *FakeClient) CreateGoal(ctx context.Context, projectID, prompt string) (KanbanGoal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	goalID := fmt.Sprintf("goal-%d", len(f.kanbanBoard.Goals)+1)
	goal := KanbanGoal{
		ID:        goalID,
		ProjectID: projectID,
		Title:     prompt,
		Prompt:    prompt,
		Status:    "planning",
		Revision:  1,
		UpdatedAt: time.Now(),
	}
	f.kanbanBoard.Goals = append(f.kanbanBoard.Goals, goal)
	return goal, nil
}

// StartGoal 开始执行目标并记录调用。
func (f *FakeClient) StartGoal(ctx context.Context, goalID string, revision int) (KanbanGoal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goalCalls = append(f.goalCalls, GoalCall{GoalID: goalID, Action: "start", Revision: revision})
	for i := range f.kanbanBoard.Goals {
		if f.kanbanBoard.Goals[i].ID == goalID {
			f.kanbanBoard.Goals[i].Status = "running"
			f.kanbanBoard.Goals[i].Revision++
			return f.kanbanBoard.Goals[i], nil
		}
	}
	return KanbanGoal{ID: goalID, Status: "running", Revision: revision + 1}, nil
}

// PauseGoal 暂停目标执行并记录调用。
func (f *FakeClient) PauseGoal(ctx context.Context, goalID string, revision int) (KanbanGoal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goalCalls = append(f.goalCalls, GoalCall{GoalID: goalID, Action: "pause", Revision: revision})
	for i := range f.kanbanBoard.Goals {
		if f.kanbanBoard.Goals[i].ID == goalID {
			f.kanbanBoard.Goals[i].Status = "paused"
			f.kanbanBoard.Goals[i].Revision++
			return f.kanbanBoard.Goals[i], nil
		}
	}
	return KanbanGoal{ID: goalID, Status: "paused", Revision: revision + 1}, nil
}

// CancelGoal 取消目标并记录调用。
func (f *FakeClient) CancelGoal(ctx context.Context, goalID string, revision int) (KanbanGoal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goalCalls = append(f.goalCalls, GoalCall{GoalID: goalID, Action: "cancel", Revision: revision})
	for i := range f.kanbanBoard.Goals {
		if f.kanbanBoard.Goals[i].ID == goalID {
			f.kanbanBoard.Goals[i].Status = "cancelled"
			f.kanbanBoard.Goals[i].Revision++
			return f.kanbanBoard.Goals[i], nil
		}
	}
	return KanbanGoal{ID: goalID, Status: "cancelled", Revision: revision + 1}, nil
}

// ReplanGoal 重新规划目标并记录调用。
func (f *FakeClient) ReplanGoal(ctx context.Context, goalID, feedback string, revision int) (KanbanGoal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goalCalls = append(f.goalCalls, GoalCall{GoalID: goalID, Action: "replan", Feedback: feedback, Revision: revision})
	for i := range f.kanbanBoard.Goals {
		if f.kanbanBoard.Goals[i].ID == goalID {
			f.kanbanBoard.Goals[i].Status = "planning"
			f.kanbanBoard.Goals[i].Revision++
			return f.kanbanBoard.Goals[i], nil
		}
	}
	return KanbanGoal{ID: goalID, Status: "planning", Revision: revision + 1}, nil
}

// WatchKanban 监听看板变更。
func (f *FakeClient) WatchKanban(onChange func()) (stop func()) {
	f.mu.Lock()
	f.nextKanbanWatcherID++
	id := f.nextKanbanWatcherID
	if f.kanbanWatchers == nil {
		f.kanbanWatchers = make(map[int]func())
	}
	f.kanbanWatchers[id] = onChange
	f.mu.Unlock()

	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.kanbanWatchers, id)
	}
}

// Automations 获取指定项目的自动化列表，按 ProjectID 过滤。
func (f *FakeClient) Automations(ctx context.Context, projectID string) ([]Automation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res []Automation
	for _, a := range f.automations {
		if projectID == "" || a.ProjectID == projectID {
			res = append(res, a)
		}
	}
	return res, nil
}

// CreateAutomation 创建自动化任务并追加到列表。
func (f *FakeClient) CreateAutomation(ctx context.Context, in AutomationInput) (Automation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createAutomationCalls = append(f.createAutomationCalls, in)
	catchUp := true
	if in.CatchUp != nil {
		catchUp = *in.CatchUp
	}
	auto := Automation{
		ID:                     fmt.Sprintf("auto-%d", len(f.automations)+1),
		ProjectID:              in.ProjectID,
		Name:                   in.Name,
		Prompt:                 in.Prompt,
		Status:                 "active",
		StopWhen:               in.StopWhen,
		Schedule:               in.Schedule,
		Enabled:                in.Enabled,
		MaxConsecutiveFailures: in.MaxConsecutiveFailures,
		MaxIterations:          in.MaxRuns,
		MaxRuns:                in.MaxRuns,
		CatchUp:                catchUp,
		Revision:               1,
	}
	f.automations = append(f.automations, auto)
	return auto, nil
}

// UpdateAutomation 更新自动化任务。
func (f *FakeClient) UpdateAutomation(ctx context.Context, id string, in AutomationInput, revision int) (Automation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateAutomationCalls = append(f.updateAutomationCalls, in)
	f.updateAutomationRevisions = append(f.updateAutomationRevisions, revision)
	catchUp := true
	if in.CatchUp != nil {
		catchUp = *in.CatchUp
	}
	for i := range f.automations {
		if f.automations[i].ID == id {
			if f.automations[i].Revision != revision {
				return Automation{}, ErrRevisionConflict
			}
			f.automations[i].Name = in.Name
			f.automations[i].Prompt = in.Prompt
			f.automations[i].StopWhen = in.StopWhen
			f.automations[i].Schedule = in.Schedule
			f.automations[i].Enabled = in.Enabled
			f.automations[i].MaxConsecutiveFailures = in.MaxConsecutiveFailures
			f.automations[i].MaxIterations = in.MaxRuns
			f.automations[i].MaxRuns = in.MaxRuns
			f.automations[i].CatchUp = catchUp
			f.automations[i].Revision++
			return f.automations[i], nil
		}
	}
	auto := Automation{
		ID:                     id,
		ProjectID:              in.ProjectID,
		Name:                   in.Name,
		Prompt:                 in.Prompt,
		Schedule:               in.Schedule,
		Enabled:                in.Enabled,
		MaxConsecutiveFailures: in.MaxConsecutiveFailures,
		MaxIterations:          in.MaxRuns,
		MaxRuns:                in.MaxRuns,
		CatchUp:                catchUp,
		Revision:               revision + 1,
	}
	f.automations = append(f.automations, auto)
	return auto, nil
}

// DeleteAutomation 删除自动化任务。
func (f *FakeClient) DeleteAutomation(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteAutomationCalls = append(f.deleteAutomationCalls, id)
	filtered := f.automations[:0]
	for _, a := range f.automations {
		if a.ID != id {
			filtered = append(filtered, a)
		}
	}
	f.automations = filtered
	return nil
}

// RunAutomation 立即触发运行一次自动化任务。
func (f *FakeClient) RunAutomation(ctx context.Context, id string) (AutomationRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runAutomationCalls = append(f.runAutomationCalls, id)
	if f.runAutomationErr != nil {
		return AutomationRun{}, f.runAutomationErr
	}
	run := AutomationRun{
		ID:           fmt.Sprintf("run-%d", len(f.runAutomationCalls)),
		AutomationID: id,
		Status:       "running",
		Trigger:      "manual",
		StartedAt:    time.Now(),
	}
	if f.automationRuns == nil {
		f.automationRuns = make(map[string][]AutomationRun)
	}
	f.automationRuns[id] = append(f.automationRuns[id], run)
	return run, nil
}

// AutomationRuns 获取指定自动化任务的历史运行记录。
func (f *FakeClient) AutomationRuns(ctx context.Context, id string, limit int) ([]AutomationRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	runs := f.automationRuns[id]
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	res := make([]AutomationRun, len(runs))
	copy(res, runs)
	return res, nil
}

// AutomationSchedulerStatus 返回假客户端设置的调度锁状态。
func (f *FakeClient) AutomationSchedulerStatus(ctx context.Context) (AutomationSchedulerStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedulerStatusCalls++
	return f.schedulerStatus, f.schedulerStatusErr
}

// SetSwarmState 设置指定 leadThreadID 的 Swarm 状态。
func (f *FakeClient) SetSwarmState(lead string, st SwarmState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.swarmStates == nil {
		f.swarmStates = make(map[string]SwarmState)
	}
	f.swarmStates[lead] = st
}

// SetSwarmError 设置 Swarm 调用时返回的模拟错误。
func (f *FakeClient) SetSwarmError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swarmErr = err
}

// SetSwarmMergeDiff 设置指定 bee 的模拟合并 diff。
func (f *FakeClient) SetSwarmMergeDiff(beeID string, d ThreadDiff) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.swarmMergeDiffs == nil {
		f.swarmMergeDiffs = make(map[string]ThreadDiff)
	}
	f.swarmMergeDiffs[beeID] = d
}

// SetSwarmMergeDiffError 设置 SwarmMergeDiff 返回的模拟错误。
func (f *FakeClient) SetSwarmMergeDiffError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swarmMergeDiffErr = err
}

// TriggerSwarmChange 触发 Swarm 状态变更通知。
func (f *FakeClient) TriggerSwarmChange(lead string) {
	f.mu.Lock()
	var callbacks []func(string)
	for _, fn := range f.swarmWatchers {
		callbacks = append(callbacks, fn)
	}
	f.mu.Unlock()
	for _, fn := range callbacks {
		fn(lead)
	}
}

// StoppedBees 获取所有被停止的 beeThreadID 记录。
func (f *FakeClient) StoppedBees() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.stoppedBees))
	copy(res, f.stoppedBees)
	return res
}

// StopAllCalls 获取所有 StopAllSwarmBees 调用的 leadThreadID 列表。
func (f *FakeClient) StopAllCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.stopAllCalls))
	copy(res, f.stopAllCalls)
	return res
}

// ClearCalls 获取所有 ClearFinishedSwarmBees 调用的 leadThreadID 列表。
func (f *FakeClient) ClearCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.clearCalls))
	copy(res, f.clearCalls)
	return res
}

// ApproveMergeCalls 获取所有 ApproveSwarmMerge 调用的 beeThreadID 列表。
func (f *FakeClient) ApproveMergeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.approveMergeCalls))
	copy(res, f.approveMergeCalls)
	return res
}

// DiscardMergeCalls 获取所有 DiscardSwarmMerge 调用的 beeThreadID 列表。
func (f *FakeClient) DiscardMergeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.discardMergeCalls))
	copy(res, f.discardMergeCalls)
	return res
}

// Swarm 获取指定会话的 Swarm 状态。
func (f *FakeClient) Swarm(ctx context.Context, threadID string) (SwarmState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.swarmErr != nil {
		return SwarmState{}, f.swarmErr
	}
	if f.swarmStates != nil {
		if st, ok := f.swarmStates[threadID]; ok {
			return st, nil
		}
	}
	return SwarmState{
		LeadThreadID: threadID,
		Available:    true,
	}, nil
}

// SetSwarmEnabled 设置会话是否开启 Swarm，并确保 Available 为 true。
func (f *FakeClient) SetSwarmEnabled(ctx context.Context, threadID string, enabled bool) (SwarmState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setSwarmEnabledCalls = append(f.setSwarmEnabledCalls, struct {
		ThreadID string
		Enabled  bool
	}{ThreadID: threadID, Enabled: enabled})
	if f.swarmErr != nil {
		return SwarmState{}, f.swarmErr
	}
	if f.swarmStates == nil {
		f.swarmStates = make(map[string]SwarmState)
	}
	st := f.swarmStates[threadID]
	st.LeadThreadID = threadID
	st.Available = true
	st.Enabled = enabled
	f.swarmStates[threadID] = st
	return st, nil
}

// SetSwarmPlansOnly 设置会话是否开启 Swarm 只规划开关覆盖。
func (f *FakeClient) SetSwarmPlansOnly(ctx context.Context, threadID string, plansOnly bool) (SwarmState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.swarmErr != nil {
		return SwarmState{}, f.swarmErr
	}
	if f.swarmStates == nil {
		f.swarmStates = make(map[string]SwarmState)
	}
	st := f.swarmStates[threadID]
	st.LeadThreadID = threadID
	st.Available = true
	st.PlansOnly = plansOnly
	st.PlansOnlyOverridden = true
	f.swarmStates[threadID] = st
	return st, nil
}

// StopSwarmBee 停止指定的 Swarm bee。
func (f *FakeClient) StopSwarmBee(ctx context.Context, leadThreadID, beeThreadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stoppedBees = append(f.stoppedBees, beeThreadID)
	if f.swarmErr != nil {
		return f.swarmErr
	}
	if f.swarmStates != nil {
		if st, ok := f.swarmStates[leadThreadID]; ok {
			for i := range st.Bees {
				if st.Bees[i].ThreadID == beeThreadID {
					st.Bees[i].Status = "stopped"
				}
			}
			f.swarmStates[leadThreadID] = st
		}
	}
	return nil
}

// StopAllSwarmBees 停止所有运行中的 Swarm bee。
func (f *FakeClient) StopAllSwarmBees(ctx context.Context, leadThreadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopAllCalls = append(f.stopAllCalls, leadThreadID)
	if f.swarmErr != nil {
		return f.swarmErr
	}
	if f.swarmStates != nil {
		if st, ok := f.swarmStates[leadThreadID]; ok {
			for i := range st.Bees {
				if st.Bees[i].Status == "running" {
					st.Bees[i].Status = "stopped"
				}
			}
			f.swarmStates[leadThreadID] = st
		}
	}
	return nil
}

// ClearFinishedSwarmBees 清理已完成或已停止的非 running 状态 bee。
func (f *FakeClient) ClearFinishedSwarmBees(ctx context.Context, leadThreadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalls = append(f.clearCalls, leadThreadID)
	if f.swarmErr != nil {
		return f.swarmErr
	}
	if f.swarmStates != nil {
		if st, ok := f.swarmStates[leadThreadID]; ok {
			var active []SwarmBee
			for _, h := range st.Bees {
				if h.Status == "running" {
					active = append(active, h)
				}
			}
			st.Bees = active
			f.swarmStates[leadThreadID] = st
		}
	}
	return nil
}

// ForwardBeeQuestion 记录把子线程提问转交主线程回答的调用。
func (f *FakeClient) ForwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forwardQuestionCalls = append(f.forwardQuestionCalls, ForwardQuestionCall{
		LeadThreadID: leadThreadID,
		BeeThreadID:  beeThreadID,
		RequestID:    requestID,
	})
	return f.swarmErr
}

// ForwardQuestionCalls 获取所有 ForwardBeeQuestion 调用参数。
func (f *FakeClient) ForwardQuestionCalls() []ForwardQuestionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]ForwardQuestionCall, len(f.forwardQuestionCalls))
	copy(res, f.forwardQuestionCalls)
	return res
}

// SetSwarmReportSummary 设置指定 bee 的模拟存档汇报全文。
func (f *FakeClient) SetSwarmReportSummary(beeID, summary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.swarmReports == nil {
		f.swarmReports = make(map[string]string)
	}
	f.swarmReports[beeID] = summary
}

// SwarmReportSummary 返回模拟的存档汇报全文，未设置时返回错误。
func (f *FakeClient) SwarmReportSummary(ctx context.Context, leadThreadID, beeID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if text, ok := f.swarmReports[beeID]; ok {
		return text, nil
	}
	return "", errors.New("swarm report not found")
}

// SwarmMergeDiff 获取 bee 待合并改动。
func (f *FakeClient) SwarmMergeDiff(ctx context.Context, leadThreadID, beeThreadID string) (ThreadDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.swarmErr != nil {
		return ThreadDiff{}, f.swarmErr
	}
	if f.swarmMergeDiffErr != nil {
		return ThreadDiff{}, f.swarmMergeDiffErr
	}
	if f.swarmMergeDiffs != nil {
		if d, ok := f.swarmMergeDiffs[beeThreadID]; ok {
			return d, nil
		}
	}
	return ThreadDiff{}, nil
}

// ApproveSwarmMerge 批准并应用 bee 的改动。
func (f *FakeClient) ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approveMergeCalls = append(f.approveMergeCalls, beeThreadID)
	if f.swarmErr != nil {
		return f.swarmErr
	}
	if f.swarmStates != nil {
		for id, st := range f.swarmStates {
			changed := false
			for i := range st.Bees {
				if st.Bees[i].ThreadID == beeThreadID {
					st.Bees[i].PendingMerge = false
					st.Bees[i].Merged = true
					changed = true
				}
			}
			if changed {
				f.swarmStates[id] = st
			}
		}
	}
	return nil
}

// DiscardSwarmMerge 放弃 bee 的改动。
func (f *FakeClient) DiscardSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discardMergeCalls = append(f.discardMergeCalls, beeThreadID)
	if f.swarmErr != nil {
		return f.swarmErr
	}
	if f.swarmStates != nil {
		for id, st := range f.swarmStates {
			changed := false
			for i := range st.Bees {
				if st.Bees[i].ThreadID == beeThreadID {
					st.Bees[i].PendingMerge = false
					st.Bees[i].MergeDiscarded = true
					changed = true
				}
			}
			if changed {
				f.swarmStates[id] = st
			}
		}
	}
	return nil
}

// WatchSwarm 监听 Swarm 状态变更。
func (f *FakeClient) WatchSwarm(onChange func(leadThreadID string)) (stop func()) {
	f.mu.Lock()
	f.nextSwarmWatcherID++
	id := f.nextSwarmWatcherID
	if f.swarmWatchers == nil {
		f.swarmWatchers = make(map[int]func(string))
	}
	f.swarmWatchers[id] = onChange
	f.mu.Unlock()

	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.swarmWatchers, id)
	}
}

// MCPSignIn 执行模拟的 MCP 登录授权。
func (f *FakeClient) MCPSignIn(ctx context.Context, serverID, serverURL string) error {
	return f.MCPSignInWithAuth(ctx, serverID, serverURL, nil)
}

// MCPSignInWithAuth 执行模拟的 MCP 登录授权，并记录目录声明的登录方式。
func (f *FakeClient) MCPSignInWithAuth(ctx context.Context, serverID, serverURL string, auth *agentconf.MCPCatalogAuth) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mcpSignedIn == nil {
		f.mcpSignedIn = make(map[string]bool)
	}
	f.mcpSignInCalls = append(f.mcpSignInCalls, serverID)
	f.mcpSignInAuth = cloneTestCatalogAuth(auth)
	if f.mcpSignInErr != nil {
		return f.mcpSignInErr
	}
	f.mcpSignedIn[serverID] = true
	return nil
}

// MCPSignOut 执行模拟的 MCP 退出授权。
func (f *FakeClient) MCPSignOut(serverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mcpSignedIn == nil {
		f.mcpSignedIn = make(map[string]bool)
	}
	f.mcpSignOutCalls = append(f.mcpSignOutCalls, serverID)
	if f.mcpSignOutErr != nil {
		return f.mcpSignOutErr
	}
	delete(f.mcpSignedIn, serverID)
	return nil
}

// MCPIsSignedIn 查询模拟的 MCP 是否已登录。
func (f *FakeClient) MCPIsSignedIn(serverID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mcpSignedIn == nil {
		return false
	}
	return f.mcpSignedIn[serverID]
}

// cloneTestCatalogAuth 深拷贝登录声明，让测试记录的是登录时刻的快照。
func cloneTestCatalogAuth(auth *agentconf.MCPCatalogAuth) *agentconf.MCPCatalogAuth {
	if auth == nil {
		return nil
	}
	cloned := *auth
	cloned.Scopes = append([]string(nil), auth.Scopes...)
	return &cloned
}

// AgyAccounts 获取模拟的所有 agy 账号列表。
func (f *FakeClient) AgyAccounts() ([]accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agyAccountsErr != nil {
		return nil, f.agyAccountsErr
	}
	res := make([]accounts.Account, len(f.agyAccounts))
	copy(res, f.agyAccounts)
	return res, nil
}

// AgyDefaultAccountID 获取模拟的当前默认 agy 账号 ID。
func (f *FakeClient) AgyDefaultAccountID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agyDefaultAccountID == "" {
		return accounts.SystemAccountID
	}
	return f.agyDefaultAccountID
}

// AgyLoginStart 模拟发起浏览器 OAuth 流程。
func (f *FakeClient) AgyLoginStart(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agyLoginStartErr != nil {
		return "", f.agyLoginStartErr
	}
	if f.agyLoginURL == "" {
		return "https://accounts.google.com/o/oauth2/auth?mock=1", nil
	}
	return f.agyLoginURL, nil
}

// AgyLoginSubmit 模拟提交授权码或回调。
func (f *FakeClient) AgyLoginSubmit(input string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agyLoginSubmitCalls = append(f.agyLoginSubmitCalls, input)
	return f.agyLoginSubmitErr
}

// AgyLoginWait 模拟等待浏览器回调完成。
func (f *FakeClient) AgyLoginWait(ctx context.Context) (accounts.Account, error) {
	f.mu.Lock()
	done := f.agyLoginWaitDone
	f.mu.Unlock()
	if done != nil {
		select {
		case <-ctx.Done():
			return accounts.Account{}, ctx.Err()
		case <-done:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agyLoginWaitErr != nil {
		return accounts.Account{}, f.agyLoginWaitErr
	}
	return f.agyLoginWaitAccount, nil
}

// AgyLoginCancel 模拟取消登录流程。
func (f *FakeClient) AgyLoginCancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agyLoginCancelCalls++
}

// AgyTerminalLogin 模拟生成终端隔离登录参数。
func (f *FakeClient) AgyTerminalLogin() (id string, env []string, args []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agyTerminalLoginErr != nil {
		return "", nil, nil, f.agyTerminalLoginErr
	}
	id = f.agyTerminalID
	if id == "" {
		id = "term-mock-id"
	}
	env = append([]string(nil), f.agyTerminalEnv...)
	args = append([]string(nil), f.agyTerminalArgs...)
	return id, env, args, nil
}

// AgyAdoptTerminalLogin 模拟接纳终端登录凭证。
func (f *FakeClient) AgyAdoptTerminalLogin(ctx context.Context, id string) (accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agyAdoptCalls = append(f.agyAdoptCalls, id)
	if f.agyAdoptErr != nil {
		return accounts.Account{}, f.agyAdoptErr
	}
	return f.agyAdoptAccount, nil
}

// AgyRemove 模拟删除指定 agy 账号。
func (f *FakeClient) AgyRemove(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agyRemovedIDs = append(f.agyRemovedIDs, id)
	if f.agyRemoveErr != nil {
		return f.agyRemoveErr
	}
	var kept []accounts.Account
	for _, a := range f.agyAccounts {
		if a.ID != id {
			kept = append(kept, a)
		}
	}
	f.agyAccounts = kept
	return nil
}

// AgyBoundConversations 返回模拟的绑定在指定账号上的会话 ID 列表。
func (f *FakeClient) AgyBoundConversations(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agyBoundConversations == nil {
		return nil
	}
	res := make([]string, len(f.agyBoundConversations[id]))
	copy(res, f.agyBoundConversations[id])
	return res
}

// AgySetDefault 模拟设置默认账号 ID。
func (f *FakeClient) AgySetDefault(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agySetDefaultIDs = append(f.agySetDefaultIDs, id)
	if f.agySetDefaultErr != nil {
		return f.agySetDefaultErr
	}
	f.agyDefaultAccountID = id
	return nil
}

// AgyUpdate 模拟更新账号配置。
func (f *FakeClient) AgyUpdate(id string, p accounts.UpdatePatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agyUpdateCalls = append(f.agyUpdateCalls, struct {
		ID    string
		Patch accounts.UpdatePatch
	}{ID: id, Patch: p})
	if f.agyUpdateErr != nil {
		return f.agyUpdateErr
	}
	for i := range f.agyAccounts {
		if f.agyAccounts[i].ID == id {
			if p.Label != nil {
				f.agyAccounts[i].Label = *p.Label
			}
			if p.On != nil {
				f.agyAccounts[i].On = *p.On
			}
			if p.Order != nil {
				f.agyAccounts[i].Order = *p.Order
			}
			if p.Weight != nil {
				f.agyAccounts[i].Weight = *p.Weight
			}
			break
		}
	}
	return nil
}

// AgyRouting 模拟获取当前路由调度配置。
func (f *FakeClient) AgyRouting() (multiaccount.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agyRoutingErr != nil {
		return multiaccount.Config{}, f.agyRoutingErr
	}
	return f.agyRoutingConfig, nil
}

// AgySetRouting 模拟更新路由调度配置。
func (f *FakeClient) AgySetRouting(cfg multiaccount.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agySetRoutingCalls = append(f.agySetRoutingCalls, cfg)
	if f.agySetRoutingErr != nil {
		return f.agySetRoutingErr
	}
	f.agyRoutingConfig = cfg
	return nil
}

// AgyRefreshQuota 模拟刷新账号额度快照。
func (f *FakeClient) AgyRefreshQuota(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agyRefreshCalls = append(f.agyRefreshCalls, id)
	return f.agyRefreshQuotaErr
}

// AgyWarmup 模拟执行 Gemini 预热。
func (f *FakeClient) AgyWarmup(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agyWarmupCalls = append(f.agyWarmupCalls, id)
	return f.agyWarmupErr
}

// AgyAutoWarmup 模拟获取自动预热开关状态。
func (f *FakeClient) AgyAutoWarmup() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agyAutoWarmup, f.agyAutoWarmupErr
}

// AgySetAutoWarmup 模拟设置自动预热开关状态。
func (f *FakeClient) AgySetAutoWarmup(enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agySetAutoWarmupErr != nil {
		return f.agySetAutoWarmupErr
	}
	f.agyAutoWarmup = enabled
	return nil
}

// GitState 模拟获取 Git 状态，gitErr 非空时返回错误。
func (f *FakeClient) GitState(ctx context.Context, threadID string) (GitState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gitErr != nil {
		return GitState{}, f.gitErr
	}
	st := f.gitState
	st.Branches = append([]string(nil), f.gitState.Branches...)
	st.Worktrees = append([]GitWorktree(nil), f.gitState.Worktrees...)
	return st, nil
}

// GitCheckout 模拟切换分支，成功时同步更新 gitState 的当前分支。
func (f *FakeClient) GitCheckout(ctx context.Context, threadID, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitCheckoutCalls = append(f.gitCheckoutCalls, branch)
	if f.gitErr != nil {
		return f.gitErr
	}
	f.gitState.Branch = branch
	f.gitState.Detached = false
	return nil
}

// GitSwitchWorktree 模拟切换 worktree，成功时同步更新当前项。
func (f *FakeClient) GitSwitchWorktree(ctx context.Context, threadID, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitWorktreeCalls = append(f.gitWorktreeCalls, path)
	if f.gitErr != nil {
		return f.gitErr
	}
	for i := range f.gitState.Worktrees {
		f.gitState.Worktrees[i].Current = f.gitState.Worktrees[i].Path == path
		if f.gitState.Worktrees[i].Current {
			f.gitState.WorkDir = path
			f.gitState.Branch = f.gitState.Worktrees[i].Branch
		}
	}
	return nil
}

// GitCommit 模拟提交，成功时清零未提交变更数。
func (f *FakeClient) GitCommit(ctx context.Context, threadID, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitCommitMessages = append(f.gitCommitMessages, message)
	if f.gitErr != nil {
		return f.gitErr
	}
	f.gitState.Changes = 0
	return nil
}

// GitChangedFiles 返回模拟的改动文件列表。
func (f *FakeClient) GitChangedFiles(ctx context.Context, threadID string) ([]GitChangedFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gitErr != nil {
		return nil, f.gitErr
	}
	return append([]GitChangedFile(nil), f.gitChangedFiles...), nil
}

// GitCommitSelected 模拟只提交勾选的路径，成功时清空未提交变更数与改动列表。
func (f *FakeClient) GitCommitSelected(ctx context.Context, threadID string, paths []string, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitCommitSelectedCalls = append(f.gitCommitSelectedCalls, gitSelectedCommitCall{
		Paths:   append([]string(nil), paths...),
		Message: message,
	})
	if f.gitErr != nil {
		return f.gitErr
	}
	f.gitState.Changes = 0
	f.gitChangedFiles = nil
	return nil
}

// GitPush 模拟推送并记录次数。
func (f *FakeClient) GitPush(ctx context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitPushCalls++
	return f.gitErr
}

// GitCreateBranch 模拟创建并切换分支。
func (f *FakeClient) GitCreateBranch(ctx context.Context, threadID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitCreateBranchCalls = append(f.gitCreateBranchCalls, [2]string{threadID, name})
	if f.gitErr != nil {
		return f.gitErr
	}
	f.gitState.Branch = name
	f.gitState.Branches = append(f.gitState.Branches, name)
	return nil
}

// GitCreateBranchCalls 返回 GitCreateBranch 的调用记录。
func (f *FakeClient) GitCreateBranchCalls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([][2]string, len(f.gitCreateBranchCalls))
	copy(res, f.gitCreateBranchCalls)
	return res
}

// GitFetch 模拟抓取远程分支。
func (f *FakeClient) GitFetch(ctx context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitOpCalls = append(f.gitOpCalls, "GitFetch")
	return f.gitErr
}

// GitPull 模拟快进拉取：成功后清零落后提交数。
func (f *FakeClient) GitPull(ctx context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitOpCalls = append(f.gitOpCalls, "GitPull")
	if f.gitErr != nil {
		return f.gitErr
	}
	f.gitState.Behind = 0
	f.gitState.CanFastForward = false
	return nil
}

// GitFastForwardInto 模拟把当前分支快进到 branch。
func (f *FakeClient) GitFastForwardInto(ctx context.Context, threadID, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitOpCalls = append(f.gitOpCalls, "GitFastForwardInto:"+branch)
	return f.gitErr
}

// GitMergeFrom 模拟把 branch 合并进当前分支。
func (f *FakeClient) GitMergeFrom(ctx context.Context, threadID, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitOpCalls = append(f.gitOpCalls, "GitMergeFrom:"+branch)
	return f.gitErr
}

// GitStash 模拟贮藏改动：成功后变更数归零并标记有贮藏。
func (f *FakeClient) GitStash(ctx context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitOpCalls = append(f.gitOpCalls, "GitStash")
	if f.gitErr != nil {
		return f.gitErr
	}
	f.gitState.Changes = 0
	f.gitState.ChangedFiles = 0
	f.gitState.HasStash = true
	return nil
}

// GitStashPop 模拟恢复最近一条贮藏：恢复后不再有贮藏。
func (f *FakeClient) GitStashPop(ctx context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitOpCalls = append(f.gitOpCalls, "GitStashPop")
	if f.gitErr != nil {
		return f.gitErr
	}
	f.gitState.HasStash = false
	return nil
}

// SetGenerateCommitMessageResult 配置 GenerateCommitMessage 返回的结果与错误。
func (f *FakeClient) SetGenerateCommitMessageResult(msg string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gitGenCommitMsgResult = msg
	f.gitGenCommitMsgErr = err
}

// GenerateCommitMessage 模拟生成 Git 提交信息。
func (f *FakeClient) GenerateCommitMessage(ctx context.Context, threadID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gitGenCommitMsgErr != nil {
		return "", f.gitGenCommitMsgErr
	}
	if f.gitGenCommitMsgResult != "" {
		return f.gitGenCommitMsgResult, nil
	}
	return "feat: 桩实现生成的提交信息", nil
}

// SetBranchResult 配置 BranchThread 返回的新会话 ID 与错误；id 为空时自动生成。
func (f *FakeClient) SetBranchResult(id string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branchID = id
	f.branchErr = err
}

// BranchThread 模拟分支会话：记录调用参数，成功时把新会话加入源会话所在项目。
func (f *FakeClient) BranchThread(ctx context.Context, threadID, messageID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.BranchCalls = append(f.BranchCalls, [2]string{threadID, messageID})
	if f.branchErr != nil {
		return "", f.branchErr
	}
	newID := f.branchID
	if newID == "" {
		f.nextThreadSeq++
		newID = fmt.Sprintf("t-branch-%d", f.nextThreadSeq)
	}
	for projectID, list := range f.threads {
		for _, th := range list {
			if th.ID != threadID {
				continue
			}
			f.threads[projectID] = append(f.threads[projectID], Thread{
				ID:        newID,
				ProjectID: projectID,
				Title:     th.Title + " (分支)",
				Provider:  th.Provider,
				Model:     th.Model,
				UpdatedAt: time.Now(),
			})
			return newID, nil
		}
	}
	return newID, nil
}

// SetForwardResult 配置 ForwardThread 返回的新会话 ID 与错误；id 为空时自动生成。
func (f *FakeClient) SetForwardResult(id string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forwardID = id
	f.forwardErr = err
}

// ForwardThread 模拟转发会话：记录调用参数，成功时把新会话加入源会话所在项目。
func (f *FakeClient) ForwardThread(ctx context.Context, threadID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ForwardCalls = append(f.ForwardCalls, threadID)
	if f.forwardErr != nil {
		return "", f.forwardErr
	}
	newID := f.forwardID
	if newID == "" {
		f.nextThreadSeq++
		newID = fmt.Sprintf("t-forward-%d", f.nextThreadSeq)
	}
	for projectID, list := range f.threads {
		for _, th := range list {
			if th.ID != threadID {
				continue
			}
			f.threads[projectID] = append(f.threads[projectID], Thread{
				ID:        newID,
				ProjectID: projectID,
				Title:     th.Title + " (转发)",
				Provider:  th.Provider,
				Model:     th.Model,
				UpdatedAt: time.Now(),
			})
			return newID, nil
		}
	}
	return newID, nil
}

// ThreadForwardedContext 模拟读取转发会话带入的历史上下文；没有配置的会话返回不可用。
func (f *FakeClient) ThreadForwardedContext(ctx context.Context, threadID string) (ForwardedContext, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fc, ok := f.forwardedContexts[threadID]
	if !ok {
		return ForwardedContext{}, false, nil
	}
	return fc, true, nil
}

func (f *FakeClient) SetThreadComposerPrefs(threadID string, prefs ComposerPrefs) error {
	f.mu.Lock()
	f.composerPrefsCalls = append(f.composerPrefsCalls, composerPrefsCall{ThreadID: threadID, Prefs: prefs})
	hook := f.composerPrefsHook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(threadID, prefs); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].InteractionMode = prefs.InteractionMode
				list[i].Effort = prefs.Effort
				list[i].BeesProvider = prefs.BeesProvider
				list[i].BeesModel = prefs.BeesModel
				list[i].BeesEffort = prefs.BeesEffort
				list[i].Persona = prefs.Persona
			}
		}
	}
	return nil
}

// SetThreadFastMode 记录调用参数并更新 fake 内存中对应会话的快速模式与速率档位。
func (f *FakeClient) SetThreadFastMode(threadID string, fastMode bool, speedTier string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fastModeCalls = append(f.fastModeCalls, FastModeCall{ThreadID: threadID, FastMode: fastMode, SpeedTier: speedTier})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].FastMode = fastMode
				list[i].SpeedTier = speedTier
			}
		}
	}
	return nil
}

// SetThreadSwarmMode 记录调用参数并更新 fake 内存中对应会话的 Swarm 工作模式。
func (f *FakeClient) SetThreadSwarmMode(threadID, mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swarmModeCalls = append(f.swarmModeCalls, SwarmModeCall{ThreadID: threadID, Mode: mode})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].SwarmMode = mode
			}
		}
	}
	return nil
}

// LastSwarmModeCall 返回最近一次 SetThreadSwarmMode 调用，ok 为 false 表示尚未调用。
func (f *FakeClient) LastSwarmModeCall() (call SwarmModeCall, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.swarmModeCalls) == 0 {
		return SwarmModeCall{}, false
	}
	return f.swarmModeCalls[len(f.swarmModeCalls)-1], true
}

// LastFastModeCall 返回最近一次 SetThreadFastMode 调用，ok 为 false 表示尚未调用。
func (f *FakeClient) LastFastModeCall() (call FastModeCall, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fastModeCalls) == 0 {
		return FastModeCall{}, false
	}
	return f.fastModeCalls[len(f.fastModeCalls)-1], true
}

// FastModeCallCount 返回 SetThreadFastMode 的调用次数。
func (f *FakeClient) FastModeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fastModeCalls)
}

// LastComposerPrefsCall 返回最近一次 SetThreadComposerPrefs 调用，ok 为 false 表示尚未调用。
func (f *FakeClient) LastComposerPrefsCall() (threadID string, prefs ComposerPrefs, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.composerPrefsCalls) == 0 {
		return "", ComposerPrefs{}, false
	}
	last := f.composerPrefsCalls[len(f.composerPrefsCalls)-1]
	return last.ThreadID, last.Prefs, true
}

// ComposerPrefsCallCount 返回 SetThreadComposerPrefs 的调用次数。
func (f *FakeClient) ComposerPrefsCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.composerPrefsCalls)
}

// SetThreadBeeTiers 记录调用参数并更新 fake 内存中对应会话的 BeeTiers。
func (f *FakeClient) SetThreadBeeTiers(threadID, beeTiers string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beeTiersCalls = append(f.beeTiersCalls, BeeTiersCall{ThreadID: threadID, BeeTiers: beeTiers})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].BeeTiers = beeTiers
			}
		}
	}
	return nil
}

// LastBeeTiersCall 返回最近一次 SetThreadBeeTiers 调用，ok 为 false 表示尚未调用。
func (f *FakeClient) LastBeeTiersCall() (threadID, beeTiers string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.beeTiersCalls) == 0 {
		return "", "", false
	}
	last := f.beeTiersCalls[len(f.beeTiersCalls)-1]
	return last.ThreadID, last.BeeTiers, true
}

// BeeTiersCallCount 返回 SetThreadBeeTiers 的调用次数。
func (f *FakeClient) BeeTiersCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.beeTiersCalls)
}

// SetThreadSwarmPairID 记录调用参数并更新 fake 内存中对应会话的 PairID。
func (f *FakeClient) SetThreadSwarmPairID(threadID, pairID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swarmPairIDCalls = append(f.swarmPairIDCalls, SwarmPairIDCall{ThreadID: threadID, PairID: pairID})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].PairID = pairID
			}
		}
	}
	return nil
}

// LastSwarmPairIDCall 返回最近一次 SetThreadSwarmPairID 调用，ok 为 false 表示尚未调用。
func (f *FakeClient) LastSwarmPairIDCall() (call SwarmPairIDCall, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.swarmPairIDCalls) == 0 {
		return SwarmPairIDCall{}, false
	}
	return f.swarmPairIDCalls[len(f.swarmPairIDCalls)-1], true
}

// SwarmPairIDCallCount 返回 SetThreadSwarmPairID 的调用次数。
func (f *FakeClient) SwarmPairIDCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.swarmPairIDCalls)
}

func (f *FakeClient) collectQueueNotificationLocked(threadID string) ([]func(string, []QueuedFollowUp), []QueuedFollowUp) {
	var currentItems []QueuedFollowUp
	if f.queue != nil && len(f.queue[threadID]) > 0 {
		currentItems = make([]QueuedFollowUp, len(f.queue[threadID]))
		copy(currentItems, f.queue[threadID])
	} else {
		currentItems = []QueuedFollowUp{}
	}
	var watchers []func(string, []QueuedFollowUp)
	if len(f.queueWatchers) > 0 {
		watchers = make([]func(string, []QueuedFollowUp), 0, len(f.queueWatchers))
		for _, w := range f.queueWatchers {
			if w != nil {
				watchers = append(watchers, w)
			}
		}
	}
	return watchers, currentItems
}

// Queue 返回指定会话排队消息的副本。
func (f *FakeClient) Queue(ctx context.Context, threadID string) ([]QueuedFollowUp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queue == nil {
		return []QueuedFollowUp{}, nil
	}
	items := f.queue[threadID]
	res := make([]QueuedFollowUp, len(items))
	copy(res, items)
	return res, nil
}

// Enqueue 记录调用，生成 ID 为 fu-%d 的条目追加到队列并通知 watcher。
func (f *FakeClient) Enqueue(ctx context.Context, threadID, text string, attachmentIDs []string) error {
	f.mu.Lock()
	f.enqueueCalls = append(f.enqueueCalls, fakeQueueCall{
		ThreadID:      threadID,
		Text:          text,
		AttachmentIDs: append([]string(nil), attachmentIDs...),
	})
	if f.queue == nil {
		f.queue = make(map[string][]QueuedFollowUp)
	}
	n := len(f.enqueueCalls)
	f.queue[threadID] = append(f.queue[threadID], QueuedFollowUp{
		ID:              fmt.Sprintf("fu-%d", n),
		Text:            text,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		AttachmentCount: len(attachmentIDs),
	})
	watchers, currentItems := f.collectQueueNotificationLocked(threadID)
	f.mu.Unlock()

	for _, w := range watchers {
		w(threadID, currentItems)
	}
	return nil
}

// RemoveQueued 记录调用，从队列删除对应条目并通知 watcher。
func (f *FakeClient) RemoveQueued(ctx context.Context, threadID, followUpID string) error {
	f.mu.Lock()
	f.removeQueuedCalls = append(f.removeQueuedCalls, fakeQueueCall{
		ThreadID:   threadID,
		FollowUpID: followUpID,
	})
	if f.queue != nil {
		items := f.queue[threadID]
		idx := -1
		for i, fu := range items {
			if fu.ID == followUpID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			f.queue[threadID] = append(items[:idx], items[idx+1:]...)
		}
	}
	watchers, currentItems := f.collectQueueNotificationLocked(threadID)
	f.mu.Unlock()

	for _, w := range watchers {
		w(threadID, currentItems)
	}
	return nil
}

// SendQueuedNow 记录调用，从队列删除对应条目并通知 watcher。
func (f *FakeClient) SendQueuedNow(ctx context.Context, threadID, followUpID string) error {
	f.mu.Lock()
	f.sendQueuedNowCalls = append(f.sendQueuedNowCalls, fakeQueueCall{
		ThreadID:   threadID,
		FollowUpID: followUpID,
	})
	if f.queue != nil {
		items := f.queue[threadID]
		idx := -1
		for i, fu := range items {
			if fu.ID == followUpID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			f.queue[threadID] = append(items[:idx], items[idx+1:]...)
		}
	}
	watchers, currentItems := f.collectQueueNotificationLocked(threadID)
	f.mu.Unlock()

	for _, w := range watchers {
		w(threadID, currentItems)
	}
	return nil
}

// SetThreadMCPDisabled 记录调用，把禁用名单写回已登记的会话摘要。
func (f *FakeClient) SetThreadMCPDisabled(ctx context.Context, threadID string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mcpDisabledCalls = append(f.mcpDisabledCalls, fakeMCPDisabledCall{
		ThreadID: threadID,
		Keys:     append([]string(nil), keys...),
	})
	for _, list := range f.threads {
		for i := range list {
			if list[i].ID == threadID {
				list[i].MCPDisabled = append([]string(nil), keys...)
			}
		}
	}
	return nil
}

// MoveQueued 记录调用，按 beforeFollowUpID 重排队列条目并通知 watcher。
func (f *FakeClient) MoveQueued(ctx context.Context, threadID, followUpID, beforeFollowUpID string) error {
	f.mu.Lock()
	f.moveQueuedCalls = append(f.moveQueuedCalls, fakeQueueCall{
		ThreadID:         threadID,
		FollowUpID:       followUpID,
		BeforeFollowUpID: beforeFollowUpID,
	})
	if items := f.queue[threadID]; len(items) > 0 {
		idx := -1
		for i, fu := range items {
			if fu.ID == followUpID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			moved := items[idx]
			rest := make([]QueuedFollowUp, 0, len(items))
			rest = append(rest, items[:idx]...)
			rest = append(rest, items[idx+1:]...)
			insertAt := len(rest)
			if beforeFollowUpID != "" {
				for i, fu := range rest {
					if fu.ID == beforeFollowUpID {
						insertAt = i
						break
					}
				}
			}
			merged := make([]QueuedFollowUp, 0, len(items))
			merged = append(merged, rest[:insertAt]...)
			merged = append(merged, moved)
			merged = append(merged, rest[insertAt:]...)
			f.queue[threadID] = merged
		}
	}
	watchers, currentItems := f.collectQueueNotificationLocked(threadID)
	f.mu.Unlock()

	for _, w := range watchers {
		w(threadID, currentItems)
	}
	return nil
}

// WatchQueue 登记 watcher，返回的 stop 负责注销。
func (f *FakeClient) WatchQueue(onChange func(threadID string, items []QueuedFollowUp)) (stop func()) {
	f.mu.Lock()
	if f.queueWatchers == nil {
		f.queueWatchers = make(map[int]func(string, []QueuedFollowUp))
	}
	f.nextQueueWatcherID++
	id := f.nextQueueWatcherID
	f.queueWatchers[id] = onChange
	f.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.queueWatchers, id)
			f.mu.Unlock()
		})
	}
}

// WatchThreadSummaries 登记会话运行态 watcher，返回的 stop 负责注销。
func (f *FakeClient) WatchThreadSummaries(onChange func(update Thread)) (stop func()) {
	f.mu.Lock()
	if f.summaryWatchers == nil {
		f.summaryWatchers = make(map[int]func(Thread))
	}
	f.nextSummaryWatcherID++
	id := f.nextSummaryWatcherID
	f.summaryWatchers[id] = onChange
	f.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.summaryWatchers, id)
			f.mu.Unlock()
		})
	}
}

// emitThreadSummary 测试辅助方法，同步通知所有会话运行态 watcher。
func (f *FakeClient) emitThreadSummary(update Thread) {
	f.mu.Lock()
	watchers := make([]func(Thread), 0, len(f.summaryWatchers))
	for _, w := range f.summaryWatchers {
		watchers = append(watchers, w)
	}
	f.mu.Unlock()
	for _, w := range watchers {
		if w != nil {
			w(update)
		}
	}
}

// setQueue 测试辅助方法，覆盖指定会话队列并同步通知 watcher。
func (f *FakeClient) setQueue(threadID string, items []QueuedFollowUp) {
	f.mu.Lock()
	if f.queue == nil {
		f.queue = make(map[string][]QueuedFollowUp)
	}
	cp := make([]QueuedFollowUp, len(items))
	copy(cp, items)
	f.queue[threadID] = cp
	watchers, currentItems := f.collectQueueNotificationLocked(threadID)
	f.mu.Unlock()

	for _, w := range watchers {
		w(threadID, currentItems)
	}
}

func fakeDocHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// 项目文档引用约定（与 context_panel.go 的 contextDocFile 一致）：ContextDocs 返回的 Name，以及 ReadContextDoc、
// WriteContextDoc、CreateContextDoc 的 name 参数都是引用：标准文档为根部文件名（instructions.md），自定义文档为 docs/x.md。
// contextDocs 与 contextDocProjects 直接以引用为键，所有入口统一经 contextDocFile 规整。

// contextDocProjectPath 把文档绑定到项目，用于模拟多个项目的项目文档互相隔离；
// 未绑定的文档对所有项目可见（多数测试不关心项目路径）。
func (f *FakeClient) contextDocProjectPath(projectPath, name string) {
	f.mu.Lock()
	f.contextDocProjects[contextDocFile(name)] = projectPath
	f.mu.Unlock()
}

// contextDocsForPath 在持有 f.mu 时返回指定项目可见的文档，键为引用。
func (f *FakeClient) contextDocsForPath(projectPath string) map[string]ContextDoc {
	res := make(map[string]ContextDoc, len(f.contextDocs))
	for ref, doc := range f.contextDocs {
		if ctxPath := f.contextDocProjects[ref]; ctxPath != "" && ctxPath != projectPath {
			continue
		}
		res[ref] = doc
	}
	return res
}

// ContextDocs 返回指定项目可见的文档副本，按引用排序；只包含已存在的文档，缺失的标准文档由产品端补齐。
func (f *FakeClient) ContextDocs(projectPath string) ([]ContextDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	docs := f.contextDocsForPath(projectPath)
	res := make([]ContextDoc, 0, len(docs))
	for _, doc := range docs {
		res = append(res, doc)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Name < res[j].Name
	})
	return res, nil
}

// ReadContextDoc 读取指定文档，找不到时返回包装了 projctx.ErrNotFound 的错误（产品端据此回退到模板）。
func (f *FakeClient) ReadContextDoc(projectPath, name string) (ContextDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ref := contextDocFile(name)
	doc, ok := f.contextDocsForPath(projectPath)[ref]
	if !ok {
		return ContextDoc{}, fmt.Errorf("context doc not found: %s: %w", ref, projctx.ErrNotFound)
	}
	return doc, nil
}

// WriteContextDoc 写入或更新文档，校验 CAS 哈希，ModTime 设为当前时间并递增 contextWriteCalls。
func (f *FakeClient) WriteContextDoc(projectPath, name, content, expectedHash string) (ContextDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ref := contextDocFile(name)
	if existing, ok := f.contextDocs[ref]; ok {
		if existing.Content != content && expectedHash != "" && existing.Hash != expectedHash {
			return ContextDoc{}, fmt.Errorf("%w: %s", ErrContextConflict, ref)
		}
	} else if expectedHash != "" {
		return ContextDoc{}, fmt.Errorf("%w: %s", ErrContextConflict, ref)
	}

	if f.contextDocs == nil {
		f.contextDocs = make(map[string]ContextDoc)
	}
	doc := ContextDoc{
		Name:    ref,
		Content: content,
		Hash:    fakeDocHash(content),
		ModTime: time.Now(),
	}
	f.contextDocs[ref] = doc
	f.contextWriteCalls++
	if projectPath != "" {
		f.contextDocProjects[ref] = projectPath
	}
	return doc, nil
}

// WatchContextDocs 登记项目文档文档变更监听器，返回的 stop 负责注销。
func (f *FakeClient) WatchContextDocs(projectPath string, onChange func()) (stop func(), err error) {
	f.mu.Lock()
	if f.contextWatchers == nil {
		f.contextWatchers = make(map[int]func())
	}
	f.nextContextWatcherID++
	id := f.nextContextWatcherID
	f.contextWatchers[id] = onChange
	f.mu.Unlock()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.contextWatchers, id)
			f.mu.Unlock()
		})
	}
	return stop, nil
}

// SetContextDoc 写入项目文档文档（不计入 contextWriteCalls），并在释放锁后调用所有 contextWatchers，用于模拟外部修改。
// name 可以是标准文档文件名（instructions.md）或自定义文档名（api-spec.md 与 docs/api-spec.md 等价）。
func (f *FakeClient) SetContextDoc(name, content string) {
	f.mu.Lock()
	if f.contextDocs == nil {
		f.contextDocs = make(map[string]ContextDoc)
	}
	ref := contextDocFile(name)
	f.contextDocs[ref] = ContextDoc{
		Name:    ref,
		Content: content,
		Hash:    fakeDocHash(content),
		ModTime: time.Now(),
	}
	watchers := make([]func(), 0, len(f.contextWatchers))
	for _, w := range f.contextWatchers {
		if w != nil {
			watchers = append(watchers, w)
		}
	}
	f.mu.Unlock()

	for _, w := range watchers {
		w()
	}
}

// ContextWriteCalls 返回 WriteContextDoc 的调用次数。
func (f *FakeClient) ContextWriteCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.contextWriteCalls
}

// CreateContextDoc 新建空文档，name 为引用（docs/x.md）；已存在时返回错误，不覆盖。
func (f *FakeClient) CreateContextDoc(projectPath, name string) (ContextDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := contextDocFile(name)
	if _, ok := f.contextDocs[ref]; ok {
		return ContextDoc{}, fmt.Errorf("文档 %s 已存在", ref)
	}
	if f.contextDocs == nil {
		f.contextDocs = make(map[string]ContextDoc)
	}
	doc := ContextDoc{Name: ref, Hash: fakeDocHash(""), ModTime: time.Now()}
	f.contextDocs[ref] = doc
	if projectPath != "" {
		f.contextDocProjects[ref] = projectPath
	}
	return doc, nil
}

func (f *FakeClient) RemoteStatus(ctx context.Context) (RemoteStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remoteStatus, f.remoteErr
}

func (f *FakeClient) SetRemoteEnabled(ctx context.Context, enabled bool) (RemoteStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setRemoteEnabledCalls = append(f.setRemoteEnabledCalls, enabled)
	if f.remoteErr == nil {
		if enabled {
			f.remoteStatus.State = RemoteStateRunning
		} else {
			f.remoteStatus.State = RemoteStateStopped
		}
	}
	return f.remoteStatus, f.remoteErr
}

func (f *FakeClient) RemotePairing(ctx context.Context, refresh bool) (RemotePairing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remotePairingCalls = append(f.remotePairingCalls, refresh)
	return f.remotePairing, f.remoteErr
}

func (f *FakeClient) RemoteDevices(ctx context.Context) ([]RemoteDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remoteDevicesErr != nil {
		return nil, f.remoteDevicesErr
	}
	res := make([]RemoteDevice, len(f.remoteDevices))
	copy(res, f.remoteDevices)
	return res, nil
}

func (f *FakeClient) RevokeRemoteDevice(ctx context.Context, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeDeviceCalls = append(f.revokeDeviceCalls, deviceID)
	if f.remoteErr != nil {
		return f.remoteErr
	}
	var filtered []RemoteDevice
	for _, d := range f.remoteDevices {
		if d.ID != deviceID {
			filtered = append(filtered, d)
		}
	}
	f.remoteDevices = filtered
	return nil
}

func (f *FakeClient) RenameRemoteDevice(ctx context.Context, deviceID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameDeviceCalls = append(f.renameDeviceCalls, [2]string{deviceID, name})
	if f.remoteErr != nil {
		return f.remoteErr
	}
	for i := range f.remoteDevices {
		if f.remoteDevices[i].ID == deviceID {
			f.remoteDevices[i].Name = name
		}
	}
	return nil
}

func (f *FakeClient) RevokeAllRemoteDevices(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeAllDevicesCalls++
	if f.remoteErr != nil {
		return f.remoteErr
	}
	f.remoteDevices = nil
	return nil
}

func (f *FakeClient) RestartRemote(ctx context.Context) (RemoteStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartRemoteCalls++
	if f.remoteErr != nil {
		return f.remoteStatus, f.remoteErr
	}
	f.remoteStatus.State = RemoteStateRunning
	f.remoteStatus.Error = ""
	return f.remoteStatus, nil
}

// SendRemoteTestPush 模拟桌面端测试推送，返回已订阅的端点数。
func (f *FakeClient) SendRemoteTestPush(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remoteErr != nil {
		return 0, f.remoteErr
	}
	return f.remoteStatus.PushSubscriptions, nil
}

func (f *FakeClient) RemoteTLSStatus(ctx context.Context) (RemoteTLSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remoteTLSStatusCalls++
	return f.remoteTLSStatus, f.remoteTLSErr
}

func (f *FakeClient) SaveRemoteCloudflareToken(token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveRemoteTokenCalls = append(f.saveRemoteTokenCalls, token)
	if f.remoteErr != nil {
		return f.remoteErr
	}
	f.remoteTLSStatus.HasCloudflareToken = token != ""
	return nil
}

func (f *FakeClient) HasRemoteCloudflareToken() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remoteTLSStatus.HasCloudflareToken
}

func (f *FakeClient) IssueRemoteTLSCertificate(ctx context.Context) (RemoteTLSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issueRemoteTLSCalls++
	if f.remoteTLSErr != nil {
		return f.remoteTLSStatus, f.remoteTLSErr
	}
	f.remoteTLSStatus.Configured = true
	return f.remoteTLSStatus, nil
}

func (f *FakeClient) ImportRemoteTLSCertificate(ctx context.Context, chainPath, keyPath string) (RemoteTLSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.importRemoteTLSCalls = append(f.importRemoteTLSCalls, [2]string{chainPath, keyPath})
	if f.remoteTLSErr != nil {
		return f.remoteTLSStatus, f.remoteTLSErr
	}
	f.remoteTLSStatus.Configured = true
	return f.remoteTLSStatus, nil
}

func (f *FakeClient) RemoveRemoteTLSCertificate(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeRemoteTLSCalls++
	if f.remoteTLSErr != nil {
		return f.remoteTLSErr
	}
	f.remoteTLSStatus.Configured = false
	f.remoteTLSStatus.CertDomains = nil
	return nil
}

func (f *FakeClient) RemoteTailscaleStatus(ctx context.Context) (RemoteTailscaleStatus, error) {
	// 先在锁内拍快照，再等闸门：闸门打开后返回的仍是发起时的旧状态。
	f.mu.Lock()
	status, err := f.remoteTailscaleStatus, f.remoteTailscaleErr
	gate := f.remoteTailscaleGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return status, err
}

func (f *FakeClient) SetRemoteTailscaleEnabled(ctx context.Context, enabled bool) (RemoteTailscaleStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setRemoteTailscaleCalls = append(f.setRemoteTailscaleCalls, enabled)
	if f.remoteTailscaleErr != nil {
		return f.remoteTailscaleStatus, f.remoteTailscaleErr
	}
	f.remoteTailscaleStatus.Enabled = enabled
	if !enabled {
		f.remoteTailscaleStatus.State = RemoteTailscaleDisabled
	}
	return f.remoteTailscaleStatus, nil
}

func (f *FakeClient) ResetRemoteTailscale(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetRemoteTailscaleCalls++
	if f.remoteTailscaleErr != nil {
		return f.remoteTailscaleErr
	}
	f.remoteTailscaleStatus = RemoteTailscaleStatus{State: RemoteTailscaleDisabled}
	return nil
}

func (f *FakeClient) CustomEndpoints(ctx context.Context) ([]CustomEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.customEndpointErr != nil {
		return nil, f.customEndpointErr
	}
	res := make([]CustomEndpoint, len(f.customEndpoints))
	copy(res, f.customEndpoints)
	return res, nil
}

func (f *FakeClient) SaveCustomEndpoint(ctx context.Context, ep CustomEndpoint, apiKey, webKey string) (CustomEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.customEndpointErr != nil {
		return CustomEndpoint{}, f.customEndpointErr
	}
	f.saveCustomEndpointCalls = append(f.saveCustomEndpointCalls, ep)
	f.saveCustomEndpointKeys = append(f.saveCustomEndpointKeys, apiKey)
	f.saveCustomEndpointWebKeys = append(f.saveCustomEndpointWebKeys, webKey)
	if ep.ID == "" {
		ep.ID = fmt.Sprintf("ep%d", len(f.saveCustomEndpointCalls))
	}
	if apiKey != "" {
		ep.HasAPIKey = true
	}
	// web 管理密钥：非空表示覆盖，管理地址为空表示删掉已保存的密钥，否则保留原状态。
	if webKey != "" {
		ep.HasWebKey = true
	} else if ep.PersonaWebURL == "" {
		ep.HasWebKey = false
	}
	found := false
	for i, existing := range f.customEndpoints {
		if existing.ID == ep.ID {
			if apiKey == "" {
				ep.HasAPIKey = existing.HasAPIKey
			}
			if webKey == "" && ep.PersonaWebURL != "" {
				ep.HasWebKey = existing.HasWebKey
			}
			f.customEndpoints[i] = ep
			found = true
			break
		}
	}
	if !found {
		f.customEndpoints = append(f.customEndpoints, ep)
	}
	return ep, nil
}

func (f *FakeClient) DeleteCustomEndpoint(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.customEndpointErr != nil {
		return f.customEndpointErr
	}
	f.deleteCustomEndpointCalls = append(f.deleteCustomEndpointCalls, id)
	filtered := make([]CustomEndpoint, 0, len(f.customEndpoints))
	for _, ep := range f.customEndpoints {
		if ep.ID != id {
			filtered = append(filtered, ep)
		}
	}
	f.customEndpoints = filtered
	return nil
}

func (f *FakeClient) TestCustomEndpoint(ctx context.Context, ep CustomEndpoint, apiKey string) (CustomEndpointTestResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.customEndpointErr != nil {
		return CustomEndpointTestResult{}, f.customEndpointErr
	}
	f.testCustomEndpointCalls = append(f.testCustomEndpointCalls, ep)
	return f.testCustomEndpointResult, nil
}

func (f *FakeClient) TestMagpiePersonas(ctx context.Context, ep CustomEndpoint, webKey string) (PersonaSourceTestResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.testMagpiePersonasCalls = append(f.testMagpiePersonasCalls, testMagpiePersonasCall{ep: ep, webKey: webKey})
	return f.testMagpiePersonasResult, nil
}

func (f *FakeClient) MagpieQuotaProviders(ctx context.Context, endpointID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.magpieQuotaProvidersCalls++
	if f.magpieQuotaProvidersErr != nil {
		return nil, f.magpieQuotaProvidersErr
	}
	if f.magpieQuotaProviders == nil {
		return nil, nil
	}
	res := make([]string, len(f.magpieQuotaProviders))
	copy(res, f.magpieQuotaProviders)
	return res, nil
}

func (f *FakeClient) ProviderSettings(ctx context.Context) ([]ProviderSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.providerSettingsErr != nil {
		return nil, f.providerSettingsErr
	}
	res := make([]ProviderSetting, len(f.providerSettings))
	copy(res, f.providerSettings)
	return res, nil
}

func (f *FakeClient) SaveProviderSetting(ctx context.Context, s ProviderSetting, apiKey string) (ProviderSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.providerSettingsErr != nil {
		return ProviderSetting{}, f.providerSettingsErr
	}
	f.saveProviderSettingCalls = append(f.saveProviderSettingCalls, s)
	f.saveProviderSettingKeys = append(f.saveProviderSettingKeys, apiKey)
	if apiKey != "" {
		s.HasAPIKey = true
	}
	found := false
	for i, existing := range f.providerSettings {
		if existing.ID == s.ID {
			if apiKey == "" {
				s.HasAPIKey = existing.HasAPIKey
			}
			f.providerSettings[i] = s
			found = true
			break
		}
	}
	if !found {
		f.providerSettings = append(f.providerSettings, s)
	}
	return s, nil
}

func (f *FakeClient) MoveToWorktree(ctx context.Context, threadID, branch string, transferFiles bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moveToWorktreeCalls = append(f.moveToWorktreeCalls, struct {
		ThreadID      string
		Branch        string
		TransferFiles bool
	}{ThreadID: threadID, Branch: branch, TransferFiles: transferFiles})
	return f.moveToWorktreeErr
}

func (f *FakeClient) BulkMarkRead(ctx context.Context, projectID string, threadIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bulkMarkReadCalls = append(f.bulkMarkReadCalls, BulkMarkReadCall{
		ProjectID: projectID,
		ThreadIDs: append([]string(nil), threadIDs...),
	})
	return f.bulkMarkReadErr
}

func (f *FakeClient) BulkArchive(ctx context.Context, projectID string, threadIDs []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bulkArchiveCalls = append(f.bulkArchiveCalls, BulkArchiveCall{
		ProjectID: projectID,
		ThreadIDs: append([]string(nil), threadIDs...),
	})
	if f.bulkArchiveErr != nil {
		return "", f.bulkArchiveErr
	}
	token := f.bulkArchiveToken
	if token == "" {
		token = fmt.Sprintf("undo-%d", len(f.bulkArchiveCalls))
	}
	return token, nil
}

func (f *FakeClient) UndoBulkOperation(ctx context.Context, projectID, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undoBulkOpCalls = append(f.undoBulkOpCalls, UndoBulkOpCall{
		ProjectID: projectID,
		Token:     token,
	})
	return f.undoBulkOpErr
}

func (f *FakeClient) BulkMarkReadCalls() []BulkMarkReadCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]BulkMarkReadCall, len(f.bulkMarkReadCalls))
	copy(res, f.bulkMarkReadCalls)
	return res
}

func (f *FakeClient) BulkArchiveCalls() []BulkArchiveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]BulkArchiveCall, len(f.bulkArchiveCalls))
	copy(res, f.bulkArchiveCalls)
	return res
}

func (f *FakeClient) UndoBulkOpCalls() []UndoBulkOpCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]UndoBulkOpCall, len(f.undoBulkOpCalls))
	copy(res, f.undoBulkOpCalls)
	return res
}

func (f *FakeClient) RecallTurn(ctx context.Context, threadID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recallTurnCalls = append(f.recallTurnCalls, threadID)
	return f.recallTurnText, f.recallTurnErr
}

func (f *FakeClient) PreviewRevert(ctx context.Context, threadID, turnID string) (*RevertPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.previewRevertCalls = append(f.previewRevertCalls, struct {
		ThreadID string
		TurnID   string
	}{ThreadID: threadID, TurnID: turnID})
	if f.previewRevertErr != nil {
		return nil, f.previewRevertErr
	}
	if f.previewRevertResult != nil {
		return f.previewRevertResult, nil
	}
	return &RevertPreview{TurnID: turnID}, nil
}

func (f *FakeClient) ExecuteRevert(ctx context.Context, threadID, turnID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executeRevertCalls = append(f.executeRevertCalls, struct {
		ThreadID string
		TurnID   string
	}{ThreadID: threadID, TurnID: turnID})
	return f.executeRevertErr
}

func (f *FakeClient) ScanWorktrees(ctx context.Context) ([]WorktreeInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanWorktreesCalls++
	if f.scanWorktreesErr != nil {
		return nil, f.scanWorktreesErr
	}
	res := make([]WorktreeInfo, len(f.scanWorktreesResult))
	copy(res, f.scanWorktreesResult)
	return res, nil
}

func (f *FakeClient) CleanSafeWorktrees(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanSafeWorktreesCalls++
	return f.cleanSafeWorktreesResult, f.cleanSafeWorktreesErr
}

func (f *FakeClient) TriggerAutoMerge(ctx context.Context, leadThreadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggerAutoMergeCalls = append(f.triggerAutoMergeCalls, leadThreadID)
	return f.triggerAutoMergeErr
}

func (f *FakeClient) UpdateModelPreference(ctx context.Context, pin ModelPin, pref ModelPreference) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := config.ModelsConfig{Preferences: f.modelPreferences}
	cfg.SetModelPreference(pin, pref)
	f.modelPreferences = cfg.Preferences
	return nil
}

func (f *FakeClient) ReorderModelList(ctx context.Context, pins []ModelPin) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := config.ModelsConfig{}
	cfg.ReorderModelList(pins)
	f.modelList = cfg.Pins
	return nil
}

func (f *FakeClient) ModelList(ctx context.Context) ([]ModelPin, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]ModelPin, len(f.modelList))
	copy(res, f.modelList)
	return res, 30, nil
}

func (f *FakeClient) AddModelToList(ctx context.Context, pin ModelPin) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pin.Provider == "" || pin.ModelID == "" {
		return false, nil
	}
	if len(f.modelList) >= 30 {
		return false, nil
	}
	key := pin.Key()
	for _, p := range f.modelList {
		if p.Key() == key {
			return false, nil
		}
	}
	f.modelList = append(f.modelList, pin)
	return true, nil
}

func (f *FakeClient) RemoveModelFromList(ctx context.Context, pin ModelPin) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := pin.Key()
	for i, p := range f.modelList {
		if p.Key() == key {
			f.modelList = append(f.modelList[:i], f.modelList[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *FakeClient) ModelPreferences(ctx context.Context, pin ModelPin) (ModelPreference, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := config.ModelsConfig{Preferences: f.modelPreferences}
	return cfg.ModelPreferenceFor(pin), nil
}

func (f *FakeClient) SwarmPairs(ctx context.Context) ([]SwarmPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]SwarmPair, len(f.swarmPairs))
	cfg := config.OrchestrationConfig{Pairs: f.swarmPairs}
	for i, pair := range f.swarmPairs {
		res[i], _ = cfg.Pair(pair.ID)
	}
	return res, nil
}

func (f *FakeClient) AddSwarmPair(ctx context.Context, p SwarmPair) (SwarmPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := config.OrchestrationConfig{Pairs: f.swarmPairs}
	added := cfg.AddPair(p)
	f.swarmPairs = cfg.Pairs
	return added, nil
}

func (f *FakeClient) UpdateSwarmPair(ctx context.Context, p SwarmPair) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := config.OrchestrationConfig{Pairs: f.swarmPairs}
	if cfg.UpdatePair(p) {
		f.swarmPairs = cfg.Pairs
		return nil
	}
	return fmt.Errorf("swarm pair not found: %s", p.ID)
}

func (f *FakeClient) RemoveSwarmPair(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.swarmPairs {
		if existing.ID == id {
			f.swarmPairs = append(f.swarmPairs[:i], f.swarmPairs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("swarm pair not found: %s", id)
}

func (f *FakeClient) ProviderVersion(ctx context.Context, provider string) (ProviderVersionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providerVersionCalls = append(f.providerVersionCalls, provider)
	if f.providerVersionErr != nil {
		return ProviderVersionInfo{}, f.providerVersionErr
	}
	if f.providerVersionResult.Provider != "" {
		return f.providerVersionResult, nil
	}
	return ProviderVersionInfo{Provider: provider}, nil
}

func (f *FakeClient) CheckProviderUpdate(ctx context.Context, provider string) (ProviderVersionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkProviderUpdateCalls = append(f.checkProviderUpdateCalls, provider)
	if f.checkProviderUpdateErr != nil {
		return ProviderVersionInfo{}, f.checkProviderUpdateErr
	}
	if f.checkProviderUpdateResult.Provider != "" {
		return f.checkProviderUpdateResult, nil
	}
	return ProviderVersionInfo{Provider: provider}, nil
}

func (f *FakeClient) ProviderAccount(ctx context.Context, provider string) (ProviderAccountInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providerAccountCalls = append(f.providerAccountCalls, provider)
	if f.providerAccountErr != nil {
		return ProviderAccountInfo{}, f.providerAccountErr
	}
	if f.providerAccountResult.Provider != "" {
		return f.providerAccountResult, nil
	}
	return ProviderAccountInfo{Provider: provider}, nil
}

func (f *FakeClient) ProviderQuota(ctx context.Context, provider string) ([]ProviderQuotaWindow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providerQuotaCalls = append(f.providerQuotaCalls, provider)
	if f.providerQuotaErr != nil {
		return nil, f.providerQuotaErr
	}
	res := make([]ProviderQuotaWindow, len(f.providerQuotaResult))
	copy(res, f.providerQuotaResult)
	return res, nil
}

// ProviderQuotaCalls 返回 ProviderQuota 收到的 provider 列表（测试用）。
func (f *FakeClient) ProviderQuotaCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.providerQuotaCalls...)
}

// RefreshProviderQuota 记录手动刷新请求，并按预置的错误与闸门返回。
func (f *FakeClient) RefreshProviderQuota(ctx context.Context, provider string) error {
	f.mu.Lock()
	f.refreshProviderQuotaCalls = append(f.refreshProviderQuotaCalls, provider)
	gate, err := f.refreshProviderQuotaGate, f.refreshProviderQuotaErr
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// RefreshProviderQuotaCalls 返回 RefreshProviderQuota 收到的 provider 列表（测试用）。
func (f *FakeClient) RefreshProviderQuotaCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refreshProviderQuotaCalls...)
}

// threadQuotaRefreshCall 是一次 RefreshThreadQuota 收到的参数，供测试断言。
type threadQuotaRefreshCall struct {
	ThreadID string
	Provider string
}

// RefreshThreadQuota 记录按会话刷新额度的请求（测试用）。
func (f *FakeClient) RefreshThreadQuota(ctx context.Context, threadID, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshThreadQuotaCalls = append(f.refreshThreadQuotaCalls, threadQuotaRefreshCall{ThreadID: threadID, Provider: provider})
	return nil
}

// RefreshThreadQuotaCalls 返回 RefreshThreadQuota 收到的参数列表（测试用）。
func (f *FakeClient) RefreshThreadQuotaCalls() []threadQuotaRefreshCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]threadQuotaRefreshCall(nil), f.refreshThreadQuotaCalls...)
}

func (f *FakeClient) PiMagpieEndpoints(ctx context.Context) ([]PiMagpieEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]PiMagpieEndpoint, len(f.piMagpieEndpoints))
	for i, ep := range f.piMagpieEndpoints {
		ep.Models = append([]PiMagpieModel(nil), ep.Models...)
		ep.Selected = append([]string(nil), ep.Selected...)
		res[i] = ep
	}
	return res, nil
}

// SetPiMagpieEndpoints 设置 PiMagpieEndpoints 返回的假数据。
func (f *FakeClient) SetPiMagpieEndpoints(endpoints []PiMagpieEndpoint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.piMagpieEndpoints = append([]PiMagpieEndpoint(nil), endpoints...)
}

// MagpiePersonas 返回测试预置的假人设列表。
func (f *FakeClient) MagpiePersonas(ctx context.Context, provider, model string) ([]MagpiePersona, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.magpiePersonas == nil {
		return nil, nil
	}
	res := make([]MagpiePersona, len(f.magpiePersonas))
	copy(res, f.magpiePersonas)
	return res, nil
}

// SetMagpiePersonas 设置 MagpiePersonas 返回的假数据。
func (f *FakeClient) SetMagpiePersonas(personas []MagpiePersona) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.magpiePersonas = personas
}

func (f *FakeClient) SetPiMagpieModels(endpointID string, modelIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 与后端一致：保存后端点返回新的勾选，重读快照不会把界面回退到旧值。
	for i := range f.piMagpieEndpoints {
		if f.piMagpieEndpoints[i].ID != endpointID {
			continue
		}
		f.piMagpieEndpoints[i].Selected = append([]string(nil), modelIDs...)
		break
	}
	return nil
}

// SetAgentMagpie 预置 AgentMagpie 返回的设置，按 setting.Agent 索引。
func (f *FakeClient) SetAgentMagpie(setting AgentMagpieSetting) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agentMagpie == nil {
		f.agentMagpie = make(map[string]AgentMagpieSetting)
	}
	f.agentMagpie[setting.Agent] = setting
}

// AgentMagpie 返回测试预置的设置；没有预置时返回只支持运行时注入的空设置。
func (f *FakeClient) AgentMagpie(ctx context.Context, agent string) (AgentMagpieSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agentMagpieCalls = append(f.agentMagpieCalls, agent)
	if f.agentMagpieErr != nil {
		return AgentMagpieSetting{}, f.agentMagpieErr
	}
	setting, ok := f.agentMagpie[agent]
	if !ok {
		return AgentMagpieSetting{Agent: agent, Mode: agentMagpieModeInject}, nil
	}
	// 深拷贝端点与勾选，避免渲染期间的乐观更新改到测试预置值。
	setting.Agent = agent
	endpoints := make([]PiMagpieEndpoint, len(setting.Endpoints))
	for i, ep := range setting.Endpoints {
		ep.Models = append([]PiMagpieModel(nil), ep.Models...)
		ep.Selected = append([]string(nil), ep.Selected...)
		endpoints[i] = ep
	}
	setting.Endpoints = endpoints
	if setting.Mode == "" {
		setting.Mode = agentMagpieModeInject
	}
	return setting, nil
}

// SetAgentMagpieModels 记录模型勾选保存请求，并返回测试预置的错误。
func (f *FakeClient) SetAgentMagpieModels(ctx context.Context, agent, endpointID string, modelIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setAgentMagpieModelsCalls = append(f.setAgentMagpieModelsCalls, setAgentMagpieModelsCall{
		Agent:      agent,
		EndpointID: endpointID,
		ModelIDs:   append([]string(nil), modelIDs...),
	})
	return f.setAgentMagpieModelsErr
}

// SetAgentMagpieMode 记录使用方式切换请求，并返回测试预置的错误。
func (f *FakeClient) SetAgentMagpieMode(ctx context.Context, agent, mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setAgentMagpieModeCalls = append(f.setAgentMagpieModeCalls, setAgentMagpieModeCall{Agent: agent, Mode: mode})
	return f.setAgentMagpieModeErr
}

// SetAgentMagpieDefaultModel 记录默认模型保存请求，并更新假设置里的两个字段。
func (f *FakeClient) SetAgentMagpieDefaultModel(ctx context.Context, agent, endpointID, modelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setAgentMagpieDefaultModelCalls = append(f.setAgentMagpieDefaultModelCalls, setAgentMagpieDefaultModelCall{
		Agent:      agent,
		EndpointID: endpointID,
		ModelID:    modelID,
	})
	if f.setAgentMagpieDefaultModelErr != nil {
		return f.setAgentMagpieDefaultModelErr
	}
	if setting, ok := f.agentMagpie[agent]; ok {
		setting.DefaultEndpointID = endpointID
		setting.DefaultModel = modelID
		f.agentMagpie[agent] = setting
	}
	return nil
}

// AgentMagpieGlobalAction 记录全局配置操作请求，并返回测试预置的错误。
func (f *FakeClient) AgentMagpieGlobalAction(ctx context.Context, agent, action string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agentMagpieGlobalActionCalls = append(f.agentMagpieGlobalActionCalls, agentMagpieGlobalActionCall{Agent: agent, Action: action})
	return f.agentMagpieGlobalActionErr
}

// AgentMagpieCalls 返回 AgentMagpie 收到的 agent 参数列表（测试用）。
func (f *FakeClient) AgentMagpieCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.agentMagpieCalls...)
}

// SetAgentMagpieModelsCalls 返回 SetAgentMagpieModels 收到的参数列表（测试用）。
func (f *FakeClient) SetAgentMagpieModelsCalls() []setAgentMagpieModelsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]setAgentMagpieModelsCall(nil), f.setAgentMagpieModelsCalls...)
}

// SetAgentMagpieModeCalls 返回 SetAgentMagpieMode 收到的参数列表（测试用）。
func (f *FakeClient) SetAgentMagpieModeCalls() []setAgentMagpieModeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]setAgentMagpieModeCall(nil), f.setAgentMagpieModeCalls...)
}

// SetAgentMagpieDefaultModelCalls 返回 SetAgentMagpieDefaultModel 收到的参数列表（测试用）。
func (f *FakeClient) SetAgentMagpieDefaultModelCalls() []setAgentMagpieDefaultModelCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]setAgentMagpieDefaultModelCall(nil), f.setAgentMagpieDefaultModelCalls...)
}

// AgentMagpieGlobalActionCalls 返回 AgentMagpieGlobalAction 收到的参数列表（测试用）。
func (f *FakeClient) AgentMagpieGlobalActionCalls() []agentMagpieGlobalActionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentMagpieGlobalActionCall(nil), f.agentMagpieGlobalActionCalls...)
}

func (f *FakeClient) ProviderCredits(ctx context.Context, provider string) (ProviderCredits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providerCreditsCalls = append(f.providerCreditsCalls, provider)
	if f.providerCreditsErr != nil {
		return ProviderCredits{}, f.providerCreditsErr
	}
	if c, ok := f.providerCreditsResult[provider]; ok {
		return c, nil
	}
	return ProviderCredits{}, ErrNotImplemented
}

func TestProviderCredits_Depleted(t *testing.T) {
	cases := []struct {
		name    string
		credits ProviderCredits
		want    bool
	}{
		{name: "余额充足", credits: ProviderCredits{Total: 12.5, Usable: true}, want: false},
		{name: "余额为零", credits: ProviderCredits{Total: 0, Usable: true}, want: true},
		{name: "不可用", credits: ProviderCredits{Total: 12.5, Usable: false}, want: true},
	}
	for _, tc := range cases {
		if got := tc.credits.Depleted(); got != tc.want {
			t.Errorf("%s: Depleted() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ————— 记忆面板（Nowledge）的测试替身 —————

// MemoryProposals 按项目与状态过滤内存里的提议，status 为空表示不过滤。
func (f *FakeClient) MemoryProposals(projectPath, status string) ([]MemoryProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memoryProposalsErr != nil {
		return nil, f.memoryProposalsErr
	}
	out := make([]MemoryProposal, 0, len(f.memoryProposals))
	for _, p := range f.memoryProposals {
		if p.ProjectPath != projectPath {
			continue
		}
		if status != "" && p.Status != status {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// CountPendingMemoryProposals 统计内存里待审的提议条数。
func (f *FakeClient) CountPendingMemoryProposals(projectPath string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memoryProposalsErr != nil {
		return 0, f.memoryProposalsErr
	}
	count := 0
	for _, p := range f.memoryProposals {
		if p.ProjectPath == projectPath && p.Status == "pending" {
			count++
		}
	}
	return count, nil
}

// UpdateMemoryProposal 记录调用并把标题、内容、类型、重要度写回内存。
func (f *FakeClient) UpdateMemoryProposal(p MemoryProposal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryUpdateCalls = append(f.memoryUpdateCalls, p)
	if f.memoryUpdateErr != nil {
		return f.memoryUpdateErr
	}
	for i := range f.memoryProposals {
		if f.memoryProposals[i].ID != p.ID {
			continue
		}
		f.memoryProposals[i].Title = p.Title
		f.memoryProposals[i].Content = p.Content
		if p.UnitType != "" {
			f.memoryProposals[i].UnitType = p.UnitType
		}
		f.memoryProposals[i].Importance = p.Importance
		return nil
	}
	return nil
}

// AcceptMemoryProposal 记录调用并把提议置为 accepted；注入错误时写回 Error 并保持 pending。
func (f *FakeClient) AcceptMemoryProposal(id string) (MemoryProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryAcceptCalls = append(f.memoryAcceptCalls, id)
	for i := range f.memoryProposals {
		if f.memoryProposals[i].ID != id {
			continue
		}
		if f.memoryAcceptErr != nil {
			f.memoryProposals[i].Error = f.memoryAcceptErr.Error()
			return f.memoryProposals[i], f.memoryAcceptErr
		}
		f.memoryProposals[i].Status = "accepted"
		f.memoryProposals[i].RemoteMemoryID = "remote-" + id
		f.memoryProposals[i].Error = ""
		return f.memoryProposals[i], nil
	}
	return MemoryProposal{}, fmt.Errorf("记忆提议 %s 不存在", id)
}

// RejectMemoryProposal 记录调用并把提议置为 rejected。
func (f *FakeClient) RejectMemoryProposal(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryRejectCalls = append(f.memoryRejectCalls, id)
	if f.memoryRejectErr != nil {
		return f.memoryRejectErr
	}
	for i := range f.memoryProposals {
		if f.memoryProposals[i].ID == id {
			f.memoryProposals[i].Status = "rejected"
			return nil
		}
	}
	return nil
}

// CreateMemoryProposal 记录调用并追加一条 Source=user、状态 pending 的提议。
func (f *FakeClient) CreateMemoryProposal(projectPath, threadID, title, content string) (MemoryProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryCreateCalls = append(f.memoryCreateCalls, [4]string{projectPath, threadID, title, content})
	if f.memoryCreateErr != nil {
		return MemoryProposal{}, f.memoryCreateErr
	}
	p := MemoryProposal{
		ID:          fmt.Sprintf("mp-%d", len(f.memoryProposals)+1),
		ProjectPath: projectPath,
		ThreadID:    threadID,
		Title:       title,
		Content:     content,
		UnitType:    "fact",
		Importance:  0.5,
		Source:      "user",
		Status:      "pending",
	}
	f.memoryProposals = append(f.memoryProposals, p)
	return p, nil
}

// DistillThreadMemories 记录调用并返回预设的新增条数或错误。
func (f *FakeClient) DistillThreadMemories(projectPath, threadID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryDistillCalls = append(f.memoryDistillCalls, [2]string{projectPath, threadID})
	if f.memoryDistillErr != nil {
		return 0, f.memoryDistillErr
	}
	return f.memoryDistillCount, nil
}

// SearchMemories 记录调用并返回预置的检索结果。
func (f *FakeClient) SearchMemories(projectPath, query string) ([]MemoryItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memorySearchCalls = append(f.memorySearchCalls, [2]string{projectPath, query})
	if f.memorySearchErr != nil {
		return nil, f.memorySearchErr
	}
	return append([]MemoryItem(nil), f.memoryItems...), nil
}

// ForgetMemory 记录调用并返回预置的删除结果与错误。
func (f *FakeClient) ForgetMemory(projectPath, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryForgetCalls = append(f.memoryForgetCalls, [2]string{projectPath, id})
	if f.memoryForgetErr != nil {
		return false, f.memoryForgetErr
	}
	return f.memoryForgetDeleted, nil
}

// SetMemoryForgetResult 设置 ForgetMemory 的返回：deleted 为 true 表示已真删除，false 表示已标记废弃。
func (f *FakeClient) SetMemoryForgetResult(deleted bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryForgetDeleted = deleted
	f.memoryForgetErr = err
}

// MemoryForgetCalls 返回 ForgetMemory 的调用记录。
func (f *FakeClient) MemoryForgetCalls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.memoryForgetCalls...)
}

// ExportMemories 记录调用并返回预置的 Markdown 或错误。
func (f *FakeClient) ExportMemories(projectPath string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryExportCalls = append(f.memoryExportCalls, projectPath)
	if f.memoryExportErr != nil {
		return "", f.memoryExportErr
	}
	return f.memoryExportBody, nil
}

// SetMemoryExport 设置 ExportMemories 返回的 Markdown 与错误。
func (f *FakeClient) SetMemoryExport(body string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryExportBody = body
	f.memoryExportErr = err
}

// MemoryExportCalls 返回 ExportMemories 的调用记录。
func (f *FakeClient) MemoryExportCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.memoryExportCalls...)
}

// SetMemoryProposalTarget 记录调用并把内存里对应提议的 TargetMemoryID 更新为给定值。
func (f *FakeClient) SetMemoryProposalTarget(id, targetMemoryID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryTargetCalls = append(f.memoryTargetCalls, [2]string{id, targetMemoryID})
	for i := range f.memoryProposals {
		if f.memoryProposals[i].ID != id {
			continue
		}
		f.memoryProposals[i].TargetMemoryID = targetMemoryID
		return nil
	}
	return nil
}

// CondenseMemory 记录调用并返回预设的精简结果；未设置结果时原样返回入参标题与正文。
func (f *FakeClient) CondenseMemory(projectPath, title, content, mergeWith string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryCondenseCalls = append(f.memoryCondenseCalls, [4]string{projectPath, title, content, mergeWith})
	if f.memoryCondenseErr != nil {
		return "", "", f.memoryCondenseErr
	}
	if !f.memoryCondenseSet {
		return title, content, nil
	}
	return f.memoryCondenseTitle, f.memoryCondenseBody, nil
}

// WorkingMemory 返回预置的 Working Memory 正文。
func (f *FakeClient) WorkingMemory(projectPath string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memoryWorkingErr != nil {
		return "", f.memoryWorkingErr
	}
	return f.memoryWorking, nil
}

// SetMemorySpaces 设置 MemorySpaces 返回的空间列表，并同时供 ResolveMemorySpace 匹配。
func (f *FakeClient) SetMemorySpaces(spaces []MemorySpace) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memorySpaces = append([]MemorySpace(nil), spaces...)
}

// MemorySpaces 返回预置的空间列表。
func (f *FakeClient) MemoryBackend() string {
	if f.memoryBackend == "local" {
		return "local"
	}
	return "nmem"
}

func (f *FakeClient) MemorySpaces(ctx context.Context) ([]MemorySpace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memoryWorkingErr != nil {
		return nil, f.memoryWorkingErr
	}
	return append([]MemorySpace(nil), f.memorySpaces...), nil
}

// ResolveMemorySpace 按预置的空间列表做不区分大小写的名字匹配，行为与 nmem.ResolveSpace 的
// name 匹配一致；未命中时 Space 仍是候选名。
func (f *FakeClient) ResolveMemorySpace(ctx context.Context, projectPath, override string) (MemorySpaceResolution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memoryWorkingErr != nil {
		return MemorySpaceResolution{}, f.memoryWorkingErr
	}
	want := strings.TrimSpace(override)
	res := MemorySpaceResolution{Space: want, FromOverride: want != ""}
	if want == "" {
		// 测试替身不跑 git：没有 override 时用项目目录名当候选名。
		want = strings.ToLower(filepath.Base(projectPath))
		res.Space = want
	}
	for _, s := range f.memorySpaces {
		if strings.EqualFold(s.Name, want) {
			res.Space = s.Name
			res.Hit = true
			break
		}
	}
	return res, nil
}

// SetMemoryProposal 写入或替换一条提议（Status 为空时按 pending 处理）。
func (f *FakeClient) SetMemoryProposal(p MemoryProposal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Status == "" {
		p.Status = "pending"
	}
	for i := range f.memoryProposals {
		if f.memoryProposals[i].ID == p.ID {
			f.memoryProposals[i] = p
			return
		}
	}
	f.memoryProposals = append(f.memoryProposals, p)
}

// SetMemorySearchResults 设置 SearchMemories 的返回结果。
func (f *FakeClient) SetMemorySearchResults(items []MemoryItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryItems = append([]MemoryItem(nil), items...)
}

// SetWorkingMemory 设置 WorkingMemory 的返回正文。
func (f *FakeClient) SetWorkingMemory(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryWorking = text
}

// SetMemoryErrors 一次性设置记忆相关调用的注入错误（nil 表示不注入）。
func (f *FakeClient) SetMemoryErrors(propsals, accept error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryProposalsErr = propsals
	f.memoryAcceptErr = accept
}

// MemoryAcceptCalls 返回 AcceptMemoryProposal 收到的提议 ID 序列。
func (f *FakeClient) MemoryAcceptCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.memoryAcceptCalls...)
}

// MemoryRejectCalls 返回 RejectMemoryProposal 收到的提议 ID 序列。
func (f *FakeClient) MemoryRejectCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.memoryRejectCalls...)
}

// MemoryCreateCalls 返回 CreateMemoryProposal 的调用参数。
func (f *FakeClient) MemoryCreateCalls() [][4]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][4]string(nil), f.memoryCreateCalls...)
}

// MemorySearchCalls 返回 SearchMemories 的调用参数（项目路径、关键字）。
func (f *FakeClient) MemorySearchCalls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.memorySearchCalls...)
}

// MemoryTargetCalls 返回 SetMemoryProposalTarget 的调用参数（提议 ID、目标记忆 ID）。
func (f *FakeClient) MemoryTargetCalls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.memoryTargetCalls...)
}

// MemoryCondenseCalls 返回 CondenseMemory 的调用参数（项目路径、标题、正文、合并来源）。
func (f *FakeClient) MemoryCondenseCalls() [][4]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][4]string(nil), f.memoryCondenseCalls...)
}

// SetMemoryCondenseResult 设置 CondenseMemory 的返回结果与注入错误；传空串也能生效。
func (f *FakeClient) SetMemoryCondenseResult(title, content string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryCondenseTitle = title
	f.memoryCondenseBody = content
	f.memoryCondenseErr = err
	f.memoryCondenseSet = true
}

// MemoryDistillCalls 返回 DistillThreadMemories 的调用参数（项目路径、会话 ID）。
func (f *FakeClient) MemoryDistillCalls() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.memoryDistillCalls...)
}

// SetMemoryDistillResult 设置 DistillThreadMemories 的返回结果与注入错误。
func (f *FakeClient) SetMemoryDistillResult(count int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryDistillCount = count
	f.memoryDistillErr = err
}
