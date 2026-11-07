// mcode MCP bridge, loaded into Command Code as an extra `--mod` (see mcp.go).
//
// MCODE_MCP_SERVERS holds a JSON list [{name, url, headers}] of Streamable HTTP servers.
// Each server gets its own session; every tool it lists is registered as
// mcp__<server>__<tool> and forwards to tools/call. A server that fails to start is
// logged and skipped, the rest still load.

type ServerEntry = { name: string, url: string, headers: Record<string, string> }
type BridgedTool = { server: string, name: string, description: string, inputSchema: any, call: (args: any) => Promise<string> }

const MCP_PROTOCOL = "2025-06-18"
const SETUP_LIMIT_MS = 15000

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function readServerList(): ServerEntry[] {
  const raw = process.env.MCODE_MCP_SERVERS
  if (!raw) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch (error) {
    console.error("mcode: MCODE_MCP_SERVERS is not valid JSON: " + describeError(error))
    return []
  }
  if (!Array.isArray(parsed)) return []
  const servers: ServerEntry[] = []
  for (const item of parsed) {
    if (!item || typeof item !== "object" || typeof item.url !== "string") continue
    servers.push({
      name: String(item.name ?? ""),
      url: item.url,
      headers: item.headers && typeof item.headers === "object" ? item.headers : {},
    })
  }
  return servers
}

// flattenContent turns an MCP content list into plain text; non-text parts become a placeholder.
function flattenContent(content: unknown): string {
  if (!Array.isArray(content)) return ""
  return content
    .map((part: any) => (part?.type === "text" && typeof part.text === "string" ? part.text : `[${part?.type || "unsupported"} content]`))
    .join("\n")
}

// pickRpcReply finds the JSON-RPC reply with the given id in a JSON or SSE response body.
function pickRpcReply(body: string, contentType: string, id: number): any {
  const candidates: string[] = contentType.includes("text/event-stream")
    ? body.split(/\r?\n/).filter(line => line.startsWith("data:")).map(line => line.slice(5).trim())
    : [body]
  for (const candidate of candidates) {
    if (!candidate) continue
    try {
      const message = JSON.parse(candidate)
      if (message && message.id === id && ("result" in message || "error" in message)) return message
    } catch {}
  }
  return undefined
}

// HttpSession is one Streamable HTTP connection. Plain fields only (no parameter
// properties or enums), so a strip-types-only TypeScript loader can run this file.
class HttpSession {
  url: string
  headers: Record<string, string>
  sessionId = ""
  negotiated = ""
  counter = 0

  constructor(url: string, headers: Record<string, string>) {
    this.url = url
    this.headers = headers
  }

  async post(payload: object): Promise<Response> {
    const headers: Record<string, string> = {
      ...this.headers,
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
    }
    if (this.sessionId) headers["Mcp-Session-Id"] = this.sessionId
    if (this.negotiated) headers["MCP-Protocol-Version"] = this.negotiated
    const response = await fetch(this.url, { method: "POST", headers, body: JSON.stringify(payload) })
    this.sessionId = response.headers.get("mcp-session-id") || this.sessionId
    return response
  }

  async request(method: string, params: object = {}): Promise<any> {
    const id = ++this.counter
    const response = await this.post({ jsonrpc: "2.0", id, method, params })
    const reply = pickRpcReply(await response.text(), response.headers.get("content-type") || "", id)
    if (!reply) throw new Error(`${method}: no JSON-RPC reply (HTTP ${response.status})`)
    if (reply.error) throw new Error(`${method}: ${reply.error.message || "request failed"}`)
    if (method === "initialize") this.negotiated = String(reply.result?.protocolVersion || MCP_PROTOCOL)
    return reply.result
  }

  async notify(method: string): Promise<void> {
    try {
      const response = await this.post({ jsonrpc: "2.0", method })
      await response.text()
    } catch {}
  }
}

async function listAllTools(session: HttpSession): Promise<any[]> {
  const tools: any[] = []
  let cursor: string | undefined
  do {
    const page = await session.request("tools/list", cursor ? { cursor } : {})
    if (Array.isArray(page?.tools)) tools.push(...page.tools)
    cursor = page?.nextCursor || undefined
  } while (cursor)
  return tools
}

async function runTool(session: HttpSession, name: string, args: any): Promise<string> {
  try {
    const result = await session.request("tools/call", { name, arguments: args && typeof args === "object" ? args : {} })
    const text = flattenContent(result?.content)
    return result?.isError ? "Error: " + (text || "tool failed") : text
  } catch (error) {
    return "Error: " + describeError(error)
  }
}

async function bridgeServer(entry: ServerEntry): Promise<BridgedTool[]> {
  const session = new HttpSession(entry.url, entry.headers)
  await session.request("initialize", { protocolVersion: MCP_PROTOCOL, capabilities: {}, clientInfo: { name: "mcode", version: "1" } })
  await session.notify("notifications/initialized")
  const tools = await listAllTools(session)
  return tools.map((tool: any) => {
    const name = String(tool.name)
    return {
      server: entry.name,
      name,
      description: typeof tool.description === "string" && tool.description ? tool.description : name,
      inputSchema: tool.inputSchema && typeof tool.inputSchema === "object" ? tool.inputSchema : { type: "object" },
      call: (args: any) => runTool(session, name, args),
    }
  })
}

function withinLimit<T>(work: Promise<T>): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  const expired = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`no answer within ${SETUP_LIMIT_MS / 1000}s`)), SETUP_LIMIT_MS)
  })
  return Promise.race([work, expired]).finally(() => clearTimeout(timer))
}

// loadMCPTools connects to all servers at once and keeps the configured order.
async function loadMCPTools(): Promise<BridgedTool[]> {
  const servers = readServerList()
  const outcomes = await Promise.allSettled(servers.map(entry => withinLimit(bridgeServer(entry))))
  const tools: BridgedTool[] = []
  outcomes.forEach((outcome, index) => {
    if (outcome.status === "fulfilled") {
      tools.push(...outcome.value)
    } else {
      console.error(`mcode: MCP server ${servers[index].name || servers[index].url} skipped: ${describeError(outcome.reason)}`)
    }
  })
  return tools
}

export default function (cmd: any) {
  loadMCPTools()
    .then(tools => {
      for (const tool of tools) {
        cmd.addTool({
          schema: { name: `mcp__${tool.server}__${tool.name}`, description: tool.description, input_schema: tool.inputSchema },
          run: async ({ input }: any) => ({ ok: true, content: [{ type: "text", text: (await tool.call(input || {})) || "(no output)" }] }),
        })
      }
    })
    .catch(error => console.error("mcode: MCP bridge failed: " + describeError(error)))
}
