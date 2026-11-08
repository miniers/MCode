import React from 'react';

// 工具步骤内嵌的行内 diff：把 Edit / MultiEdit / Write / apply_patch 这类文件修改工具的输入
// 直接渲染成「文件路径 + 增删行」。解析失败就返回 null，由调用方回落到原有纯文本渲染。
// 对齐桌面端 internal/ui/timeline_diff.go 的 renderInlineToolDiff / renderInlineToolDiffFile。

export interface ToolDiffLine {
  type: 'add' | 'del' | 'context' | 'hunk';
  text: string;
}

export interface ToolDiffFile {
  id: string;
  path: string;
  additions: number;
  deletions: number;
  lines: ToolDiffLine[];
  truncated: boolean;
  fullLines?: ToolDiffLine[];
}

/** 单个文件最多渲染的 diff 行数，超出部分只提示已截断（与桌面端 toolDiffMaxLines 一致）。 */
const MAX_DIFF_LINES = 200;

/** 展开后首屏最多渲染的行数，避免几万行 diff 一次塞进移动端 DOM。 */
const EXPANDED_DIFF_LINE_LIMIT = 2000;

/** 返回当前实际渲染的 diff 行；展开超过上限时只给前 2000 行和剩余行数。 */
export function toolDiffVisibleLines(
  file: ToolDiffFile,
  expanded: boolean,
  showAll: boolean
): { lines: ToolDiffLine[]; hidden: number } {
  if (!expanded) return { lines: file.lines, hidden: 0 };
  const full = file.fullLines ?? file.lines;
  if (showAll || full.length <= EXPANDED_DIFF_LINE_LIMIT) return { lines: full, hidden: 0 };
  return {
    lines: full.slice(0, EXPANDED_DIFF_LINE_LIMIT),
    hidden: full.length - EXPANDED_DIFF_LINE_LIMIT
  };
}

// 算作文件修改的工具名（已转小写并去掉 _ - .），与桌面端 fileEditToolNames 对齐。
const FILE_EDIT_TOOL_NAMES = new Set([
  'edit',
  'multiedit',
  'write',
  'writefile',
  'editfile',
  'createfile',
  'writetofile',
  'applypatch',
  'patch',
  'strreplace',
  'strreplaceeditor',
  'strreplacebasededittool',
  'filechange',
  'notebookedit',
  'editnotebook',
  'replacefilecontent',
  'multireplacefilecontent'
]);

const PATH_KEYS = [
  'file_path',
  'filePath',
  'filepath',
  'path',
  'target_file',
  'targetFile',
  'TargetFile',
  'notebook_path',
  'AbsolutePath',
  'Path',
  'FilePath'
];

const OLD_TEXT_KEYS = ['old_string', 'oldString', 'old_str', 'oldStr', 'old_text', 'oldText'];
const NEW_TEXT_KEYS = ['new_string', 'newString', 'new_str', 'newStr', 'new_text', 'newText'];
const CONTENT_KEYS = ['content', 'file_text', 'fileContent', 'new_content'];
const PATCH_TEXT_KEYS = ['input', 'patch', 'patchText', 'diff'];

function normalizeToolName(name?: string): string {
  return (name ?? '').toLowerCase().replace(/[_\-\s.]/g, '');
}

export function isFileEditToolName(name?: string): boolean {
  return FILE_EDIT_TOOL_NAMES.has(normalizeToolName(name));
}

function readField(record: Record<string, unknown>, keys: string[]): string {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim().length > 0) return value;
  }
  return '';
}

function textLines(text: string): string[] {
  if (!text) return [];
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  // old_string / new_string 常以换行结尾，去掉由此产生的空尾行
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

function toRecord(raw: string): Record<string, unknown> | null {
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
    return parsed as Record<string, unknown>;
  } catch {
    return null;
  }
}

function buildFile(id: string, path: string, lines: ToolDiffLine[]): ToolDiffFile {
  let additions = 0;
  let deletions = 0;
  for (const line of lines) {
    if (line.type === 'add') additions += 1;
    else if (line.type === 'del') deletions += 1;
  }
  const truncated = lines.length > MAX_DIFF_LINES;
  return {
    id,
    path: path.trim() || '（未提供文件路径）',
    additions,
    deletions,
    lines: truncated ? lines.slice(0, MAX_DIFF_LINES) : lines,
    truncated,
    fullLines: truncated ? lines : undefined
  };
}

function appendReplacement(lines: ToolDiffLine[], oldText: string, newText: string): void {
  for (const text of textLines(oldText)) lines.push({ type: 'del', text });
  for (const text of textLines(newText)) lines.push({ type: 'add', text });
}

/** 解析 apply_patch 风格的纯文本补丁：按「*** Update File:」切分文件。 */
function parsePatchText(text: string): ToolDiffFile[] {
  const files: ToolDiffFile[] = [];
  let currentPath = '';
  let lines: ToolDiffLine[] = [];

  const flush = () => {
    if (currentPath.length > 0 || lines.length > 0) {
      files.push(buildFile(`patch-${files.length}`, currentPath, lines));
    }
    currentPath = '';
    lines = [];
  };

  for (const line of text.replace(/\r\n/g, '\n').split('\n')) {
    const header = line.match(/^\*\*\* (?:Update|Add|Delete) File: (.+)$/);
    if (header) {
      flush();
      currentPath = header[1].trim();
      continue;
    }
    if (/^\*\*\* (?:Begin Patch|End Patch)/.test(line)) continue;
    if (currentPath.length === 0) continue;
    if (line.startsWith('@@')) {
      lines.push({ type: 'hunk', text: line });
    } else if (line.startsWith('+')) {
      lines.push({ type: 'add', text: line.slice(1) });
    } else if (line.startsWith('-')) {
      lines.push({ type: 'del', text: line.slice(1) });
    } else if (line.startsWith(' ')) {
      lines.push({ type: 'context', text: line.slice(1) });
    }
  }
  flush();
  return files;
}

/**
 * 把文件修改类工具的输入解析成可渲染的行内 diff。
 * 不是文件修改工具、输入为空或无法解析时返回 null（调用方回落到纯文本渲染）。
 */
export function parseToolEditDiff(toolName?: string, toolInput?: string): ToolDiffFile[] | null {
  if (!isFileEditToolName(toolName)) return null;
  const raw = (toolInput ?? '').trim();
  if (raw.length === 0) return null;

  const files: ToolDiffFile[] = [];
  const record = toRecord(raw);

  if (record) {
    const path = readField(record, PATH_KEYS);
    const edits = Array.isArray(record.edits) ? (record.edits as unknown[]) : null;

    if (edits && edits.length > 0) {
      const lines: ToolDiffLine[] = [];
      edits.forEach((candidate, index) => {
        if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) return;
        const edit = candidate as Record<string, unknown>;
        const editPath = readField(edit, PATH_KEYS);
        lines.push({
          type: 'hunk',
          text: `@@ 编辑 ${index + 1}${editPath && editPath !== path ? ` · ${editPath}` : ''}`
        });
        appendReplacement(lines, readField(edit, OLD_TEXT_KEYS), readField(edit, NEW_TEXT_KEYS));
      });
      if (lines.some((line) => line.type !== 'hunk')) {
        files.push(buildFile('file-0', path, lines));
      }
    } else {
      const lines: ToolDiffLine[] = [];
      appendReplacement(lines, readField(record, OLD_TEXT_KEYS), readField(record, NEW_TEXT_KEYS));
      if (lines.length === 0) {
        // Write：整份内容都是新增行
        for (const text of textLines(readField(record, CONTENT_KEYS))) {
          lines.push({ type: 'add', text });
        }
      }
      if (lines.length > 0) files.push(buildFile('file-0', path, lines));
    }

    if (files.length === 0) {
      const patchText = readField(record, PATCH_TEXT_KEYS);
      if (patchText.length > 0) files.push(...parsePatchText(patchText));
    }
  }

  if (files.length === 0) files.push(...parsePatchText(raw));

  const usable = files.filter((file) => file.lines.length > 0);
  return usable.length > 0 ? usable : null;
}

/** 汇总多个文件的增删行数，用于工具条目标签旁的 +N/-M。 */
export function summarizeToolDiff(files: ToolDiffFile[] | null): { additions: number; deletions: number } | null {
  if (!files || files.length === 0) return null;
  let additions = 0;
  let deletions = 0;
  for (const file of files) {
    additions += file.additions;
    deletions += file.deletions;
  }
  if (additions === 0 && deletions === 0) return null;
  return { additions, deletions };
}

const LINE_CLASS: Record<ToolDiffLine['type'], string> = {
  add: 'bg-emerald-500/10 text-[var(--foreground)]',
  del: 'bg-rose-500/10 text-[var(--foreground)]',
  context: 'text-[var(--foreground-muted)]',
  hunk: 'bg-[var(--card-alt,var(--card))] text-[var(--foreground-muted)]'
};

const LINE_MARK: Record<ToolDiffLine['type'], string> = {
  add: '+',
  del: '-',
  context: ' ',
  hunk: ''
};

const ToolEntryDiffFile: React.FC<{ file: ToolDiffFile }> = ({ file }) => {
  const [expanded, setExpanded] = React.useState(false);
  const [showAll, setShowAll] = React.useState(false);
  const fullLines = file.fullLines ?? file.lines;
  const { lines, hidden } = toolDiffVisibleLines(file, expanded, showAll);
  const remaining = fullLines.length - file.lines.length;
  return (
      <div
        className="min-w-0 overflow-hidden rounded-lg border border-[var(--border)] bg-[var(--card)]"
      >
        <div className="flex items-center justify-between gap-2 border-b border-[var(--border)] px-2 py-1 text-mc-caption">
          <span className="min-w-0 truncate font-mono text-[var(--foreground)]" title={file.path}>
            {file.path}
          </span>
          <span className="shrink-0 font-mono">
            <span className="text-emerald-600 dark:text-emerald-400">+{file.additions}</span>
            <span className="ml-1.5 text-rose-600 dark:text-rose-400">-{file.deletions}</span>
          </span>
        </div>
        <div className="max-h-[280px] overflow-auto py-1">
          {lines.map((line, index) => (
            <div
              key={`${file.id}-${index}`}
              className={`flex items-start gap-1 px-2 py-0.5 font-mono text-mc-caption leading-[16px] ${LINE_CLASS[line.type]}`}
            >
              <span className="shrink-0 select-none opacity-70">{LINE_MARK[line.type]}</span>
              <span className="min-w-0 whitespace-pre-wrap break-words select-text">
                {line.text.length > 0 ? line.text : '\u00a0'}
              </span>
            </div>
          ))}
          {expanded && hidden > 0 && (
            <button type="button" onClick={() => setShowAll(true)} className="px-2 py-1 text-xs text-[var(--primary)] hover:underline">
              +{hidden} 行
            </button>
          )}
          {!expanded && remaining > 0 && (
            <button type="button" onClick={() => setExpanded(true)} className="px-2 py-1 text-xs text-[var(--primary)] hover:underline">
              显示其余 {remaining} 行
            </button>
          )}
          {file.truncated && !file.fullLines && (
            <p className="px-2 py-1 text-mc-caption text-[var(--warning-foreground)]">
              改动过多，仅显示前 {MAX_DIFF_LINES} 行
            </p>
          )}
        </div>
      </div>
  );
};

export const ToolEntryDiff: React.FC<{ files: ToolDiffFile[] }> = ({ files }) => (
  <div className="space-y-2 min-w-0">
    {files.map((file) => <ToolEntryDiffFile key={file.id} file={file} />)}
  </div>
);

ToolEntryDiff.displayName = 'ToolEntryDiff';
