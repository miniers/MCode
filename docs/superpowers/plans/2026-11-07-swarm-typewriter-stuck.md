# Swarm 派发后正文揭示与光标问题

## 已确认的因果链

- 后端分别发布主线程真实运行状态与 swarm 忙态。`internal/remote/memory_backend.go:558` `	if td.runTurnID != "" || td.runCancel != nil {`；`internal/remote/memory_backend.go:570` `	s.SwarmBusy = b.swarmBusyLocked(td)`。因此主线程已停止而 bee 仍运行时，可以出现 `IsRunning=false, SwarmBusy=true`。
- UI 将两者合并。`internal/ui/chat.go:115` `			running = th.IsRunning || th.SwarmBusy`；然后直接用于消息流式判断。`internal/ui/chat.go:169` `		isRunning := running`；`internal/ui/chat.go:173` `				liveID = lastMsg.ID`；`internal/ui/timeline_rows.go:2882` `	isStreamingNow := liveID != "" && m.ID == liveID`。
- 默认 Markdown 分支将光标附加到源文本。`internal/ui/swarm_mentions.go:887` `			return markdown.Revealed(markdown.Stream(c, findHighlightSource(s, m.ID, m.Text+streamingCursor), &s.mdOpts))`。因此只要最后消息仍是助手回复，bee 持续运行就会持续显示光标，且操作栏被流式状态压住。`internal/ui/timeline_rows.go:2809` `	return isLastAssistant && !isStreaming && revealed && strings.TrimSpace(m.Text) != ""`。这解释持续光标，但不单独证明正文永久缺失。
- swarm 委派对象闭合时即可派发，不必等待整个块或主回合结束。`internal/app/swarm_wiring.go:685` `	for _, entry := range st.parser.Feed(delta) {`。显示时先剥离包含未闭合块的 swarm 围栏，再调用正文渲染。`internal/ui/timeline_rows.go:2880` `	m.Text = s.stripSwarmFenceCached(m)`；`internal/ui/swarm_mentions.go:94` `		if inSwarm {`；`internal/ui/swarm_mentions.go:113` `			if isOrchestrationFenceInfo(info) {`。
- 揭示基线与显示正文长度并非始终同一口径：历史基线记录原始文本。`internal/ui/model.go:1421` `		s.revealProgress[m.ID] = &messageReveal{shown: len(m.Text)}`；渲染时传入的是剥离文本。`internal/ui/model.go:1455` `	if len(m.Text) <= st.shown {`。这会让重开会话后少量可见增量被视为旧内容而整段显示；`shown` 只是决定是否启用动画的字节长度，不能直接证明 Markdown 内部揭示进度卡住。已经进入流式模式后该分支直接返回。`internal/ui/model.go:1452` `	if st.streaming {`。

## 依赖库核对与证据边界

- 当前依赖版本见 `go.mod:8` `	github.com/egoist/mygo v0.8.4-0.20261107102458-435d9411845d`。下列 `ui/markdown/...` 路径相对于该依赖模块根目录，不属于本仓库。
- 依赖库 `ui/markdown/reveal.go:82` `	edited := rs.shown > len(src) || !strings.HasPrefix(src, rs.src[:rs.shown])`；`ui/markdown/reveal.go:85` `	if !streaming && !pending || r.Unit == AtOnce || edited {`。源文本缩短或已显示前缀改变时立即显示全文，而非等待追赶。光标拼在源文本末尾，随增量到来可能改变已揭示前缀，导致动画跳显；它不宜参与 Markdown 源文本。
- `internal/ui/chat.go:1002` `		s.mdOpts.Reveal = &markdown.Reveal{Unit: markdown.ByCharacter, CatchUp: 250 * time.Millisecond}`。字符模式不会等待句号或换行。依赖库 `ui/markdown/view.go:283` `		c.AnimationFrame()`，在揭示或淡入未完成时主动申请下一帧；`ui/markdown/reveal.go:265` `func (rs *revealState) revealed() bool { return rs.shown >= len(rs.src) && !rs.fading() }`。所以在组件持续挂载且动画帧正常交付的情况下，没有新增数据也应继续推进；源码本身不能证明截图中的半句永久不出现。需以实际视图回归测试区分帧未交付、虚拟列表重建和 Markdown 解析等原因，不能把原因武断归咎于长度不一致。
- 依赖库 `ui/markdown/reveal.go:211` `	return rs.fadeAt(c.Now(), c.Preferences().ReduceMotion, r)` 只在淡入阶段读取 ReduceMotion；`step` 本身仍按 `ByCharacter` 推进。当前设置因此关闭淡入但未关闭逐字揭示，与项目减少动效要求不符。

## 最小修复步骤

1. 在 `internal/ui/chat.go:105` 分开记录主线程 `IsRunning` 与整体 `IsRunning || SwarmBusy`。保留整体忙态供活动组、等待指示和回合逻辑使用；在 `internal/ui/chat.go:167` 计算消息 `liveID` 时只用主线程 `IsRunning`，确保 bee 仍运行而主线程已结束时转到 `lastLiveID` 的 `markdown.View` 收尾。仍运行的主线程沿用原行为，不能仅因有 swarm 块或 bee 已启动就提前结束。
2. 在 `internal/ui/swarm_mentions.go:883` 将流式光标从 Markdown 源移到独立的末尾 UI 元素，并保持与文本自然相邻；实现时注意 Markdown 块级布局及折行，若无法无损保持行内布局，先保留现有拼接并在测试确认截断是否与其有关后再选择实现。完成态始终以同一 Markdown 视图位置调用 `markdown.View`，保留依赖库未完成的揭示进度；不得用静态视图强制跳过正常收尾。
3. 在 `internal/ui/chat.go:1001` 为 `ReduceMotion` 使用独立的 `markdown.AtOnce`、`Fade: -1` 配置，偏好恢复时恢复原 `ByCharacter`，避免只在首次初始化时读取偏好；检查 `internal/ui/swarm_mentions.go:865` 和 `internal/ui/timeline_rows.go:3161` 使用相同配置的路径，避免修改共享静态配置或引入数据竞争。
4. 在 `internal/ui/model.go:1415` 与 `internal/ui/timeline_rows.go:2880` 统一历史基线与渲染输入的可见文本口径；需要覆盖 bee 浮窗及其他 `takeStreamReveal` 调用方，不能盲目把所有类型消息剥离。保留视图重建时重置基线的行为。此步骤修正已确认的长度口径问题，不应宣称它已证明截图中的截断根因。
5. 参考 `internal/ui/swarm_bee_reveal_test.go:31` 新增主会话 UI 回归：逐步输入普通正文、行内代码、未闭合及闭合的 swarm 块，模拟 bee 运行时主线程结束且不再有增量，持续推进时间，断言完整正文、光标消失、`Revealed` 最终为真、操作栏条件恢复；同时测试主线程仍在输出时光标保留、块缩短和新增正文、ReduceMotion 首帧完整显示、历史会话及视图销毁重建不重播。测试应使用真实 `renderChat` 状态路径，并对无新数据的后续帧验证，而不只调用单个判断函数。

## 未决事项

正文永久截断尚无可确定根因：必须由主代理运行上述回归或实测后定位；如果重现，进一步检查列表是否销毁或复用 Markdown 状态、动画帧是否交付及 Markdown 流式解析。没有执行构建、测试或任何验证，符合任务限制。
