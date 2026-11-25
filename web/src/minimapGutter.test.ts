// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';
import {
  MINIMAP_MIN_ENTRIES,
  buildQuestionMinimapData,
  questionMinimapVisible,
  type QuestionMinimapData
} from './components/thread/QuestionMinimap';

function readText(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

function makeMinimapData(count: number): QuestionMinimapData {
  const entries = Array.from({ length: count }, (_, idx) => ({
    messageID: `m-${idx + 1}`,
    index: idx,
    ordinal: idx + 1,
    text: `提问 ${idx + 1}`,
    weight: 0.3,
    createdAt: ''
  }));
  return {
    entries,
    order: new Map(entries.map((entry) => [entry.messageID, entry.index]))
  };
}

// 1. 纯函数阈值判断：条目不足返回 false，达到或超过阈值返回 true
assert(questionMinimapVisible(makeMinimapData(0)) === false, '0 条提问时应隐藏小地图');
assert(
  questionMinimapVisible(makeMinimapData(MINIMAP_MIN_ENTRIES - 1)) === false,
  `少于 ${MINIMAP_MIN_ENTRIES} 条提问时应隐藏小地图`
);
assert(
  questionMinimapVisible(makeMinimapData(MINIMAP_MIN_ENTRIES)) === true,
  `达到 ${MINIMAP_MIN_ENTRIES} 条提问时应显示小地图`
);
assert(
  questionMinimapVisible(makeMinimapData(MINIMAP_MIN_ENTRIES + 2)) === true,
  '超过阈值时应显示小地图'
);

// 结合 buildQuestionMinimapData 验证非用户消息不计入显示阈值
const threeQuestions = buildQuestionMinimapData([
  { id: 'u1', role: 'user', text: '问题一', createdAt: '', turnIndex: 1 },
  { id: 'a1', role: 'assistant', text: '回答一', createdAt: '', turnIndex: 1 },
  { id: 'u2', role: 'user', text: '问题二', createdAt: '', turnIndex: 2 },
  { id: 'u3', role: 'user', text: '问题三', createdAt: '', turnIndex: 3 }
]);
assert(questionMinimapVisible(threeQuestions) === false, '仅 3 条用户提问时不应显示小地图');

const fourQuestions = buildQuestionMinimapData([
  { id: 'u1', role: 'user', text: '问题一', createdAt: '', turnIndex: 1 },
  { id: 'u2', role: 'user', text: '问题二', createdAt: '', turnIndex: 2 },
  { id: 'u3', role: 'user', text: '问题三', createdAt: '', turnIndex: 3 },
  { id: 'u4', role: 'user', text: '问题四', createdAt: '', turnIndex: 4 }
]);
assert(questionMinimapVisible(fourQuestions) === true, '4 条用户提问时应显示小地图');

// 2. QuestionMinimap 内部与 ThreadView 复用同一可见性判断，且 ThreadView 在可见时加大右内边距
const minimapTsx = readText('./components/thread/QuestionMinimap.tsx');
assert(
  minimapTsx.includes('if (!questionMinimapVisible(data)) return null;'),
  'QuestionMinimap.tsx 内部应复用 questionMinimapVisible(data)'
);

const threadViewTsx = readText('./components/ThreadView.tsx');
assert(
  threadViewTsx.includes('questionMinimapVisible(questionMinimapData)'),
  'ThreadView.tsx 应调用 questionMinimapVisible(questionMinimapData)'
);
assert(
  threadViewTsx.includes("showQuestionMinimap ? 'p-3 pr-9 sm:p-4 sm:pr-9 min-[1320px]:pr-4' : 'p-3 sm:p-4'"),
  'ThreadView.tsx 应在小地图可见时为滚动区添加 pr-9 避让内边距，不可见时保持 p-3 sm:p-4'
);
