// mcode 加载进每个 omp 会话的门禁扩展（以 `-e` 加载，见 gate.go）。omp 自带的审批挡不住
// 东西：用户的 tools.approval 配置或 yolo 都能让工具直接执行，所以每次 tool_call 都先经过这里，
// 按本轮的 gate 文件（MCODE_OMP_GATE）放行、拦下，或以标题 "mcode-gate {…}" 的 select 交回适配器。
import fs from "node:fs";

// 只看不写、只回报结果的 omp 工具（Go 侧 rpc.go 的 readOnlyGateTools 须与此一致）。
// omp 以后新增的工具不在表里，默认要问。
const LOOK_ONLY_TOOLS = new Set(["read", "grep", "glob", "find", "ast_grep", "web_search", "todo", "wait", "think", "ask"]);
const FILE_TOOLS = new Set(["write", "edit", "ast_edit", "notebook"]);
const COMMAND_TOOLS = new Set(["bash", "eval"]);

const MAX_FIELD_CHARS = 20000;
const KIND_WORDS = { command: "command", fileChange: "file change", tool: "tool call" };

// SAFE_PROGRAMS 是只读程序，只看到程序名还不够，命令串里还不能有重定向、命令替换、管道与第二段命令。
const SAFE_PROGRAMS = new Set([
  "ls", "cat", "head", "tail", "wc", "grep", "rg", "find", "tree", "du", "stat", "file",
  "pwd", "echo", "which", "sort", "uniq", "diff",
]);
const SAFE_GIT = new Set(["status", "log", "diff", "show", "blame", "branch"]);

// DANGEROUS_FIND_ACTIONS 是 find 里会删除文件、写文件或执行命令的动作，出现任何一个都不算只读。
const DANGEROUS_FIND_ACTIONS = new Set([
  "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls",
]);

// SAFE_BRANCH_FLAGS 是 git branch 里只读的开关；其余（-d / -D / -m / -c / -u / --set-upstream-to 等）会改仓库。
const SAFE_BRANCH_FLAGS = new Set([
  "-a", "-r", "-v", "-vv", "--list", "--all", "--remotes", "--show-current", "--verbose",
  "--contains", "--merged", "--no-merged",
]);

// onlyReads 判断一条 shell 命令的每一段是否都是 SAFE_PROGRAMS / SAFE_GIT 里的读操作（与 pi 侧一致）。
function onlyReads(command) {
  const raw = String(command ?? "");
  // 换行、命令替换、反引号、过程替换、重定向和单个 & 都能在“只读”程序后面夹带写操作或第二条命令，一律不当只读。
  if (/[\r\n]/.test(raw)) return false;
  if (raw.includes("$(") || raw.includes("`") || raw.includes("<(") || raw.includes(">(")) return false;
  if (raw.includes(">")) return false;
  if (/(^|[^&])&([^&]|$)/.test(raw)) return false;
  for (const piece of raw.split(/&&|\|\||[;|]/)) {
    const tokens = piece.trim().split(/\s+/).filter(Boolean);
    if (!tokens.length) continue;
    const program = tokens[0];
    const sub = tokens[1];
    if (program === "git") {
      if (!SAFE_GIT.has(sub)) return false;
      if (tokens.slice(2).some((t) => t === "--ext-diff" || t === "--output" || t.startsWith("--output="))) return false;
      if (sub === "branch" && tokens.slice(2).some((t) => !SAFE_BRANCH_FLAGS.has(t))) return false;
      continue;
    }
    if (!SAFE_PROGRAMS.has(program)) return false;
    if (program === "rg" && tokens.slice(1).some((t) => t === "--pre" || t.startsWith("--pre="))) return false;
    // sort -o / --output、find -delete / -exec 等会把“只读”变成写入。
    if (program === "sort" && tokens.slice(1).some((t) => t.startsWith("-o") || t.startsWith("--output"))) return false;
    if (program === "find" && tokens.slice(1).some((t) => DANGEROUS_FIND_ACTIONS.has(t))) return false;
  }
  return true;
}

// gate 文件由适配器在每轮开始前写好；读不到时当作没有任何放行依据，一律去问。
function loadTurnGate() {
  try {
    return JSON.parse(fs.readFileSync(process.env.MCODE_OMP_GATE || "", "utf8"));
  } catch {
    return {};
  }
}

function kindOf(toolName) {
  if (COMMAND_TOOLS.has(toolName)) return "command";
  if (FILE_TOOLS.has(toolName)) return "fileChange";
  return "tool";
}

// verdictWithoutAsking 返回 "allow"、一个拦截结果，或 null 表示需要去问。
function verdictWithoutAsking(gate, name, command, hasUI) {
  // yield 是子代理交回结果的调用，没什么可拦的。
  if (name === "yield") return "allow";
  if (gate.finalReport) return { block: true, reason: "Blocked by mcode: this turn is only for the final report, so tools are off." };
  // task 会起一个不经审批的子代理，只有 Hydra 让 heads 跑在 omp 里时才放开。
  if (name === "task" && !gate.allowTask) {
    return { block: true, reason: "Blocked by mcode: subagents are disabled in this chat; Hydra is not running heads inside omp." };
  }
  if (LOOK_ONLY_TOOLS.has(name)) return "allow";
  if (gate.interactionMode === "plan") {
    // 计划模式仍要能调研：只读 shell 命令（ls / cat / rg / git status 等）放行，其余改动一律拦下。
    if (name === "bash" && onlyReads(command)) return "allow";
    return { block: true, reason: "Blocked by mcode: plan mode only allows reading." };
  }
  if (gate.runtimeMode === "fullAccess") return "allow";
  if (gate.runtimeMode === "auto" || gate.runtimeMode === "autoAcceptEdits") {
    if (FILE_TOOLS.has(name) || (name === "bash" && onlyReads(command))) return "allow";
  }
  // 子代理里没有可以问的人（ctx.hasUI 为 false），这里不再拦。
  if (!hasUI) return "allow";
  return null;
}

// clippedInput 截短超长字符串字段，避免把整份文件内容塞进 select 标题。
function clippedInput(input) {
  const out = {};
  for (const [key, value] of Object.entries(input || {})) {
    out[key] = typeof value === "string" && value.length > MAX_FIELD_CHARS ? value.slice(0, MAX_FIELD_CHARS) : value;
  }
  return out;
}

export default async function (pi) {
  // 适配器启动时查命令表，看到这个名字才确认门禁已加载。
  pi.registerCommand("mcode-gate", { description: "mcode's tool gate", handler: async () => {} });

  pi.on("tool_call", async (event, ctx) => {
    const name = event.toolName;
    const verdict = verdictWithoutAsking(loadTurnGate(), name, event.input?.command, ctx.hasUI);
    if (verdict === "allow") return;
    if (verdict) return verdict;
    const kind = kindOf(name);
    const payload = { kind, toolName: name, toolCallId: event.toolCallId, input: clippedInput(event.input) };
    // rpc-ui 模式下这次选择以 extension_ui_request 回到适配器。
    const answer = await ctx.ui.select("mcode-gate " + JSON.stringify(payload), ["Approve", "Deny"]);
    if (answer === "Approve") return;
    return { block: true, reason: `Blocked by mcode: the user rejected this ${KIND_WORDS[kind]}.` };
  });
}
