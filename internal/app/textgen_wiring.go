package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/textgen"
)

// DefaultThreadTitle mcode 现有的新会话默认标题。
const DefaultThreadTitle = "新会话"

// IsDefaultTitle 判断指定标题是否仍为默认标题（纯函数，供单元测试验证）。
func IsDefaultTitle(title string) bool {
	t := strings.TrimSpace(title)
	return t == "" || t == DefaultThreadTitle
}

var (
	autoTitleAttemptedMu sync.Mutex
	autoTitleAttempted   = make(map[string]bool)
)

// HasAttemptedAutoTitle 检查并记录该会话是否已尝试过自动生成标题。
// 若已尝试过返回 true，未尝试过则记录后返回 false。
func HasAttemptedAutoTitle(threadID string) bool {
	autoTitleAttemptedMu.Lock()
	defer autoTitleAttemptedMu.Unlock()
	if autoTitleAttempted[threadID] {
		return true
	}
	autoTitleAttempted[threadID] = true
	return false
}

// autoTitleAttemptedCheck 只读地判断该会话是否已尝试过自动生成标题（不登记）。
func autoTitleAttemptedCheck(threadID string) bool {
	autoTitleAttemptedMu.Lock()
	defer autoTitleAttemptedMu.Unlock()
	return autoTitleAttempted[threadID]
}

// ResetAutoTitleAttempted 供测试重置指定会话的尝试状态。
func ResetAutoTitleAttempted(threadID string) {
	autoTitleAttemptedMu.Lock()
	defer autoTitleAttemptedMu.Unlock()
	delete(autoTitleAttempted, threadID)
}

// autoTitleInput 是自动标题生成所需的输入：首条用户消息、附件名与首条助手答复。
type autoTitleInput struct {
	firstUserMsg string
	attachments  []string
	firstReply   string
}

// empty 表示既没有用户文本也没有附件，无法据此生成标题。
func (in autoTitleInput) empty() bool {
	return strings.TrimSpace(in.firstUserMsg) == "" && len(in.attachments) == 0
}

// collectAutoTitleInput 从会话历史里取首条用户消息（含附件名）与首条助手答复，
// assistantText 非空时优先作为首条答复（避免等历史回写）。
// 转发会话的上下文导言（continuation）仅在底层组装 harnessPrompt 时送入模型，不写入 ThreadMessage，
// 因此历史中的首条 user 消息即为用户自身在当前会话的提问，无需额外过滤。
func collectAutoTitleInput(hist wire.HistoryPage, assistantText string) autoTitleInput {
	in := autoTitleInput{firstReply: strings.TrimSpace(assistantText)}
	userSeen := false
	for _, m := range hist.Messages {
		if m.Role == "user" && !userSeen {
			userSeen = true
			in.firstUserMsg = m.Text
			for _, ref := range m.Media {
				in.attachments = append(in.attachments, ref.Name)
			}
		}
		if m.Role == "assistant" && in.firstReply == "" {
			in.firstReply = m.Text
		}
	}
	return in
}

// shouldAutoGenerateTitle 判断当前标题是否仍可被 AI 生成的标题覆盖（纯函数）。
// 标题为空/默认占位标题、仍沿用转发源会话标题（用户未手动改名），
// 或仍是首条用户消息的本地截取结果（用户没手动改过）时可覆盖；
// 用户手动改名或标题已由别处定下的会话不会被覆盖。
func shouldAutoGenerateTitle(title, firstUserMsg, inheritedTitle string) bool {
	if IsDefaultTitle(title) {
		return true
	}
	if trimmedInherited := strings.TrimSpace(inheritedTitle); trimmedInherited != "" && strings.TrimSpace(title) == trimmedInherited {
		return true
	}
	if strings.TrimSpace(firstUserMsg) == "" {
		return false
	}
	return strings.TrimSpace(title) == remote.ThreadTitleFromText(firstUserMsg)
}

// inheritedContinuationTitle 计算转发会话沿用的源会话标题。
// th.ForkedFromThreadID 非空、ForkedFromMessageID 为 nil 或空串时，
// 用 backend.GetThread 取源会话并返回它的 Title；其余情况或取不到时返回空串。
func inheritedContinuationTitle(ctx context.Context, backend *remote.MemoryBackend, th wire.ThreadSummary) string {
	if backend == nil || th.ForkedFromThreadID == nil || strings.TrimSpace(*th.ForkedFromThreadID) == "" {
		return ""
	}
	if th.ForkedFromMessageID != nil && strings.TrimSpace(*th.ForkedFromMessageID) != "" {
		return ""
	}
	src, err := backend.GetThread(ctx, *th.ForkedFromThreadID)
	if err != nil {
		return ""
	}
	return src.Title
}

// TriggerAutoTitle 在后台异步为尚未命名的会话生成 AI 标题。
// 判定与模型调用都在 goroutine 内完成，不阻塞回复流；每个会话最多真正尝试一次，
// 失败、超时或模型未配置时静默保持原标题。
func TriggerAutoTitle(ctx context.Context, backend *remote.MemoryBackend, dataDir, threadID, assistantText string) {
	if backend == nil || threadID == "" {
		return
	}
	if autoTitleAttemptedCheck(threadID) {
		return
	}

	go func() {
		th, err := backend.GetThread(ctx, threadID)
		if err != nil {
			return
		}
		hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
		if err != nil {
			return
		}
		in := collectAutoTitleInput(hist, assistantText)
		if in.empty() {
			// 首条消息还没落库，不登记，后续事件再试。
			return
		}
		// 抢占本会话唯一的一次自动生成机会，避免并发重复调用模型。
		if HasAttemptedAutoTitle(threadID) {
			return
		}
		inherited := inheritedContinuationTitle(ctx, backend, th)
		if !shouldAutoGenerateTitle(th.Title, in.firstUserMsg, inherited) {
			return
		}
		ctx = withThreadTextgenNotice(ctx, backend, threadID)
		cleaned, err := generateAutoTitle(ctx, dataDir, in)
		if err != nil || cleaned == "" {
			return
		}
		// 生成期间用户可能手动改过标题，写回前再确认一次，避免覆盖手动命名。
		cur, err := backend.GetThread(context.Background(), threadID)
		if err != nil {
			return
		}
		curInherited := inheritedContinuationTitle(context.Background(), backend, cur)
		if !shouldAutoGenerateTitle(cur.Title, in.firstUserMsg, curInherited) {
			return
		}
		_, _ = backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
			ThreadID: threadID,
			Title:    &cleaned,
		})
	}()
}

// autoTitleGenerateText 是自动标题专用的文本生成入口，默认直接用 generateText，测试可替换以免拉起真实模型。
var autoTitleGenerateText = generateText

// generateAutoTitle 用一次性文本生成模型产出会话标题。
// 模型选择与 Git 提交信息生成完全共用（见 textgenModelSelection）；
// 无可用输入或清洗后为空/默认标题时返回空串。
func generateAutoTitle(ctx context.Context, dataDir string, in autoTitleInput) (string, error) {
	if in.empty() {
		return "", nil
	}

	cfg, err := config.Load(dataDir)
	if err != nil {
		cfg = config.Defaults()
	}
	config.ApplyEnv(&cfg, os.LookupEnv)

	// 与 Git 提交信息生成共用同一个模型配置，
	// 模型未配置或调用失败时由调用方静默回退到原标题。
	provider, model, effort := textgenModelSelection(cfg)

	prompt := textgen.TitlePromptWithAttachments(in.firstUserMsg, in.firstReply, in.attachments)
	genCtx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()

	genCtx = withTextgenGlobalSelection(genCtx, cfg)
	out, err := autoTitleGenerateText(genCtx, cfg, config.NewSecretStore("mcode"), provider, model, effort, prompt)
	if err != nil {
		return "", err
	}

	cleaned := textgen.CleanTitle(out)
	if IsDefaultTitle(cleaned) {
		return "", nil
	}
	return cleaned, nil
}

// RegenerateTitle 同步重新生成会话标题并写回，无视“已尝试过”标记；返回新标题。
func RegenerateTitle(ctx context.Context, backend *remote.MemoryBackend, dataDir, threadID string) (string, error) {
	if backend == nil || threadID == "" {
		return "", errors.New("title regeneration: backend or threadID missing")
	}
	if _, err := backend.GetThread(ctx, threadID); err != nil {
		return "", err
	}
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		return "", err
	}
	ctx = withThreadTextgenNotice(ctx, backend, threadID)
	title, err := generateAutoTitle(ctx, dataDir, collectAutoTitleInput(hist, ""))
	if err != nil {
		return "", err
	}
	if title == "" {
		return "", errors.New("title regeneration: no usable title generated")
	}
	if _, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: threadID, Title: &title}); err != nil {
		return "", err
	}
	return title, nil
}

func withThreadTextgenNotice(ctx context.Context, backend *remote.MemoryBackend, threadID string) context.Context {
	return withTextgenFallbackNotice(ctx, func(_, to string) {
		_ = backend.AppendNotice(threadID, fmt.Sprintf("已切换到 %s", to))
	})
}

// HandleTurnFinished 供轮次结束钩子（如 TurnFinishedHook）调用。
func HandleTurnFinished(backend *remote.MemoryBackend, dataDir, threadID, projectID, assistantText string, completed bool) {
	if !completed {
		return
	}
	TriggerAutoTitle(context.Background(), backend, dataDir, threadID, assistantText)
}

// wireAutoTitle 将自动标题生成挂接到后端中：每轮结束后异步尝试为尚未命名的会话生成 AI 标题。
func wireAutoTitle(backend *remote.MemoryBackend, dataDir string) {
	if backend == nil {
		return
	}

	// 通过 StreamHub 观察者监听状态，既无侵入地感知轮次结束，又避免覆盖已有钩子。
	if hub := backend.Hub(); hub != nil {
		hub.AddObserver(func(threadID, method string, params any) {
			if method == "thread.summary" {
				if sum, ok := params.(wire.ThreadSummary); ok {
					// 标题仍为默认占位或仍是首条消息的本地截取值时，才由 AI 标题替换（详见 shouldAutoGenerateTitle）。
					if !sum.IsRunning {
						TriggerAutoTitle(context.Background(), backend, dataDir, threadID, "")
					}
				}
			}
		})
	}
}
