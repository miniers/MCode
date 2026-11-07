package mcodeui

import "time"

// JumpToLatestThreshold 是显示「跳到最新」按钮的离底阈值。
const JumpToLatestThreshold float32 = 120

// 距离底部不超过该值时，新内容到达后跟随到底。
const followStickDistance float32 = 40

// 用户滚动后暂缓自动跟随，避免新内容抢走滚动位置。
const followCooldown = 150 * time.Millisecond

// shouldShowJumpToLatest 判定是否显示「跳到最新」按钮。
// 距离不可得（小于 0）时保留按 atEnd 判断的退路。
func shouldShowJumpToLatest(atEnd bool, distanceFromBottom float32) bool {
	if atEnd {
		return false
	}
	if distanceFromBottom < 0 {
		return true
	}
	return distanceFromBottom > JumpToLatestThreshold
}

// shouldAutoFollow 判定是否应在新内容到达后跟随到底。
func shouldAutoFollow(distanceFromBottom float32, sinceUserScroll time.Duration) bool {
	return distanceFromBottom >= 0 && distanceFromBottom <= followStickDistance && sinceUserScroll >= followCooldown
}
