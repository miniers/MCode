// Package localmem 提供不依赖 nmem 服务的本地记忆后端，记忆以文档小节存于
// 项目层 .mcode/context 和全局层 ~/.mcode/context。
// 若全局 home 本身是 Git worktree，projctx 会将其重定向到主 checkout。
package localmem
