import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

if (!process.argv[2]) {
  console.error("Usage: node gate_driver.mjs <path-to-mcode-gate.ts>");
  process.exit(1);
}

const gateTsPath = path.resolve(process.argv[2]);
if (!fs.existsSync(gateTsPath)) {
  console.error(`mcode-gate source file not found: ${gateTsPath}`);
  process.exit(1);
}

// 确保在 import 之前已设置 MCODE_PI_GATE 环境变量，且所在目录与文件存在
if (!process.env.MCODE_PI_GATE) {
  const tmpGateDir = fs.mkdtempSync(path.join(os.tmpdir(), "mcode-pi-gate-"));
  process.env.MCODE_PI_GATE = path.join(tmpGateDir, "gate.json");
}
fs.mkdirSync(path.dirname(process.env.MCODE_PI_GATE), { recursive: true });
if (!fs.existsSync(process.env.MCODE_PI_GATE)) {
  fs.writeFileSync(process.env.MCODE_PI_GATE, "{}", "utf8");
}

const tempModDir = fs.mkdtempSync(path.join(os.tmpdir(), "mcode-gate-mod-"));

// 准备 typebox 模块依赖
const nodeModulesDir = path.join(tempModDir, "node_modules");
fs.mkdirSync(nodeModulesDir, { recursive: true });
const typeboxCandidates = [
  path.join(path.dirname(process.execPath), "../lib/node_modules/@earendil-works/pi-coding-agent/node_modules/typebox"),
  path.join(path.dirname(process.execPath), "../lib/node_modules/typebox"),
];
let typeboxFound = false;
for (const cand of typeboxCandidates) {
  if (fs.existsSync(cand)) {
    try {
      fs.symlinkSync(cand, path.join(nodeModulesDir, "typebox"), "dir");
      typeboxFound = true;
      break;
    } catch {}
  }
}
if (!typeboxFound) {
  const tbDir = path.join(nodeModulesDir, "typebox");
  fs.mkdirSync(tbDir, { recursive: true });
  fs.writeFileSync(
    path.join(tbDir, "package.json"),
    JSON.stringify({
      name: "typebox",
      type: "module",
      exports: { ".": { import: "./index.mjs" } },
    })
  );
  fs.writeFileSync(
    path.join(tbDir, "index.mjs"),
    `export const Type = {
  Object: (props, opts) => ({ type: "object", properties: props, ...opts }),
  String: (opts) => ({ type: "string", ...opts }),
  Optional: (schema) => ({ ...schema, optional: true }),
  Array: (items, opts) => ({ type: "array", items, ...opts }),
  Unsafe: (schema) => schema,
};
`
  );
}

process.on("exit", () => {
  try {
    fs.rmSync(tempModDir, { recursive: true, force: true });
  } catch {}
});

// loadGate 按给定的环境变量装载一份门禁扩展。扩展在注册阶段就会读环境变量
// （例如 MCODE_PI_VISION_MODEL 决定要不要注册 vision），所以每换一组环境就要一份全新的
// 模块副本；同一组环境复用第一次装载的结果。
const loadedVariants = new Map();
let variantCount = 0;
async function loadGate(envOverrides) {
  const key = JSON.stringify(envOverrides || {});
  const cached = loadedVariants.get(key);
  if (cached) {
    return cached;
  }

  for (const [name, value] of Object.entries(envOverrides || {})) {
    if (value === null || value === undefined) {
      delete process.env[name];
    } else {
      process.env[name] = String(value);
    }
  }

  // 为稳妥兼容各类 node 版本，把 mcode-gate.ts 复制为临时的 .mjs 动态 import；
  // 副本仍放在 tempModDir 下，这样 node_modules/typebox 能正常解析。
  const variantPath = path.join(tempModDir, `mcode-gate-${variantCount++}.mjs`);
  fs.copyFileSync(gateTsPath, variantPath);

  const handlers = {};
  const tools = [];
  const fakePi = {
    on(event, handler) {
      if (!handlers[event]) {
        handlers[event] = [];
      }
      handlers[event].push(handler);
    },
    registerTool(tool) {
      tools.push(tool);
    },
  };

  const gateMod = await import(pathToFileURL(variantPath).href);
  const initFn = gateMod.default || gateMod;
  await initFn(fakePi);

  const loaded = { handlers, tools, toolNames: tools.map((t) => t.name), buildVisionArgs: gateMod.buildVisionArgs };
  loadedVariants.set(key, loaded);
  return loaded;
}

// 从 stdin 读取测试用例 JSON 数组
const chunks = [];
for await (const chunk of process.stdin) {
  chunks.push(chunk);
}
const rawInput = Buffer.concat(chunks).toString("utf8").trim();
const cases = rawInput
  ? JSON.parse(rawInput)
  : [
      // supervised 模式下 mcode_mcp_list 列举工具直接放行
      {
        id: "default_mcp_list_supervised",
        gate: { runtimeMode: "supervised" },
        toolName: "mcode_mcp_list",
        input: {},
      },
    ];

const results = [];
for (let i = 0; i < cases.length; i++) {
  const tc = cases[i];
  // 用例里的 env 会在装载扩展前生效，并在结果里带回本次真正注册的工具名。
  const gate = await loadGate(tc.env);
  const toolCallHandler = (gate.handlers["tool_call"] || [])[0];

  // vision 参数用例：不启动 pi，只用给定环境算一遍 buildVisionArgs 的参数列表。
  if (tc.visionArgs) {
    const visionCase = tc.visionArgs;
    results.push({
      id: tc.id || "",
      result: "visionArgs",
      visionArgs: gate.buildVisionArgs(
        visionCase.model || "",
        visionCase.prompt || "",
        visionCase.imagePath || "",
        visionCase.env || {}
      ),
      registeredTools: gate.toolNames,
    });
    continue;
  }

  if (tc.execute) {
    const tool = gate.tools.find((t) => t.name === tc.toolName);
    if (!tool) {
      results.push({
        id: tc.id || "",
        result: "error",
        error: `Tool ${tc.toolName} not registered`,
        input: tc.input || {},
        registeredTools: gate.toolNames,
      });
      continue;
    }

    let uiMethod = "";
    let uiArgs = [];
    const execCtx = {
      ui: {
        select: async (question, options) => {
          uiMethod = "select";
          uiArgs = [question, options];
          return tc.uiAnswer;
        },
        input: async (question) => {
          uiMethod = "input";
          uiArgs = [question];
          return tc.uiAnswer;
        },
      },
    };

    const execRes = await tool.execute(tc.toolCallId || `tc-${i}`, tc.input, undefined, undefined, execCtx);
    results.push({
      id: tc.id || "",
      result: "execute",
      uiMethod,
      uiArgs,
      executeResult: execRes,
      input: tc.input || {},
      registeredTools: gate.toolNames,
    });
    continue;
  }

  if (!toolCallHandler) {
    results.push({
      id: tc.id || "",
      result: "error",
      error: "tool_call handler not registered",
      input: tc.input || {},
      registeredTools: gate.toolNames,
    });
    continue;
  }

  // 逐个写 gate 文件并设置 process.env.MCODE_PI_GATE
  fs.writeFileSync(process.env.MCODE_PI_GATE, JSON.stringify(tc.gate || {}), "utf8");

  let selectCalled = false;
  let selectTitle = "";
  let selectOptions = null;

  const ctx = {
    ui: {
      select: async (title, options) => {
        selectCalled = true;
        selectTitle = title;
        selectOptions = options;
        return "Deny";
      },
    },
  };

  const event = {
    toolName: tc.toolName,
    input: tc.input ? { ...tc.input } : {},
    toolCallId: tc.toolCallId || `tc-${i}`,
  };

  const toolResult = await toolCallHandler(event, ctx);

  let sensitive = "";
  if (selectCalled && selectTitle.startsWith("mcode-gate ")) {
    try {
      const payload = JSON.parse(selectTitle.slice("mcode-gate ".length));
      if (payload.sensitive) {
        sensitive = payload.sensitive;
      }
    } catch {}
  }

  let result = "allow";
  if (selectCalled) {
    result = "ask";
  } else if (toolResult && toolResult.block) {
    result = "block";
  } else {
    result = "allow";
  }

  results.push({
    id: tc.id || "",
    result,
    sensitive,
    options: selectOptions || [],
    input: event.input,
    registeredTools: gate.toolNames,
  });
}

// 顶层 registeredTools 取第一份装载结果，保持原先的兼容行为。
const firstGate = loadedVariants.values().next().value;

process.stdout.write(
  JSON.stringify({
    registeredTools: firstGate ? firstGate.toolNames : [],
    results,
  })
);
