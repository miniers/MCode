// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:assert
import assert from 'node:assert/strict';
import { isMermaidLanguage } from './lazyMermaid';

assert.equal(isMermaidLanguage('mermaid'), true);
assert.equal(isMermaidLanguage(' Mermaid '), true);
assert.equal(isMermaidLanguage('mermaidjs'), false);
assert.equal(isMermaidLanguage(''), false);
