package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// actionLogFrom 解码假设置里的动作日志，JSON 不合法时直接失败。
func actionLogFrom(t *testing.T, settings *fakeSettings) []actionRecord {
	t.Helper()
	raw := settings.get(settingActionLog)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var records []actionRecord
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		t.Fatalf("动作日志应为合法 JSON: %v，原始值为 %q", err, raw)
	}
	return records
}

// newGuardRig 返回一套动作护栏依赖：假设置与可控时钟。
func newGuardRig() (*ActionGuard, *fakeSettings, *fakeClock) {
	settings := newFakeSettings()
	clk := newFakeClock()
	return NewActionGuard(settings, clk.Now, 0), settings, clk
}

// TestActionGuardAllowBeforeCap 验证未达上限时放行。
func TestActionGuardAllowBeforeCap(t *testing.T) {
	guard, settings, _ := newGuardRig()
	ctx := context.Background()

	if err := guard.Allow(ctx, "thread", "thread-1"); err != nil {
		t.Fatalf("未记录动作时应放行，得到 %v", err)
	}
	for i := 0; i < DefaultActionCap-1; i++ {
		if err := guard.Record(ctx, "thread", "thread-1"); err != nil {
			t.Fatalf("第 %d 次记录失败: %v", i+1, err)
		}
	}
	if got := len(actionLogFrom(t, settings)); got != DefaultActionCap-1 {
		t.Fatalf("动作日志应有 %d 条，实际 %d 条", DefaultActionCap-1, got)
	}
	if err := guard.Allow(ctx, "thread", "thread-1"); err != nil {
		t.Fatalf("未达上限时应放行，得到 %v", err)
	}
}

// TestActionGuardCapReached 验证记录满 3 次后被拒，且错误可用 errors.Is 识别。
func TestActionGuardCapReached(t *testing.T) {
	guard, settings, _ := newGuardRig()
	ctx := context.Background()

	for i := 0; i < DefaultActionCap; i++ {
		if err := guard.Record(ctx, "thread", "thread-1"); err != nil {
			t.Fatalf("第 %d 次记录失败: %v", i+1, err)
		}
	}
	err := guard.Allow(ctx, "thread", "thread-1")
	if err == nil {
		t.Fatal("达到上限后应拒绝")
	}
	if !errors.Is(err, ErrActionCapReached) {
		t.Fatalf("错误应能被 errors.Is 识别为 ErrActionCapReached，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "3/3") {
		t.Fatalf("错误信息应带上已用次数，得到 %v", err)
	}

	records := actionLogFrom(t, settings)
	if len(records) != DefaultActionCap {
		t.Fatalf("动作日志应有 %d 条，实际 %d 条", DefaultActionCap, len(records))
	}
	if records[0].SubjectType != "thread" || records[0].SubjectID != "thread-1" {
		t.Fatalf("日志应记录对象类型与标识，得到 %+v", records[0])
	}
	if _, parseErr := time.Parse(time.RFC3339, records[0].At); parseErr != nil {
		t.Fatalf("日志时间应为 RFC3339，得到 %q", records[0].At)
	}
}

// TestActionGuardSubjectsIndependent 验证不同对象的计数互不影响。
func TestActionGuardSubjectsIndependent(t *testing.T) {
	guard, _, _ := newGuardRig()
	ctx := context.Background()

	for i := 0; i < DefaultActionCap; i++ {
		if err := guard.Record(ctx, "thread", "thread-1"); err != nil {
			t.Fatalf("记录失败: %v", err)
		}
	}
	if err := guard.Allow(ctx, "thread", "thread-1"); !errors.Is(err, ErrActionCapReached) {
		t.Fatalf("同一对象应被拒，得到 %v", err)
	}
	if err := guard.Allow(ctx, "thread", "thread-2"); err != nil {
		t.Fatalf("不同对象 ID 应放行，得到 %v", err)
	}
	// 同一 ID 但对象类型不同，属于另一个对象。
	if err := guard.Allow(ctx, "task", "thread-1"); err != nil {
		t.Fatalf("不同对象类型应放行，得到 %v", err)
	}
}

// TestActionGuardWindowExpiry 验证滚动 24 小时窗口：满 24 小时后恢复，24 小时内仍然拒绝。
func TestActionGuardWindowExpiry(t *testing.T) {
	guard, settings, clk := newGuardRig()
	ctx := context.Background()

	for i := 0; i < DefaultActionCap; i++ {
		if err := guard.Record(ctx, "thread", "thread-1"); err != nil {
			t.Fatalf("记录失败: %v", err)
		}
	}
	clk.Advance(actionWindow - time.Minute)
	if err := guard.Allow(ctx, "thread", "thread-1"); !errors.Is(err, ErrActionCapReached) {
		t.Fatalf("24 小时内应保持拒绝，得到 %v", err)
	}

	clk.Advance(2 * time.Minute)
	if err := guard.Allow(ctx, "thread", "thread-1"); err != nil {
		t.Fatalf("满 24 小时后应恢复，得到 %v", err)
	}
	if got := len(actionLogFrom(t, settings)); got != DefaultActionCap {
		t.Fatalf("只读判定不应改写日志，期望 %d 条，实际 %d 条", DefaultActionCap, got)
	}
	if err := guard.Record(ctx, "thread", "thread-1"); err != nil {
		t.Fatalf("恢复后记录失败: %v", err)
	}
	records := actionLogFrom(t, settings)
	if len(records) != 1 {
		t.Fatalf("过期记录应在写入时被丢弃，期望 1 条，实际 %d 条", len(records))
	}
}

// TestActionGuardPersistsAcrossInstances 验证记录落在设置里，新建的护栏同样看得到。
func TestActionGuardPersistsAcrossInstances(t *testing.T) {
	guard, settings, clk := newGuardRig()
	ctx := context.Background()

	for i := 0; i < DefaultActionCap; i++ {
		if err := guard.Record(ctx, "thread", "thread-1"); err != nil {
			t.Fatalf("记录失败: %v", err)
		}
	}

	rebuilt := NewActionGuard(settings, clk.Now, 0)
	if err := rebuilt.Allow(ctx, "thread", "thread-1"); !errors.Is(err, ErrActionCapReached) {
		t.Fatalf("新建护栏应看到已持久化的记录，得到 %v", err)
	}
}

// TestActionGuardCorruptLog 验证 JSON 损坏或时间不可解析时按空记录处理且不报错。
func TestActionGuardCorruptLog(t *testing.T) {
	guard, settings, _ := newGuardRig()
	ctx := context.Background()

	settings.set(settingActionLog, "{不是 JSON")
	if err := guard.Allow(ctx, "thread", "thread-1"); err != nil {
		t.Fatalf("损坏日志应按空记录处理，得到 %v", err)
	}
	if err := guard.Record(ctx, "thread", "thread-1"); err != nil {
		t.Fatalf("损坏日志不应阻断记录，得到 %v", err)
	}
	if got := len(actionLogFrom(t, settings)); got != 1 {
		t.Fatalf("损坏日志应被覆盖为 1 条合法记录，实际 %d 条", got)
	}

	// 时间不可解析的记录无法判断有效期，按过期丢弃。
	settings.set(settingActionLog, `[{"subjectType":"thread","subjectID":"thread-1","at":"昨天"}]`)
	if err := guard.Allow(ctx, "thread", "thread-1"); err != nil {
		t.Fatalf("时间不可解析的记录应被忽略，得到 %v", err)
	}
}

// TestActionGuardLogTruncation 验证日志超过条数上限时丢弃最旧的记录。
func TestActionGuardLogTruncation(t *testing.T) {
	guard, settings, clk := newGuardRig()
	ctx := context.Background()

	prefilled := make([]actionRecord, 0, actionLogLimit)
	at := clk.Now().UTC().Format(time.RFC3339)
	for i := 0; i < actionLogLimit; i++ {
		prefilled = append(prefilled, actionRecord{
			SubjectType: "thread",
			SubjectID:   fmt.Sprintf("thread-%03d", i),
			At:          at,
		})
	}
	data, err := json.Marshal(prefilled)
	if err != nil {
		t.Fatalf("预置日志序列化失败: %v", err)
	}
	settings.set(settingActionLog, string(data))

	if err := guard.Record(ctx, "thread", "thread-new"); err != nil {
		t.Fatalf("记录失败: %v", err)
	}

	records := actionLogFrom(t, settings)
	if len(records) != actionLogLimit {
		t.Fatalf("日志应截断到 %d 条，实际 %d 条", actionLogLimit, len(records))
	}
	if records[0].SubjectID == "thread-000" {
		t.Fatal("最旧的记录应被丢弃")
	}
	if last := records[len(records)-1]; last.SubjectID != "thread-new" {
		t.Fatalf("最新的记录应在末尾，得到 %+v", last)
	}
}

// TestActionGuardConcurrent 验证并发 Allow/Record 无数据竞争、日志不超上限。
func TestActionGuardConcurrent(t *testing.T) {
	guard, settings, _ := newGuardRig()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_ = guard.Allow(ctx, "thread", "shared-thread")
				if err := guard.Record(ctx, "thread", fmt.Sprintf("thread-%d", i)); err != nil {
					t.Errorf("并发记录失败: %v", err)
					return
				}
				_ = guard.Allow(ctx, "thread", fmt.Sprintf("thread-%d", i))
			}
		}(i)
	}
	wg.Wait()

	records := actionLogFrom(t, settings)
	if len(records) > actionLogLimit {
		t.Fatalf("日志不应超过 %d 条，实际 %d 条", actionLogLimit, len(records))
	}
	if len(records) != 8*5 {
		t.Fatalf("并发记录应全部落盘，期望 40 条，实际 %d 条", len(records))
	}
}
