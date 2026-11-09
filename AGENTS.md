# AGENTS.md

后续协同工作 Agent 请遵守以下约定：

1. **默认语言**：除代码、标识符、文件路径与引用命令保持原文外，对话与文档撰写一律使用简体中文。
2. **存储与协议规范**：mcode 使用自定义的存储位置与格式，并遵循 `docs/remote-control.schema.json` 定义的 JSON-RPC 2.0 协议契约。
3. **禁止 cgo**：MyGo 运行时设计为纯 Go（无 cgo）。调用平台特定系统 API 时，仅允许通过 purego、预置动态库或调用轻量级独立 Swift CLI 小工具子进程实现，不得引入 cgo 编译依赖。
4. **Git 分支与推送约定**：严禁自行执行 `git push`、修改远程分支或在工作区外提交代码；所有变更需经由工作流指引并在本工作副本中完成。
5. **弹窗与浮层外层**：自绘卡片的弹窗必须用 `ui.DialogBase`（或 `modalDragModal`），浮层用 `ui.PopoverBase`（或 `appPopover`），面板只设尺寸上限、不设外观；禁止使用 `ui.Modal` / `ui.Popover`，否则卡片外会多一圈黑边（`internal/ui/modal_style_guard_test.go` 会拦截）。
6. **命令与后台任务闭环**：远程、网络与大范围遍历命令必须设超时或限定范围（如 `timeout 10s`、`ssh -o ConnectTimeout=5`、`find -maxdepth`）；不再需要的后台任务立即用当前 provider 的终止工具结束（agy 用 `manage_task` 的 kill，Claude 用 `KillShell` / `TaskStop`），结束回合前确认没有遗留的后台任务。
7. **适当动效**：状态变化要让用户看得见，例如展开与收起、浮层升起、选中与焦点切换、列表增删、数字变化、加载与完成反馈。动效优先复用 `internal/ui/motion_presets.go`（场景过渡、弹簧、motionLoop 等）、`internal/ui/motion.go` 与 `internal/ui/tokens.go` 里的预设和时长 token，可以参考 lobe-mygo（https://github.com/lobehub/lobe-mygo，MIT）`pkg/motion` 的弹簧与过渡写法。唯一硬性要求：所有动效（包括循环动画）都必须尊重 `ReduceMotion` 偏好，减少动效时关闭或直接到终点。
8. **新组件先查现成实现**：需要新 UI 组件时，先查看 CrychicUI（https://github.com/ELY-Open-Source/CrychicUI，MIT，纯 Go，基于同一个 `github.com/egoist/mygo/ui`，1500 个组件、38 个包）是否已有合适的组件。有合适的就直接复用对应包，不从头自研；没有合适的再自己开发。引入时注意：该库的 `sound` 包默认开启，必须统一关闭或接入 mcode 的提示音配置；弹窗与浮层不使用其 `overlay` 包，仍按第 5 条用 `ui.DialogBase` / `ui.PopoverBase`；接入后要在 dev 版本里确认减少动效（`ReduceMotion`）时会关闭。变更说明里要写明引入的包路径与版本。

Important:

1. I want this to be as perfect as Linear's website or Apple Notes: simple purpose, simple architecture, simple design, simple implementation

2. Cut taglines, helper text and secondary labels. Avoid unnecessary secondary labels and descriptions. Let the layout do the explaining. If a screen needs them to make sense, fix the layout

3. Every decision should elevate the user experience (UX), developer experience (DX) and agent experience (AX) at once, without breaking anything
