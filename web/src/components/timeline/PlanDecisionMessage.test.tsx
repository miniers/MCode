import { parsePlanDecision } from './PlanDecisionMessage';

const approved = parsePlanDecision(JSON.stringify({ requestID: 'r1', plan: '# Plan', approved: true, feedback: '' }));
if (!approved || !approved.approved || approved.plan !== '# Plan') throw new Error('批准计划解析失败');
const declined = parsePlanDecision(JSON.stringify({ requestID: 'r2', plan: 'Plan', approved: false, feedback: '缩小范围' }));
if (!declined || declined.approved || declined.feedback !== '缩小范围') throw new Error('驳回意见解析失败');
if (parsePlanDecision('{') !== null) throw new Error('非法 JSON 必须返回 null');
if (parsePlanDecision(JSON.stringify({ requestID: '', approved: true })) !== null) throw new Error('无 requestID 必须返回 null');
