// 校验 parsePRReview 对新格式、旧对象格式、旧数组格式与非法 JSON 的解析，以及分组顺序和 PRReviewCard / MarkdownView 渲染。
import { renderToStaticMarkup } from 'react-dom/server';
import { MarkdownView } from './MarkdownView';
import { parsePRReview, PRReviewCard } from './PRReviewCard';

function assertEqual<T>(actual: T, expected: T, message: string): void {
  const actualJson = JSON.stringify(actual);
  const expectedJson = JSON.stringify(expected);
  if (actualJson !== expectedJson) {
    throw new Error(`${message}：期望 ${expectedJson}，实际 ${actualJson}`);
  }
}

function assertIncludes(haystack: string, needle: string, message: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`${message}：期望包含 “${needle}”，实际为：${haystack}`);
  }
}

function assertBefore(haystack: string, first: string, second: string, message: string): void {
  const firstIdx = haystack.indexOf(first);
  const secondIdx = haystack.indexOf(second);
  if (firstIdx < 0 || secondIdx < 0 || firstIdx >= secondIdx) {
    throw new Error(`${message}：期望 “${first}” 出现在 “${second}” 之前`);
  }
}

// 1. 新格式 JSON：verdict / reason / comments，且阻塞组排在建议组之前
{
  const raw = JSON.stringify({
    verdict: 'fix',
    reason: '存在 1 个阻塞问题需先修复',
    comments: [
      {
        path: 'web/src/a.ts',
        line: 12,
        severity: 'suggestion',
        headline: '建议提取辅助函数',
        impact: '可维护性稍弱',
        fix: '拆成纯函数',
        patch: '@@ -10,2 +10,2 @@\n-old\n+new'
      },
      {
        path: 'web/src/b.ts',
        line: 45,
        severity: 'blocking',
        headline: '缺少空值保护会导致崩溃',
        impact: '传入空数组时抛 TypeError',
        fix: '在访问前检查长度',
        patch: '-arr[0].id\n+arr[0]?.id'
      }
    ]
  });

  const parsed = parsePRReview(raw);
  if (!parsed) throw new Error('新格式 JSON 应解析成功');
  assertEqual(parsed.verdict, 'fix', 'verdict 应为 fix');
  assertEqual(parsed.verdictLabel, '修复后合并', 'fix 标签应为修复后合并');
  assertEqual(parsed.reason, '存在 1 个阻塞问题需先修复', 'reason 应保留');
  assertEqual(parsed.blocking.length, 1, '应有 1 条阻塞项');
  assertEqual(parsed.suggestions.length, 1, '应有 1 条建议项');
  assertEqual(
    parsed.comments.map((c) => c.severity),
    ['blocking', 'suggestion'],
    'comments 应按阻塞在前、建议在后排列'
  );
  assertEqual(parsed.comments[0].path, 'web/src/b.ts', '阻塞项应排在第一位');
  assertEqual(parsed.comments[0].line, 45, '阻塞项行号应为 45');

  const html = renderToStaticMarkup(<PRReviewCard review={parsed} />);
  assertIncludes(html, '修复后合并', '卡片顶部应包含结论标签');
  assertIncludes(html, '1 阻塞 · 1 建议', '卡片顶部应包含问题数');
  assertIncludes(html, '存在 1 个阻塞问题需先修复', '卡片顶部应包含理由');
  assertBefore(html, '阻塞合并', '建议修复', '阻塞合并组应渲染在建议修复组之前');
  assertBefore(html, '缺少空值保护会导致崩溃', '建议提取辅助函数', '阻塞条目应在建议条目之前');
  assertIncludes(html, 'web/src/b.ts:45', '应显示 path:line');
  assertIncludes(html, 'web/src/a.ts:12', '应显示建议项 path:line');
  assertIncludes(html, '参考改动片段', '应渲染 patch 片段触发区');
}

// 2. 四种 verdict 标签映射
{
  const cases: Array<[string, string]> = [
    ['merge', '可以合并'],
    ['fix', '修复后合并'],
    ['comment', '需作者修改'],
    ['close', '不建议合并']
  ];
  for (const [verdict, expectedLabel] of cases) {
    const parsed = parsePRReview(JSON.stringify({ verdict, reason: 'r', comments: [] }));
    if (!parsed) throw new Error(`verdict=${verdict} 应解析成功`);
    assertEqual(parsed.verdictLabel, expectedLabel, `verdict=${verdict} 标签错误`);
  }
}

// 3. 旧对象格式：summary 代替 reason，条目只有 body（headline 取 body 第一行）
{
  const raw = JSON.stringify({
    verdict: 'comment',
    summary: '旧格式摘要',
    comments: [
      {
        path: 'internal/ui/chat.go',
        line: 88,
        severity: 'suggestion',
        body: '第一行作为标题\n第二行详细说明'
      }
    ]
  });
  const parsed = parsePRReview(raw);
  if (!parsed) throw new Error('旧对象格式应解析成功');
  assertEqual(parsed.reason, '旧格式摘要', 'summary 应回退为 reason');
  assertEqual(parsed.verdictLabel, '需作者修改', 'comment 标签应为需作者修改');
  assertEqual(parsed.comments[0].headline, '第一行作为标题', '无 headline 时应取 body 第一行');

  const html = renderToStaticMarkup(<PRReviewCard review={parsed} />);
  assertIncludes(html, '旧格式摘要', '卡片应显示旧格式 summary');
  assertIncludes(html, '第一行作为标题', '卡片应显示 body 第一行作为标题');
  assertIncludes(html, 'internal/ui/chat.go:88', '卡片应显示 path:line');
}

// 4. 旧数组格式：整个 JSON 是条目数组，且按 blocking 在前排序
{
  const raw = JSON.stringify([
    { path: 'a.ts', line: 1, severity: 'suggestion', body: '普通建议\n补充' },
    { path: 'b.ts', line: 2, severity: 'blocking', body: '阻塞错误' }
  ]);
  const parsed = parsePRReview(raw);
  if (!parsed) throw new Error('旧数组格式应解析成功');
  assertEqual(parsed.blocking.length, 1, '数组格式应识别 blocking');
  assertEqual(parsed.suggestions.length, 1, '数组格式应识别 suggestion');
  assertEqual(
    parsed.comments.map((c) => c.severity),
    ['blocking', 'suggestion'],
    '数组格式也应阻塞在前、建议在后'
  );
  assertEqual(parsed.comments[0].headline, '阻塞错误', '数组格式条目应提取 body 第一行');
}

// 5. 非法 JSON 与非审查对象返回 null，MarkdownView 退回普通代码块
{
  const invalidInputs = [
    '',
    '   ',
    '{not json',
    '[{"broken": ]',
    '123',
    '"hello"',
    'null',
    '{"unrelated": true}'
  ];
  for (const input of invalidInputs) {
    assertEqual(parsePRReview(input), null, `非法输入应返回 null: ${input}`);
  }

  const fallbackHtml = renderToStaticMarkup(
    <MarkdownView text={'```pr-review\n{invalid json\n```'} />
  );
  assertIncludes(fallbackHtml, 'aria-label="复制代码"', '非法 JSON 时 pr-review 应按普通代码块渲染');

  const cardHtml = renderToStaticMarkup(
    <MarkdownView
      text={'```pr-review\n{"verdict":"merge","reason":"检查通过","comments":[]}\n```'}
    />
  );
  assertIncludes(cardHtml, '可以合并', '合法 pr-review 代码块应渲染成审查报告卡片');
  assertIncludes(cardHtml, '检查通过', '合法 pr-review 代码块应显示理由');
}
