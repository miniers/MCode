import { nextCommandActivity } from './commandActivity';
const start = Date.parse('2026-01-01T00:00:00Z');
const proc = { pid: 1, ppid: 0, startTime: new Date(start).toISOString(), cpuTimeMs: 500, rss: 100, command: 'go test ./...' };
let state = nextCommandActivity(undefined, [proc, { ...proc, pid: 2, command: 'tail file', rss: 200 }], start + 1000);
state = nextCommandActivity(state, [{ ...proc, cpuTimeMs: 1750 }, { ...proc, pid: 2, command: 'tail file', rss: 200 }], start + 2000);
if (state.cpu !== 125 || state.rss !== 300 || state.processes.length !== 1 || state.processes[0].name !== 'go') throw new Error('进程采样计算错误');
for (let i = 0; i < 35; i++) state = nextCommandActivity(state, [{ ...proc, cpuTimeMs: 1750 + i * 10 }], start + 3000 + i * 1000);
if (state.history.length !== 30 || state.history[29] !== 1) throw new Error('采样历史错误');
