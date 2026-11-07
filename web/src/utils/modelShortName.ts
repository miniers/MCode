import type { RemoteProvider } from '../types/generated';
import { modelDisplayName } from './modelLabel';

// 短名规则移植自桌面端 internal/ui/model_capsule_view.go 的 modelBrandKey / modelShortName：
// 唯一差异是颜色图标判断，web 里只按品牌前缀表决定是否去前缀。

/** 品牌前缀表（带一个空格），键是 modelBrandKey 返回的品牌键，比较时不区分大小写。 */
const BRAND_PREFIXES: Record<string, string> = {
  claude: 'Claude ',
  deepseek: 'DeepSeek ',
  meta: 'Meta '
};

/** Provider ID 别名：与桌面端 providerIconKey 保持一致。 */
const PROVIDER_ALIASES: Record<string, string> = {
  agy: 'antigravity',
  cmd: 'commandcode',
  mu: 'pi',
  openai: 'codex'
};

// 桌面端 colorProviderIcons 的键集合，供 providerIconKey 判断「-」前缀是否已知品牌。
const COLOR_PROVIDER_ICON_KEYS = new Set([
  'codex',
  'openai',
  'claude',
  'agy',
  'antigravity',
  'copilot',
  'pi',
  'mu',
  'omp',
  'commandcode',
  'cmd',
  'deepseek',
  'zai',
  'meta',
  'droid',
  'cline',
  'acp',
  'cursor',
  'devin',
  'grok',
  'opencode'
]);

/** 短名里保持全大写的缩写词段。 */
const KEBAB_ABBREVIATIONS = new Set(['gpt', 'glm', 'qwq']);

/** 归一化 Provider ID：别名映射为主标识，未知的「-」前缀回落到已知品牌。 */
function providerIconKey(id: string): string {
  const key = (id || '').toLowerCase().trim();
  const alias = PROVIDER_ALIASES[key];
  if (alias) return alias;
  if (!COLOR_PROVIDER_ICON_KEYS.has(key)) {
    const dash = key.indexOf('-');
    if (dash > 0) {
      const prefix = key.slice(0, dash);
      if (COLOR_PROVIDER_ICON_KEYS.has(prefix)) return prefix;
    }
  }
  return key;
}

/**
 * 按模型判断品牌键：模型名/ID 命中已知品牌时返回品牌键，否则退回 Provider 的图标键，
 * 让品牌前缀跟随模型本身而不是接入方。
 */
export function modelBrandKey(providerID: string, modelID: string, modelName: string): string {
  const haystack = `${modelName} ${modelID}`.toLowerCase();
  if (['claude', 'opus', 'sonnet', 'haiku', 'fable'].some((needle) => haystack.includes(needle))) {
    return 'claude';
  }
  if (haystack.includes('deepseek')) return 'deepseek';
  if (haystack.includes('gemini')) return 'gemini';
  if (haystack.includes('grok')) return 'grok';
  if (haystack.includes('glm')) return 'zai';
  if (haystack.includes('llama') || haystack.startsWith('meta/') || haystack.startsWith('meta-')) {
    return 'meta';
  }
  if (haystack.includes('gpt') || haystack.includes('codex')) return 'codex';
  return providerIconKey(providerID);
}

/** 去掉名字结尾的 8 位日期后缀，如 -20250929。 */
function trimModelDateSuffix(name: string): string {
  if (name.length < 9 || name[name.length - 9] !== '-') return name;
  const tail = name.slice(name.length - 8);
  if (!/^\d{8}$/.test(tail)) return name;
  return name.slice(0, name.length - 9);
}

/**
 * 去掉名字结尾的上下文窗口括号，如 (1M context)、(200K Context Window)；
 * 括号内去掉 context/context window 后剩下的部分必须以数字开头，因此 (Thinking) 之类保留。
 */
function modelContextWindowParenTail(name: string): string {
  if (!name.endsWith(')')) return '';
  const open = name.lastIndexOf('(');
  if (open < 0) return '';
  const inner = name.slice(open + 1, name.length - 1).trim().toLowerCase();
  let value = '';
  if (inner.endsWith('context window')) {
    value = inner.slice(0, inner.length - 'context window'.length).trim();
  } else if (inner.endsWith('context')) {
    value = inner.slice(0, inner.length - 'context'.length).trim();
  } else {
    return '';
  }
  if (value === '' || value[0] < '0' || value[0] > '9') return '';
  return name.slice(0, open).trim();
}

/** 去掉名字结尾的方括号上下文标记，如 claude-opus-5-5[1m]；括号内只能是数字加 k/m。 */
function modelContextWindowBracketTail(name: string): string {
  if (!name.endsWith(']')) return '';
  const open = name.lastIndexOf('[');
  if (open < 0) return '';
  const inner = name.slice(open + 1, name.length - 1).trim().toLowerCase();
  if (inner.length < 2) return '';
  const unit = inner[inner.length - 1];
  if (unit !== 'k' && unit !== 'm') return '';
  if (!/^\d+$/.test(inner.slice(0, inner.length - 1))) return '';
  return name.slice(0, open).trim();
}

/** 单个 kebab 段的展示形式：缩写词全大写，v+数字 段首转 V，数字开头保持原样，其余首字母大写。 */
function kebabSegment(segment: string): string {
  if (KEBAB_ABBREVIATIONS.has(segment.toLowerCase())) return segment.toUpperCase();
  if (/^v\d/i.test(segment)) return `V${segment.slice(1)}`;
  if (/^\d/.test(segment)) return segment;
  return segment.charAt(0).toUpperCase() + segment.slice(1);
}

/**
 * kebab 形式的模型 ID 美化：只在名字不含空格且含「-」时生效。
 * 丢空段与 8 位纯数字段；连续的不超过 2 位的纯数字段用「.」拼接；段间用空格连接，
 * 但缩写词段后面紧跟数字开头的段时用「-」连接（如 gpt-5.6 → GPT-5.6）。
 */
function beautifyKebabID(name: string): string {
  if (name.includes(' ') || !name.includes('-')) return name;
  const segments = name.split('-').filter((segment) => segment !== '' && !/^\d{8}$/.test(segment));
  if (segments.length === 0) return name;

  const parts: Array<{ text: string; abbreviation: boolean; numericStart: boolean }> = [];
  let index = 0;
  while (index < segments.length) {
    const segment = segments[index];
    if (/^\d{1,2}$/.test(segment)) {
      const run: string[] = [];
      while (index < segments.length && /^\d{1,2}$/.test(segments[index])) {
        run.push(segments[index]);
        index += 1;
      }
      parts.push({ text: run.join('.'), abbreviation: false, numericStart: true });
      continue;
    }
    parts.push({
      text: kebabSegment(segment),
      abbreviation: KEBAB_ABBREVIATIONS.has(segment.toLowerCase()),
      numericStart: /^\d/.test(segment)
    });
    index += 1;
  }

  let out = parts[0].text;
  for (let next = 1; next < parts.length; next += 1) {
    const previous = parts[next - 1];
    out += `${previous.abbreviation && parts[next].numericStart ? '-' : ' '}${parts[next].text}`;
  }
  return out;
}

/**
 * 把模型显示名压成胶囊用的短名：先去掉「 · 」之后的附加说明（如 routing group、端点名），
 * 再取 ID 末段、去日期与 (auto)/(latest) 后缀，也去掉上下文窗口后缀，
 * 然后把 kebab ID 美化，最后去掉品牌前缀（仅当剩余部分以字母开头）。
 */
export function modelShortName(name: string, brand: string): string {
  let current = (name || '').trim();
  const aside = current.indexOf(' · ');
  if (aside >= 0) {
    const head = current.slice(0, aside).trim();
    if (head !== '') current = head;
  }
  if (!current.includes(' ') && current.includes('/')) {
    current = current.slice(current.lastIndexOf('/') + 1);
  }
  current = trimModelDateSuffix(current);
  for (;;) {
    const trimmed = current.trim();
    const lower = trimmed.toLowerCase();
    let next = '';
    if (lower.endsWith('(auto)')) {
      next = trimmed.slice(0, trimmed.length - '(auto)'.length).trim();
    } else if (lower.endsWith('(latest)')) {
      next = trimmed.slice(0, trimmed.length - '(latest)'.length).trim();
    } else if (trimmed.endsWith(')')) {
      next = modelContextWindowParenTail(trimmed);
    } else if (trimmed.endsWith(']')) {
      next = modelContextWindowBracketTail(trimmed);
    }
    if (next === '') {
      current = trimmed;
      break;
    }
    current = next;
  }
  current = beautifyKebabID(current);

  const prefix = BRAND_PREFIXES[brand];
  if (!prefix || current.length <= prefix.length) return current;
  if (!current.toLowerCase().startsWith(prefix.toLowerCase())) return current;
  const rest = current.slice(prefix.length);
  return /^\p{L}/u.test(rest) ? rest : current;
}

/**
 * 判断配对名是否为旧版自动生成的全名标题或旧快速保存名；这类名字按「未命名」处理，
 * 让浮层回落到短名拼接。fullLead/fullWorker 为空串表示该侧未指定模型。
 */
export function isAutoPairName(name: string, fullLead: string, fullWorker: string): boolean {
  const trimmed = (name || '').trim();
  if (fullLead === '' && fullWorker === '') return trimmed === '模型配对' || trimmed === '任意模型 + 跟随主代理';
  const fullPairName = `${fullLead || '任意模型'} + ${fullWorker || '跟随主代理'}`;
  if (trimmed === fullPairName) return true;
  if (fullWorker === '') return trimmed === fullLead;
  if (fullLead === '') return trimmed === fullWorker;
  return false;
}

/** 返回胶囊使用的模型短名：先按 provider + 模型 ID 取全名，再按品牌压短。 */
export function modelShortDisplayName(
  providers: RemoteProvider[] | undefined | null,
  providerID: string | null | undefined,
  modelID: string | null | undefined
): string {
  const full = modelDisplayName(providers, providerID, modelID);
  return modelShortName(full, modelBrandKey(providerID ?? '', modelID ?? '', full));
}
