// mcode approval extension for the Command Code CLI, loaded with `--mod` (see approval.go).
// MCODE_APPROVALS names the directory used to ask mcode before a tool that can change things runs;
// without it the extension does nothing. MCP tools come from the separate mcp_bridge.ts mod.
//
// Approval exchange (one pair of files per tool call):
//   extension writes <id>.request.json  {toolCallId, toolName, input}
//   mcode writes     <id>.reply.json    {decision: "allow" | "deny", reason?}
// Both sides write to a .tmp file first and rename it, so a reader never sees half a file.

import fs from 'node:fs'
import path from 'node:path'

// Command Code tools that cannot modify the workspace; these run without asking.
const NO_APPROVAL_NEEDED = new Set([
  // reading and searching
  'read_file', 'read_directory', 'glob', 'grep', 'get_diagnostics', 'web_search', 'web_fetch',
  // inspecting background shells
  'shell_output', 'shell_tasks',
  // planning and bookkeeping
  'todo_write', 'task_create', 'task_update', 'task_list', 'task_get', 'cron_list',
  'plan_review', 'enter_plan_mode', 'exit_plan_mode',
  // conversation and delegation
  'ask_user_question', 'agent', 'agent_output', 'activate_skill', 'taste', 'sleep',
])

const POLL_MS = 40
const GIVE_UP_AFTER_MS = 6 * 60 * 60 * 1000

type Verdict = undefined | { block: true, additionalContext: string }

function deny(reason: string): Verdict {
  return { block: true, additionalContext: reason }
}

function removeQuietly(file: string) {
  try { fs.unlinkSync(file) } catch {}
}

function readIfPresent(file: string): string | undefined {
  try { return fs.readFileSync(file, 'utf8') } catch { return undefined }
}

function writeAtomically(file: string, body: string) {
  fs.writeFileSync(file + '.tmp', body)
  fs.renameSync(file + '.tmp', file)
}

// safeId keeps the file name inside the approvals directory.
function safeId(raw: unknown): string {
  const id = raw ? String(raw) : `call-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
  return id.replace(/[^A-Za-z0-9_.-]/g, '_')
}

function verdictFrom(raw: string): Verdict {
  let parsed: any
  try { parsed = JSON.parse(raw) } catch { parsed = undefined }
  if (parsed && parsed.decision === 'allow') return undefined
  return deny((parsed && parsed.reason) || 'Rejected by the user in mcode.')
}

async function askMcode(dir: string, call: any, ctx: any): Promise<Verdict> {
  const id = safeId(call.toolCallId)
  const requestFile = path.join(dir, id + '.request.json')
  const replyFile = path.join(dir, id + '.reply.json')
  removeQuietly(replyFile)
  writeAtomically(requestFile, JSON.stringify({ toolCallId: id, toolName: call.toolName, input: call.input }))

  const deadline = Date.now() + GIVE_UP_AFTER_MS
  while (true) {
    const raw = readIfPresent(replyFile)
    if (raw !== undefined) {
      removeQuietly(replyFile)
      return verdictFrom(raw)
    }
    if (ctx?.signal?.aborted) {
      removeQuietly(requestFile)
      return deny('The turn was stopped before this call could run.')
    }
    // ppid 1 means mcode went away and the CLI was re-parented; nobody will answer.
    if (process.ppid === 1 || Date.now() > deadline) {
      removeQuietly(requestFile)
      return deny('No answer came from mcode, so the call was not run.')
    }
    await new Promise(resolve => setTimeout(resolve, POLL_MS))
  }
}

export default function (cmd: any) {
  const approvalsDir = process.env.MCODE_APPROVALS
  if (!approvalsDir) return
  cmd.hooks({
    beforeToolCall: async (call: any, ctx: any) => {
      if (NO_APPROVAL_NEEDED.has(call.toolName)) return
      return askMcode(approvalsDir, call, ctx)
    },
  })
}
