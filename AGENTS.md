# AGENTS.md

协同 Agent 请遵守以下约定。

## 通用

1. **语言**：对话与文档用简体中文；代码、标识符、路径和命令保持原文。
2. **设计取向**：像 Linear 官网或 Apple Notes 一样，目的、架构、设计、实现都简单。不加标语、辅助说明和次要标签，让布局自己说明；界面要靠说明文字才看得懂，就改布局。每个决定都要同时改善 UX、DX 和 AX，且不破坏现有功能。
3. **约束须有证据**：没有证据（明确需求、真实报告、真实日志或事故、可复现的真实失败）时，不加臆想的场景、限制、防护、路径、测试或验收标准。本文件的要求、信任边界校验、防止数据丢失的错误处理、安全，以及覆盖本次改动行为的测试不在此限。
4. **Git**：不得自行 `git push` 或修改远程分支；只在当前工作副本内改动。
5. **命令与后台任务**：远程、网络与大范围遍历命令必须设超时或限定范围（如 `go test -timeout 60s`、`perl -e 'alarm 10; exec @ARGV' <命令>`、`ssh -o ConnectTimeout=5`、`find -maxdepth`；macOS 没有 `timeout`）。不再需要的后台任务立即用当前 provider 的终止工具结束（如 agy 的 `manage_task` kill、Claude 的 `KillShell` / `TaskStop`），结束回合前确认没有遗留。全量检查用 `bash scripts/check.sh`。

## Go 与协议

6. **禁止 cgo**：运行时是纯 Go。调用平台 API 只能用 purego、预置动态库或独立的 Swift CLI 子进程。
7. **存储与协议**：mcode 用自定义的存储位置与格式；远程控制遵循 `docs/remote-control.schema.json` 定义的 JSON-RPC 2.0 契约。

## 桌面 UI

8. **弹窗与浮层外层**：自绘卡片的弹窗用 `ui.DialogBase`（或 `modalDragModal`），浮层用 `ui.PopoverBase`（或 `appPopover`），面板只设尺寸上限、不设外观。禁止 `ui.Modal` / `ui.Popover`，否则卡片外多一圈黑边（`internal/ui/modal_style_guard_test.go` 会拦截）。
9. **新组件先查现成实现**：先看 CrychicUI（https://github.com/ELY-Open-Source/CrychicUI，MIT，基于同一个 `github.com/egoist/mygo/ui`）有没有合适的组件，有就复用。引入时：关闭其 `sound` 包或接入 mcode 提示音配置；不用其 `overlay` 包（按第 8 条）；确认减少动效时会关闭；变更说明写明包路径与版本。
10. **要有动效**：状态变化要让用户看得见，如展开收起、浮层升起、选中与焦点切换、列表增删、数字变化、加载与完成。过渡和弹簧复用 `internal/ui/motion_presets.go`、`internal/ui/motion.go`、`internal/ui/tokens.go` 的预设和时长 token（可参考 lobe-mygo `pkg/motion`）。所有动效都要尊重应用内的减少动效开关（`appearance.reduce_motion`，代码里用 `reduceMotion(c)`）：开启时关闭或直接到终点。
11. **动效必须会结束，静止时不重绘**：mygo 只要有一个组件级动画（`AnimateTo`、`Transition`、`Loop`、`Keyframes` 等）没结束，就每帧重建整棵组件树、重绘整个窗口（真实事故：额度小仪表空闲时 60 帧/秒重绘，CPU 长期 40% 以上）。
   - 动画目标值在显示不变时必须不变：不要直接传 `time.Now`、`usagePanelNow()`、布局测量等连续变化的值，先按像素量化（如 `math.Round(v*w)/w`）。
   - 持续性动画（运行中图标、扫光、旋转、骨架屏、脉冲等）不用 `motionLoop` / `Loop` / `Keyframes` 配 `RepeatForever`，也不在渲染函数里反复挂 `c.After`；改在 `Draw` 回调里按 `p.Now()` 算相位，用 `p.After` 或 `p.AnimationFrameRate` 请求重画，帧率够用即可（参考 `internal/ui/sidebar_bees.go` 的 `renderRunningGlyph`）。
   - 实在只能用组件级循环动画时，只在状态确实在进行时挂载，结束即卸载；不要把运行态写死为 `true`。
   - 渲染函数每帧执行，不做文件系统、子进程、SQLite、`config.Load` 等 IO，不对全部线程拷贝或排序；在弹层打开、点击时计算，或在数据变化时缓存。后台定时任务不按周期全量回载历史数据。
   - 新增或改动动画时，在 headless 测试里推进时钟到动画结束，断言不再请求下一帧。怀疑空闲重绘时用 `MCODE_PPROF` 抓 CPU profile，看 `runFrame` 是否常驻。
