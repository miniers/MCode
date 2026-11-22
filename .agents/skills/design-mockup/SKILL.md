---
name: design-mockup
description: 为 mcode 做 UI 设计时使用：产出可交互、高还原的 HTML 设计稿，复用 web/src/tokens.css 的真实 token，不画 ASCII 线稿；架构图用 mermaid 代码块。
---

# 高还原设计稿

1. 不画 ASCII 线稿。UI 方案写成 HTML 文件，放在 `docs/design/<主题>.html`，一个方案一个文件；同一方案的多个状态或视口并排放在同一个文件里。
2. 样式只用真实 token：`<link rel="stylesheet" href="../../web/src/tokens.css">`，颜色、字号、间距、圆角、阴影都取 `--mc-*` 变量；深色在 `<html>` 上加 `class="dark"`。token 源头是 `internal/ui/tokens.go` 与 `internal/ui/theme.go`，不自造数值。
3. 交互用原生 JS 写在同文件的 `<script>` 里（展开收起、选中、悬停、状态切换）。不引用外链 CDN、远程字体，不发网络请求，预览的 CSP 会拦截；图标用内联 SVG，可从 `internal/ui/icons_assets/` 复制。
4. 遵守项目 UI 规则：不加标语、辅助说明和次要标签；弹窗与浮层见 AGENTS.md 第 5 条；动效时长参照 `internal/ui/tokens.go`。
5. 写完在回复里给出文件路径。用户在文件面板打开后点「在浏览器打开」预览，用标注圈选元素回传修改意见。
6. 架构图、流程图、状态机用 ```mermaid 代码块，不画 ASCII 框图。
