package store

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 真实库冒烟测试：用一致性备份出来的真实 SQLite 库副本跑一遍 bee 消息保留任务，
// 逐字节验证「主会话不动、bee 工具消息可无损读回、重复执行幂等」。
// 默认跳过；设置 MCODE_RETENTION_SMOKE_DB 指向备份文件后才执行。
//
// 只碰副本：先把备份文件整体拷进 t.TempDir()，之后所有读写都发生在副本上，
// 原路径只被读一次字节，绝不会被 SQLite 打开或修改。

const (
	// smokeRetentionDBEnv 是真实库备份文件路径的环境变量名。
	smokeRetentionDBEnv = "MCODE_RETENTION_SMOKE_DB"
	// smokeRetentionPageLimit 是逐会话翻页读取消息的每页条数。
	smokeRetentionPageLimit = 500
)

// smokeMsgDigest 是一条消息的比较指纹：正文只留 sha256，几十万条消息也不会把内存撑爆。
type smokeMsgDigest struct {
	Role      string
	Kind      string
	Text      [sha256.Size]byte
	ToolInput [sha256.Size]byte
}

// smokeThreadSnapshot 是一个会话运行前的快照。
type smokeThreadSnapshot struct {
	IsHelper bool
	Msgs     map[string]smokeMsgDigest
}

// TestBeeRetentionSmokeOnRealDB 在真实库副本上验证 bee 消息保留任务。
func TestBeeRetentionSmokeOnRealDB(t *testing.T) {
	src := os.Getenv(smokeRetentionDBEnv)
	if src == "" {
		t.Skipf("未设置 %s，跳过真实库冒烟测试", smokeRetentionDBEnv)
	}
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatalf("读取冒烟测试库 %s 失败: %v", src, err)
	}
	if fi.IsDir() {
		t.Fatalf("%s 必须是 SQLite 文件而不是目录: %s", smokeRetentionDBEnv, src)
	}

	// 只操作副本：先整文件拷进临时目录，原路径此后不再被打开。
	dst := filepath.Join(t.TempDir(), "retention-smoke.db")
	if err := copyFileForSmoke(src, dst); err != nil {
		t.Fatalf("复制数据库副本失败: %v", err)
	}

	ctx := context.Background()
	s, err := Open(dst)
	if err != nil {
		t.Fatalf("打开数据库副本失败: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("关闭数据库副本失败: %v", err)
		}
	}()

	before := readAllThreadSnapshots(t, ctx, s)
	t.Logf("运行前快照: 会话 %d 个", len(before))

	// Now 拨到 400 天后，让所有已结束的 bee 会话都越过 30 天保留期。
	cfg := BeeRetentionConfig{Now: time.Now().UTC().Add(400 * 24 * time.Hour)}
	rep, err := s.RunBeeMessageRetention(ctx, cfg)
	if err != nil {
		t.Fatalf("第一轮 bee 消息保留任务失败: %v", err)
	}
	logSmokeReport(t, "第一轮", rep)
	for reason, count := range rep.Skipped {
		t.Logf("第一轮跳过原因 %s: %d", reason, count)
	}

	after := readAllThreadSnapshots(t, ctx, s)
	deletedThinking := compareBeeRetentionSnapshots(t, before, after)
	logSmokePragma(t, ctx, s, "第一轮后")
	t.Logf("第一轮后: 被删除的思考消息 %d 条，实际处理的 bee 会话 %d 个，字节 %d -> %d",
		deletedThinking, rep.ThreadsEligible, rep.BytesBefore, rep.BytesAfter)
	if rep.ThreadsEligible == 0 {
		t.Logf("注意：本轮没有满足条件的 bee 会话，仅验证了主会话不被改动")
	}

	// 第二轮：同一参数必须幂等，不再压缩也不再删除。
	rep2, err := s.RunBeeMessageRetention(ctx, cfg)
	if err != nil {
		t.Fatalf("第二轮 bee 消息保留任务失败: %v", err)
	}
	logSmokeReport(t, "第二轮", rep2)
	if rep2.MessagesCompressed != 0 {
		t.Errorf("第二轮不应再压缩消息，实际压缩了 %d 条", rep2.MessagesCompressed)
	}
	if rep2.MessagesDeleted != 0 {
		t.Errorf("第二轮不应再删除消息，实际删除了 %d 条", rep2.MessagesDeleted)
	}

	after2 := readAllThreadSnapshots(t, ctx, s)
	compareBeeRetentionSnapshots(t, before, after2)
	logSmokePragma(t, ctx, s, "第二轮后")
}

// copyFileForSmoke 把一个文件逐字节拷到目标路径；原文件只读打开。
func copyFileForSmoke(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// readAllThreadSnapshots 逐会话读完全部消息并生成指纹；读完一个会话就丢弃原始消息，内存只留指纹。
func readAllThreadSnapshots(t *testing.T, ctx context.Context, s *Store) map[string]smokeThreadSnapshot {
	t.Helper()
	threads, badThreads, err := s.ListThreadsTolerant(ctx)
	if err != nil {
		t.Fatalf("读取会话列表失败: %v", err)
	}
	if len(badThreads) > 0 {
		t.Logf("注意：%d 个会话行解码失败被跳过: %v", len(badThreads), badThreads)
	}

	snapshots := make(map[string]smokeThreadSnapshot, len(threads))
	for _, th := range threads {
		snap := smokeThreadSnapshot{IsHelper: th.IsHelper, Msgs: make(map[string]smokeMsgDigest)}
		var beforeSeq int64
		for {
			page, _, badIDs, err := s.ListMessagesTolerant(ctx, th.ID, beforeSeq, smokeRetentionPageLimit)
			if err != nil {
				t.Fatalf("读取会话 %s 的消息失败: %v", th.ID, err)
			}
			if len(badIDs) > 0 {
				t.Logf("注意：会话 %s 有 %d 条消息解码失败被跳过: %v", th.ID, len(badIDs), badIDs)
			}
			if len(page) == 0 {
				break
			}
			for _, m := range page {
				snap.Msgs[m.ID] = smokeDigestOf(m)
			}
			// 下一页取本页最小的 seq 往前翻；即使中间有空页也会继续，直到取不到行为止。
			beforeSeq = page[0].Seq
		}
		snapshots[th.ID] = snap
	}
	return snapshots
}

// smokeDigestOf 计算一条消息的指纹；tool_input 为空指针与空串视为同一内容。
func smokeDigestOf(m Message) smokeMsgDigest {
	return smokeMsgDigest{
		Role:      m.Role,
		Kind:      m.Kind,
		Text:      sha256.Sum256([]byte(m.Text)),
		ToolInput: sha256.Sum256([]byte(smokeDeref(m.ToolInput))),
	}
}

// smokeDeref 把可空字符串摊平成比较值。
func smokeDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// compareBeeRetentionSnapshots 逐会话比对运行前后的快照，返回被删除的思考消息总数。
// 规则：主会话一条都不能少、不能变；bee 会话只允许少掉 thinking 消息，其余（尤其 tool 消息）必须逐字节一致。
func compareBeeRetentionSnapshots(t *testing.T, before, after map[string]smokeThreadSnapshot) int {
	t.Helper()
	deletedThinking := 0
	for threadID, b := range before {
		a, ok := after[threadID]
		if !ok {
			t.Errorf("会话 %s 在运行后消失了", threadID)
			continue
		}
		if a.IsHelper != b.IsHelper {
			t.Errorf("会话 %s 的 is_helper 被改动: %v -> %v", threadID, b.IsHelper, a.IsHelper)
			continue
		}
		// 运行后多出来的消息：保留任务不该新增任何消息。
		for msgID := range a.Msgs {
			if _, ok := b.Msgs[msgID]; !ok {
				t.Errorf("会话 %s 运行后多出消息 %s", threadID, msgID)
			}
		}
		for msgID, want := range b.Msgs {
			got, ok := a.Msgs[msgID]
			if !ok {
				if b.IsHelper && want.Kind == "thinking" {
					deletedThinking++
					continue
				}
				t.Errorf("会话 %s（is_helper=%v）的消息 %s（role=%s kind=%s）被删除了",
					threadID, b.IsHelper, msgID, want.Role, want.Kind)
				continue
			}
			if got != want {
				t.Errorf("会话 %s 的消息 %s 内容被改动: role %s -> %s, kind %s -> %s, text 一致=%v, tool_input 一致=%v",
					threadID, msgID, want.Role, got.Role, want.Kind, got.Kind,
					want.Text == got.Text, want.ToolInput == got.ToolInput)
			}
		}
	}
	return deletedThinking
}

// logSmokeReport 打印一轮任务的每个统计字段。
func logSmokeReport(t *testing.T, stage string, rep BeeRetentionReport) {
	t.Helper()
	t.Logf("%s报告: 扫描会话=%d 满足条件会话=%d 压缩工具消息=%d 删除思考消息=%d 字节=%d->%d 跳过=%v",
		stage, rep.ThreadsScanned, rep.ThreadsEligible, rep.MessagesCompressed,
		rep.MessagesDeleted, rep.BytesBefore, rep.BytesAfter, rep.Skipped)
}

// logSmokePragma 打印 freelist 与总页数，用来观察空间是否真的被回收。
func logSmokePragma(t *testing.T, ctx context.Context, s *Store, stage string) {
	t.Helper()
	var freelist, pageCount int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count;").Scan(&freelist); err != nil {
		t.Fatalf("%s查询 freelist_count 失败: %v", stage, err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count;").Scan(&pageCount); err != nil {
		t.Fatalf("%s查询 page_count 失败: %v", stage, err)
	}
	t.Logf("%s: freelist_count=%d page_count=%d", stage, freelist, pageCount)
}
