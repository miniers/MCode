# mcode 统一动效模块设计

日期：2026-11-09。状态：设计完成，未实施。仅修改本文；未运行构建、测试、lint、类型检查、GUI 或其他验证。未读取 GOMODCACHE。MyGo 契约以主代理提供的 `github.com/egoist/mygo/ui v0.8.4-0.20261107102458-435d9411845d` 为准。

## 1. 决策与边界

保留 package mcodeui。在 `internal/ui/motion.go` 放偏好处理与小型帮助函数，在新增的 `internal/ui/motion_presets.go` 放两个弹簧和场景过渡。调用者继续直接使用 MyGo Element，不引入控制器、插值引擎、注册中心、全局时间轴、第三方依赖或新的组件框架。

唯一硬约束：`c.Preferences().ReduceMotion` 为 true 时，所有动画（含透明度、颜色、模糊、循环）关闭或立即到终点。运行中切换偏好也要生效。新的动效不使用 `NonMotion: true` 绕过此规则。不恢复弹簧白名单、循环禁令或时长 AST 白名单。当前时长与曲线 token 作为默认值继续存在；特定场景仍可通过 MyGo 原语定制。

直接拖动、resize、滚动和业务数据刷新仍需工作。它们不是自动补间：减少动效时不能禁止指针跟随、加载轮询、复制反馈到期或计时文字更新。只关闭其中用于装饰的逐帧更新。

另一个组件方案为 `docs/superpowers/plans/2026-11-09-ui-component-consolidation.md`。该方案中的“只用三个 duration”“不新增循环”“执行 motion_guard”已被本任务的新约定替代；不要修改或重建已删除的 motion_guard。其公共组件签名、布局与业务状态边界继续有效。本文的迁移必须等待相交的组件批次合并，不与其并行编辑同一文件。

## 2. 当前基础与容易踩错的契约

- internal/ui/motion.go:43 `func reduceMotion(c *ui.Context) bool {`
- internal/ui/motion.go:47 `func animateDuration(c *ui.Context, d time.Duration) time.Duration {`
- internal/ui/motion.go:57 `func staggerDelayFor(c *ui.Context, i int, step time.Duration) time.Duration {`
- internal/ui/motion.go:67 `func motionUnfoldBody(c *ui.Context, key string, open bool, fn func()) {`
- internal/ui/motion.go:73 `k := clip.AnimateTo(key+"/unfold", target, ui.Animation{Duration: animateDuration(c, motionUnfold), Ease: motionEaseStandard})`
- internal/ui/tokens.go:92 `DurFast  = 90 * time.Millisecond`
- internal/ui/tokens.go:93 `DurSnap  = 140 * time.Millisecond`
- internal/ui/tokens.go:94 `DurBase  = 200 * time.Millisecond`
- internal/ui/tokens.go:95 `DurSheet = 280 * time.Millisecond`
- internal/ui/tokens.go:96 `DurSlow  = 420 * time.Millisecond`
- internal/ui/tokens.go:104 `EaseOut       = ui.CubicBezier(0.165, 0.84, 0.44, 1)`
- internal/ui/tokens.go:105 `EaseSnap      = ui.CubicBezier(0.32, 0.72, 0, 1)`
- internal/ui/tokens.go:106 `EaseOvershoot = ui.CubicBezier(0.34, 1.3, 0.64, 1)`

现有 `motionFast=DurSnap`、`motionIconFade=DurFast`、`motionUnfold=DurBase`、`motionPop=DurBase`、`motionSweep=1200ms`、`motionRoll=900ms`、`motionRollFade=DurSlow` 暂不改变；保留 hover 延迟、4 DIP rise 和 2 DIP handle。数字滚动和长距离停靠已有独立节奏，不在引入接口时顺手调速。

MyGo 有三种不同的“零时长”语义，必须区分：

| 原语 | ReduceMotion 的上游行为 | mcode 的处理 |
| --- | --- | --- |
| Animate / AnimateWith | 直接到目标 | 继续使用，duration 可经 animateDuration |
| AnimateTo | 直接到目标，除非 NonMotion=true；Animation.Duration=0 在正常模式代表弹簧 | 弹簧直接传 preset；不要把 duration=0 当成跨原语的禁用方式 |
| Keyframes | 无 NonMotion 时立即完成，停最后一帧 | 有限序列可保留；循环优先在注册前显式分支 |
| Loop | 完全不检查 ReduceMotion，并持续请求下一帧 | 只能经 motionLoop 使用；减少动效时不调用 Loop |
| Transition | 位移会停；颜色仍可平滑变化；纯淡出、模糊仍可能播放；Duration=0 默认 200ms，负数禁用 | 每个场景 preset 都经 motionTransition；减少动效返回负时长且清空 Animation/Enter/Exit 的禁用描述 |
| Painter.After / Context.After | 不自动识别装饰动画 | 绘制时静态分支必须跳过 After；业务刷新定时器保留 |

不能只给 Transition 填 `animateDuration(c, DurBase)`，否则 0 又变回默认 200ms。不能只把 Enter/Exit 位移清零，因为颜色、淡出仍可能继续。不要在一个元素上调用两个 Transition 期望它们叠加；需要修改同一份描述后再包一次 motionTransition。

## 3. 生产调用盘点与迁移决定

使用 `rg -n '\bTransition\(|\bAnimateTo\(|\bAnimateWith\(|\.Animate\(|\bKeyframes\(|RepeatForever|\.Loop\(' internal/ui -g '*.go' -g '!**/*_test.go'` 静态检索，并阅读用途上下文。以下每条保留单行原代码；同一条链上的其他字段见相应文件。当前未发现生产 `.Loop(` 调用；因此 motionLoop 是新增安全入口，不是现有 Loop 漏洞修复。

### 3.1 展开收起、布局与锚定

- internal/ui/root.go:109 `collapse := shell.AnimateTo("sidebar-collapse", collapseTarget, ui.Animation{` —— 保留宽度裁切，复用 DurBase/EaseOut；不要用侧边浮层 preset 替换占位侧栏。
- internal/ui/motion.go:73 `k := clip.AnimateTo(key+"/unfold", target, ui.Animation{Duration: animateDuration(c, motionUnfold), Ease: motionEaseStandard})` —— 保留 motionUnfoldBody；它负责未知高度与关闭后销毁子树。
- internal/ui/timeline_rows.go:3216 `chevronRotation := header.AnimateTo("think-chevron", chevronTarget, ui.Animation{Duration: animateDuration(c, motionUnfold), Ease: motionEaseStandard})` —— 保留角度通道，与展开节奏一致。
- internal/ui/timeline_rows.go:3873 `rotation := btn.AnimateTo("todo-chevron-"+m.ID, chevronTarget, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})` —— 保留角度通道。
- internal/ui/timeline_rows.go:5948 `rotation = header.AnimateTo(item.id+"/chevron", chevronTarget, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})` —— 同上。
- internal/ui/timeline_background_tasks.go:530 `animatedHeight := wrap.AnimateTo("expand", height, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut})` —— 保留已测量高度通道，不另叠 Collapse。
- internal/ui/chat_anchor.go:376 `v = host.AnimateTo(anchorPadKey, a.padTarget, ui.Smooth)` —— 可用 springGentle；保留原 Jump、Arrived/Settled、padAnimating 状态机。
- internal/ui/chat.go:248 `chatBody.AnimateWith("swarm-reserve-trailing", swarmReserve.trailing, animateDuration(c, DurSheet), motionEaseStandard),` —— 保留两侧预留空间通道。
- internal/ui/chat.go:250 `chatBody.AnimateWith("swarm-reserve-leading", swarmReserve.leading, animateDuration(c, DurSheet), motionEaseStandard))` —— 同上。
- internal/ui/composer.go:688 `padBottom := container.AnimateWith("composer-dock-pad", dockPadBottom, animateDuration(c, composerDockDuration), ui.EaseOut)` —— 保留布局计算。
- internal/ui/composer.go:710 `container.Transition(ui.ElementTransition{` —— 保留 Position-only，外包 motionTransition，Animation 改为 springGentle。
- internal/ui/composer.go:715 `container.Transition(ui.ElementTransition{Colors: true})` —— 改 motionFeedback 或包装原描述，减少动效时不能走默认颜色过渡。
- internal/ui/composer.go:726 `width := outerCol.AnimateWith("composer-dock-width", dockWidth, animateDuration(c, composerDockDuration), ui.EaseOut)` —— 保留宽度通道。
- internal/ui/composer.go:774 `minH = card.AnimateWith("composer-card-min-h", minH, animateDuration(c, composerCardMinHDuration), ui.EaseOut)` —— 保留高度通道。
- internal/ui/composer_dock.go:14 `Spring: ui.SpringOptions{Duration: 420 * time.Millisecond, Bounce: 0},` —— 改为 `var composerDockTravelAnimation = springGentle` 的兼容别名，后续有需要再删别名。

### 3.2 浮层、弹窗、提示与页面进出

- internal/ui/popover.go:41 `k := panel.Keyframes("pop-in", []ui.Keyframe{` —— 组件 C 合并后在 anchoredPopover 统一用 motionPopover；保留 popoverRiseOffset 的实际翻转方向计算。
- internal/ui/controls_popover.go:64 `progress = panel.Keyframes("popover-enter", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, DurBase), Value: 1, Ease: EaseSnap}})` —— 组件 C 会删这套独立缩放，不再在此和 popoverRise 各播一次。
- internal/ui/approval.go:505 `Transition(ui.ElementTransition{` —— 采用 motionListItem，避免默认 Position/Size 跟随审批区高度变化。
- internal/ui/chat.go:274 `heroCol.Transition(ui.ElementTransition{` —— 保留 Exit-only 描述并经 motionTransition；不能为 Hero 引入位置/尺寸补间。
- internal/ui/chat.go:310 `timeline.Transition(ui.ElementTransition{` —— 保留 Enter-only、Colors-only 注册时机并经 motionTransition。
- internal/ui/swarm_bee_floats.go:885 `card.Transition(ui.ElementTransition{` —— 保留 Exit-only 描述并经 motionTransition；不叠第二套位置动画。
- internal/ui/composer_mcp_menu.go:296 `Transition(ui.ElementTransition{` —— 保留二级页横向 8 DIP 方向，包装 motionTransition；不要套升起 preset 改变导航语义。
- internal/ui/composer_complete.go:1287 `p := col.Animate("open", target, animateDuration(c, 220*time.Millisecond))` —— 保留快照退出与 MaxHeight；用 DurBase，不叠另一套 Collapse。
- internal/ui/composer_complete.go:1294 `slide := col.Keyframes("composer-complete-slide", []ui.Keyframe{` —— 保留 Run=层级号；同层输入不重新播放。
- internal/ui/composer_complete.go:1299 `Transition(ui.ElementTransition{` —— 包装 motionTransition，保留 Colors-only 与 AttachTo 位置。
- internal/ui/composer_attach.go:1254 `Transition(ui.ElementTransition{` —— 提示行用 motionListItem（折叠版），变化仅在公共附件容器内。
- internal/ui/composer_attach.go:1275 `Transition(ui.ElementTransition{` —— 同上，通知到期计时仍按业务原值。
- internal/ui/steward_task_page.go:204 `alpha := band.AnimateTo("enter", 1, ui.Animation{Duration: animateDuration(c, DurBase), Ease: motionEaseStandard})` —— 保留单次淡入；也可换 motionEnter 后只使用其透明度，不增加位移。
- internal/ui/update_popover.go:98 `progress = btn.AnimateTo("update-capsule-in", 1, ui.Animation{Duration: animateDuration(c, DurBase), Ease: motionEaseStandard})` —— 保留单次进度，不为同一更新状态重播。
- internal/ui/model_picker_anim.go:57 `progress := el.AnimateTo(modelPickerPageProgressKey, 1, ui.Animation{` —— 保留页深度和 Jump，直接原语无需万能页面 preset。
- internal/ui/onboarding.go:364 `pop := card.AnimateTo("onboarding-open", 1, ui.Animation{Duration: animateDuration(c, DurSheet), Ease: EaseOvershoot})` —— 卡片入口可用 motionDialog；不要同时留下原 Scale/Opacity 通道。
- internal/ui/onboarding.go:396 `progress := body.AnimateTo("step", 1, ui.Animation{Duration: animateDuration(c, DurSheet), Ease: motionEaseEmphasis})` —— 保留步序身份和内容进入通道。
- internal/ui/onboarding.go:493 `k := ring.Keyframes("ring", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, DurSlow), Value: 1, Ease: ui.EaseOut}})` —— 保留有限完成反馈。
- internal/ui/onboarding.go:497 `pop := check.AnimateTo("celebration", 1, ui.Animation{Duration: animateDuration(c, DurSheet), Ease: EaseOvershoot})` —— 可用 springSnappy，减少动效直接画完成图形。

当前 Toast 是 `c.Toast`，不是本地 ToastHost：

- internal/ui/root.go:81 `c.Toast(state.toastMessage)`
- internal/ui/root.go:181 `c.Toast(state.toastMessage)`

motionToast 提供给以后拥有可绘制 Toast Element 的宿主使用；本轮不凭空新增 Toast 管理器或改消息生命周期。给出的 MyGo 原语没有 c.Toast 的禁用选项，不能声称它已满足严格的减少动效约束；由主代理通过现有接口测试/开发版确认，若上游不遵循则单独补上游修复，不能静默删掉通知。

### 3.3 列表增删、交错与定位

- internal/ui/timeline_rows.go:502 `k := el.Keyframes("msg-enter-"+msgID, []ui.Keyframe{` —— 改 motionEnter(c, el, 同一 key, 0)，保留 shouldAnimate/seenMsgIDs 的首见判定；虚拟列表不一律注册退出动画。
- internal/ui/composer_attach.go:1168 `k := card.Keyframes("composer-attach-enter-"+a.LocalID, []ui.Keyframe{` —— 改 motionListItem 的进出，附件 ID 稳定；不要保留第二套 0.95 缩放。
- internal/ui/onboarding_hero.go:21 `k := el.Keyframes("entry", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, DurBase), Value: 1, Ease: motionEaseStandard}}, ui.KeyframeOptions{Delay: staggerDelayFor(c, index, DurFast)})` —— 改 motionEnter，复用 staggerDelayFor；保留原 index 与 step。
- internal/ui/timeline_rows.go:1936 `k := row.Keyframes(fmt.Sprintf("jump-flash-%d", flashSeq), []ui.Keyframe{` —— 保留有限跳转高亮；减少动效停末帧，定位滚动与选择态不丢。
- internal/ui/settings_tree.go:483 `pulse := row.Keyframes(fmt.Sprintf("settings-search-pulse-%d", settingsRowFocus.pulseSeq), settingsSearchPulseKeyframes())` —— 保留有限高亮；减少动效无闪烁，ScrollIntoView 与选中底色继续工作。

消息列表只给真实新消息入场，不给加载历史、滚动复用行或切会话批量交错。可移除的小型非虚拟列表（附件、队列、Toast）才用带 Exit 的 motionListItem。Exit 快照中的元素不应接收点击/焦点；实施时用 MyGo 已有语义与测试确认，不自建幽灵节点列表。

### 3.4 数字、计数、选中变化与 hover/按压

- internal/ui/rolling_number.go:157 `k = cell.AnimateTo("roll", 1, ui.Animation{Duration: animateDuration(c, motionRoll), Ease: motionEaseEmphasis})` —— 连同下一条收进 motionNumberProgress；rollingNumber 保留全部数字状态、Label、方向与裁切。
- internal/ui/rolling_number.go:158 `fade = cell.AnimateTo("fade", 1, ui.Animation{Duration: animateDuration(c, motionRollFade), Ease: ui.Linear})` —— 同上；减少动效不得保留离场数字。
- internal/ui/quota_ring.go:109 `outerValue = host.AnimateTo("outer", outerValue, ui.Animation{Duration: animateDuration(c, DurSlow), Ease: ui.EaseOut})` —— 保留比率动画，不强制换成弹簧以免超界。
- internal/ui/quota_ring.go:110 `innerValue = host.AnimateTo("inner", innerValue, ui.Animation{Duration: animateDuration(c, DurSlow), Ease: ui.EaseOut})` —— 同上。
- internal/ui/model_capsule_view.go:368 `progress := row.AnimateWith(key+"/progress", float32(filled), animateDuration(c, effortSignalDuration), ui.EaseOut)` —— 保留离散信号与定时通道。
- internal/ui/model_capsule_view.go:375 `Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})` —— 改 motionFeedback。
- internal/ui/model_picker_anim.go:68 `return el.AnimateTo(modelPickerTabSlideKey, sel, ui.Animation{` —— 改传 springSnappy；保留原索引与布局逻辑。
- internal/ui/onboarding.go:376 `v := dot.AnimateTo("active", target, ui.Animation{Duration: animateDuration(c, DurBase), Ease: motionEaseStandard})` —— 保留点选中状态通道。
- internal/ui/settings.go:1357 `hover := row.AnimateTo("settings-nav-hover", hoverTarget, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap})` —— 保留颜色混合进度；不让导航文字随 hover 放大。
- internal/ui/settings.go:1358 `sel := row.AnimateTo("settings-nav-sel", selTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: EaseSnap})` —— 同上。
- internal/ui/controls_button.go:216 `hover := b.AnimateTo("button-hover", hoverTarget, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap})` —— 保留颜色混合用的 hover 通道。
- internal/ui/controls_button.go:217 `press := b.AnimateTo("button-press", pressTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: EaseSnap})` —— 保留颜色用 press，缩放改由 motionPressScale 提供，避免将背景变化绑定到弹簧的超调。
- internal/ui/controls_button.go:233 `scale := 1 - 0.025*press` —— 替换为 motionPressScale；减少动效直接返回 1。
- internal/ui/steward_decision_card.go:172 `state := card.AnimateTo("answered", target, ui.Animation{Duration: animateDuration(c, DurFast)})` —— 保留已答复描边反馈。
- internal/ui/steward_decision_card.go:218 `press := btn.AnimateTo("press", pressed, ui.Animation{Duration: animateDuration(c, DurFast)})` —— 保留背景反馈；若加缩放只调用 motionPressScale 一次。
- internal/ui/composer_attach.go:998 `op := btn.AnimateTo("remove-fade", target, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})` —— 保留 hover/focus 揭示，不改变命中与焦点。
- internal/ui/timeline_rows.go:5501 `stamp.Opacity(stamp.AnimateTo("tool-group-time-reveal", reveal, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard}))` —— 保留时间戳揭示。
- internal/ui/side_pane.go:440 `ink := handle.AnimateTo("ink", sidePaneHandleInkTarget(hovered, held, elapsed), ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})` —— 保留拖拽手柄显隐；不得延迟实际 resize。
- internal/ui/effort_slider.go:329 `Transition(ui.ElementTransition{Colors: true, Duration: effortFastModeDuration})` —— 改 motionFeedback。
- internal/ui/effort_slider.go:352 `on := btn.AnimateTo(effortFastModeIconKey, iconOn, ui.Animation{Duration: animateDuration(c, effortFastModeDuration), Ease: ui.EaseOut})` —— 保留图标交叉淡化，NonMotion 必须为 false。
- internal/ui/settings_page_about.go:49 `progress = dot.AnimateTo("update-dot", 1, ui.Animation{Duration: animateDuration(c, DurFast), Ease: motionEaseStandard})` —— 保留有限更新标记。
- internal/ui/composer.go:1217 `Transition(ui.ElementTransition{Colors: true, Duration: motionFast})` —— 改 motionFeedback。
- internal/ui/composer.go:1222 `iconFade := btn.AnimateTo(composerSendIconFadeKey, 1, ui.Animation{` —— 保留 Jump 与双图标淡化；减少动效不建旧图标。
- internal/ui/composer_capsules.go:82 `Transition(ui.ElementTransition{Colors: true, Duration: motionFast})` —— 改 motionFeedback。

### 3.5 加载、呼吸和循环的实际停机情况

Keyframes + RepeatForever 的生产调用共五处，其中 controls_surface 的 Skeleton 无生产调用且组件 C 将删除。其余四处均有显式减少动效分支，静态阅读可确认该分支不注册循环；没有在本次运行 Tester 验证动态切换偏好。

| 来源与原代码 | 停机情况 | 迁移 |
| --- | --- | --- |
| internal/ui/timeline_skeleton.go:132 `breath := line.Keyframes("timeline-skeleton-breath", []ui.Keyframe{` | 128 行 reduceMotion 后 continue，透明度 1，确实跳过 RepeatForever | 保留静态 35% 填充；正常模式可保留有 Alternate 的关键帧 |
| internal/ui/onboarding_hero.go:30 `return el.Keyframes("pulse", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, motionSweep), Value: 1, Ease: ui.Linear}}, ui.KeyframeOptions{Repeat: ui.RepeatForever, Alternate: alternate})` | 27 行提前返回 0，确实不注册循环 | 保留反向播放的语义；非 alternate 场景可调用 motionLoop |
| internal/ui/timeline_background_tasks.go:624 `k := title.Keyframes(key+"/shimmer-pos", []ui.Keyframe{` | 613 行直接返回 fn，确实不注册扫光 | 扫光改 motionLoop，保留运行/静态分支与未知宽度短路 |
| internal/ui/sidebar_hover_card.go:288 `k := text.Keyframes("marquee", []ui.Keyframe{` | 277 行 !hovered 或 reduceMotion 直接返回，确实不注册 | 保留带 Delay/Alternate 的 Keyframes；减少动效使用省略号 |
| internal/ui/controls_surface.go:150 `progress = el.Keyframes("skeleton-pulse", []ui.Keyframe{` | period 经 animateDuration，0 时跳过；无活调用 | 等组件 C 删除，不为死代码加新帮助函数 |

按调用计数，不将 RepeatForever 注释或 KeyframeOptions 行重复计算。Alternate 使用原半周期：例如骨架 1.2s 到达另一端，完整往返为 2.4s；迁到 Loop 时不能把半周期误当完整周期。

另外两个有限层透明度：

- internal/ui/timeline_skeleton.go:158 `alpha := layer.AnimateWith("timeline-skeleton-layer/alpha", target, animateDuration(c, motionFast), motionEaseStandard)` —— 数据到位后立即静态消失，保留状态机与 max wait。
- internal/ui/composer_drop_overlay.go:160 `alpha := layer.AnimateWith("composer-drop-overlay/alpha", target, animateDuration(c, motionFast), motionEaseStandard)` —— 拖入/拖出反馈，保留外层生命周期。
- internal/ui/timeline_background_tasks.go:472 `fade := row.AnimateTo("stop", target, ui.Animation{Duration: animateDuration(c, DurFast), Ease: ui.EaseOut})` —— 停止后有限退出，保留停止时间与一秒业务刷新。

真正缺口在定时绘制，不能只查 RepeatForever：

- internal/ui/sidebar_steward.go:339 `p.After(time.Duration(frame-ms%frame) * time.Millisecond)` —— 总管头像忙碌点无 reduceMotion 分支，1000ms 周期、120ms 采样，当前会一直呼吸并排帧；在 Draw 内静态分支给 alpha=1 且跳过 Now/After。
- internal/ui/sidebar_bees.go:483 `p.After(time.Duration(frame-ms%frame) * time.Millisecond)` —— 3×3 运行点阵无偏好检查；同样保留低频局部 Draw，减少动效时固定亮度且不请求后续绘制。
- internal/ui/swarm_dispatch_placeholder.go:167 `c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)` —— 派发占位无偏好检查；减少动效时 pulse=1 且不 After。
- internal/ui/composer_capsules.go:133 `c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)` —— 实时胶囊无偏好检查；同上。
- internal/ui/composer_plan_pill.go:320 `c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)` —— 计划进度环 Running 分支无偏好检查；同上。
- internal/ui/controls_chip.go:175 `c.After(period / steps)` —— 已由 period>0 保护，period=animateDuration；已停机，不强制改为高刷 Loop。
- internal/ui/timeline_rows.go:3226 `c.After(175 * time.Millisecond)` —— 已在 isLive && !reduceMotion 分支内，保留低频路径。
- internal/ui/timeline_rows.go:6088 `c.After(175 * time.Millisecond)` —— 已在 !reduceMotion 分支内，保留。

不要为了“统一”把上述 8 档/局部 Draw 动画全部换成每显示帧都重建的 Loop。统一的是偏好契约和新增接口，现有低频绘制作为已知性能优化保留。`Now/After` 是 MyGo 原语；不另写计时器或波形引擎。`swarmDispatchPulseAlpha` 等现有透明度计算函数继续复用。

还存在多处 `ui.Spinner(c)`，提供的原语没有其 ReduceMotion 契约，不能默认其会停止。迁移入口通过 `controls_loading.go` 的 LoadingIndicator：减少动效时画有原 Label 的静态图形，正常模式才调用 Spinner。尺寸、文本色和可点击按钮布局由原场景保留。完整文件所有权见 M2、M4–M7；原 controls_message 的 Spinner 随组件 A 删除，无需迁移。

### 3.6 拖拽

- internal/ui/swarm_bee_floats.go:915 `x := card.AnimateWith("swarm-panel-x", f.mainLeft+pos.x, animateDuration(c, dur), ui.EaseOut)`
- internal/ui/swarm_bee_floats.go:916 `y := card.AnimateWith("swarm-panel-y", pos.y, animateDuration(c, dur), ui.EaseOut)`

拖动时直接 `Jump` 到指针位置；释放后可用 springGentle 或保留原短 EaseOut 停靠。只让一个位置通道拥有 Left/Top。减少动效时直接落到 rest；窗口停靠、resize、存储与多面板布局算法不动。

- internal/ui/settings_page_models.go:922 `row.Translate(0, offset)` —— 当前模型拖动和被挤行直接跳位置，可只给被挤行用 springSnappy；被拖行仍直接跟随。ReduceMotion 直接目标位移。不能用 ElementTransition 默认 Position/Size 与手动 Translate 同时控制位置。

## 4. lobe-mygo 参考结论

已先用 `curl --max-time 15` 请求 GitHub contents API，再用同样超时读取 `main/pkg/motion/` 的 easing.go、spring.go、spring_test.go、transition.go、transition_test.go，以及任务指定的全部八个消费文件，均存在且已阅读。参考仓库 MIT；只借鉴组织方式，不引入 Go 包或复制弹簧求解器。

来源：

- https://api.github.com/repos/lobehub/lobe-mygo/contents/pkg/motion
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/motion/easing.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/motion/spring.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/motion/transition.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/feedback/modal.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/feedback/drawer.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/feedback/toast.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/overlay/tooltip.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/overlay/popover.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/choice/switch.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/action/segmented.go
- https://raw.githubusercontent.com/lobehub/lobe-mygo/main/pkg/display/accordion.go

contents API 返回 spring.go blob `760a3f25051fefa550db299cad2b8b89f3503ae7`、transition.go blob `053741051b81bd8d44fb9538787b05d1bf2743dd`。这些是文件 blob，不是完整仓库版本；main 会变化，不把它当作 mcode 当前 MyGo 契约。

| 做法 | 读到的实现 | mcode 取舍 |
| --- | --- | --- |
| 弹簧 | Smooth(zeta=1, threshold=.001)、Snappy(.8,.001)、Bouncy(.7,.003)；质量 1，按 settle time 计算 omega，再设 stiffness=omega²、damping=2*zeta*omega | 直接用当前 MyGo 的 SpringOptions.Duration/Bounce；不搬 unitSettle/stepResponse 数值扫描 |
| Switch | thumb 用 Stiffness=360、Damping=24、Mass=1；位置与按下后宽度共用弹簧；track 颜色独立 | 取“几何与颜色分通道”；现有 ui.Switch 不重写，也不照搬其阴影 |
| Popup | 150ms 进入、75ms 退出；Scale=.96，Y=-6，Flip=true | 取进退分开；mcode 保留现有实际落点方向与 4 DIP，不机械套负方向 |
| Tooltip | Bouncy(280ms) 入场，100ms ExitEase 退场；内容变化单独 120ms 淡入，避免整框尺寸插值 | 取快退场；不新增第七个 Tooltip preset，公共 Tooltip 复用 motionPopover/原生行为 |
| Modal | 220ms/.97 入场，120ms/.98 退场；Backdrop 180ms 单独淡化 | motionDialog 使用 DurSheet/DurSnap；遮罩也必须经 motionTransition，不留隐藏的颜色过渡 |
| Drawer | 300ms 进入、220ms 退出，按边设置 ±100% 位移，Opacity=1 | 只提供左右侧边浮层，复用 DurSheet/DurBase；普通居中 Sheet 仍用 motionDialog |
| Toast | 400ms ToastEase；入退 YPercent=±150；stack y/scale/height 各有 AnimateTo，避免重复布局位移 | 提供小位移 Toast preset，不引入 Toast stack；现有 c.Toast 另核实上游偏好 |
| Segmented | 120ms 按压缩到 .98；选中指示器 240ms，共享 Match；源码还用 AnimatePose/ZIndex | 用现有元素的 Scale/AnimateTo；不依赖 brief 未提供的 Match/AnimatePose/ZIndex API |
| Accordion | AccordionBase 提供 Progress，箭头旋转、Opacity、6 DIP 位移共用进度 | 保留 motionUnfoldBody，避免同步两个独立高度引擎 |

指定参考文件没有通用 stagger helper，也未看到列表 index 延迟；不能把 mcode 的交错实现归功于它。交错继续复用本地 staggerDelayFor。lobe 的 Modal/Drawer 还委托内部 present 生命周期；额外尝试读取 `pkg/feedback/present.go` 返回 404，未假定其实现，也不照搬其退出生命周期。

## 5. API 与实现契约

### 5.1 最小签名表

以下均为包内函数；返回原生描述或标量，调用点无需组装新类型。

```go
// 保留
func reduceMotion(c *ui.Context) bool
func animateDuration(c *ui.Context, d time.Duration) time.Duration
func staggerDelayFor(c *ui.Context, i int, step time.Duration) time.Duration
func motionUnfoldBody(c *ui.Context, key string, open bool, fn func())
func rollingNumber(c *ui.Context, key string, value int) ui.Element

// motion.go：偏好边界和少量标量帮助函数
func motionTransition(c *ui.Context, t ui.ElementTransition) ui.ElementTransition
func motionPressScale(c *ui.Context, e ui.Element, key any, pressed bool) float32
func motionHoverScale(c *ui.Context, e ui.Element, key any, hovered bool) float32
func motionNumberProgress(c *ui.Context, e ui.Element, changed bool) (progress, fade float32)
func motionLoop(c *ui.Context, e ui.Element, key any, period time.Duration, ease ui.Easing) float32
func motionEnter(c *ui.Context, e ui.Element, key any, index int) float32

// motion_presets.go：用途明确的场景描述
func motionPopover(c *ui.Context, offsetY float32) ui.ElementTransition
func motionDialog(c *ui.Context) ui.ElementTransition
func motionSidePanel(c *ui.Context, fromLeft bool) ui.ElementTransition
func motionCollapse(c *ui.Context) ui.ElementTransition
func motionListItem(c *ui.Context) ui.ElementTransition
func motionToast(c *ui.Context) ui.ElementTransition
func motionFeedback(c *ui.Context) ui.ElementTransition
```

两个 ui.Animation 值：`springSnappy = ui.Animation{Spring: ui.SpringOptions{Duration: DurSheet, Bounce: 0.15}}`，`springGentle = ui.Animation{Spring: ui.SpringOptions{Duration: DurSlow, Bounce: 0}}`。Animation.Duration 保持 0，NonMotion 保持 false。作为包内只读预设使用；需要改 Delay 等字段时复制值，不修改共享变量。不再添加第三个弹簧；以后有具体需求可以直接定义 MyGo Animation。

### 5.2 减少动效的统一边界

`motionTransition` 正常模式原样返回传入描述；减少动效模式返回全新的 `ui.ElementTransition{Duration: -time.Nanosecond, ExitDuration: -time.Nanosecond, Colors: true}`。新描述的 Animation、Enter、Exit 为 nil，其余几何开关为 false。负时长明确关闭引擎动画；Colors=true 避免三项全 false 触发默认位置/尺寸路径。不要保留调用者传来的 Animation 指针或延迟，不只修改 Enter/Exit。

所有场景预设构建正常描述，最后一行调用 motionTransition。自定义场景一行可写 `card.Transition(motionTransition(c, ui.ElementTransition{...}))`。这不是禁止直接用 MyGo 的守卫；它提供审查时清晰可见的 ReduceMotion 边界。

如果偏好在动画运行中切到 true，AnimateTo 用原通道接收终值；Transition 用禁用描述取消活动进退场。对 Loop/RepeatForever/After 的旧请求允许至多一个已经排队的绘制回调，不得再从该回调排出下一帧。测试应覆盖切换后已退出的快照是否立即清理；若上游负时长不清理已有退出快照，需定位上游行为，不能仅凭返回值测试宣称完成。

### 5.3 场景预设默认值

| 函数 | 正常进入 | 正常退出 | 通道选择 |
| --- | --- | --- | --- |
| motionPopover(c, offsetY) | DurBase / EaseSnap，Y=offsetY，Opacity=0，Scale=.98 | DurFast / EaseOut，同向小位移、Opacity=0、Scale=.99 | 显式 Colors=true；不跟位置/尺寸 |
| motionDialog(c) | springGentle；Y=4、Opacity=0、Scale=.98 | DurSnap / EaseOut；Y=4、Opacity=0、Scale=.99 | Colors=true；遮罩独立用经包装的纯淡化描述 |
| motionSidePanel(c, fromLeft) | DurSheet / EaseSnap；XPercent=-100 或 +100，Opacity=1 | DurBase / EaseOut；相同侧的 XPercent，Opacity=1 | Colors=true；仅覆盖式侧板，不改变主区占位 |
| motionCollapse(c) | DurBase / EaseOut；Collapse=true，Opacity=0 | DurSnap / EaseOut；Collapse=true，Opacity=0 | Colors=true；只用于条件构建的小内容，不替代持久折叠测量 |
| motionListItem(c) | DurBase / EaseSnap；Y=4、Opacity=0、Collapse=true | DurSnap / EaseOut；Y=-4、Opacity=0、Collapse=true | Colors=true；非虚拟列表，避免全局默认 Size/Position |
| motionToast(c) | DurSheet / EaseOut；Y=8、Opacity=0 | DurSnap / EaseOut；Y=4、Opacity=0 | Colors=true；不管理消息与 timeout |
| motionFeedback(c) | DurSnap / EaseSnap，只有 Colors=true | 不设 Enter/Exit | 不动画布局；减少动效颜色立即切换 |

参数 offsetY 由现有 popoverRiseOffset(panel, anchor) 决定。上方/下方不能只按请求方向，因为框架可能翻转。保留现有测量与首帧默认方向；在浮层基础组件里统一求值。用新鲜的 Motion 指针构造每份 preset，调用者对 Enter/Exit 的局部改动不会污染另一个浮层。

注意：Motion 的零值 Opacity 表示淡入起点 0；侧板设 1 表示不淡化。不要把未设置 Scale 的 0 当作真的缩到 0。没有在 brief 中确认的 API 一律不作为实现依赖。

### 5.4 按压、hover、数字与交错

`motionPressScale`：正常目标为 pressed ? .975 : 1，使用 springSnappy 的 AnimateTo。减少动效直接返回 1；禁用按钮由调用方传 false。`motionHoverScale`：正常目标为 hovered ? 1.015 : 1，使用 springGentle；减少动效返回 1。普通文字导航只变颜色，不默认 hover 放大。两个函数只返回值，不擅自调用 Scale，调用方按 `s := motionPressScale(...); button.Scale(s, s)` 应用一次。保留当前命中与焦点语义，不把缩放变成布局尺寸。

`motionNumberProgress` 只给一个数字 cell 使用，固定沿用该元素本地的 `"roll"`/`"fade"` 两个通道。changed 表示“这位数字变化且有离场字符”，不直接把整串 changed 无条件传入。正常模式 changed 时对这两个通道 Jump 到 0，然后用原 motionRoll/motionRollFade 动画到 1；减少动效返回 (1,1)，并将已有通道 Jump 到 1，防止重新关闭减少动效时旧数字回滚。rollingDigitCell 的布局和离场条件不移动，只把通道代码换成一行调用；rollingNumber 的现有包级 256 项剪枝、方向、负号和可访问 Label 继续保留。

`motionEnter` 返回 0→1 的单次 Keyframes 进度，不主动操作透明度或位移。正常模式使用 DurBase/EaseSnap，Delay=`staggerDelayFor(c, min(max(index, 0), staggerLimit), staggerStep)`，最多 8 个 step；减少动效直接返回 1，不注册 Keyframes。调用者一行取进度后按需要用 Opacity/Translate；onboardingEntry 若必须保持原 DurFast 的交错节奏，可继续直接使用 staggerDelayFor(c, index, DurFast)，不为一次差异添加 options 结构。稳定 key 决定只播一次；用 Run 明确表示业务重播，不能每帧更换随机 key。

`motionLoop` 的核心必须是：

```go
if reduceMotion(c) || period <= 0 {
    return 0
}
return e.Loop(key, period, ease)
```

固定相位为 0，不读取时间、不调用 Loop、不补请求帧。传 nil ease 的处理跟随 MyGo；调用点统一传已知 ui.Linear/EaseOut 等值，不另增默认曲线分支。返回 0 表示相位，不保证每个消费者都自然得到正确静态外观：骨架静态 Opacity=1、运行点静态 alpha=1、跑马灯静态 Translate=0、扫光静态完全不画，仍由消费者的小分支明确表达。Normal 模式不把 period 经 animateDuration 转成 0 后交给 Loop；先做分支。

Loop 只提供相位，复杂的 Delay/Alternate/多帧节奏继续使用 MyGo Keyframes 并在减少动效时跳过注册。不得为统一 API 自建可配置循环波形或关键帧解释器。

## 6. 值得补的状态反馈

这些是落到公共组件的新增工作，不到八个复制调用点分别加动画。新文件目前尚未合并，以下定位使用其现有替换源锚点，不伪造新文件行号。

| 状态 | 唯一实施位置 | 一句话行为与现有锚点 |
| --- | --- | --- |
| 浮层升起/关闭 | controls_popover.go 的 anchoredPopover | 统一一次 rise 与快退；沿用实际翻转方向。internal/ui/popover.go:61 `popoverRise(panel, anchor)` |
| 居中 Sheet 进入/退出 | controls_sheet.go | 卡片用 motionDialog，遮罩单独包装，不对可拖动面板再开默认布局动画。internal/ui/settings_tree.go:276 `ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {` |
| 列表增删 | controls_tabs.go + 业务稳定 key 的队列/附件容器 | 标签出现/关闭采用有限进出，关闭事件先于激活；虚拟历史消息只做首见进入。internal/ui/file_manager_tabs_view.go:203 `func renderFileManagerTabItem(c *ui.Context, s *chatState, snap fileManagerPanelSnapshot, tab fileManagerTabSnapshot) {` |
| 选中切换 | controls_tabs.go、controls_toggle.go | 选中底色用 motionFeedback；模型选择器已有滑块继续复用，不另造共享匹配身份。internal/ui/controls_toggle.go:61 `item.Background(ThemeSurface(c))` |
| 数字变化 | controls_tabs.go、controls_toggle.go | 可见计数调用已有 rollingNumber；0 的隐藏条件仍由业务决定，避免多建 CountBadge。internal/ui/controls_toggle.go:76 `ui.Text(c, strconv.Itoa(segment.Count)).FontSize(TypeCaption).TextColor(color).SingleLine()` |
| 复制成功 | controls_copy.go | 图标换为对勾时有限淡入，Reduced 时直接成功态；只使用调用方 Copied，不接管业务到期计时。internal/ui/approval.go:877 `func renderPlanCopyButton(c *ui.Context, key, planText string) {` |
| 已知进度变化/完成 | controls_progress.go | 已知 Ratio 经 AnimateTo 平滑到新值并夹紧 [0,1]，完成直接用满值；未知进度不伪装 0%。internal/ui/composer_attach.go:1048 `func renderAttachmentUploadBar(c *ui.Context, a composerAttachment) {` |
| 加载结束 | controls_loading.go、controls_empty.go | 加载图形有限退出，结果/空态有限进入；不改页面何时切换分支。internal/ui/diff_panel.go:447 `func renderDiffStatusState(c *ui.Context, key string, icon *ui.SVG, title, subtitle string, onRetry func()) {` |
| 忙碌角标结束 | controls_avatar.go + 既有忙碌包装 | 公共头像只处理静态替换反馈，侧栏忙碌点归 sidebar_steward/sidebar_bees；不增加 Avatar.Busy。internal/ui/sidebar_steward.go:331 `renderStewardSectionAvatar(c, key+"-img", avatar)` |
| 表单提交忙碌/完成 | controls_form.go 的已有内容槽 | 操作行可以应用 motionFeedback；spinner 由 LoadingIndicator 统一处理，不新增提交状态机。internal/ui/git_form_windows_ui.go:944 `func renderGitFormActions(c *ui.Context, busy bool, primaryLabel string, primaryDisabled bool, primaryKey string, primary, onCancel func()) {` |
| 模型排序落位 | settings_page_models.go | 非被拖行平滑让位，被拖行跟随指针，减少动效直接新位置。internal/ui/settings_page_models.go:922 `row.Translate(0, offset)` |

复制计划按钮当前没有可供公共组件消费的成功状态；本次不新建全局计时器，不凭点击执行动画宣称复制成功。公共 CopyButton 只有接收到 Copied 才画成功反馈，计划入口的状态补齐作为明确的后续业务项。此处与组件方案“允许计划保持无反馈”一致。

不为 EmptyState 增加插图、说明文字或循环；不为 Avatar 叠加持续闪光；不为 ProgressBar 引入新的阶段状态。已有状态层次足以表达结果。

## 7. 实施批次与文件所有权

下面 M1–M7 的可修改集合互不重叠，测试文件也只有一个所有者。列为“新”的文件是计划产物，未创建。各批实施者不得顺手修改另一批的文件。先合并 M1；M2 在组件 A/B/C/D/G 合并后进行，剩余批次按依赖和相交组件批次排队。主代理统一执行下列测试；本次设计没有运行任何命令检查。

### M1：动效基础与行为测试

可修改完整集合：

- internal/ui/motion.go
- internal/ui/motion_presets.go（新）
- internal/ui/motion_test.go
- internal/ui/motion_system_test.go（新）
- internal/ui/visual_style_guard_test.go

与组件抽离：无文件交集，可以先做。不得改 tokens.go、go.mod、go.sum 或任何业务调用文件。

步骤：

1. 保留现有 token、reduceMotion、animateDuration、staggerDelayFor、motionUnfoldBody 与其签名；新增第 5 节函数及两个弹簧。
2. 所有场景 Transition 显式选通道并经过 motionTransition。motionLoop 在 Reduced/非法 period 时返回 0，不调用 Loop；motionEnter 直接返回 1。数字通道辅助不移动 rolling_number.go。
3. 删除下列时长测试和专用表/函数，改文件头注释为只守阴影与状态色；共享 eachProductionFile、styleViolation、writeProbeFile、requireViolation、AST 导入继续供剩余测试使用，只有确实无引用的导入才移除。
4. 保留已有 MotionTokens/Stagger/AnimateDuration/Unfold 测试；补系统行为测试，不新建 AST 白名单。

锚点：

- internal/ui/visual_style_guard_test.go:54 `func TestMotionDurationUsesTokens(t *testing.T) {`
- internal/ui/visual_style_guard_test.go:209 `var motionDurationTokens = map[string]bool{`
- internal/ui/visual_style_guard_test.go:228 `var motionDurationBaseline = map[string]int{`
- internal/ui/visual_style_guard_test.go:242 `func motionDurationViolations(t *testing.T, dir string) []styleViolation {`
- internal/ui/visual_style_guard_test.go:18 `func TestShadowOnlyOnOverlays(t *testing.T) {`
- internal/ui/visual_style_guard_test.go:104 `func TestStatusColorsUseThemeTokens(t *testing.T) {`
- internal/ui/motion_test.go:106 `func TestMotionUnfoldBodyExpandsAndCollapses(t *testing.T) {`

必须的测试：

1. `TestMotionSystemReduceMotion` 用 `ui.NewTesterWith(view, w, h, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})` 从首帧设置偏好；不使用先 NewTester 再 SetPreferences 的方式作为唯一覆盖，因为那会先注册一次动画。
2. view 内仅建一个测试元素并调用 motionLoop。读取返回值始终为 0；初始布局完成后，用 Frame/Advance 消费必要的一次布局帧，随后 `FramePending()` 为 false。正常模式同一测试元素会请求帧且进度变化；period=0/负值也不排帧。禁止在测试 view 里调用其他计时器或 Spinner 污染判断。
3. 表驱动遍历七个场景 preset（SidePanel 左右各测），在上面的 Reduced Tester 中先显示、修改样式/目标，再移除元素；Frame 后即终态：无起点位移/缩放/半透明、Collapse 不占高度、退出快照不再显示、FramePending 不持续为 true。读取可访问 Label/几何；颜色/透明度用项目现有渲染采样能力检查，不能只测试 preset.Duration 小于 0 就声称渲染已验证。现有 Tester 支持哪些采样能力由实施者按项目例子确认，不发明方法。
4. 正常模式分别检查进入/退出非零、中间状态、最终停止；弹簧只测到位与容差，不要求和 EaseOut 一样单调。过渡不意外插值业务 Width/Left。自定义 Colors-only 和 Exit-only 的 motionTransition 同测。
5. 首帧普通偏好启动动画，中途 SetPreferences 为 ReduceMotion=true，消费至多一个已排队回调后无持续帧；包括 Loop、Exit 快照和带 Delay 的 motionEnter。恢复偏好后按正常新动作播放，不回放旧数字。
6. press/hover reduced 始终为 1；数字 reduced 为 (1,1)，正常 changed 重置、未变不重播；交错 index<=0 为 0，超过 8 封顶；Unfold reduced 立即展开/关闭并销毁子树。保留正常 Unknown-height 测试。
7. 单独执行 TestShadowOnlyOnOverlays、TestStatusColorsUseThemeTokens，确认删除时长辅助没有误删共享遍历逻辑。

### M2：公共组件统一接入

可修改完整集合：

- internal/ui/controls_popover.go
- internal/ui/popover.go
- internal/ui/controls_sheet.go（组件 C 新建后）
- internal/ui/controls_copy.go（组件 A 新建后）
- internal/ui/controls_progress.go（组件 A 新建后）
- internal/ui/controls_avatar.go（组件 B 新建后）
- internal/ui/controls_empty.go（组件 B 新建后）
- internal/ui/controls_loading.go（组件 B 新建后）
- internal/ui/controls_tabs.go（组件 D 新建后）
- internal/ui/controls_form.go（组件 D 新建后）
- internal/ui/controls_setting.go（组件 G 新建后）
- internal/ui/controls_section.go（组件 G 新建后）
- internal/ui/controls_button.go
- internal/ui/controls_toggle.go
- internal/ui/controls_chip.go
- internal/ui/motion_controls_test.go（新）

交集：组件 A/B/C/D/G，上述新文件和 popover.go/controls_popover.go 必须等对应批次合并。为了避免跨轮回头改公共文件，M2 统一等 G 也合并；它不阻塞 M3/M4 中互不相交的文件。组件 C 已删除的 controls_surface Skeleton 不在本批重建。

步骤：

1. anchoredPopover 只应用一份 motionPopover，沿用 popoverRiseOffset 翻转计算；如果旧 popoverRise 签名无 Context，可在其唯一公共底层适配而不逐业务调用修改。保留 Dismissed 检查时序与无外观 Base。
2. Sheet 的卡片和遮罩分别使用 motionDialog/经 motionTransition 的纯淡化，拖动和 resize 不被补间接管。不要向 DialogBase/PopoverBase 外层添加卡片外观。
3. CopyButton 消费 Copied 的有限反馈；ProgressBar 插值 clamp 后 Ratio；TabButton 选中颜色、数量和增删反馈由公共组件实现。FormActions/SettingRow/SectionHeading 不强加无意义入场，只有已有状态变化才使用反馈；Avatar 不增加 Busy/Selected 字段。
4. LoadingIndicator 在减少动效时完全跳过 Spinner；核对其 key、Label、尺寸和色彩能保留既有使用。无需为未发生的加载完成创建新业务状态。renderButton 的 Loading 分支用这个入口，正常保持 fg，减少动效静态图形仍表达忙碌。
5. Button 缩放统一 motionPressScale，保留原颜色进度；Segmented 选中颜色及 Count 数字统一；StatusDot 保留低频周期分支，不提高刷新率。

锚点：

- internal/ui/popover.go:29 `func popoverRiseOffset(panel, anchor ui.Element) float32 {`
- internal/ui/controls_popover.go:39 `func PopoverCard(c *ui.Context, p PopoverProps, content func()) ui.Element {`
- internal/ui/controls_button.go:233 `scale := 1 - 0.025*press`
- internal/ui/controls_button.go:236 `ui.Spinner(c).Size(iconSize, iconSize).TextColor(fg)`
- internal/ui/controls_toggle.go:76 `ui.Text(c, strconv.Itoa(segment.Count)).FontSize(TypeCaption).TextColor(color).SingleLine()`
- internal/ui/controls_chip.go:166 `period := animateDuration(c, statusDotPulsePeriod)`

测试：在新 motion_controls_test.go 覆盖 Reduced 下复制成功图标、进度立即终值、标签选中与 Count、spinner 静态且无循环帧；正常反馈完成后停止。Popover 正反方向、窗口边界翻转、Escape/外部点击回调一次、卡片一层；Sheet 拖动/resize 和失焦；Disabled/Loading 不可点击。既有组件测试只执行，不由本批改写；若测试断言依赖旧动画时序，在本批新文件里补真实回归，由 lead 协调原断言变更，不能跨所有权编辑。

### M3：低频循环停机与侧栏

可修改完整集合：

- internal/ui/sidebar_steward.go
- internal/ui/sidebar_bees.go
- internal/ui/sidebar_hover_card.go
- internal/ui/sidebar.go
- internal/ui/swarm_dispatch_placeholder.go
- internal/ui/composer_capsules.go
- internal/ui/composer_plan_pill.go
- internal/ui/motion_busy_test.go（新）

交集：组件 B 的 sidebar_steward/sidebar_bees、组件 I 的 sidebar，必须等 B/I 合并；业务未迁完 LoadingIndicator 时还等 M2。保留组件替换后的静态头像调用。

步骤：

1. 给两处 Painter.Draw 呼吸加减少动效静态分支，跳过 Now/After；保留正常局部绘制的 120ms/200ms 采样。
2. 给派发占位、LiveDot、PlanPill 的装饰 After 分支加偏好检查；复用现有 alpha 计算，正常仍 8 档；减少动效静态 alpha=1。
3. Composer Capsule 的颜色 Transition 改 motionFeedback；跑马灯保持 Delay/Alternate，仅核实偏好中途改变后不再排帧。
4. sidebar.go 的加载 Spinner 改 LoadingIndicator；队列/项目原生拖放由组件 I 持有，不改 payload 或排序事务。

锚点：

- internal/ui/sidebar_steward.go:339 `p.After(time.Duration(frame-ms%frame) * time.Millisecond)`
- internal/ui/sidebar_bees.go:483 `p.After(time.Duration(frame-ms%frame) * time.Millisecond)`
- internal/ui/swarm_dispatch_placeholder.go:167 `c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)`
- internal/ui/composer_capsules.go:133 `c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)`
- internal/ui/composer_plan_pill.go:320 `c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)`
- internal/ui/sidebar.go:2046 `ui.Spinner(c).Size(12, 12).TextColor(ThemeAccent(c))`

测试：每种图形单独构建 Reduced Tester，静态图形仍可见、无装饰后续帧；正常模式按原采样更新。切换偏好后消耗一个已排回调不再续排。busy→done/remove 立即去角标，不能误关业务刷新。侧栏过滤时仍不拖放；跑马灯离开/减少动效立即回原位，标题省略正常。

### M4：时间线、停靠与主布局

可修改完整集合：

- internal/ui/timeline_rows.go
- internal/ui/timeline_background_tasks.go
- internal/ui/timeline_skeleton.go
- internal/ui/approval.go
- internal/ui/chat.go
- internal/ui/root.go
- internal/ui/chat_anchor.go
- internal/ui/composer.go
- internal/ui/composer_dock.go
- internal/ui/composer_drop_overlay.go
- internal/ui/steward_decision_card.go
- internal/ui/steward_task_page.go
- internal/ui/motion_timeline_test.go（新）

交集：组件 A 的 timeline_rows/approval、组件 I 的 composer/composer_drop_overlay，等 A/I 合并；LoadingIndicator 消费等 M2。此批与 M3 可并行，文件无重叠。

步骤：

1. 迁所有本批 Transition 为明确 preset 或 motionTransition，尤其 Hero Exit、timeline Enter、Composer Position-only。保留 promote/Transitioning 的注册时机，禁止把输入框塞进 Hero 退出快照。
2. 原布局标量 AnimateWith/AnimateTo 保留；Composer 长距离弹簧复用 springGentle；chatAnchor 保留 Jump/Arrived；sidebar-collapse 保留裁切而非改覆盖抽屉。
3. 新消息用 motionEnter 且只触发原首见条件，有限跳转闪烁与各 chevron 保留；骨架 Alternate 继续显式减少动效短路；扫光循环改 motionLoop，但减少动效完全不建 band。保留 thinkPulse 的低频已保护分支。
4. 将本批文件中的 Spinner 替换为 LoadingIndicator；renderCopy/Progress 等已抽公共组件的路径不再加独立动效。发送按钮旧图标在 reduced 时不保留。
5. root 的 c.Toast 和原生 Switch 类上游组件只做偏好行为验证；不发明 ToastHost。若上游仍播放，列明外部阻塞并交 lead 修上游，不删除消息。

锚点：

- internal/ui/chat.go:274 `heroCol.Transition(ui.ElementTransition{`
- internal/ui/composer.go:710 `container.Transition(ui.ElementTransition{`
- internal/ui/timeline_rows.go:502 `k := el.Keyframes("msg-enter-"+msgID, []ui.Keyframe{`
- internal/ui/timeline_background_tasks.go:624 `k := title.Keyframes(key+"/shimmer-pos", []ui.Keyframe{`
- internal/ui/timeline_skeleton.go:128 `if reduceMotion(c) {`
- internal/ui/chat_anchor.go:374 `v = host.Jump(anchorPadKey, a.padTarget)`
- internal/ui/root.go:181 `c.Toast(state.toastMessage)`

测试：沿用 timeline_background_tasks、chat_anchor、composer_dock、timeline_skeleton 的相关用例，另在 motion_timeline_test.go 覆盖 ReduceMotion 首帧、运行中切换、Hero→Dock 一帧终态、审批高卡不遮挡提交、消息历史/虚拟行复用不入场、骨架加载完成消失；有业务一秒计时器的集成场景不要求整个页面 FramePending=false。无业务定时器的孤立动效场景必须停止排帧。Toast 的实际表现必须记录验证状态。

### M5：设置、模型、额度与浮窗拖拽

可修改完整集合：

- internal/ui/settings.go
- internal/ui/settings_tree.go
- internal/ui/settings_page_about.go
- internal/ui/settings_page_models.go
- internal/ui/settings_page_skills.go
- internal/ui/effort_slider.go
- internal/ui/model_picker_anim.go
- internal/ui/model_capsule_view.go
- internal/ui/quota_ring.go
- internal/ui/side_pane.go
- internal/ui/update_popover.go
- internal/ui/swarm_bee_floats.go
- internal/ui/motion_panels_test.go（新）

交集：组件 G 的 settings_tree/settings_page_skills、组件 E 的 swarm_bee_floats，必须等 G/E 合并；已迁到 Sheet/Avatar/EmptyState 的动效从 M2 继承，不返回业务层重复添加。

步骤：

1. 本批所有颜色 Transition 改 motionFeedback；模型二级页保留业务 Run/Jump；Tab slide 可用 springSnappy，quota ratio 保留有界时长补间。
2. 设置搜索有限高亮不改为常驻 Loop；减少动效末帧不闪，ScrollIntoView 与选中底色照常。已有搜索过期计时是业务反馈到期，保留。
3. 浮窗 Exit-only 经 motionTransition；拖动位置直接 Jump，松手位置只由一个 AnimateTo/AnimateWith 通道持有；保留停靠与 resize 算法。模型排序只给未被拖行让位加 springSnappy，直接拖行不加滞后。
4. 本批 Spinner 路径统一 LoadingIndicator；原生设置 Switch 要在 reduced 下确认没有残余颜色或位移动画，不在缺契约时声称已修。

锚点：

- internal/ui/settings_tree.go:483 `pulse := row.Keyframes(fmt.Sprintf("settings-search-pulse-%d", settingsRowFocus.pulseSeq), settingsSearchPulseKeyframes())`
- internal/ui/effort_slider.go:329 `Transition(ui.ElementTransition{Colors: true, Duration: effortFastModeDuration})`
- internal/ui/model_capsule_view.go:375 `Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})`
- internal/ui/swarm_bee_floats.go:885 `card.Transition(ui.ElementTransition{`
- internal/ui/swarm_bee_floats.go:915 `x := card.AnimateWith("swarm-panel-x", f.mainLeft+pos.x, animateDuration(c, dur), ui.EaseOut)`
- internal/ui/settings_page_models.go:922 `row.Translate(0, offset)`

测试：motion_panels_test.go 检查模型页切换、索引快速反向、额度 0/1/跳变不越界、设置跳转可见与 reduced 不闪；浮窗移动松手/关闭到终态、拖动不被 Transition Size/Position 接管。完整原生拖放与 resize 在 dev 数据隔离窗口人工确认，Tester 不支持时明确记录，不编造 API。

### M6：附件、引导、补全与数字

可修改完整集合：

- internal/ui/composer_attach.go
- internal/ui/composer_complete.go
- internal/ui/composer_mcp_menu.go
- internal/ui/onboarding.go
- internal/ui/onboarding_hero.go
- internal/ui/rolling_number.go
- internal/ui/motion_content_test.go（新）

交集：组件 E 的 composer_attach，必须等 E 合并；依赖 M2 的 ProgressBar/LoadingIndicator，但不再次修改公共组件。

步骤：

1. 附件增删/提示行采用本方案 preset，移除对应旧手动 Scale/Opacity；原上传完成、失败、取消与通知到期逻辑不变。
2. 补全高度/透明度保留原业务进度，Transition 只包已有描述；二级页横向 Keyframes 保留 Run；MCP 菜单保持 X 方向，不强制 rise。
3. onboarding 复用两个 spring、motionEnter/staggerDelayFor；alternate 呼吸继续 Keyframes 显式分支，非 alternate 才可用 motionLoop。有限庆祝不循环。普通偏好仍允许原设计的 overshoot。
4. rollingDigitCell 用 motionNumberProgress 一次，changed&&rolling 传入；数字状态机、负号、剪枝与读屏 Label 不动。本批 Spinner 统一 LoadingIndicator。

锚点：

- internal/ui/composer_attach.go:1168 `k := card.Keyframes("composer-attach-enter-"+a.LocalID, []ui.Keyframe{`
- internal/ui/composer_complete.go:1297 `}, ui.KeyframeOptions{Run: run})`
- internal/ui/onboarding_hero.go:27 `if reduceMotion(c) {`
- internal/ui/rolling_number.go:152 `if !reduceMotion(c) {`

测试：motion_content_test.go 覆盖附件上传未知/中间/完成/取消；补全关闭不越过输入框、同层输入不重播/层级切换重播；引导首次/下一步/完成及中途减少动效；数字 9→10、10→9、正负、快速更新、首次不滚、只滚不同位，Reduced 只剩终值节点。运行相关既有 rolling_number/onboarding/composer 测试。

### M7：其余加载入口收口

可修改完整集合：

- internal/ui/automation.go
- internal/ui/chat_header.go
- internal/ui/settings_page_archive.go
- internal/ui/revert_dialog.go
- internal/ui/kanban_modal.go
- internal/ui/git_pr_ui.go
- internal/ui/settings_page_nowledge.go
- internal/ui/tray_quota_panel.go
- internal/ui/timeline_diff.go
- internal/ui/memory_panel_views.go
- internal/ui/settings_archive_worktrees.go
- internal/ui/context_panel_views.go
- internal/ui/git_form_windows_ui.go
- internal/ui/settings_plan_limits.go
- internal/ui/git_stash_ui.go
- internal/ui/kanban.go
- internal/ui/file_manager.go
- internal/ui/diff_panel.go
- internal/ui/motion_loading_migration_test.go（新）

交集：组件 B 的 diff_panel；组件 D 的 git_form_windows_ui；组件 F 的 memory_panel_views/context_panel_views；组件 H 的 file_manager。必须等相应批次合并，再等 M2。其余文件无组件抽离交集。只迁 LoadingIndicator，已有 Sheet/Tab/Empty 的动画由公共文件继承；不扩展业务加载、网络或锁范围。

步骤：

1. 对清单中每个生产 Spinner 使用 LoadingIndicator（静态 reduced 路径），保留原尺寸、文本色、Label、gap、busy 判定和错误分支。若公共签名无法保留原文本色，优先沿用返回 Element 的样式继承并通过测试确认，不在各调用点自建另一个 loading helper。
2. 不改原时间刷新、搜索防抖、后台轮询或回调取消。这些计时器不能因 ReduceMotion 一起被关掉。
3. diff/文件/记忆从加载切结果只经共享 LoadingIndicator/EmptyState 提供有限反馈，不在页面重复套 rise；仍有旧数据时不闪空列表。

锚点（同一文件的其他 Spinner 一并处理）：

- internal/ui/chat_header.go:1004 `ui.Spinner(c).Size(12, 12).TextColor(ThemeAccent(c))`
- internal/ui/settings_page_archive.go:580 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/revert_dialog.go:219 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/kanban_modal.go:877 `ui.Spinner(c).Size(IconLG, IconLG).TextColor(ThemeAccent(c))`
- internal/ui/git_pr_ui.go:331 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/settings_page_nowledge.go:294 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c)).Margin(settingsRowPadY, settingsRowPadX)`
- internal/ui/tray_quota_panel.go:695 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))`
- internal/ui/timeline_diff.go:687 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/memory_panel_views.go:369 `ui.Spinner(c).Size(IconMD, IconMD).TextColor(ThemeAccent(c))`
- internal/ui/settings_archive_worktrees.go:397 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/automation.go:1376 `ui.Spinner(c).Size(IconMD, IconMD).TextColor(ThemeAccent(c))`
- internal/ui/context_panel_views.go:522 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/git_form_windows_ui.go:558 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/settings_plan_limits.go:101 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))`
- internal/ui/git_stash_ui.go:293 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/kanban.go:1490 `ui.Spinner(c).Size(IconLG, IconLG).TextColor(ThemeAccent(c))`
- internal/ui/file_manager.go:405 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`
- internal/ui/diff_panel.go:351 `ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))`

测试：motion_loading_migration_test.go 以已知 busy 状态构建典型按钮内/单独行/未知进度三个场景；Reduced 不调用 Spinner，不产生循环排帧，成功、错误、取消均退出加载。执行受影响业务域既有测试；业务自身持续轮询时仅对孤立加载子视图断言帧停止。dev 确认 12/14/18 DIP 等非标准尺寸不撑行、主题颜色不丢。

## 8. 合并与验收说明

1. M1 提供唯一基础 API。M2 统一新增组件的反馈。M3–M7 只在各自文件内迁移，不能回头给控件加“临时”字段或另写循环帮助函数。
2. 组件 C 删除 controls_surface Skeleton，组件 A 删除 controls_message；这些位置在静态盘点中保留证据，但不列入迁移所有权，不复活死组件。
3. 行号属于设计时工作副本。组件合并后按函数名、原代码单行重新定位；新组件文件没有伪造行号。
4. 主代理在合并后执行 UI 包相关测试、保留的两个视觉守卫、modal_style_guard 和项目支持的纯 Go 路径；不执行已删除的 motion_guard，不引入 cgo。实际命令/环境由主代理掌握，本次未验证能否运行。
5. dev 验收用隔离数据窗口：浮层边缘翻转、连续开合、加载完成、两次复制反馈、计数快变、浮窗拖动/resize、系统偏好中途切换。Reduced 下没有持续视觉变化，也没有由装饰动画触发的持续排帧。

未解决与注意事项：MyGo 原生 `c.Toast`、`ui.Switch` 以及 Tester 的颜色/透明度/退出快照采样能力不在给定契约内，必须由主代理核实；本文不声称这些上游路径已经无动画。各低频 Draw 的“已排队一次回调”可能要消耗一帧才完全静默，应测不再续排而非机械要求切偏好当天零帧。计划复制缺少 Copied 状态，本轮只提供公共反馈能力，不伪造成功。未引入 lobe-mygo/CrychicUI 包；不涉及 sound。没有启动后台任务。
