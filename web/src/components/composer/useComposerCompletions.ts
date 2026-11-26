import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { MutableRefObject } from 'react';
import type { RemoteFileEntry } from '../../types/contract';
import type { RemoteProviderListWithCatalog } from '../../context/remoteTypes';
import type { DraftThreadMention } from '../../utils/threadMention';
import type { CommandScope, CompletionToken, SuggestionItem, TriggerQueryInfo } from '../../utils/slashCommands';
import { artifactLabel, commandScopeKey, commandToSuggestion, deleteCompletionToken, extractTriggerQuery, fileEntryToSuggestion, fileSearchScope, filterSkillCommands, filterSlashCommands, modelSuggestions, OPEN_ARTIFACT, rebaseCompletionTokens, skillCommandToSuggestion } from '../../utils/slashCommands';
import { useThreadMentionCandidates } from '../../hooks/useThreadMentionCandidates';
import { useRemoteStatic } from '../../context/RemoteContext';

export interface ComposerCompletionsArgs {
  draft: string;
  threadID?: string;
  selectedProjectID?: string;
  commandScope?: CommandScope;
  providers: RemoteProviderListWithCatalog;
  isRunning: boolean;
  artifactSelected: boolean;
  textareaRef: MutableRefObject<HTMLTextAreaElement | null>;
  onDraftChange: (value: string) => void;
  setDraftThreadMentions: React.Dispatch<React.SetStateAction<DraftThreadMention[]>>;
}

export function useComposerCompletions({
  draft, threadID, selectedProjectID, commandScope, providers, isRunning, artifactSelected,
  textareaRef, onDraftChange, setDraftThreadMentions
}: ComposerCompletionsArgs) {
  const { capabilities, commandSnapshots, searchThreadFiles, listThreadFiles, loadThreadCommands } = useRemoteStatic();
  const canMention = Boolean(
    threadID &&
    capabilities?.features?.includes('files.read') &&
    capabilities?.methods?.includes('files.search')
  );
  // `/` 命令与 `#模型` 委派都由宿主 feature `threads.commands` 管；旧宿主不弹，也不请求。
  const commandsSupported = Boolean(
    capabilities?.features?.includes('threads.commands') &&
    capabilities.methods.includes('threads.commands')
  );
  const canSlash = commandsSupported && Boolean(commandScope) && !artifactSelected;
  // `$` 技能候选同样来自 threads.commands，取数范围与 `/` 相同（草稿页用项目 + Provider）。
  const canSkill = commandsSupported && Boolean(commandScope);
  // 委派只对已有会话有意义；运行中宿主拒绝委派，不弹候选（发送侧另有提示）。
  const canDelegate = commandsSupported && Boolean(commandScope && 'threadID' in commandScope) && !isRunning;
  const scopeKey = commandScope ? commandScopeKey(commandScope) : null;
  const commandSnapshot = scopeKey ? commandSnapshots[scopeKey] : undefined;

  const scopeRef = useRef(commandScope);
  scopeRef.current = commandScope;

  // 补全状态：@ 文件（异步搜索）、/ 命令与 # 模型（本地过滤）共用一个弹层
  const [triggerInfo, setTriggerInfo] = useState<TriggerQueryInfo | null>(null);
  const [artifactPicker, setArtifactPicker] = useState(false);
  const completed = useRef<{ draft: string; tokens: CompletionToken[] }>({ draft, tokens: [] });
  useEffect(() => {
    completed.current = { draft, tokens: rebaseCompletionTokens(completed.current.draft, draft, completed.current.tokens) };
  }, [draft]);
  const artifactTypes = commandSnapshot?.commands.find((command) => command.name === OPEN_ARTIFACT)?.artifactTypes;
  const [fileSuggestions, setFileSuggestions] = useState<SuggestionItem[]>([]);
  const [selectedSuggestionIndex, setSelectedSuggestionIndex] = useState<number>(0);
  const [isLoadingSuggestions, setIsLoadingSuggestions] = useState<boolean>(false);
  const mentionDebounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mentionGenRef = useRef<number>(0);
  const triggerInfoRef = useRef<TriggerQueryInfo | null>(null);

  // `@` 补全里的会话候选：与文件候选共用一个菜单，会话排在文件之前，最多 5 条
  // （对应 internal/ui/composer_complete.go:303 的 mentionCompletionItems 顺序与 composer_complete.go:490 的上限）。
  const threadMentionCandidates = useThreadMentionCandidates({
    query: triggerInfo?.trigger === '@' ? triggerInfo.query : '',
    limit: 5,
    excludeThreadID: threadID,
    projectID: selectedProjectID
  });
  // 会话候选的 SuggestionItem 用 key 区分，选中时再取回 threadID / 标题
  const threadMentionPicks = useMemo(() => {
    if (!triggerInfo || triggerInfo.trigger !== '@') {
      return [] as { item: SuggestionItem; mention: DraftThreadMention }[];
    }
    return threadMentionCandidates.slice(0, 5).map((candidate) => ({
      item: {
        key: `thread:${candidate.threadID}`,
        title: candidate.insert,
        detail: candidate.detail ? `会话 · ${candidate.detail}` : '会话',
        group: '会话',
        icon: 'link' as const,
        kind: 'command' as const,
        insertText: ''
      },
      mention: { threadID: candidate.threadID, title: candidate.title } as DraftThreadMention
    }));
  }, [threadMentionCandidates, triggerInfo]);

  // 进入输入区拉一次命令清单（已有新鲜缓存时直接复用）；scope 变化（草稿切 Provider/项目）重新拉。
  const [commandsFailed, setCommandsFailed] = useState(false);
  const hasCommandSnapshot = Boolean(commandSnapshot);
  const hasCommandSnapshotRef = useRef(hasCommandSnapshot);
  hasCommandSnapshotRef.current = hasCommandSnapshot;
  const requestCommands = useCallback(() => {
    const scope = scopeRef.current;
    if (!scope) return;
    loadThreadCommands(scope).then(
      () => setCommandsFailed(false),
      () => setCommandsFailed(true)
    );
  }, [loadThreadCommands]);
  // 每个 scope 进入时请求一次；快照被清掉（重连重置）后再补拉一次，已有快照时不重复请求。
  const requestedScopeKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (!canSlash) {
      requestedScopeKeyRef.current = null;
      return;
    }
    if (requestedScopeKeyRef.current === scopeKey && hasCommandSnapshot) return;
    requestedScopeKeyRef.current = scopeKey;
    requestCommands();
  }, [canSlash, hasCommandSnapshot, requestCommands, scopeKey]);

  const resetTrigger = useCallback(() => {
    setArtifactPicker(false);
    if (mentionDebounceTimerRef.current) {
      clearTimeout(mentionDebounceTimerRef.current);
      mentionDebounceTimerRef.current = null;
    }
    mentionGenRef.current++;
    triggerInfoRef.current = null;
    setTriggerInfo(null);
    setFileSuggestions([]);
    setIsLoadingSuggestions(false);
  }, []);

  // 侦测光标处是否正在输入 @ / / / $ / # 片段；@ 防抖搜索文件，其余直接本地过滤
  const checkTrigger = useCallback(
    (text: string, cursor: number) => {
      const candidates: TriggerQueryInfo[] = [];
      if (canMention) {
        const info = extractTriggerQuery(text, cursor, '@');
        if (info) candidates.push(info);
      }
      if (canSlash) {
        const info = extractTriggerQuery(text, cursor, '/');
        if (info) candidates.push(info);
      }
      if (canSkill) {
        const info = extractTriggerQuery(text, cursor, '$');
        if (info) candidates.push(info);
      }
      if (canDelegate) {
        const info = extractTriggerQuery(text, cursor, '#');
        if (info) candidates.push(info);
      }
      // 多个触发符同时满足时取离光标最近的一个
      const info = candidates.sort((lhs, rhs) => rhs.startIndex - lhs.startIndex)[0] ?? null;
      if (!info) {
        resetTrigger();
        return;
      }

      const previousInfo = triggerInfoRef.current;
      if (!previousInfo || previousInfo.trigger !== info.trigger || previousInfo.query !== info.query) {
        setArtifactPicker(false);
        setSelectedSuggestionIndex(0);
      }
      triggerInfoRef.current = info;
      setTriggerInfo(info);
      if ((info.trigger === '/' || info.trigger === '$') && !hasCommandSnapshotRef.current) requestCommands();
      if (info.trigger !== '@') {
        // 本地过滤：丢弃在途的文件搜索
        if (mentionDebounceTimerRef.current) {
          clearTimeout(mentionDebounceTimerRef.current);
          mentionDebounceTimerRef.current = null;
        }
        mentionGenRef.current++;
        setFileSuggestions([]);
        setIsLoadingSuggestions(false);
        return;
      }

      if (mentionDebounceTimerRef.current) {
        clearTimeout(mentionDebounceTimerRef.current);
      }

      const currentGen = ++mentionGenRef.current;
      setIsLoadingSuggestions(true);

      mentionDebounceTimerRef.current = setTimeout(async () => {
        try {
          let entries: RemoteFileEntry[] = [];
          const { query, path } = fileSearchScope(info.query.trim());
          if (query.length > 0) {
            const res = await searchThreadFiles(threadID!, query, path);
            entries = res.entries;
          } else {
            // 查询词为空（仅输入了 @）：若具备 list 能力则优先拉取根目录，否则走搜索空串
            if (capabilities?.methods?.includes('files.list')) {
              const res = await listThreadFiles(threadID!, path);
              entries = res.entries;
            } else {
              const res = await searchThreadFiles(threadID!, '');
              entries = res.entries;
            }
          }

          if (mentionGenRef.current !== currentGen) return;
          // 受保护项不得补全，并限制建议项数量
          const filtered = entries.filter((entry) => !entry.protected)
            .sort((a, b) => Number(Boolean(a.ignored)) - Number(Boolean(b.ignored)))
            .slice(0, 8);
          setFileSuggestions(filtered.map((entry) => ({ ...fileEntryToSuggestion(entry), group: '文件' })));
          setSelectedSuggestionIndex(0);
        } catch {
          if (mentionGenRef.current === currentGen) {
            setFileSuggestions([]);
          }
        } finally {
          if (mentionGenRef.current === currentGen) {
            setIsLoadingSuggestions(false);
          }
        }
      }, 180);
    },
    [canDelegate, canMention, canSkill, canSlash, capabilities, listThreadFiles, requestCommands, resetTrigger, searchThreadFiles, threadID]
  );

  // 清理补全定时器及在途代际
  useEffect(() => {
    return () => {
      if (mentionDebounceTimerRef.current) {
        clearTimeout(mentionDebounceTimerRef.current);
        mentionDebounceTimerRef.current = null;
      }
      mentionGenRef.current++;
    };
  }, []);

  // 会话切换或对应能力消失时清理补全状态（运行中开始后 # 候选也随之收起）
  useEffect(() => {
    if (!triggerInfo) return;
    const stillAllowed =
      (triggerInfo.trigger === '@' && canMention) ||
      (triggerInfo.trigger === '/' && canSlash) ||
      (triggerInfo.trigger === '$' && canSkill) ||
      (triggerInfo.trigger === '#' && canDelegate);
    if (!stillAllowed) resetTrigger();
  }, [canDelegate, canMention, canSkill, canSlash, resetTrigger, triggerInfo]);
  useEffect(() => {
    resetTrigger();
    completed.current.tokens = [];
  }, [resetTrigger, scopeKey, threadID]);

  // `/` 与 `$` 共用同一份命令快照，未就绪时弹层显示加载中
  const commandTriggerLoading =
    (triggerInfo?.trigger === '/' || triggerInfo?.trigger === '$') &&
    !commandsFailed &&
    (!commandSnapshot || commandSnapshot.loading);
  const suggestionItems = useMemo<SuggestionItem[]>(() => {
    if (!triggerInfo) return [];
    if (triggerInfo.trigger === '@') return [...threadMentionPicks.map((pick) => pick.item), ...fileSuggestions];
    if (triggerInfo.trigger === '$') {
      if (!commandSnapshot) return [];
      return filterSkillCommands(commandSnapshot.commands, triggerInfo.query).slice(0, 30).map(skillCommandToSuggestion);
    }
    if (triggerInfo.trigger === '/') {
      if (!commandSnapshot) return [];
      if (artifactPicker && artifactTypes?.length) {
        return artifactTypes.map((type) => ({
          key: `artifact:${type.id}`,
          title: artifactLabel(type),
          detail: '',
          icon: 'command',
          insertText: `/${OPEN_ARTIFACT} ${type.id} `,
          kind: 'command'
        }));
      }
      return filterSlashCommands(commandSnapshot.commands, triggerInfo.query, commandSnapshot.recent)
        .slice(0, 30)
        .map((command) => commandToSuggestion(command, triggerInfo.query === ''));
    }
    return modelSuggestions(providers, triggerInfo.query);
  }, [artifactPicker, artifactTypes, commandSnapshot, fileSuggestions, providers, threadMentionPicks, triggerInfo]);
  const suggestionIndex = Math.min(selectedSuggestionIndex, Math.max(suggestionItems.length - 1, 0));
  const showSuggestions = Boolean(triggerInfo) && (suggestionItems.length > 0 || Boolean(commandTriggerLoading));
  const suggestionFootnote = (triggerInfo?.trigger === '/' || triggerInfo?.trigger === '$') && commandSnapshot
    ? commandSnapshot.loading
      ? 'Mac 正在读取命令列表，稍后会自动补全'
      : commandSnapshot.error
      ? '命令列表读取失败，仅显示内置命令'
      : suggestionItems[suggestionIndex]?.hint ?? null
    : null;

  // 选择建议项，精准替换触发片段，保留光标前后内容
  const handleSelectSuggestion = useCallback(
    (item: SuggestionItem) => {
      if (!triggerInfo) return;
      if (item.key === `command:${OPEN_ARTIFACT}` && artifactTypes?.length) {
        setArtifactPicker(true);
        setSelectedSuggestionIndex(0);
        return;
      }
      const pick = threadMentionPicks.find((entry) => entry.item.key === item.key);
      const before = draft.slice(0, triggerInfo.startIndex);
      const after = draft.slice(triggerInfo.endIndex);
      if (pick) {
        // 会话引用：草稿里只删掉 `@查询` 片段，再在草稿上方挂一颗引用胶囊
        // （对应 internal/ui/composer_complete.go:612 与 addDraftThreadMention 的去重规则）。
        onDraftChange(before + after);
        setDraftThreadMentions((previous) =>
          previous.some((mention) => mention.threadID === pick.mention.threadID)
            ? previous.map((mention) =>
                mention.threadID === pick.mention.threadID ? pick.mention : mention
              )
            : [...previous, pick.mention]
        );
        resetTrigger();
        requestAnimationFrame(() => {
          if (textareaRef.current) {
            const nextCursor = before.length;
            textareaRef.current.setSelectionRange(nextCursor, nextCursor);
            textareaRef.current.focus();
          }
        });
        return;
      }
      const replacement = item.insertText;
      const nextDraft = before + replacement + after;
      if (item.key.startsWith('file:') || item.key.startsWith('skill:')) {
        const tokens = rebaseCompletionTokens(completed.current.draft, nextDraft, completed.current.tokens);
        tokens.push({ start: before.length, end: before.length + replacement.trimEnd().length });
        completed.current = { draft: nextDraft, tokens };
      }
      onDraftChange(nextDraft);

      resetTrigger();

      requestAnimationFrame(() => {
        if (textareaRef.current) {
          const nextCursor = item.key.startsWith('artifact:') ? 0 : before.length + replacement.length;
          textareaRef.current.setSelectionRange(nextCursor, nextCursor);
          textareaRef.current.focus();
        }
      });
    },
    [artifactTypes, draft, onDraftChange, resetTrigger, threadMentionPicks, triggerInfo]
  );

  const deleteToken = (caret: number, key: 'Backspace' | 'Delete') =>
    deleteCompletionToken(draft, caret, key, rebaseCompletionTokens(completed.current.draft, draft, completed.current.tokens));

  return { triggerInfo, artifactPicker, deleteToken, suggestionItems, suggestionIndex, showSuggestions, suggestionFootnote, commandTriggerLoading,
    isLoadingSuggestions, handleSelectSuggestion, resetTrigger, checkTrigger, setSelectedSuggestionIndex, commandsSupported };
}
