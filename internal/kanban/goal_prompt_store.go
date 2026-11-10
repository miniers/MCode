package kanban

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
	"mcode/internal/store"
)

// D-KB-015：超长目标 Prompt 的内容寻址落盘。
//
// Prompt 的 UTF-8 字节数超过 4096 时，
// 按 SHA-256 写入 <数据目录>/goals/<hash>.md（内容寻址，同哈希不重写），
// 数据库 prompt 字段只保留摘要；读取时从文件回读全文并校验哈希。
// 未配置数据目录（Options.DataDir 为空）时退化为全部存库，行为与改动前一致。

const (
	// goalPromptMaterializeThreshold 是触发落盘的 UTF-8 字节阈值。
	goalPromptMaterializeThreshold = 4096
	// goalPromptMaterializedMarker 是库内摘要中的落盘标记，读取时据此判断需要回读文件。
	goalPromptMaterializedMarker = "…（全文已落盘，共"
	// goalPromptSummaryPreviewRunes 是摘要保留的原文前缀字符数。
	goalPromptSummaryPreviewRunes = 200
)

// goalPromptDir 返回目标 Prompt 的落盘目录；未配置数据目录时返回空串，表示该功能未启用。
func (s *Service) goalPromptDir() string {
	dir := strings.TrimSpace(s.opts.DataDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "goals")
}

// isMaterializedGoalPrompt 判断库内 Prompt 是否为落盘摘要。
// 摘要形如「<前缀>\n\n…（全文已落盘，共 N 字节，sha256:<hash>）」，
// 要求标记行与 sha256 片段同时出现，避免把普通 Prompt 误判为落盘摘要。
func isMaterializedGoalPrompt(prompt string) bool {
	return strings.Contains(prompt, "\n\n"+goalPromptMaterializedMarker) && strings.Contains(prompt, "sha256:")
}

// materializeGoalPrompt 在 Prompt 超过阈值且配置了数据目录时按内容哈希落盘，
// 返回库内应存的文本；未达阈值、未配置目录或写入失败时返回原文，保持全部存库。
func (s *Service) materializeGoalPrompt(prompt, contentHash string) string {
	byteCount := len([]byte(prompt))
	if byteCount <= goalPromptMaterializeThreshold {
		return prompt
	}
	dir := s.goalPromptDir()
	if dir == "" {
		return prompt
	}
	if err := writeGoalPromptFile(dir, contentHash, prompt); err != nil {
		return prompt
	}
	return goalPromptSummary(prompt, contentHash, byteCount)
}

// goalPromptSummary 生成库内摘要：保留原文前若干字符并附落盘标记、字节数与内容哈希。
func goalPromptSummary(prompt, contentHash string, byteCount int) string {
	runes := []rune(prompt)
	if len(runes) > goalPromptSummaryPreviewRunes {
		runes = runes[:goalPromptSummaryPreviewRunes]
	}
	return fmt.Sprintf("%s\n\n%s %d 字节，sha256:%s）", string(runes), goalPromptMaterializedMarker, byteCount, contentHash)
}

// writeGoalPromptFile 以内容寻址方式原子写入 <dir>/<contentHash>.md：同哈希文件已存在时直接复用，
// 否则先写临时文件再 rename 发布，避免留下半截文件。
func writeGoalPromptFile(dir, contentHash, prompt string) error {
	path := filepath.Join(dir, contentHash+".md")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, []byte(prompt), 0o600)
}

// readMaterializedGoalPrompt 从落盘文件回读全文并校验 SHA-256；内容被篡改时返回 ErrHashMismatch。
func (s *Service) readMaterializedGoalPrompt(goal store.Goal) (string, error) {
	dir := s.goalPromptDir()
	hash := strings.ToLower(strings.TrimSpace(goal.ContentHash))
	if dir == "" || hash == "" {
		return "", ErrHashMismatch
	}
	data, err := os.ReadFile(filepath.Join(dir, hash+".md"))
	if err != nil {
		return "", fmt.Errorf("kanban: read materialized goal prompt: %w", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return "", ErrHashMismatch
	}
	return string(data), nil
}

// expandGoalPrompt 在库内 Prompt 为落盘摘要时回读全文，供规划器使用；
// 回读失败时返回错误，由调用方把目标置为 plan_failed：
// 拿 200 字摘要继续规划，会把残缺需求当成完整需求落成任务。
func (s *Service) expandGoalPrompt(goal store.Goal) (store.Goal, error) {
	if !isMaterializedGoalPrompt(goal.Prompt) {
		return goal, nil
	}
	full, err := s.readMaterializedGoalPrompt(goal)
	if err != nil {
		return goal, fmt.Errorf("目标提示词全文读取失败: %w", err)
	}
	goal.Prompt = full
	return goal, nil
}
