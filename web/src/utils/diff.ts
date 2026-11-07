// Git Unified Diff 纯文本安全行解析器与按文件分组

export interface DiffLine {
  id: number;
  type: 'add' | 'del' | 'hunk' | 'header' | 'context';
  text: string;
  oldLineNumber?: number | null;
  newLineNumber?: number | null;
}

export interface ParsedDiffLine {
  id: string;
  type: 'add' | 'del' | 'hunk' | 'header' | 'context';
  text: string;
  oldLineNumber?: number | null;
  newLineNumber?: number | null;
  isCommentable: boolean;
}

export interface ParsedFileDiff {
  id: string;
  oldPath: string;
  newPath: string;
  displayPath: string;
  status: 'modified' | 'added' | 'deleted' | 'renamed' | 'binary' | 'mode_change';
  isBinary: boolean;
  headerLines: string[];
  lines: ParsedDiffLine[];
  additionsCount: number;
  deletionsCount: number;
  hasHunks: boolean;
}

export interface DiffComment {
  id: string;
  filePath: string;
  lineId: string;
  oldLineNumber?: number | null;
  newLineNumber?: number | null;
  lineType: 'add' | 'del' | 'context';
  lineText: string;
  content: string;
  createdAt: number;
}

// 原始统一 Diff 逐行解析器（保持向后兼容，增补行号）
export function parseUnifiedDiff(diffText: string): DiffLine[] {
  if (!diffText) return [];
  const rawLines = diffText.split(/\r?\n/);
  const result: DiffLine[] = [];

  let curOld = 0;
  let curNew = 0;
  let inHunk = false;

  for (let i = 0; i < rawLines.length; i++) {
    const line = rawLines[i];
    let type: DiffLine['type'] = 'context';
    let oldLineNumber: number | null = null;
    let newLineNumber: number | null = null;

    if (
      line.startsWith('diff --git') ||
      line.startsWith('index ') ||
      line.startsWith('--- ') ||
      line.startsWith('+++ ')
    ) {
      type = 'header';
      inHunk = false;
    } else if (line.startsWith('@@')) {
      type = 'hunk';
      inHunk = true;
      const match = line.match(/^@@\s+-(\d+)(?:,(\d+))?\s+\+(\d+)(?:,(\d+))?\s+@@/);
      if (match) {
        curOld = parseInt(match[1], 10);
        curNew = parseInt(match[3], 10);
      }
    } else if (line.startsWith('+')) {
      type = 'add';
      if (inHunk) {
        newLineNumber = curNew;
        curNew++;
      }
    } else if (line.startsWith('-')) {
      type = 'del';
      if (inHunk) {
        oldLineNumber = curOld;
        curOld++;
      }
    } else {
      type = 'context';
      if (inHunk) {
        oldLineNumber = curOld;
        newLineNumber = curNew;
        curOld++;
        curNew++;
      }
    }

    result.push({
      id: i,
      type,
      text: line,
      oldLineNumber,
      newLineNumber
    });
  }

  return result;
}

// 将 Unified Diff 按文件拆分并结构化解析
export function parseFilesFromUnifiedDiff(
  diffText: string,
  filesChanged?: string[]
): ParsedFileDiff[] {
  const files: ParsedFileDiff[] = [];
  if (!diffText && (!filesChanged || filesChanged.length === 0)) {
    return files;
  }

  const rawLines = diffText ? diffText.split(/\r?\n/) : [];

  // 按照 diff --git 切分文件块
  const fileChunks: string[][] = [];
  let currentChunk: string[] = [];

  for (let i = 0; i < rawLines.length; i++) {
    const line = rawLines[i];
    if (line.startsWith('diff --git ') && currentChunk.length > 0) {
      fileChunks.push(currentChunk);
      currentChunk = [line];
    } else {
      currentChunk.push(line);
    }
  }
  if (currentChunk.length > 0) {
    fileChunks.push(currentChunk);
  }

  // 逐个解析文件块
  fileChunks.forEach((chunk, fileIndex) => {
    let oldPath = '';
    let newPath = '';
    let isBinary = false;
    let isDeleted = false;
    let isAdded = false;
    let isRenamed = false;
    const headerLines: string[] = [];
    const diffLines: ParsedDiffLine[] = [];
    let inHunk = false;
    let curOld = 0;
    let curNew = 0;
    let additionsCount = 0;
    let deletionsCount = 0;
    let hasHunks = false;

    // 解析头部与行
    for (let lineIndex = 0; lineIndex < chunk.length; lineIndex++) {
      const line = chunk[lineIndex];

      if (!inHunk && !line.startsWith('@@')) {
        headerLines.push(line);

        const gitHeaderMatch = line.match(/^diff --git\s+a\/(.+?)\s+b\/(.+)$/);
        if (gitHeaderMatch) {
          oldPath = gitHeaderMatch[1];
          newPath = gitHeaderMatch[2];
        }

        if (line.startsWith('rename from ')) {
          oldPath = line.slice('rename from '.length).trim();
          isRenamed = true;
        } else if (line.startsWith('rename to ')) {
          newPath = line.slice('rename to '.length).trim();
          isRenamed = true;
        } else if (line.startsWith('deleted file mode')) {
          isDeleted = true;
        } else if (line.startsWith('new file mode')) {
          isAdded = true;
        } else if (
          line.startsWith('Binary files ') ||
          line.startsWith('GIT binary patch')
        ) {
          isBinary = true;
        } else if (line.startsWith('--- ')) {
          const from = line.slice(4).trim();
          if (from === '/dev/null') isAdded = true;
          else if (from.startsWith('a/')) oldPath = from.slice(2);
          else if (!oldPath) oldPath = from;
        } else if (line.startsWith('+++ ')) {
          const to = line.slice(4).trim();
          if (to === '/dev/null') isDeleted = true;
          else if (to.startsWith('b/')) newPath = to.slice(2);
          else if (!newPath) newPath = to;
        }
        continue;
      }

      // Hunk 行解析
      if (line.startsWith('@@')) {
        inHunk = true;
        hasHunks = true;
        const hunkMatch = line.match(/^@@\s+-(\d+)(?:,(\d+))?\s+\+(\d+)(?:,(\d+))?\s+@@/);
        if (hunkMatch) {
          curOld = parseInt(hunkMatch[1], 10);
          curNew = parseInt(hunkMatch[3], 10);
        }
        diffLines.push({
          id: `${fileIndex}-${lineIndex}`,
          type: 'hunk',
          text: line,
          oldLineNumber: null,
          newLineNumber: null,
          isCommentable: false
        });
      } else if (line.startsWith('+') && !line.startsWith('+++')) {
        additionsCount++;
        diffLines.push({
          id: `${fileIndex}-${lineIndex}`,
          type: 'add',
          text: line,
          oldLineNumber: null,
          newLineNumber: curNew,
          isCommentable: true
        });
        curNew++;
      } else if (line.startsWith('-') && !line.startsWith('---')) {
        deletionsCount++;
        diffLines.push({
          id: `${fileIndex}-${lineIndex}`,
          type: 'del',
          text: line,
          oldLineNumber: curOld,
          newLineNumber: null,
          isCommentable: true
        });
        curOld++;
      } else if (line.startsWith('\\ No newline')) {
        diffLines.push({
          id: `${fileIndex}-${lineIndex}`,
          type: 'header',
          text: line,
          oldLineNumber: null,
          newLineNumber: null,
          isCommentable: false
        });
      } else if (line.startsWith(' ')) {
        diffLines.push({
          id: `${fileIndex}-${lineIndex}`,
          type: 'context',
          text: line,
          oldLineNumber: curOld,
          newLineNumber: curNew,
          isCommentable: true
        });
        curOld++;
        curNew++;
      }
    }

    let status: ParsedFileDiff['status'] = 'modified';
    if (isBinary) status = 'binary';
    else if (isRenamed) status = 'renamed';
    else if (isDeleted) status = 'deleted';
    else if (isAdded) status = 'added';
    else if (!hasHunks && headerLines.some((h) => h.includes('mode change'))) {
      status = 'mode_change';
    }

    const displayPath = isDeleted ? oldPath || newPath : newPath || oldPath || `file-${fileIndex + 1}`;

    files.push({
      id: `file-${fileIndex}-${displayPath}`,
      oldPath: oldPath || displayPath,
      newPath: newPath || displayPath,
      displayPath,
      status,
      isBinary,
      headerLines,
      lines: diffLines,
      additionsCount,
      deletionsCount,
      hasHunks
    });
  });

  // 如果提供了 filesChanged，将差异中未涵盖的文件（如纯元数据或被截断文件）补全为占位节点
  if (filesChanged && filesChanged.length > 0) {
    const existingPaths = new Set(files.map((f) => f.displayPath));
    filesChanged.forEach((filePath, idx) => {
      if (!existingPaths.has(filePath)) {
        files.push({
          id: `file-extra-${idx}-${filePath}`,
          oldPath: filePath,
          newPath: filePath,
          displayPath: filePath,
          status: 'modified',
          isBinary: false,
          headerLines: [],
          lines: [],
          additionsCount: 0,
          deletionsCount: 0,
          hasHunks: false
        });
      }
    });
  }

  return files;
}

// 内存中审阅评论存储（按 threadID 和 scopeKey 隔离）
const reviewCommentsStore = new Map<string, DiffComment[]>();

export function getReviewComments(threadID: string, scopeKey?: string | null): DiffComment[] {
  const key = `${threadID}:${scopeKey || 'all'}`;
  return reviewCommentsStore.get(key) ?? [];
}

export function saveReviewComment(
  threadID: string,
  scopeKey: string | null,
  comment: DiffComment
): DiffComment[] {
  const key = `${threadID}:${scopeKey || 'all'}`;
  const existing = reviewCommentsStore.get(key) ?? [];
  const idx = existing.findIndex((c) => c.id === comment.id);
  let next: DiffComment[];
  if (idx >= 0) {
    next = [...existing];
    next[idx] = comment;
  } else {
    next = [...existing, comment];
  }
  reviewCommentsStore.set(key, next);
  return next;
}

export function deleteReviewComment(
  threadID: string,
  scopeKey: string | null,
  commentId: string
): DiffComment[] {
  const key = `${threadID}:${scopeKey || 'all'}`;
  const existing = reviewCommentsStore.get(key) ?? [];
  const next = existing.filter((c) => c.id !== commentId);
  reviewCommentsStore.set(key, next);
  return next;
}

// 汇总评论为可读反馈文本
export function formatCommentsFeedback(comments: DiffComment[]): string {
  if (!comments || comments.length === 0) return '';
  const lines: string[] = ['### 代码审阅反馈', ''];

  const grouped = new Map<string, DiffComment[]>();
  for (const c of comments) {
    const list = grouped.get(c.filePath) ?? [];
    list.push(c);
    grouped.set(c.filePath, list);
  }

  for (const [filePath, fileComments] of grouped.entries()) {
    lines.push(`**文件: \`${filePath}\`**`);
    for (const c of fileComments) {
      const lineLabel =
        c.newLineNumber != null
          ? `第 ${c.newLineNumber} 行`
          : c.oldLineNumber != null
          ? `旧第 ${c.oldLineNumber} 行`
          : '代码行';
      lines.push(`- ${lineLabel}: ${c.content}`);
    }
    lines.push('');
  }

  return lines.join('\n').trim();
}
