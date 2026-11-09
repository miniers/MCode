import { loadOpenDecisionCount } from '../context/remoteStewardApi';
import type { UseRemoteCallResult } from '../context/useRemoteCall';
import { RpcError } from '../services/rpc';
import type { RemoteStewardDecision, RemoteStewardDecisionsListParams } from '../types/generated';

const decision = (status: RemoteStewardDecision['status']): RemoteStewardDecision => ({
  id: '', taskID: '', threadID: '', question: '', options: [], recommended: '',
  status, answer: '', answeredBy: '', answeredAt: null, createdAt: ''
});
const call: UseRemoteCallResult['call'] = async <T>(method: string, params?: unknown): Promise<T> => {
  if (method !== 'steward.decisions.list' || (params as RemoteStewardDecisionsListParams).status !== 'open') {
    throw new Error('决策查询参数错误');
  }
  return { decisions: [decision('open'), decision('answered'), decision('ignored'), decision('superseded'), decision('open')] } as T;
};
if (await loadOpenDecisionCount(call) !== 2) throw new Error('只应统计 open 决策');
const withTotal: UseRemoteCallResult['call'] = async <T>(): Promise<T> => ({ decisions: [decision('open')], total: 300 }) as T;
if (await loadOpenDecisionCount(withTotal) !== 300) throw new Error('应优先使用总数');
const unsupported: UseRemoteCallResult['call'] = async () => { throw new RpcError(-32601, 'Method not found'); };
if (await loadOpenDecisionCount(unsupported) !== null) throw new Error('方法不存在时应返回不支持');
