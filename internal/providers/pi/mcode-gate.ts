// mcode 加载进每个 pi 会话的门禁扩展（以 `-e` 加载，见 gate.go）。pi 自己只有
// --approve / --no-approve 两档，按运行模式细分的门禁在这里做：每次 tool_call 先查本轮的
// gate 文件（MCODE_PI_GATE），能直接定的当场放行或拦下，其余以标题 "mcode-gate {…}" 的
// select 交回适配器，由 turn.Approver 应答。
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { Type } from "typebox";

// pi 内置的只看不写工具（Go 侧 extension.go 的 readOnlyGateTools 须与此一致）。
const LOOK_ONLY_TOOLS = new Set(["read", "grep", "find", "ls", "mcode_mcp_list", "ask_user_question", "vision"]);

// mcode 自带 MCP 工具的前缀（见 mcp_bridge.ts）：这些工具的权限由 mcode MCP 服务端按权限档
// 自己校验，计划模式下门禁不再当成写操作拦下——总管话题会话就靠它们写任务书、开工。
const BUILTIN_MCP_TOOL_PREFIX = "mcp__mcode__";

// 防止命令挂死，让这一轮一直卡住。
const BASH_DEFAULT_TIMEOUT_SECONDS = 300;
const BASH_MAX_TIMEOUT_SECONDS = 1800;

// auto / readonly 模式下可以不问就跑的 shell 程序与 git 子命令。
const SAFE_PROGRAMS = new Set([
  "ls", "cat", "head", "tail", "wc", "grep", "rg", "find", "tree", "du", "stat", "file",
  "pwd", "echo", "which", "sort", "uniq", "diff", "cd",
]);
const SAFE_GIT = new Set(["status", "log", "diff", "show", "blame", "branch", "ls-files"]);

// DANGEROUS_FIND_ACTIONS 是 find 里会删除文件、写文件或执行命令的动作，出现任何一个都不算只读。
const DANGEROUS_FIND_ACTIONS = new Set([
  "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls",
]);

// SAFE_BRANCH_FLAGS 是 git branch 里只读的开关；其余（-d / -D / -m / -c / -u / --set-upstream-to 等）会改仓库。
const SAFE_BRANCH_FLAGS = new Set([
  "-a", "-r", "-v", "-vv", "--list", "--all", "--remotes", "--show-current", "--verbose",
  "--contains", "--merged", "--no-merged",
]);

const MAX_FIELD_CHARS = 20000;

// vision 工具只在用户配置了外部视觉模型时注册；值的格式是 provider/model（由上层注入）。
const VISION_MODEL_ENV = "MCODE_PI_VISION_MODEL";

// Magpie 路由的模型不在子进程自己的配置里，要靠账号桥注册。桥的路径与端点清单由上层注入，
// 名字须与 Go 侧 MagpieEnvVar / MagpieBridgeEnvVar 一致。
const MAGPIE_ENV = "MCODE_PI_MAGPIE";
const MAGPIE_BRIDGE_ENV = "MCODE_PI_MAGPIE_BRIDGE";

// pi 自己按文件内容嗅探图片类型，这里先按扩展名挡掉明显不支持的路径。
const VISION_IMAGE_EXTENSIONS = new Set([".png", ".jpg", ".jpeg", ".gif", ".webp"]);

// 视觉子进程是完整的一次模型调用，给足时间；超时直接杀掉，别让这一轮一直卡住。
const VISION_TIMEOUT_MS = 120000;
const VISION_MAX_RESULT_CHARS = 20000;
const VISION_MAX_STDERR_CHARS = 2000;

// VISION_PROMPT 是默认提示词：只要客观描述，不猜、不漏文字，界面要列控件与状态。
const VISION_PROMPT =
  "Describe this image accurately and do not guess. Transcribe every visible piece of text verbatim. " +
  "If it shows a user interface, list the label and state of each control.";

// gate 文件由适配器在每轮开始前写好；读不到时当作没有任何放行依据，一律去问。
function loadTurnGate() {
  try {
    return JSON.parse(fs.readFileSync(process.env.MCODE_PI_GATE || "", "utf8"));
  } catch {
    return {};
  }
}

// splitCommandPieces 只按引号外的 &&、||、;、| 切命令：引号里的分隔符属于参数内容，
// 切开会把 grep "a; rm b" 这类命令误当成两段。单引号内没有转义；双引号内与引号外的
// 反斜杠转义下一个字符。引号未闭合时返回 null，调用方按不只读处理。
function splitCommandPieces(text) {
  const source = String(text ?? "");
  const pieces = [];
  let current = "";
  let quote = "";
  for (let i = 0; i < source.length; i++) {
    const ch = source[i];
    if (quote === "'") {
      current += ch;
      if (ch === "'") quote = "";
      continue;
    }
    if (ch === "\\") {
      current += ch;
      if (i + 1 < source.length) {
        current += source[i + 1];
        i++;
      }
      continue;
    }
    if (quote === '"') {
      current += ch;
      if (ch === '"') quote = "";
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      current += ch;
      continue;
    }
    if (ch === "|") {
      pieces.push(current);
      current = "";
      if (source[i + 1] === "|") i++;
      continue;
    }
    if (ch === "&" && source[i + 1] === "&") {
      pieces.push(current);
      current = "";
      i++;
      continue;
    }
    if (ch === ";") {
      pieces.push(current);
      current = "";
      continue;
    }
    current += ch;
  }
  if (quote) return null;
  pieces.push(current);
  return pieces;
}

// onlyReads 判断一条 shell 命令的每一段是否都是 SAFE_PROGRAMS / SAFE_GIT 里的读操作。
function onlyReads(command) {
  const raw = String(command ?? "");
  // >/dev/null 与 2>&1 只是丢弃输出、不写文件，先摘掉再跑下面的检查，否则 2>/dev/null 会被当成写文件。
  const text = raw
    .replace(/\d*&?>>?\s*\/dev\/null(?=[\s;|&]|$)/g, "")
    .replace(/\d?>&\d/g, "");
  // 换行、命令替换、反引号、过程替换、重定向和单个 & 都能在“只读”程序后面夹带写操作或第二条命令，一律不当只读。
  if (/[\r\n]/.test(text)) return false;
  if (text.includes("$(") || text.includes("`") || text.includes("<(") || text.includes(">(")) return false;
  if (text.includes(">")) return false;
  if (/(^|[^&])&([^&]|$)/.test(text)) return false;
  const pieces = splitCommandPieces(text);
  if (pieces === null) return false;
  for (const piece of pieces) {
    const tokens = piece.trim().split(/\s+/).filter(Boolean);
    if (!tokens.length) continue;
    const program = tokens[0];
    const sub = tokens[1];
    if (program === "git") {
      // git worktree 只有 list 与读一样；add / remove / prune / move 等都会改仓库，不放行。
      if (sub === "worktree") {
        if (tokens[2] !== "list") return false;
        continue;
      }
      if (!SAFE_GIT.has(sub)) return false;
      if (sub === "branch" && tokens.slice(2).some((t) => t.startsWith("-") && !SAFE_BRANCH_FLAGS.has(t))) return false;
      continue;
    }
    if (!SAFE_PROGRAMS.has(program)) return false;
    // sort -o / --output、find -delete / -exec 等会把“只读”变成写入。
    if (program === "sort" && tokens.slice(1).some((t) => t === "-o" || t.startsWith("--output"))) return false;
    if (program === "find" && tokens.slice(1).some((t) => DANGEROUS_FIND_ACTIONS.has(t))) return false;
  }
  return true;
}

// isBuiltinMcpTool 判断这次调用是否落在 mcode 自带的 MCP 服务上。
function isBuiltinMcpTool(toolName) {
  return String(toolName ?? "").startsWith(BUILTIN_MCP_TOOL_PREFIX);
}

function kindOf(toolName) {
  if (toolName === "bash") return "command";
  if (toolName === "edit" || toolName === "write") return "fileChange";
  return "tool";
}

const KIND_WORDS = { command: "command", fileChange: "file change", tool: "tool call" };

// safeRealpath 尝试解析符号链接，失败时回退为 resolve 绝对路径。
function safeRealpath(dir) {
  try {
    return fs.realpathSync(dir);
  } catch {
    return path.resolve(dir);
  }
}

// resolveNearestExisting 寻找目标路径最近的已存在父目录做 realpathSync，防止 symlink 绕过。
function resolveNearestExisting(targetPath) {
  let curr = path.resolve(targetPath);
  const remaining = [];
  while (true) {
    try {
      if (fs.existsSync(curr)) {
        const real = fs.realpathSync(curr);
        return remaining.length > 0 ? path.join(real, ...remaining) : real;
      }
    } catch {
      // 遇到无权限等错误继续向上找
    }
    const parent = path.dirname(curr);
    if (parent === curr) break;
    remaining.unshift(path.basename(curr));
    curr = parent;
  }
  return path.resolve(targetPath);
}

// isInsideOrEqual 判断 childPath 是否落在 parentDir 或为其自身。
function isInsideOrEqual(childPath, parentDir) {
  const rel = path.relative(parentDir, childPath);
  return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

// getControlPlaneDirs 获取控制面路径列表（目录或单个文件，包含原始绝对路径与其 realpath）。
function getControlPlaneDirs() {
  const dirs = [];
  const home = os.homedir();
  if (home) {
    dirs.push(path.join(home, ".pi", "agent"));
  }
  // 门禁文件由 gate.go 的 prepareGateFile 用 os.CreateTemp 建在系统临时目录，
  // 它的父目录就是 $TMPDIR，保护父目录会把临时目录下的普通写入都当成控制面调用。
  // 这里只保护门禁文件本身。
  if (process.env.MCODE_PI_GATE) {
    try {
      dirs.push(process.env.MCODE_PI_GATE);
    } catch {}
  }
  try {
    if (typeof import.meta !== "undefined" && import.meta.url) {
      dirs.push(path.dirname(fileURLToPath(import.meta.url)));
    }
  } catch {}

  const result = [];
  for (const d of dirs) {
    if (!d) continue;
    const abs = path.resolve(d);
    result.push(abs);
    const real = safeRealpath(abs);
    if (real && real !== abs) {
      result.push(real);
    }
  }
  return [...new Set(result)];
}

// isControlPlanePath 判断目标路径是否落在控制面路径（目录或门禁文件）中。相对路径按 process.cwd() 解析。
function isControlPlanePath(targetPath) {
  if (typeof targetPath !== "string" || !targetPath.trim()) return false;
  let expanded = targetPath.trim();
  const home = os.homedir();
  if (home && (expanded === "~" || expanded.startsWith("~/"))) {
    expanded = path.join(home, expanded.slice(2));
  }
  const abs = path.resolve(process.cwd(), expanded);
  const resolved = resolveNearestExisting(abs);
  const cpDirs = getControlPlaneDirs();
  for (const dir of cpDirs) {
    if (isInsideOrEqual(abs, dir) || isInsideOrEqual(resolved, dir)) {
      return true;
    }
  }
  return false;
}

// commandTouchesControlPlane 判断 bash 命令字符串中是否出现控制面目录的绝对路径。
function commandTouchesControlPlane(cmd) {
  if (typeof cmd !== "string" || !cmd) return false;
  const cpDirs = getControlPlaneDirs();
  for (const dir of cpDirs) {
    if (dir && cmd.includes(dir)) {
      return true;
    }
  }
  return false;
}

// matchesEnvFile 匹配敏感 .env 文件；传入项目目录时只匹配项目外的路径。
function matchesEnvFile(text, projectDir) {
  if (typeof text !== "string" || !text) return false;
  const envRegex = /(?:^|[\\/\s"'`=])\.env(?:\.([^\s\\/'"`=;:,&|]+))?(?=[\\/\s"'`=;:,&|]|$)/g;
  let m;
  while ((m = envRegex.exec(text)) !== null) {
    const ext = m[1]?.toLowerCase();
    if (ext === "example" || ext === "sample" || ext === "template") {
      continue;
    }
    if (projectDir) {
      const envIndex = m.index + m[0].indexOf(".env");
      const prefix = text.slice(0, envIndex).match(/[^\s"'`=;:,&|]*$/)?.[0] || "";
      const token = prefix + m[0].slice(m[0].indexOf(".env"));
      if (!token.startsWith("~") && isInsideOrEqual(path.resolve(projectDir, token), projectDir)) continue;
    }
    return true;
  }
  return false;
}

// touchesCredential 识别凭证路径及敏感凭据文件。
function touchesCredential(text, projectDir) {
  if (typeof text !== "string" || !text) return false;
  const home = os.homedir();
  const rawTargets = [
    "~/.ssh/",
    "/.ssh/",
    "id_rsa",
    "id_ed25519",
    "~/.aws/",
    "~/.config/gcloud",
    "~/.docker/config.json",
    "~/.kube/config",
    "~/.netrc",
    "~/.pi/agent/auth.json",
  ];

  for (const target of rawTargets) {
    if (text.includes(target)) return true;
    if (home && target.startsWith("~/")) {
      const rel = target.slice(2);
      const expanded = path.join(home, rel);
      const expandedWithSlash = target.endsWith("/") && !expanded.endsWith("/") ? expanded + "/" : expanded;
      if (text.includes(expandedWithSlash) || text.includes(expanded)) return true;
    }
  }

  // ~/.ssh 与 ~/.aws 即使末尾不带斜杠也按凭证识别
  if (text.includes("~/.ssh") || text.includes("~/.aws")) return true;
  if (home && (text.includes(path.join(home, ".ssh")) || text.includes(path.join(home, ".aws")))) return true;

  // /proc/<pid>/environ 与 /proc/self/environ
  if (/\/proc\/(?:self|\d+)\/environ\b/.test(text)) {
    return true;
  }

  // .env 或 .env.<x>（排除 .env.example、.env.sample、.env.template）
  if (matchesEnvFile(text, projectDir)) {
    return true;
  }

  return false;
}

// toolTouchesCredential 检查只读工具路径类入参（path、pattern、glob 等字段）是否命中凭证。
function toolTouchesCredential(input, projectDir) {
  if (!input || typeof input !== "object") return false;
  for (const [key, val] of Object.entries(input)) {
    if (typeof val === "string") {
      const lower = key.toLowerCase();
      if (
        lower.includes("path") ||
        lower.includes("pattern") ||
        lower.includes("glob") ||
        lower.includes("file") ||
        lower.includes("dir")
      ) {
        if (touchesCredential(val, projectDir)) return true;
      }
    }
  }
  return false;
}

// checkSensitive 判定本次调用是否属于凭证读取或控制面写入。
function checkSensitive(event, kind, gate, cwd) {
  const projectDir = gate.runtimeMode === "fullAccess" ? path.resolve(cwd) : undefined;
  // 控制面写入防护
  if (kind === "fileChange") {
    const target = event.input?.path || event.input?.file_path;
    if (typeof target === "string" && isControlPlanePath(target)) {
      return "controlPlane";
    }
  }
  if (kind === "command") {
    const cmd = event.input?.command;
    // 控制面只防写：读会话、配置不算敏感，凭证另有判断。
    if (typeof cmd === "string" && commandTouchesControlPlane(cmd) && !onlyReads(cmd)) {
      return "controlPlane";
    }
  }

  // 凭证路径识别
  if (LOOK_ONLY_TOOLS.has(event.toolName)) {
    if (toolTouchesCredential(event.input, projectDir)) {
      return "credential";
    }
  }
  if (kind === "command") {
    const cmd = event.input?.command;
    if (typeof cmd === "string" && touchesCredential(cmd, projectDir)) {
      return "credential";
    }
  }
  if (kind === "fileChange") {
    const target = event.input?.path || event.input?.file_path;
    if (typeof target === "string" && touchesCredential(target, projectDir)) {
      return "credential";
    }
  }

  return null;
}

// verdictWithoutAsking 返回 "allow"、一个拦截结果，或 null 表示需要去问。
function verdictWithoutAsking(gate, event, kind, sensitive) {
  if (gate.finalReport) return { block: true, reason: "Blocked by mcode: this turn is only for the final report, so tools are off." };
  if (gate.interactionMode === "plan") {
    // 计划模式仍要能调研：只看不写的工具、只读 shell 命令（ls / cat / rg / git status 等）放行，
    // 其余改动一律拦下；mcode 自带 MCP 工具既不在这里拦也不在这里放行，
    // 而是落到下面的常规判定，由 runtimeMode 决定放行还是询问。
    const researches =
      LOOK_ONLY_TOOLS.has(event.toolName) ||
      (kind === "command" && onlyReads(event.input?.command));
    if (researches) {
      // 计划模式下能调研的调用：触碰凭证或控制面需要询问，其余放行
      if (sensitive) return null;
      return "allow";
    }
    if (!isBuiltinMcpTool(event.toolName)) {
      return { block: true, reason: "Blocked by mcode: plan mode only allows reading." };
    }
  }
  // 敏感请求一律询问，不自动放行
  if (sensitive) return null;

  if (LOOK_ONLY_TOOLS.has(event.toolName)) return "allow";
  const mode = gate.runtimeMode;
  if (mode === "fullAccess") return "allow";
  if (kind === "fileChange" && (mode === "auto" || mode === "autoAcceptEdits")) return "allow";
  if (kind === "command" && (mode === "auto" || mode === "readonly") && onlyReads(event.input?.command)) return "allow";
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

// visionText 统一 vision 的返回形状，并截断超长输出（整段日志对模型没有价值）。
function visionText(text) {
  const raw = String(text ?? "");
  const clipped =
    raw.length > VISION_MAX_RESULT_CHARS ? raw.slice(0, VISION_MAX_RESULT_CHARS) + "\n[output truncated]" : raw;
  return { content: [{ type: "text", text: clipped }], details: {} };
}

// describeError 取错误里可读的那句话。
function describeError(err) {
  return err instanceof Error ? err.message : String(err);
}

// visionBridgePath 返回要带进 vision 子进程的 Magpie 桥路径。桥路径与端点清单缺一不可，
// 且桥文件必须真的存在；否则不加 -e，子进程就像以前一样不看任何扩展。
function visionBridgePath(env) {
  const source = env || {};
  const bridge = String(source[MAGPIE_BRIDGE_ENV] ?? "").trim();
  const endpoints = String(source[MAGPIE_ENV] ?? "").trim();
  if (!bridge || !endpoints) return "";
  try {
    if (!fs.existsSync(bridge)) return "";
  } catch {
    return "";
  }
  return bridge;
}

// buildVisionArgs 组装 vision 子进程的参数（纯函数，便于测试）。
// --no-extensions 只屏蔽自动发现、配置与内置扩展，显式 -e 的路径仍会加载（pi 1.3 文档与
// resource-loader 的实现都如此），所以 Magpie 桥照样能进这个干净进程。
export function buildVisionArgs(model, prompt, imagePath, env) {
  const bridge = visionBridgePath(env);
  return [
    "-p",
    "--no-session",
    "--no-extensions",
    ...(bridge ? ["-e", bridge] : []),
    "--no-skills",
    "--no-prompt-templates",
    "--no-context-files",
    "--no-mcp",
    "--no-tools",
    "--no-approve",
    "--model",
    model || "",
    prompt,
    "@" + imagePath,
  ];
}

// runVisionModel 启动一个干净的 pi 子进程，让它用外部视觉模型描述图片。
// pi 的非交互模式（-p）支持用 @<路径> 把图片附进首条提示；子进程同时关掉会话、技能、
// MCP 与全部工具，只做一次看图（扩展只保留 Magpie 桥，路由模型才找得到）。
function runVisionModel(imagePath, prompt, cwd, signal) {
  return new Promise((resolve) => {
    // 用当前进程的可执行文件与入口脚本启动，保证调用的是同一个 pi。
    // bun 编译版没有入口脚本，只给可执行文件。
    const entry = process.argv[1];
    const useEntry = !process.versions?.bun && typeof entry === "string" && fs.existsSync(entry);
    const args = [
      ...(useEntry ? [entry] : []),
      ...buildVisionArgs(process.env[VISION_MODEL_ENV] || "", prompt, imagePath, process.env),
    ];

    const env = { ...process.env };
    // 子进程不再过门禁，也不会注册自己的 vision：去掉这两项，避免递归。
    // MCODE_PI_MAGPIE 与 MCODE_PI_MAGPIE_BRIDGE 保留，桥要靠它们注册路由模型。
    delete env.MCODE_PI_GATE;
    delete env[VISION_MODEL_ENV];

    let child;
    try {
      child = spawn(process.execPath, args, { cwd, env, stdio: ["ignore", "pipe", "pipe"] });
    } catch (err) {
      resolve({ error: `vision: failed to start pi: ${describeError(err)}` });
      return;
    }

    let stdout = "";
    let stderr = "";
    let settled = false;
    let timedOut = false;
    let timer = null;

    const finish = (value) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      if (typeof signal?.removeEventListener === "function") signal.removeEventListener("abort", onAbort);
      resolve(value);
    };
    const kill = () => {
      try {
        child.kill("SIGKILL");
      } catch {}
    };
    const onAbort = () => {
      kill();
      finish({ error: "vision: the call was cancelled" });
    };

    child.stdout?.on("data", (chunk) => {
      stdout += chunk.toString("utf8");
    });
    child.stderr?.on("data", (chunk) => {
      stderr += chunk.toString("utf8");
    });
    child.on("error", (err) => finish({ error: `vision: failed to run pi: ${describeError(err)}` }));
    child.on("close", (code, killedBy) => {
      if (settled) return;
      if (timedOut) {
        finish({ error: `vision: pi did not answer within ${VISION_TIMEOUT_MS / 1000} seconds` });
        return;
      }
      if (code === 0) {
        const text = stdout.trim();
        finish(text ? { text } : { error: "vision: pi returned no text" });
        return;
      }
      const detail = (stderr.trim().length > VISION_MAX_STDERR_CHARS
        ? stderr.trim().slice(0, VISION_MAX_STDERR_CHARS) + "…"
        : stderr.trim());
      const reason = code === null ? `killed by ${killedBy || "signal"}` : `exit code ${code}`;
      finish({ error: `vision: pi failed (${reason}): ${detail}` });
    });

    timer = setTimeout(() => {
      timedOut = true;
      kill();
    }, VISION_TIMEOUT_MS);
    if (signal) {
      if (signal.aborted) onAbort();
      else if (typeof signal.addEventListener === "function") signal.addEventListener("abort", onAbort);
    }
  });
}

// 计划模式下门禁会拦掉所有非只读调用；提前告诉模型，免得它先试写再被拒。
const PLAN_PROMPT =
  "Plan mode is on, so this turn is read-only: no file edits and no commands that change state. " +
  "You can still investigate: read files, use grep / find / ls, run read-only shell commands " +
  "(ls, cat, rg, git status, ...), and call the mcode MCP tools. " +
  "Look into the task, then finish with the plan written in markdown.";

export default async function (pi) {
  // 视觉模型由上层在用户配置后注入；没配置时下面的 vision 工具完全不注册，模型也就看不到它。
  const visionModel = String(process.env[VISION_MODEL_ENV] ?? "").trim();

  // 注册 ask_user_question 工具：需要用户做决定或补充信息时由模型调用
  pi.registerTool({
    name: "ask_user_question",
    label: "Ask user a question",
    description:
      "Ask the user a question when a decision or additional information is needed to proceed. " +
      "Call this tool instead of asking questions in the regular text message and ending your turn.",
    parameters: Type.Object({
      question: Type.String({ description: "The question to ask the user." }),
      options: Type.Optional(
        Type.Array(Type.String(), {
          maxItems: 6,
          description: "Optional list of choices for the user to select from (maximum 6 choices).",
        })
      ),
    }),
    async execute(_toolCallId, params, _signal, _onUpdate, ctx) {
      const question = String(params?.question ?? "");
      const rawOptions = params?.options;
      const options =
        Array.isArray(rawOptions) && rawOptions.length > 0
          ? rawOptions.slice(0, 6).map(String)
          : undefined;

      let answer;
      if (options && options.length > 0) {
        answer = ctx?.ui?.select ? await ctx.ui.select(question, options) : undefined;
      } else {
        answer = ctx?.ui?.input ? await ctx.ui.input(question) : undefined;
      }

      const text = typeof answer === "string" && answer.trim() ? answer : "The user did not answer.";
      return {
        content: [{ type: "text", text }],
        details: {},
      };
    },
  });

  // 注册 vision 工具：pi 自己看不到图片时，交给外部多模态模型描述本地图片。
  if (visionModel) {
    pi.registerTool({
      name: "vision",
      label: "Vision",
      description:
        "Describe a local image with an external multimodal model. Use it for screenshots, UI mockups, " +
        "diagrams, and charts. Use it only when you cannot see the image content yourself.",
      parameters: Type.Object({
        path: Type.String({
          description: "Absolute image path, or a path relative to the current working directory.",
        }),
        query: Type.Optional(Type.String({ description: "Optional: what to focus on in the image." })),
      }),
      async execute(_toolCallId, params, signal, _onUpdate, ctx) {
        const rawPath = typeof params?.path === "string" ? params.path.trim() : "";
        if (!rawPath) {
          return visionText("vision: path is required");
        }

        // 相对路径按本轮 cwd 解析；绝对路径 path.resolve 会原样保留。
        const baseDir = typeof ctx?.cwd === "string" && ctx.cwd ? ctx.cwd : process.cwd();
        const imagePath = path.resolve(baseDir, rawPath);
        const ext = path.extname(imagePath).toLowerCase();
        if (!VISION_IMAGE_EXTENSIONS.has(ext)) {
          return visionText(
            `vision: unsupported image type "${ext || "none"}": use .png, .jpg, .jpeg, .gif, or .webp`
          );
        }

        let stat = null;
        try {
          stat = fs.statSync(imagePath);
        } catch {
          stat = null;
        }
        if (!stat || !stat.isFile()) {
          return visionText(`vision: image not found: ${imagePath}`);
        }

        const query = typeof params?.query === "string" ? params.query.trim() : "";
        const prompt = query ? `${VISION_PROMPT}\n\nFocus on: ${query}` : VISION_PROMPT;
        const result = await runVisionModel(imagePath, prompt, baseDir, signal);
        return visionText(result.error || result.text);
      },
    });
  }

  // 驻留进程跨轮复用，所以每次 run 开始都重读本轮的 gate 文件。直接改可变的 systemPromptOptions，
  // 不返回 systemPrompt（返回会整段替换，把项目上下文与 skills 一起丢掉）。
  pi.on("before_agent_start", async (event, ctx) => {
    // 当前模型自己能看图时 vision 就是多余的，从本轮活动工具里拿掉；看不到图时保留。
    if (visionModel && ctx?.model?.input?.includes?.("image")) {
      try {
        if (typeof pi.getActiveTools === "function" && typeof pi.setActiveTools === "function") {
          const active = pi.getActiveTools();
          if (Array.isArray(active) && active.includes("vision")) {
            pi.setActiveTools(active.filter((name) => name !== "vision"));
          }
        }
      } catch {}
    }

    if (loadTurnGate().interactionMode !== "plan") return;
    const options = event.systemPromptOptions;
    if (!options) return;
    options.appendSystemPrompt = [options.appendSystemPrompt, PLAN_PROMPT].filter(Boolean).join("\n\n");
  });

  pi.on("tool_call", async (event, ctx) => {
    // 处理 bash 默认超时与上限
    if (event.toolName === "bash") {
      if (!event.input || typeof event.input !== "object") {
        event.input = {};
      }
      const t = event.input.timeout;
      if (typeof t !== "number" || !Number.isFinite(t) || t <= 0) {
        event.input.timeout = BASH_DEFAULT_TIMEOUT_SECONDS;
      } else if (t > BASH_MAX_TIMEOUT_SECONDS) {
        event.input.timeout = BASH_MAX_TIMEOUT_SECONDS;
      }
    }

    const kind = kindOf(event.toolName);
    const gate = loadTurnGate();
    const cwd = typeof ctx?.cwd === "string" && ctx.cwd ? ctx.cwd : process.cwd();
    const sensitive = checkSensitive(event, kind, gate, cwd);
    const verdict = verdictWithoutAsking(gate, event, kind, sensitive);
    if (verdict === "allow") return;
    if (verdict) return verdict;
    const payload = {
      kind,
      toolName: event.toolName,
      toolCallId: event.toolCallId,
      input: clippedInput(event.input),
      ...(sensitive ? { sensitive } : {}),
    };
    // 敏感调用不提供会话级放行选项，必须每次确认
    const options = sensitive ? ["Allow", "Deny"] : ["Allow", "Allow for session", "Deny"];
    // rpc 模式下这次选择以 extension_ui_request 回到适配器。
    const answer = await ctx.ui.select("mcode-gate " + JSON.stringify(payload), options);
    if (answer === "Allow" || answer === "Allow for session") return;
    return { block: true, reason: `Blocked by mcode: the user rejected this ${KIND_WORDS[kind]}.` };
  });
}
