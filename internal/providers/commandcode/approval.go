package commandcode

// 本文件实现 Command Code 的审批门禁：内嵌 mcode 自己的扩展 approval_mod.ts，
// 运行时写到临时目录由 `--mod` 加载；mod 把每个可变更工具调用写成审批目录里的
// <toolCallId>.request.json，gate 轮询目录、按权限模式自动放行或经 harness.Approver /
// Interactor 询问后把 <toolCallId>.reply.json 写回去。

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcode/internal/harness"
)

// approvalMod 是 mcode 的 Command Code 扩展源码（TypeScript，CLI 加载时编译）。
//
//go:embed approval_mod.ts
var approvalMod string

const (
	// approvalModFile 是 mod 写到临时目录里的文件名。
	approvalModFile = "mcode-approvals.ts"
	// approvalsEnv 是 mod 读取审批目录的环境变量名，须与 approval_mod.ts 一致。
	approvalsEnv = "MCODE_APPROVALS"
	// approvalPollInterval 是轮询审批目录的间隔；mod 侧以 40ms 的节奏等待回复文件。
	approvalPollInterval = 50 * time.Millisecond
)

// denialReason 是拒绝时写给模型的说明。
const denialReason = "Rejected by the user in mcode. Don't repeat the same call; pick a different approach or tell the user what you need."

// gateRequest 是 mod 写进请求文件的载荷：{toolCallId, toolName, input}。
type gateRequest struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
}

// gate 是一次运行的工具审批门禁。
type gate struct {
	// dir 是本次运行的审批目录。
	dir  string
	turn harness.Turn
	// grants 是本会话已放行的请求种类（"Approve for session"），由 Harness 持有并跨 turn 复用。
	grants map[string]bool
	// grantsLock 保护 grants：门禁轮询在独立 goroutine 里读写它，Run 主循环的 grantsFollow
	// 会在 Harness 锁下遍历同一个 map，所以 Run 把 Harness 的锁注入进来共用。
	grantsLock sync.Locker
	// interval 覆盖轮询间隔（测试用）；为 0 时用 approvalPollInterval。
	interval time.Duration
}

// newGate 创建一次运行的门禁。
func newGate(dir string, turn harness.Turn, grants map[string]bool) *gate {
	return &gate{dir: dir, turn: turn, grants: grants, grantsLock: &sync.Mutex{}}
}

// poll 以固定间隔扫描审批目录，直到 ctx 结束。
func (g *gate) poll(ctx context.Context) {
	interval := g.interval
	if interval <= 0 {
		interval = approvalPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.scan(ctx)
		}
	}
}

// scan 处理目录里所有 *.request.json：按文件名排序逐个读取，读完即删（同一请求只处理
// 一次），然后决定放行或询问。
func (g *gate) scan(ctx context.Context) {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".request.json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(g.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		_ = os.Remove(path)
		id := strings.TrimSuffix(name, ".request.json")
		g.decide(ctx, id, data)
	}
}

// decide 对一条请求决定放行还是拒绝，并把回复写成回复文件。
// 自动放行（按权限模式或会话级放行）不询问审批方；其余请求经 Interactor / Approver 询问，
// 没有审批方时按拒绝处理：headless 运行里悬着会让当轮卡死。
func (g *gate) decide(ctx context.Context, id string, raw json.RawMessage) {
	var request gateRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return
	}
	toolName := firstNonEmpty(request.ToolName, id)
	req := approvalRequest(id, toolName, request.Input)
	if g.autoApproves(toolName, request.Input) || g.granted(req.Kind) {
		_ = g.reply(id, true, "")
		return
	}
	allow, always := g.ask(ctx, req)
	if always {
		g.grantedKind(req.Kind, true)
	}
	reason := ""
	if !allow {
		reason = denialReason
	}
	err := g.reply(id, allow, reason)
	harness.AckDelivery(g.turn.Approver, req, err)
}

// ask 询问审批方：优先 Interactor（可带回 "always" / "allow for session" 的会话级放行），
// 否则用 Approver 的允许/拒绝判决。返回 always 表示本次答复要求整个会话放行。
func (g *gate) ask(ctx context.Context, req harness.ApprovalRequest) (allow bool, always bool) {
	if interactor, ok := g.turn.Approver.(harness.Interactor); ok && interactor != nil {
		if response, err := interactor.Interact(ctx, req); err == nil {
			return approveFromResponse(response)
		}
		return false, false
	}
	if g.turn.Approver != nil {
		if decision, err := g.turn.Approver.Approve(ctx, req); err == nil {
			return decision == harness.DecisionAllow, false
		}
	}
	return false, false
}

// approvedKinds 记录 / 查询会话级放行。
func (g *gate) granted(kind harness.RequestKind) bool {
	g.grantsLock.Lock()
	defer g.grantsLock.Unlock()
	return g.grants != nil && g.grants[string(kind)]
}

func (g *gate) grantedKind(kind harness.RequestKind, value bool) {
	g.grantsLock.Lock()
	defer g.grantsLock.Unlock()
	if g.grants == nil {
		return
	}
	g.grants[string(kind)] = value
}

// autoApproves 判断该工具调用在当前权限模式下无需询问即可放行。
// 完全访问到不了这里：它不加载 gate。
func (g *gate) autoApproves(toolName string, input json.RawMessage) bool {
	isEdit := toolName == "write_file" || toolName == "edit_file"
	switch g.turn.Mode {
	case harness.ModeFullAccess:
		return true
	case harness.ModeReadOnly, harness.ModeSupervised:
		// 只读与会话模式一律询问（没有审批方时按拒绝处理），保证只读模式下不会有写改溜过去。
		return false
	case harness.ModeAutoAcceptEdits:
		return isEdit
	case harness.ModeAuto:
		if isEdit {
			return true
		}
		switch toolName {
		case "shell_command", "run_command", "powershell", "monitor_command":
			return !shellMayWriteFiles(inputString(input, "command"))
		case "kill_shell", "cron_create", "cron_delete", "task_stop":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// approvalRequest 把请求文件载荷包装成 harness 的审批请求：命令与文件写改分别归到
// command / fileChange，摘要取命令或路径。
func approvalRequest(id, toolName string, input json.RawMessage) harness.ApprovalRequest {
	kind := harness.RequestKindTool
	summary := humanize(toolName)
	switch toolName {
	case "shell_command", "run_command", "powershell", "monitor_command", "shell_output", "shell_tasks", "kill_shell":
		kind = harness.RequestKindCommand
		summary = firstNonEmpty(inputString(input, "command"), humanize(toolName))
	case "write_file", "edit_file":
		kind = harness.RequestKindFileChange
		summary = firstNonEmpty(inputString(input, "file_path"), inputString(input, "path"), humanize(toolName))
	}
	return harness.ApprovalRequest{
		ToolID:   id,
		ToolName: toolName,
		Input:    input,
		Summary:  summary,
		Kind:     kind,
	}
}

// approveFromResponse 把交互答复折算成是否放行与是否会话级放行：
// 答案里有 "always" / "allow for session" 即会话级放行，否则看 Decision。
func approveFromResponse(response harness.ApprovalResponse) (allow bool, always bool) {
	for _, answers := range response.Answers {
		for _, answer := range answers {
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "always", "allow for session":
				return true, true
			}
		}
	}
	return response.Decision == harness.DecisionAllow, false
}

// reply 把答复写进 <id>.reply.json：先写 .tmp 再改名，mod 只在改名完成后读到完整内容。
// id 取自请求文件名，不含路径分隔符。
func (g *gate) reply(id string, allow bool, reason string) error {
	payload := map[string]string{"decision": "allow"}
	if !allow {
		payload = map[string]string{"decision": "deny", "reason": firstNonEmpty(reason, denialReason)}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	target := filepath.Join(g.dir, id+".reply.json")
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}

// humanize 把 task_create 变成 "Task create"。
func humanize(name string) string {
	if name == "" {
		return name
	}
	words := strings.Split(name, "_")
	first := words[0]
	if first != "" {
		first = strings.ToUpper(first[:1]) + first[1:]
	}
	return strings.Join(append([]string{first}, words[1:]...), " ")
}

// shellCommandReaders 是只读的 shell 工具。
var shellCommandReaders = map[string]bool{
	"ls": true, "cat": true, "bat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "tree": true,
	"pwd": true, "echo": true, "printf": true, "which": true, "command": true, "type": true,
	"stat": true, "file": true, "du": true, "df": true,
	"ps": true, "printenv": true, "date": true, "whoami": true, "hostname": true, "uname": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true, "column": true,
	"uniq": true, "cut": true, "tr": true, "nl": true, "diff": true, "cmp": true, "jq": true,
	"xxd": true, "od": true, "md5": true, "shasum": true, "cksum": true,
	"man": true, "true": true, "false": true, "sleep": true,
	"cd": true, "pushd": true, "popd": true,
}

// gitReaders 是只报结果的 git 子命令；git 的其他用法一律算可写。
// 注意 `symbolic-ref <ref> <value>` 与 `reflog expire/delete` 都能改写仓库，因此不入白名单。
var gitReaders = map[string]bool{
	"log": true, "status": true, "diff": true, "show": true,
	"rev-parse": true, "rev-list": true, "ls-files": true, "ls-tree": true, "blame": true,
	"describe": true, "shortlog": true, "cat-file": true,
	"grep": true, "whatchanged": true, "for-each-ref": true,
	"count-objects": true, "var": true, "version": true, "help": true,
}

// gitListOnly 是只在纯列出形式下才只读的 git 子命令：
// `git branch -D x`、`git tag -d v1`、`git remote add`、`git config a b` 都会改写仓库。
var gitListOnly = map[string]bool{"branch": true, "tag": true, "remote": true, "config": true}

// gitListArguments 是 gitListOnly 里真正只列出的参数。
var gitListArguments = map[string]bool{"-l": true, "--list": true, "-v": true, "-vv": true, "--verbose": true}

// searchActions 是会让 find / fd 执行命令的参数，可能写文件。
var searchActions = map[string]bool{
	"-delete": true, "-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
	"-x": true, "-X": true,
}

// shellMayWriteFiles 判断 shell 命令是否可能写文件：
// 重定向、命令替换与反引号直接算可写；链式命令里只要有一段不是只读工具就算可写；
// 认不出来的工具一律算可写，宁严勿松。
func shellMayWriteFiles(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if strings.Contains(command, ">") || strings.Contains(command, "$(") || strings.Contains(command, "`") {
		return true
	}
	for _, part := range splitCommandParts(command) {
		if !shellPartReadsOnly(part) {
			return true
		}
	}
	return false
}

// splitCommandParts 把命令行拆成依次执行的几条命令；引号内的分隔符也照拆
// （拆多了只会更保守）。
func splitCommandParts(command string) []string {
	replacer := strings.NewReplacer("&&", "\x01", "||", "\x01", ";", "\x01", "|", "\x01", "\n", "\x01", "&", "\x01")
	var parts []string
	for _, part := range strings.Split(replacer.Replace(command), "\x01") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

// shellPartReadsOnly 判断链式命令里的一段是否只读。
func shellPartReadsOnly(part string) bool {
	tokens := strings.Fields(part)
	if len(tokens) == 0 {
		return true
	}
	tool := filepath.Base(tokens[0])
	arguments := tokens[1:]
	switch tool {
	case "sed":
		prints := false
		inPlace := false
		for _, argument := range arguments {
			if strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") && strings.Contains(argument, "n") {
				prints = true
			}
			if strings.HasPrefix(argument, "-i") || strings.HasPrefix(argument, "--in-place") {
				inPlace = true
			}
		}
		return prints && !inPlace
	case "awk", "gawk", "mawk":
		// 重定向已被排除在外；awk 仍可能通过 system() 起子进程，那按可写处理。
		return !strings.Contains(part, "system(")
	case "find", "fd":
		for _, argument := range arguments {
			if searchActions[argument] {
				return false
			}
		}
		return true
	case "sort":
		for _, argument := range arguments {
			if strings.HasPrefix(argument, "-o") {
				return false
			}
		}
		return true
	case "git":
		subcommand := ""
		for _, argument := range arguments {
			if !strings.HasPrefix(argument, "-") {
				subcommand = argument
				break
			}
		}
		if subcommand == "" {
			// 裸 git（或只有参数）只打印帮助。
			return true
		}
		if gitListOnly[subcommand] {
			return gitListsOnly(subcommand, arguments)
		}
		return gitReaders[subcommand]
	case "yq":
		// yq 默认只把结果打到 stdout；-i/--inplace 会原地改写文件。
		for _, argument := range arguments {
			flag := argument
			if idx := strings.IndexByte(flag, '='); idx >= 0 {
				flag = flag[:idx]
			}
			switch flag {
			case "-i", "--inplace", "--in-place":
				return false
			}
		}
		return true
	case "xcodebuild":
		for _, argument := range arguments {
			switch argument {
			case "-showBuildSettings", "-list", "-version", "-showsdks":
				return true
			}
		}
		return false
	case "swift", "swiftc", "node", "npm", "python", "python3":
		if len(arguments) == 1 {
			switch arguments[0] {
			case "--version", "-version", "-v":
				return true
			}
		}
		return false
	case "env":
		// 裸 env 打印环境变量；后面带命令时那条命令就会跑起来。
		for _, argument := range arguments {
			if !strings.HasPrefix(argument, "-") {
				return false
			}
		}
		return true
	default:
		return shellCommandReaders[tool]
	}
}

// gitListsOnly 判断 gitListOnly 里的子命令是否只用纯列出参数（含不带参数）。
func gitListsOnly(subcommand string, arguments []string) bool {
	seenSubcommand := false
	for _, argument := range arguments {
		switch {
		case argument == subcommand && !seenSubcommand:
			seenSubcommand = true
		case gitListArguments[argument]:
		default:
			return false
		}
	}
	return true
}
