---
name: design-mockup
description: 为 mcode 做 UI 设计时使用：产出可交互、高还原的 HTML 设计稿，复用 web/src/tokens.css 的真实 token，不画 ASCII 线稿；架构图用 mermaid 代码块。
---

# 高还原设计稿

1. 出稿前和每次改稿前，先列出需要用户拍板的细节（布局取舍、信息层级、交互方式、风格方向等），用提问工具给 2 到 4 个选项让用户选，推荐项放第一个；用户没有思路时，先给几个设计方向让用户挑。能从需求或项目里定下来的不问。
2. 不画 ASCII 线稿。UI 方案写成 HTML 文件，放在 `.mcode/design/<主题>.html`（该目录不进 git），一个方案一个文件；同一方案的多个状态或视口并排放在同一个文件里。
3. 样式只用真实 token：`<link rel="stylesheet" href="../../web/src/tokens.css">`，颜色、字号、间距、圆角、阴影都取 `--mc-*` 变量；深色在 `<html>` 上加 `class="dark"`。token 源头是 `internal/ui/tokens.go` 与 `internal/ui/theme.go`，不自造数值。
4. 交互用原生 JS 写在同文件的 `<script>` 里（展开收起、选中、悬停、状态切换）。不引用外链 CDN、远程字体，不发网络请求，预览的 CSP 会拦截；图标用内联 SVG，可从 `internal/ui/icons_assets/` 复制。
5. 遵守项目 UI 规则：不加标语、辅助说明和次要标签；弹窗与浮层见 AGENTS.md 第 5 条；动效时长参照 `internal/ui/tokens.go`。
6. 写完或改完调用 mcode 的 `preview_open` 工具打开设计稿（改稿后再次调用 `preview_open` 打开同一文件，预览会刷新）；工具不可用时在回复里用反引号给出文件路径，用户点击即可预览。用户可用标注圈选元素回传修改意见。
7. 架构图、流程图、状态机用 ```mermaid 代码块，不画 ASCII 框图。
