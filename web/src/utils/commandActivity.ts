import type { RemoteThreadsCommandActivityProc } from '../types/generated';

type Proc = RemoteThreadsCommandActivityProc;
export interface CommandActivityState {
  cpu: number; rss: number; history: number[];
  processes: { name: string; object: string; cpu: number }[];
  previous: Record<number, Proc>; previousAt: number;
}
const basename = (value: string) => value.split('/').pop() || value;
const ignored = new Set(['bash', 'sh', 'zsh', 'dash', 'tail', 'head', 'grep', 'tee']);
function words(command: string): string[] {
  const result: string[] = [];
  let word = '', quote = '', escaped = false;
  for (const char of command) {
    if (escaped) { word += char; escaped = false; continue; }
    if (quote !== "'" && char === '\\') { escaped = true; continue; }
    if (quote) { if (char === quote) quote = ''; else word += char; continue; }
    if (char === "'" || char === '"') { quote = char; continue; }
    if (/\s/.test(char)) { if (word) result.push(word); word = ''; } else word += char;
  }
  if (word) result.push(word);
  return result;
}
function object(argv: string[], name: string): string {
  if (['compile', 'vet', 'asm', 'link'].includes(name)) {
    for (let i = 1; i < argv.length; i++) {
      if (argv[i] === '-p' && argv[i + 1]) return argv[i + 1];
      if (argv[i].startsWith('-p=')) return argv[i].slice(3);
    }
  }
  if (name.endsWith('.test')) return name.slice(0, -5);
  return Array.from(argv.slice(1).find((arg) => !arg.startsWith('-')) || '').slice(0, 40).join('');
}
export function nextCommandActivity(prev: CommandActivityState | undefined, processes: Proc[], nowMs: number): CommandActivityState {
  const children = new Set(processes.map((p) => p.ppid));
  let cpu = 0, rss = 0;
  const views: CommandActivityState['processes'] = [];
  const previous: Record<number, Proc> = {};
  for (const p of processes) {
    const old = prev?.previous[p.pid];
    const interval = prev ? nowMs - prev.previousAt : 0;
    const age = nowMs - Date.parse(p.startTime);
    const usage = old && interval > 0 ? Math.max(0, p.cpuTimeMs - old.cpuTimeMs) / interval * 100 : age > 0 && p.cpuTimeMs > 0 ? p.cpuTimeMs / age * 100 : 0;
    cpu += usage; rss += p.rss; previous[p.pid] = p;
    const argv = words(p.command);
    if (children.has(p.pid) || !argv.length || ignored.has(basename(argv[0]).toLowerCase())) continue;
    const name = basename(argv[0]).replace(/^\((.*)\)$/, '$1');
    views.push({ name, object: object(argv, name.toLowerCase()), cpu: usage });
  }
  views.sort((a, b) => b.cpu - a.cpu);
  return { cpu, rss, history: [...(prev?.history || []), cpu].slice(-30), processes: views.slice(0, 3), previous, previousAt: nowMs };
}
