package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// 动作护栏使用的持久化设置键与常量。
const (
	// settingActionLog 保存动作日志（JSON 数组），用于统计同一对象在滚动 24 小时内的动作次数。
	settingActionLog = "steward.action_log"
	// DefaultActionCap 是同一对象在滚动 24 小时内允许的最大动作次数。
	DefaultActionCap = 3
	// actionWindow 是动作计数的滚动窗口。
	actionWindow = 24 * time.Hour
	// actionLogLimit 是动作日志的总条数上限，超出时丢弃最旧的记录。
	actionLogLimit = 500
)

// ErrActionCapReached 表示同一对象在 24 小时内已达到动作次数上限，应改为通知用户。
var ErrActionCapReached = errors.New("steward: 同一对象 24 小时内已处理 3 次，请改为通知用户")

// actionRecord 是动作日志中的一条记录。
type actionRecord struct {
	// SubjectType 是对象类型，例如 thread、task、automation。
	SubjectType string `json:"subjectType"`
	// SubjectID 是对象标识。
	SubjectID string `json:"subjectID"`
	// At 是动作发生时刻（UTC，RFC3339）。
	At string `json:"at"`
}

// ActionGuard 是总管动手能力的护栏：按对象统计滚动 24 小时内的动作次数，
// 达到上限后拒绝继续动手，要求改为通知用户。并发安全。
type ActionGuard struct {
	// mu 串行化「读取—判定—写入」，避免并发动作共享同一份计数。
	mu sync.Mutex
	// settings 持久化动作日志；为空时护栏退化为不记录，始终放行。
	settings Settings
	// now 是当前时间来源，为空时用 time.Now。
	now func() time.Time
	// cap 是同一对象在窗口内允许的动作次数上限。
	cap int
}

// NewActionGuard 创建动作护栏；now 为空时用 time.Now，cap<=0 时用 DefaultActionCap。
func NewActionGuard(settings Settings, now func() time.Time, cap int) *ActionGuard {
	if now == nil {
		now = time.Now
	}
	if cap <= 0 {
		cap = DefaultActionCap
	}
	return &ActionGuard{settings: settings, now: now, cap: cap}
}

// Allow 判断现在是否还可以对该对象动手：窗口内已记录的动作数达到上限时返回
// 包装的 ErrActionCapReached（可用 errors.Is 识别），否则返回 nil。
func (g *ActionGuard) Allow(ctx context.Context, subjectType, subjectID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	records := g.recent(ctx, g.now())
	if used := countActions(records, subjectType, subjectID); used >= g.cap {
		return fmt.Errorf("%w（%s/%s 已用 %d/%d 次）",
			ErrActionCapReached, subjectType, subjectID, used, g.cap)
	}
	return nil
}

// Record 记一次动作：追加一条当前时刻的记录，写回前丢弃窗口以前的记录并裁到条数上限。
func (g *ActionGuard) Record(ctx context.Context, subjectType, subjectID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	records := g.recent(ctx, now)
	records = append(records, actionRecord{
		SubjectType: subjectType,
		SubjectID:   subjectID,
		At:          now.UTC().Format(time.RFC3339),
	})
	if len(records) > actionLogLimit {
		// 记录按时间追加，丢掉最旧的一段即可保留最近的 actionLogLimit 条。
		records = records[len(records)-actionLogLimit:]
	}
	return g.save(ctx, records)
}

// recent 读取动作日志并只保留窗口内（严格新于 now-24h）的记录；读取失败、JSON 损坏或
// 时间无法解析时按提示降级处理，不返回错误，避免护栏本身阻断总管工作。
func (g *ActionGuard) recent(ctx context.Context, now time.Time) []actionRecord {
	if g.settings == nil {
		return nil
	}
	raw, ok, err := g.settings.GetSetting(ctx, settingActionLog)
	if err != nil {
		log.Printf("steward: 读取动作日志失败，按空记录处理: %v", err)
		return nil
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var records []actionRecord
	if unmarshalErr := json.Unmarshal([]byte(raw), &records); unmarshalErr != nil {
		log.Printf("steward: 动作日志 JSON 损坏，按空记录处理: %v", unmarshalErr)
		return nil
	}

	cutoff := now.Add(-actionWindow)
	kept := make([]actionRecord, 0, len(records))
	for _, rec := range records {
		at, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(rec.At))
		if parseErr != nil || !at.After(cutoff) {
			// 过期或时间不可解析的记录直接丢弃：前者不该占用名额，后者无法判断有效期。
			continue
		}
		kept = append(kept, rec)
	}
	return kept
}

// save 把动作日志写回设置。
func (g *ActionGuard) save(ctx context.Context, records []actionRecord) error {
	if g.settings == nil {
		return nil
	}
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("steward: 序列化动作日志失败: %w", err)
	}
	if err := g.settings.SetSetting(ctx, settingActionLog, string(data)); err != nil {
		return fmt.Errorf("steward: 记录动作日志失败: %w", err)
	}
	return nil
}

// countActions 统计 records 中属于该对象的记录条数。
func countActions(records []actionRecord, subjectType, subjectID string) int {
	used := 0
	for _, rec := range records {
		if rec.SubjectType == subjectType && rec.SubjectID == subjectID {
			used++
		}
	}
	return used
}

// CanAct 报告总管现在是否被允许动手：只看总管是否启用，与普通会话一样不再受专属权限档限制。
// 每次调用都现读配置，用户关掉总管后无需重建运行时。
func (r *Runtime) CanAct() bool {
	return r.config().Enabled
}
