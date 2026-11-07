// mcode MCP bridge, loaded into pi as a second `-e` extension (see mcp.go).
//
// MCODE_MCP_SERVERS holds a JSON list [{name, url, headers}] of Streamable HTTP servers.
// Built-in mcode server tools are registered directly as mcp__mcode__<tool>.
// External MCP servers are exposed via a two-tier gateway:
//   - mcode_mcp_list: lists external tools with one-line descriptions or returns inputSchema
//   - mcode_mcp_call: forwards calls to tools/call on the target server
import { Type } from "typebox";

const MCP_PROTOCOL = "2025-06-18";
const SETUP_LIMIT_MS = 15000;
// BUILTIN_SERVER is the server name mcode injects for its own MCP tools (see mcp.go).
const BUILTIN_SERVER = "mcode";
const LEAN_DESCRIPTION_LIMIT = 200;
const DEFAULT_PAGE_LIMIT = 20;
const MAX_PAGE_LIMIT = 100;

// leanDescription shortens mcode's own tool descriptions, which are written for humans and
// would otherwise cost context every turn. Only the first sentence within the limit is kept.
function leanDescription(text) {
  const flat = String(text ?? "").replace(/\s+/g, " ").trim();
  if (flat.length <= LEAN_DESCRIPTION_LIMIT) return flat;
  const head = flat.slice(0, LEAN_DESCRIPTION_LIMIT);
  const end = head.search(/\.\s|。|！|？|\n/);
  if (end >= 0) return head.slice(0, end + 1).trim();
  return head + "…";
}

// oneLineDescription 为外部 MCP 工具列表生成精简的单行描述，避免 schema 占满模型上下文。
function oneLineDescription(text) {
  const flat = String(text ?? "").replace(/\s+/g, " ").trim();
  if (!flat) return "";
  const end = flat.search(/\.\s|。|！|？|\n/);
  if (end >= 0 && end <= 160) return flat.slice(0, end + 1).trim();
  return flat.length > 160 ? flat.slice(0, 160) + "…" : flat;
}

function describeError(error) {
  return error instanceof Error ? error.message : String(error);
}

function readServerList() {
  const raw = process.env.MCODE_MCP_SERVERS;
  if (!raw) return [];
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch (error) {
    console.error("mcode: MCODE_MCP_SERVERS is not valid JSON: " + describeError(error));
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  const servers = [];
  for (const item of parsed) {
    if (!item || typeof item !== "object" || typeof item.url !== "string") continue;
    servers.push({
      name: String(item.name ?? ""),
      url: item.url,
      headers: item.headers && typeof item.headers === "object" ? item.headers : {},
    });
  }
  return servers;
}

// flattenContent turns an MCP content list into plain text; non-text parts become a placeholder.
function flattenContent(content) {
  if (!Array.isArray(content)) return "";
  return content
    .map((part) => (part?.type === "text" && typeof part.text === "string" ? part.text : `[${part?.type || "unsupported"} content]`))
    .join("\n");
}

// pickRpcReply finds the JSON-RPC reply with the given id in a JSON or SSE response body.
function pickRpcReply(body, contentType, id) {
  const candidates = contentType.includes("text/event-stream")
    ? body.split(/\r?\n/).filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trim())
    : [body];
  for (const candidate of candidates) {
    if (!candidate) continue;
    try {
      const message = JSON.parse(candidate);
      if (message && message.id === id && ("result" in message || "error" in message)) return message;
    } catch {}
  }
  return undefined;
}

// HttpSession is one Streamable HTTP connection to a server.
class HttpSession {
  constructor(url, headers) {
    this.url = url;
    this.headers = headers;
    this.sessionId = "";
    this.negotiated = "";
    this.counter = 0;
  }

  async post(payload) {
    const headers = {
      ...this.headers,
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
    };
    if (this.sessionId) headers["Mcp-Session-Id"] = this.sessionId;
    if (this.negotiated) headers["MCP-Protocol-Version"] = this.negotiated;
    const response = await fetch(this.url, { method: "POST", headers, body: JSON.stringify(payload) });
    this.sessionId = response.headers.get("mcp-session-id") || this.sessionId;
    return response;
  }

  async request(method, params = {}) {
    const id = ++this.counter;
    const response = await this.post({ jsonrpc: "2.0", id, method, params });
    const reply = pickRpcReply(await response.text(), response.headers.get("content-type") || "", id);
    if (!reply) throw new Error(`${method}: no JSON-RPC reply (HTTP ${response.status})`);
    if (reply.error) throw new Error(`${method}: ${reply.error.message || "request failed"}`);
    if (method === "initialize") this.negotiated = String(reply.result?.protocolVersion || MCP_PROTOCOL);
    return reply.result;
  }

  async notify(method) {
    try {
      const response = await this.post({ jsonrpc: "2.0", method });
      await response.text();
    } catch {}
  }
}

async function listAllTools(session) {
  const tools = [];
  let cursor;
  do {
    const page = await session.request("tools/list", cursor ? { cursor } : {});
    if (Array.isArray(page?.tools)) tools.push(...page.tools);
    cursor = page?.nextCursor || undefined;
  } while (cursor);
  return tools;
}

async function runTool(session, name, args) {
  try {
    const result = await session.request("tools/call", { name, arguments: args && typeof args === "object" ? args : {} });
    const text = flattenContent(result?.content);
    return result?.isError ? "Error: " + (text || "tool failed") : text;
  } catch (error) {
    return "Error: " + describeError(error);
  }
}

async function bridgeServer(entry) {
  const session = new HttpSession(entry.url, entry.headers);
  await session.request("initialize", { protocolVersion: MCP_PROTOCOL, capabilities: {}, clientInfo: { name: "mcode", version: "1" } });
  await session.notify("notifications/initialized");
  const tools = await listAllTools(session);
  return {
    server: entry.name,
    session,
    tools: tools.map((tool) => {
      const name = String(tool.name);
      return {
        server: entry.name,
        name,
        description: typeof tool.description === "string" && tool.description ? tool.description : name,
        inputSchema: tool.inputSchema && typeof tool.inputSchema === "object" ? tool.inputSchema : { type: "object" },
      };
    }),
  };
}

function withinLimit(work) {
  let timer;
  const expired = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`no answer within ${SETUP_LIMIT_MS / 1000}s`)), SETUP_LIMIT_MS);
  });
  return Promise.race([work, expired]).finally(() => clearTimeout(timer));
}

// loadMCPTools 连接所有配置的 MCP 服务；内置服务工具与外部服务工具分开维护。
export async function loadMCPTools() {
  const servers = readServerList();
  const outcomes = await Promise.allSettled(servers.map((entry) => withinLimit(bridgeServer(entry))));
  const builtinTools = [];
  const externalTools = [];
  const sessions = new Map();
  const serverErrors = new Map();

  outcomes.forEach((outcome, index) => {
    const entry = servers[index];
    if (outcome.status === "fulfilled") {
      const { session, tools } = outcome.value;
      sessions.set(entry.name, session);
      for (const tool of tools) {
        const item = {
          ...tool,
          call: (args) => runTool(session, tool.name, args),
        };
        if (entry.name === BUILTIN_SERVER) {
          builtinTools.push(item);
        } else {
          externalTools.push(item);
        }
      }
    } else {
      const err = describeError(outcome.reason);
      serverErrors.set(entry.name, err);
      console.error(`mcode: MCP server ${entry.name || entry.url} skipped: ${err}`);
    }
  });

  return { builtinTools, externalTools, sessions, serverErrors, servers };
}

export default async function (pi) {
  // 扩展加载过程中的异常只记录日志，不中断会话。
  try {
    const { builtinTools, externalTools, sessions, serverErrors, servers } = await loadMCPTools();

    // 1. 内置 mcode 服务的工具仍然全量直接注册为 mcp__mcode__<tool>
    for (const tool of builtinTools) {
      pi.registerTool({
        name: `mcp__${tool.server}__${tool.name}`,
        label: `${tool.server} · ${tool.name}`,
        description: leanDescription(tool.description),
        parameters: Type.Unsafe(tool.inputSchema),
        async execute(_toolCallId, params) {
          return { content: [{ type: "text", text: (await tool.call(params)) || "(no output)" }], details: {} };
        },
      });
    }

    // 2. 其余外部服务走两级网关：只注册 mcode_mcp_list 和 mcode_mcp_call
    const hasExternal = servers.some((s) => s.name !== BUILTIN_SERVER);
    if (hasExternal) {
      pi.registerTool({
        name: "mcode_mcp_list",
        label: "mcode · mcp list",
        description:
          "按 MCP 服务名称与关键词分页列出外部 MCP 工具清单及单行描述；当同时提供 server 与 tool 参数时，返回该工具的完整 inputSchema 参数架构。",
        parameters: Type.Object({
          server: Type.Optional(Type.String({ description: "外部 MCP 服务名称（可选，用于过滤）" })),
          tool: Type.Optional(
            Type.String({ description: "工具名称（可选）。若同时提供 server 与 tool，则返回该工具的 inputSchema 参数架构" })
          ),
          keyword: Type.Optional(Type.String({ description: "搜索关键词（可选），按名称或描述模糊匹配" })),
          offset: Type.Optional(Type.Integer({ minimum: 0, description: "分页起始偏移量（默认 0）" })),
          limit: Type.Optional(Type.Integer({ minimum: 1, maximum: MAX_PAGE_LIMIT, description: "每页返回数量上限（默认 20，最大 100）" })),
        }),
        async execute(_toolCallId, params) {
          const reqServer = params?.server ? String(params.server).trim() : "";
          const reqTool = params?.tool ? String(params.tool).trim() : "";

          // 传 server 和 tool 时返回该工具的 inputSchema
          if (reqTool) {
            let matched = externalTools.find((t) => (reqServer ? t.server === reqServer : true) && t.name === reqTool);
            if (!matched && reqServer === BUILTIN_SERVER) {
              matched = builtinTools.find((t) => t.name === reqTool);
            }
            if (matched) {
              const info = {
                server: matched.server,
                tool: matched.name,
                description: matched.description,
                inputSchema: matched.inputSchema,
              };
              return {
                content: [{ type: "text", text: JSON.stringify(info, null, 2) }],
                details: info,
              };
            }
            if (reqServer && serverErrors.has(reqServer)) {
              return {
                content: [{ type: "text", text: `Error: MCP server "${reqServer}" failed to connect: ${serverErrors.get(reqServer)}` }],
                details: {},
              };
            }
            return {
              content: [
                {
                  type: "text",
                  text: `Error: Tool "${reqTool}" not found${reqServer ? ` in server "${reqServer}"` : ""}.`,
                },
              ],
              details: {},
            };
          }

          // 列表模式：按 server 和 keyword 过滤并分页
          let list = externalTools;
          if (reqServer) {
            list = list.filter((t) => t.server === reqServer);
          }
          const kw = params?.keyword ? String(params.keyword).trim().toLowerCase() : "";
          if (kw) {
            list = list.filter(
              (t) =>
                t.name.toLowerCase().includes(kw) ||
                t.description.toLowerCase().includes(kw) ||
                t.server.toLowerCase().includes(kw)
            );
          }

          const offset = Math.max(0, Number(params?.offset) || 0);
          const limit = Math.min(MAX_PAGE_LIMIT, Math.max(1, Number(params?.limit) || DEFAULT_PAGE_LIMIT));
          const total = list.length;
          const slice = list.slice(offset, offset + limit);

          if (total === 0) {
            let msg = "No external MCP tools found";
            if (reqServer) msg += ` for server "${reqServer}"`;
            if (kw) msg += ` matching keyword "${kw}"`;
            msg += ".";
            if (reqServer && serverErrors.has(reqServer)) {
              msg += ` (Server connection error: ${serverErrors.get(reqServer)})`;
            }
            return { content: [{ type: "text", text: msg }], details: { total: 0 } };
          }

          const lines = slice.map((t) => `${t.server} · ${t.name}: ${oneLineDescription(t.description)}`);
          lines.push(`\nShowing ${offset + 1}-${offset + slice.length} of ${total} external MCP tools.`);
          if (offset + slice.length < total) {
            lines.push(`Use offset=${offset + slice.length} to view the next page.`);
          }
          lines.push("Call mcode_mcp_list with server and tool to view a tool's full inputSchema.");
          lines.push("Call mcode_mcp_call with server, tool, and arguments to execute a tool.");

          return { content: [{ type: "text", text: lines.join("\n") }], details: { total, offset, count: slice.length } };
        },
      });

      // 注册 mcode_mcp_call
      pi.registerTool({
        name: "mcode_mcp_call",
        label: "mcode · mcp call",
        description: "调用外部 MCP 服务提供的工具。参数为目标服务名称 server、工具名称 tool 以及参数对象 arguments。",
        parameters: Type.Object({
          server: Type.String({ description: "目标 MCP 服务名称" }),
          tool: Type.String({ description: "要调用的目标工具名称" }),
          arguments: Type.Optional(Type.Record(Type.String(), Type.Any(), { description: "传递给目标工具的参数键值对象" })),
        }),
        async execute(_toolCallId, params) {
          const targetServer = params?.server ? String(params.server).trim() : "";
          const targetTool = params?.tool ? String(params.tool).trim() : "";
          if (!targetServer || !targetTool) {
            return { content: [{ type: "text", text: "Error: server and tool parameters are required." }], details: {} };
          }

          const session = sessions.get(targetServer);
          if (!session) {
            if (serverErrors.has(targetServer)) {
              return {
                content: [{ type: "text", text: `Error: MCP server "${targetServer}" failed to connect: ${serverErrors.get(targetServer)}` }],
                details: {},
              };
            }
            const available = Array.from(sessions.keys()).join(", ") || "none";
            return {
              content: [{ type: "text", text: `Error: MCP server "${targetServer}" not found or not connected. Available: ${available}` }],
              details: {},
            };
          }

          const args = params?.arguments && typeof params.arguments === "object" ? params.arguments : {};
          const out = await runTool(session, targetTool, args);
          return { content: [{ type: "text", text: out || "(no output)" }], details: {} };
        },
      });
    }
  } catch (error) {
    console.error("mcode: MCP bridge failed: " + describeError(error));
  }
}
