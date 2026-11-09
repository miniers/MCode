# 回合结束后运行态计时头残留

根因：回合头的运行态依赖线程状态，但时间线缓存只比较消息、展开状态等输入；最终答复先到、线程随后空闲且消息指纹不变时，增量重建直接返回带有 `workRunning: true` 的旧条目，跳过负责摘除临时头的折叠后处理。

以下为静态代码分析与修复计划。未运行复现、测试、构建或其他验证；未修改代码。截图对应的实际事件顺序仍需主代理验证。

1. **先锁定复现顺序与状态来源，不改折叠视觉。**

   - 运行态判断来自 `runningTurn`：线程的 `IsRunning || SwarmBusy` 为真，且没有本地乐观运行标记时，返回消息中的最大回合号。消息 `IsFinal`、`TurnStatus`、`TurnCompletedAt` 与 `liveID` 都不直接决定临时头是否运行。因此，最终答复已落库、线程仍在运行的那一帧，保留运行态头是现有行为。

     internal/ui/model.go:1201 `running = th.IsRunning || th.SwarmBusy`

     internal/ui/model.go:1207 `if !running || optimistic {`

     internal/ui/model.go:1210 `return true, s.maxMessagesTurn()`

     internal/ui/timeline_rows.go:1521 `if running, turn := s.runningTurn(threadID); running {`

   - `liveID` 是渲染时选出的末条助手消息 ID；它控制流式显示，不是折叠后处理的状态源。`liveGroupID` 只传给基础条目构建，不参与消息指纹比较。

     internal/ui/chat.go:176 `liveID = lastMsg.ID`

     internal/ui/timeline_incremental.go:123 `items := buildTimelineItemsFiltered(messages, expanded, liveID, groupsOpen)`

   - 最小事件顺序：先构建运行中的「用户 → 第一组工具 → 中途正文 → 第二组工具 → 最终答复」；最后一条答复到达时线程仍为运行态。随后收到空消息的空闲通知。Watch 对选中线程仍会递增消息版本，因此聊天入口会调用增量重建；但消息指纹完全一致，增量函数直接返回旧 items。这是已读代码中能够直接解释现象的路径。

     internal/ui/model.go:1632 `s.messages = UpsertMessages(s.messages, update.Messages, update.RemovedIDs)`

     internal/ui/model.go:1633 `s.messagesRev++`

     internal/ui/model.go:1687 `s.threads[i] = update.Thread`

     internal/ui/model.go:1709 `s.invalidate()`

     internal/ui/timeline_incremental.go:141 `if len(s.timelineSnapshotMsgs) == len(messages) {`

     internal/ui/timeline_incremental.go:144 `return s.timelineCacheItems, newFps`

   - 渲染器直接相信条目的 `workRunning`，不会重新核对线程状态。旧头每秒重绘，按当前时间减回合起点，因而会从真实用时 46s 继续涨到 101s。底部完成行按最新线程状态判断，且旧最终答复没有 `workHeaderID`，因此可以同时出现完成行与运行态头。

     internal/ui/timeline_rows.go:5846 `running := item.workRunning`

     internal/ui/timeline_rows.go:5868 `headerText = runningTurnHeaderLabel(item, c.Now())`

     internal/ui/timeline_rows.go:5721 `return turnHeaderRunningLabel(now.Sub(item.workStartedAt))`

     internal/ui/timeline_rows.go:5929 `c.After(time.Second)`

     internal/ui/chat.go:204 `turnRunning, latestTurn := s.runningTurn(selectedID)`

     internal/ui/timeline_rows.go:1909 `suppressTurnEnd := item.workHeaderID != ""`

2. **给时间线快照补上有效运行回合这个输入，在状态变化时退回现有全量构建。**

   - 在 `chatState` 的时间线快照字段旁新增一个 `timelineSnapshotRunningTurn int`。统一约定：空闲为 `-1`，运行中为 `runningTurn` 返回的回合号，保留合法回合号 `0`。复用已有线程 ID 与空快照判定处理首次构建，不额外增加有效性字段。

     internal/ui/model.go:284 `timelineSnapshotThreadID   string               // 上次时间线构建所属会话 ID`

   - 在 `internal/ui/timeline_incremental.go` 增加小型私有函数 `timelineRunningTurnKey(s, threadID)`，只调用现有 `s.runningTurn(threadID)` 并把空闲归一为 `-1`。在 `rebuildTimelineIncremental` 开头计算一次当前 key，将其与快照的比较加入全量重建条件；在该分支完成 `applyTurnWorkFold` 后写入捕获的 key。这样状态变化会在「指纹无变化」的直接返回之前失效，也覆盖直接调用增量函数的测试与其他调用方。状态没有变化时，保留现有的零变更快速返回。

     internal/ui/timeline_incremental.go:102 `func (s *chatState) rebuildTimelineIncremental(threadID string, messages []Message, expanded map[string]bool, liveGroupID ...string) ([]timelineItem, []messageFingerprint) {`

     internal/ui/timeline_incremental.go:117 `if len(s.timelineCacheItems) == 0 ||`

     internal/ui/timeline_incremental.go:125 `items = applyTurnWorkFold(s, threadID, items)`

     internal/ui/timeline_incremental.go:126 `s.timelineSnapshotGroupsOpen = groupsOpen`

   - 在主会话 `renderChat` 的 `needRebuild` 中加入同一个 key 的比较，保证仅更新线程摘要、没有递增 `messagesRev` 时也会重建。计算 key 时不能持有 `s.mu`，因为 `runningTurn` 自己加锁：先安全读取选中 ID，锁外取 key，再进入现有消息快照与缓存判断。若选中 ID 已改变，重新取对应 key，不能把另一个线程的 key 当成本线程输入。

     internal/ui/chat.go:73 `needRebuild := s.messagesRev != s.timelineCacheRev || s.timelineCacheMsgs == nil || expandedRev != s.timelineCacheExpandedRev || selectedID != s.timelineSnapshotThreadID || groupsOpenChanged`

   - 仅更新摘要的路径确实存在：侧栏线程同步更新运行态版本，不递增消息版本。这里应由时间线消费方识别新增输入，不给各种状态通知补写假的消息变更。

     internal/ui/sidebar_thread_sync.go:154 `s.bumpThreadsRuntimeLocked()`

   - 同步修改唯一的另一个生产调用方 `renderBeeTimeline` 的 `needRebuild`，使用相同 key。它原来也只看消息与展开等输入；不要把 `bee.Status` 改成主会话折叠状态源。该处仍按现有方式用 `bee.Status` 选择活动工具组，折叠继续遵循 `s.runningTurn`。

     internal/ui/swarm_bee_floats.go:1363 `needRebuild := s.messagesRev != s.timelineCacheRev || s.timelineCacheMsgs == nil || expandedRev != s.timelineCacheExpandedRev || selectedID != s.timelineSnapshotThreadID || groupsOpenChanged`

     internal/ui/swarm_bee_floats.go:1378 `items, fps := s.rebuildTimelineIncremental(selectedID, messages, expanded, liveToolGroupID(messages, bee.Status == "running"))`

   设计选择：运行回合发生变化时全量构建一次，复用已有折叠出口。无需同时给零变更返回添加另一套后处理，也无需在渲染器中强行关闭计时。该选择同时处理空闲、重新运行、`SwarmBusy` 与乐观状态解除，且不增加持续轮询或定时重建。

3. **失效拼装缓存，并确认增量前缀与中途正文不需要另改算法。**

   - 在两个渲染入口写入新 `timelineCacheItems` 时，将 `s.timelineAssembledValid` 置为 `false`。新增运行态输入可能触发同一个 `messagesRev` 下的重建，现有拼装缓存按 `timelineCacheRev` 命中；只换基础 items 而不失效，会继续画旧的拼装条目。显式失效比新增一组拼装 key 更小。

     internal/ui/chat.go:132 `s.timelineCacheItems = items`

     internal/ui/swarm_bee_floats.go:1382 `s.timelineCacheItems = items`

     internal/ui/timeline_rows.go:1651 `s.timelineAssembledRev == s.timelineCacheRev &&`

     internal/ui/timeline_rows.go:1654 `items := s.timelineAssembledItems`

   - 中途正文会停止运行态头的收集，后面的工具与答复留在头外。因而后续变更点可能落在头覆盖范围之外，旧头可以进入复用前缀；但只要最终进入 `applyTurnWorkFold`，这种前缀复用本身不会导致残留。

     internal/ui/timeline_rows.go:1461 `if it.kind == timelineItemSingle && it.msg.Role == "assistant" && !isReasoningMessage(it.msg) {`

     internal/ui/timeline_rows.go:1462 `stop = i`

     internal/ui/timeline_rows.go:1510 `msgLastIndex: itemLastMsgIndex(region[len(region)-1]),`

     internal/ui/timeline_incremental.go:209 `if itemLastMsgIndex(it) < safeMsgStart {`

     internal/ui/timeline_incremental.go:187 `copy(reusedItems, prevItems[:itemCutIdx])`

     internal/ui/timeline_incremental.go:198 `return applyTurnWorkFold(s, threadID, result), newFps`

   - 后处理按整个连续回合分段，包含复用前缀中的用户消息；先把用户之后的运行态头摊回原过程，再按最后一条非空助手正文确定最终答复。中途正文和两组工具会一起进入结束态折叠，最终答复留在外面。无需扩大临时头的覆盖范围，也无需让中途正文成为新的回合。

     internal/ui/timeline_rows.go:1530 `for end < len(items) && items[end].turnIndex == items[i].turnIndex {`

     internal/ui/timeline_rows.go:1438 `if len(segment) < 2 || segment[1].kind != timelineItemTurnWork || !segment[1].workRunning {`

     internal/ui/timeline_rows.go:1443 `out = append(out, segment[1].workItems...)`

     internal/ui/timeline_rows.go:1541 `segment = inlineRunningTurnHeader(segment)`

     internal/ui/timeline_rows.go:1412 `answers[it.turnIndex] = it.msg.ID`

     internal/ui/timeline_rows.go:1555 `if fold, ok := findTurnWorkFold(segment, lastAssistant, runningTurn, skipIDs); ok {`

     internal/ui/timeline_rows.go:1560 `tail[0].workHeaderID = folded.id`

   - `findTurnWorkFold` 确实会拒绝已折叠条目，但正常路径先摊开运行态头，故此幂等保护不是当前根因。`inlineRunningTurnHeader` 只处理 `segment[1]` 是既有结构约定；本次不扩大到任意位置，避免改变边界与历史折叠语义。

     internal/ui/timeline_rows.go:1246 `if segment[i].kind == timelineItemTurnWork {`

     internal/ui/timeline_rows.go:1248 `return timelineTurnWorkFold{}, false`

   - 保留时间线分割线与压缩边界的既有处理。截图中的时间戳文字本身不足以证明存在边界消息；只有消息种类符合既有判断时才中断折叠。若真实历史带有这两类消息，需要另按真实数据分析，不能为截图假定。

     internal/ui/timeline_rows.go:1432 `return isSplitSessionItem(item) || isContextCompactedItem(item)`

4. **新增不运行 GUI 的最小回归用例，覆盖原用例缺失的事件顺序。**

   - 文件：`internal/ui/timeline_turn_header_test.go`。测试名：`TestTimelineTurnHeader_IncrementalFinishedAfterIntermediateTextWithoutMessageChanges`。
   - 复用 `turnWorkSplitToolMessages()[:6]`，去掉第二回合；给用户起点和最终答复终点设置固定时间，间隔 46s。以 `chatState{threads: []Thread{{ID: "t1", IsRunning: true}}, messages: msgs}` 起步。需要按实际分组形态测试第一段工具组时，可保留 fixture 的 reasoning；再用不含 reasoning 的子用例覆盖截图中的纯命令起步。
   - 用 `helperBuildAndAssertIncremental` 依次提交：用户及第一段过程；追加中途正文和第二条工具；在线程仍运行时追加最终答复。每次更新 `s.messages` 并递增 `messagesRev`，让回合号等派生缓存遵循生产约定。断言运行态头仅一项、中途正文与第二条工具在头外、`a1` 没有 `workHeaderID`。
   - 单独将 `s.threads[0].IsRunning = false`，模拟选中线程空消息 Watch 帧，只递增 `messagesRev`，不改任何消息字段；再次调用同一 helper。修复前，这一步会拿旧运行态头与全量结束态结果比较失败。修复后，断言所有条目以及折叠内部均无 `workRunning == true`；仅一个结束态 `timelineItemTurnWork`；两条工具与中途正文在该项内；`a1` 留在头外且 `workHeaderID == fold.id`；头文案严格等于 `用时 46s`。
   - 再增加两个表格子用例：不变消息、仅 `SwarmBusy: true → false`；已有缓存仍运行、空闲与最终答复同批到达。后者证明当前正常路径继续工作。

     internal/ui/timeline_turnwork_test.go:14 `func turnWorkSplitToolMessages() []Message {`

     internal/ui/timeline_incremental_test.go:45 `func helperBuildAndAssertIncremental(t *testing.T, s *chatState, threadID string, messages []Message, expanded map[string]bool, testCase string) {`

     internal/ui/timeline_incremental_test.go:50 `wantItems := applyTurnWorkFold(s, threadID, buildTimelineItems(messages, expanded))`

     internal/ui/timeline_incremental_test.go:56 `if !reflect.DeepEqual(gotItems, wantItems) {`

   - 另在 `internal/ui/timeline_turnwork_test.go` 新增 `TestTimelineRows_TurnWorkFoldReplacesReusedRunningHeaderWithIntermediateText`：直接构建完整运行态 items；翻转线程为空闲后，把这份含旧头的 items 直接交给 `applyTurnWorkFold`；断言与从原始消息全量构建的结束态结果相同。该测试明确保护「复用前缀里的旧头可以正常摊开」的前提，不把修复误做成调整中途正文的分组规则。

     internal/ui/timeline_rows.go:1516 `func applyTurnWorkFold(s *chatState, threadID string, items []timelineItem) []timelineItem {`

   - 现有回归先把线程置为空闲，再追加最终答复；消息长度改变，必经折叠后处理。它没有覆盖「最终答复先到，后续空闲通知不改消息」的失败出口。

     internal/ui/timeline_turn_header_test.go:361 `s.threads[0].IsRunning = false`

     internal/ui/timeline_turn_header_test.go:363 `msgs = append(msgs, Message{ID: "a1", Role: "assistant", Kind: "text", Text: "构建通过", TurnIndex: 1, IsFinal: true, CreatedAt: start.Add(12 * time.Second).Format(time.RFC3339)})`

5. **新增渲染回归，覆盖没有消息版本变化与拼装缓存命中的情况。**

   - 文件：`internal/ui/timeline_turn_header_test.go`。测试名：`TestTimelineTurnHeader_StatusOnlyFinishReplacesCachedRunningHeader`。
   - 参照 `newTurnHeaderRunningTester` 的项目、线程与消息设置，但使用 `state, view := buildRoot(fake)` 获取可变 state，再创建 `ui.NewTester(view, 1000, 700)`。复用第 4 步的固定 46s 消息与 `SetThreadUsage`。让线程保持运行，最终答复已经在消息里；等待 `已处理` 与最终答复同时出现，并渲染至拼装缓存有效。
   - 在锁内仅把该线程 `IsRunning` 设为 false，保持 `messagesRev`、消息指纹、展开状态与计划不变；发出失效通知并推进帧。断言唯一头显示 `用时 46s`、没有 `已处理`、最终答复仍在外面，默认收起时没有第二条 `已完成 ·`。再推进 2s，头文字仍不变。点开头后，能看到中途正文、两条命令与完成详情，最终答复不会重复。
   - 检查 `state.timelineCacheItems` 与拼装结果均没有运行态条目；这样能分别捕获入口未重建、基础条目已换但拼装缓存未失效这两类遗漏。用例用设置固定的收起偏好，避免用户本地设置改变默认开合断言。
   - 补一个相同流程的 Watch 变体：在锁内设为空闲并递增 `messagesRev`，消息内容保持不变。无需直接调用会触发用量、额度及 Git 后台刷新的 `onWatchUpdate`；测试只模拟它在第 1 步已经确认的状态变化。

     internal/ui/timeline_turn_header_test.go:59 `func newTurnHeaderRunningTester(t *testing.T, msgs []Message, ready string) *ui.Tester {`

     internal/ui/timeline_turn_header_test.go:61 `running := Thread{ID: "t1", ProjectID: "p1", Title: "运行中", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}`

     internal/ui/timeline_turnwork_test.go:137 `state, view := buildRoot(fake)`

     internal/ui/timeline_turn_header_test.go:52 `fake.SetThreadUsage("t1", ThreadUsage{Turns: turns})`

     internal/ui/timeline_turn_header_test.go:174 `clickText(t, tt, "用时 12s")`

   - `renderBeeTimeline` 的入口同步修改后，由主代理按现有浮窗测试框架补同类状态变化断言；本方案未展开该框架，不能声称浮窗已验证。

验收范围：只改变状态变化时的缓存失效；运行中仍使用原「已处理 Ns」头，结束后仍使用原可展开的「用时 Ns」头，最终答复留在折叠体外。保留查找命中、跳转、分割线、压缩边界、工具组展开与减少动效的既有规则。

未完成事项：代码修复与全部回归测试均待主代理实现和执行；没有核对截图对应的实际 Claude 消息历史与事件时序；没有运行任何验证，也没有启动后台任务。
