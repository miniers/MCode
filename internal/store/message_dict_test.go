package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// messageDictTestNow 是字典测试使用的固定时钟。
var messageDictTestNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// openMessageDictTestStore 打开一个临时库（path 为空时自动建目录），并建好一个项目。
func openMessageDictTestStore(t *testing.T, path string) *Store {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "message-dict.db")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s) 失败: %v", path, err)
	}
	if err := s.UpsertProject(context.Background(), Project{
		ID: "p1", Name: "P1", Path: "/p1", CreatedAt: messageDictTestNow, UpdatedAt: messageDictTestNow,
	}); err != nil {
		_ = s.Close()
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	return s
}

// seedMessageDictThread 建一个用来放样本消息的会话。
func seedMessageDictThread(t *testing.T, s *Store, threadID string) {
	t.Helper()
	if err := s.CreateThread(context.Background(), Thread{
		ID: threadID, ProjectID: "p1", Title: "字典测试", Provider: "openai",
		CreatedAt: messageDictTestNow, UpdatedAt: messageDictTestNow,
	}); err != nil {
		t.Fatalf("CreateThread %s 失败: %v", threadID, err)
	}
}

// appendMessageDictSample 追加一条 tool 消息，作为字典训练样本。
func appendMessageDictSample(t *testing.T, s *Store, threadID, id, text string) {
	t.Helper()
	if _, err := s.AppendMessage(context.Background(), Message{
		ID: id, ThreadID: threadID, Role: "tool", Kind: "tool", Text: text,
		TurnIndex: 1, CreatedAt: messageDictTestNow,
	}); err != nil {
		t.Fatalf("AppendMessage %s 失败: %v", id, err)
	}
}

// messageDictTestWords 是公共词表的前半部分；后半部分由 messageDictTestSample 生成的编号词补齐。
var messageDictTestWords = []string{
	"store", "message", "zstd", "dict", "compress", "thread", "sqlite", "schema",
	"migration", "tool", "thinking", "retention", "batch", "transaction", "column", "encoding",
	"decode", "encode", "sample", "history", "offset", "literal", "sequence", "table",
	"index", "query", "update", "insert", "select", "where", "return", "error",
	"context", "cancel", "commit", "rollback", "rows", "scan", "iterate", "close",
	"format", "parse", "local", "remote", "client", "server", "status", "detail",
}

// messageDictTestSample 生成结构高度相似的样本：字段名与词表跨样本大量复用，但单条内词很少重复，
// 这样单独压缩吃不到历史窗口，共享字典才能明显降低体积。
func messageDictTestSample(i int) string {
	var b strings.Builder
	b.WriteString(`{"tool":"read_file","status":"completed","thread":"t-demo","path":"internal/store/message_retention.go","seq":`)
	b.WriteString(strconv.Itoa(i))
	b.WriteString(`,"content":"`)
	for n := 0; n < 260; n++ {
		b.WriteString(messageDictTestWord((i*37 + n*61) % 400))
		b.WriteString(" ")
		if n%8 == 7 {
			b.WriteString("\n")
		}
	}
	b.WriteString(`"}`)
	return b.String()
}

// messageDictTestWord 返回词表里的第 k 个词：前 48 个是自然词，其余是确定性生成的十六进制词。
// 编号词之间刻意不留公共前缀，单条样本内部就匹配不出多少东西，字典的收益才可测。
func messageDictTestWord(k int) string {
	if k < len(messageDictTestWords) {
		return messageDictTestWords[k]
	}
	return fmt.Sprintf("%08x", uint32(k)*2654435761)
}

// countMessageDictRows 返回字典表里的行数。
func countMessageDictRows(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM message_zstd_dicts").Scan(&count); err != nil {
		t.Fatalf("统计字典行数失败: %v", err)
	}
	return count
}

// 样本足够时应训练出 1 个字典：字典编码能正确解码，且压缩总量比无字典更小。
func TestMessageDictTrainingCompressesBetter(t *testing.T) {
	ctx := context.Background()
	s := openMessageDictTestStore(t, "")
	defer s.Close()
	seedMessageDictThread(t, s, "t-dict")
	for i := 0; i < 660; i++ {
		appendMessageDictSample(t, s, "t-dict", fmt.Sprintf("seed-%03d", i), messageDictTestSample(i))
	}

	if err := s.ensureMessageDict(ctx); err != nil {
		t.Fatalf("ensureMessageDict 失败: %v", err)
	}
	if atomic.LoadUint32(&s.zstdDictID) == 0 {
		t.Fatal("样本充足时应训练出字典")
	}
	if got := countMessageDictRows(t, s); got != 1 {
		t.Fatalf("字典表应有 1 行，实际 %d 行", got)
	}

	// 幂等：已有字典时不应再训练第二个。
	if err := s.ensureMessageDict(ctx); err != nil {
		t.Fatalf("再次 ensureMessageDict 失败: %v", err)
	}
	if got := countMessageDictRows(t, s); got != 1 {
		t.Fatalf("已有字典时不应重复训练，实际 %d 行", got)
	}

	// 字典编码的往返必须逐字节一致。
	sample := messageDictTestSample(99999)
	withDict, _, err := encodeMessageContent(sample, "", atomic.LoadUint32(&s.zstdDictID))
	if err != nil {
		t.Fatalf("带字典编码失败: %v", err)
	}
	decoded, _, err := decodeMessageContent(messageEncodingZstd, withDict, nil)
	if err != nil {
		t.Fatalf("带字典解码失败: %v", err)
	}
	if decoded != sample {
		t.Fatal("带字典压缩后无法还原原文")
	}

	// 验证集之外的样本上，字典也应带来更小的压缩结果。
	var dictTotal, plainTotal int
	for i := 0; i < 200; i++ {
		out := messageDictTestSample(100000 + i)
		dictZ, _, err := encodeMessageContent(out, "", atomic.LoadUint32(&s.zstdDictID))
		if err != nil {
			t.Fatalf("带字典编码失败: %v", err)
		}
		plainZ, _, err := encodeMessageContent(out, "", 0)
		if err != nil {
			t.Fatalf("无字典编码失败: %v", err)
		}
		dictTotal += len(dictZ)
		plainTotal += len(plainZ)
	}
	if dictTotal >= plainTotal {
		t.Fatalf("字典未带来收益：带字典 %d 字节，无字典 %d 字节", dictTotal, plainTotal)
	}
}

// 样本不足时跳过训练，压缩流程仍照常可用。
func TestMessageDictSkippedWithoutEnoughSamples(t *testing.T) {
	ctx := context.Background()
	s := openMessageDictTestStore(t, "")
	defer s.Close()
	seedMessageDictThread(t, s, "t-small")
	for i := 0; i < 40; i++ {
		appendMessageDictSample(t, s, "t-small", fmt.Sprintf("small-%02d", i), messageDictTestSample(i))
	}

	if err := s.ensureMessageDict(ctx); err != nil {
		t.Fatalf("样本不足时 ensureMessageDict 不应报错: %v", err)
	}
	if atomic.LoadUint32(&s.zstdDictID) != 0 {
		t.Fatalf("样本不足时不应训练字典，实际 ID %d", atomic.LoadUint32(&s.zstdDictID))
	}
	if got := countMessageDictRows(t, s); got != 0 {
		t.Fatalf("样本不足时字典表应为空，实际 %d 行", got)
	}

	sample := messageDictTestSample(7)
	textZ, _, err := encodeMessageContent(sample, "", atomic.LoadUint32(&s.zstdDictID))
	if err != nil {
		t.Fatalf("无字典编码失败: %v", err)
	}
	decoded, _, err := decodeMessageContent(messageEncodingZstd, textZ, nil)
	if err != nil {
		t.Fatalf("无字典解码失败: %v", err)
	}
	if decoded != sample {
		t.Fatal("无字典压缩后无法还原原文")
	}
}

// 注册字典之后，早先无字典写入的 zstd 行仍必须能解码（帧头没带字典 ID）。
func TestNoDictZstdDecodesAfterDictRegistered(t *testing.T) {
	ctx := context.Background()
	s := openMessageDictTestStore(t, "")
	defer s.Close()
	seedMessageDictThread(t, s, "t-legacy")

	sample := messageDictTestSample(3)
	noDictZ, _, err := encodeMessageContent(sample, "", 0)
	if err != nil {
		t.Fatalf("无字典编码失败: %v", err)
	}

	for i := 0; i < 660; i++ {
		appendMessageDictSample(t, s, "t-legacy", fmt.Sprintf("legacy-%03d", i), messageDictTestSample(i))
	}
	if err := s.ensureMessageDict(ctx); err != nil {
		t.Fatalf("ensureMessageDict 失败: %v", err)
	}
	if atomic.LoadUint32(&s.zstdDictID) == 0 {
		t.Fatal("样本充足时应训练出字典")
	}

	decoded, _, err := decodeMessageContent(messageEncodingZstd, noDictZ, nil)
	if err != nil {
		t.Fatalf("注册字典后无字典帧解码失败: %v", err)
	}
	if decoded != sample {
		t.Fatal("注册字典后无字典帧还原不一致")
	}
}

// 字典确实来自数据库：清空内存注册表后重新 Open 同一库，带字典压缩的消息仍能读回原文。
func TestMessageDictReloadsFromDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reload.db")
	s := openMessageDictTestStore(t, path)
	defer resetMessageZstdDictsForTest()
	seedMessageDictThread(t, s, "t-reload")
	for i := 0; i < 660; i++ {
		appendMessageDictSample(t, s, "t-reload", fmt.Sprintf("reload-%03d", i), messageDictTestSample(i))
	}
	if err := s.ensureMessageDict(ctx); err != nil {
		t.Fatalf("ensureMessageDict 失败: %v", err)
	}
	trainedID := atomic.LoadUint32(&s.zstdDictID)
	if trainedID == 0 {
		t.Fatal("样本充足时应训练出字典")
	}

	// 造一条真正用字典压缩落库的消息：先明文追加，再把正文列替换成字典压缩字节。
	const text = "这条消息用数据库里加载的 zstd 字典压缩，重新打开库之后必须还能读回原文。"
	appendMessageDictSample(t, s, "t-reload", "dict-msg", text)
	textZ, _, err := encodeMessageContent(text, "", trainedID)
	if err != nil {
		t.Fatalf("带字典编码失败: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `
	UPDATE messages SET text = '', text_z = ?, text_encoding = ? WHERE id = ?`,
		textZ, messageEncodingZstd, "dict-msg"); err != nil {
		t.Fatalf("写入字典压缩行失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 模拟进程重启：清空内存注册表，此时带字典的帧应当解不开。
	resetMessageZstdDictsForTest()
	if _, _, err := decodeMessageContent(messageEncodingZstd, textZ, nil); err == nil {
		t.Fatal("清空注册表后不应能解码带字典的帧")
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	defer reopened.Close()
	if got := atomic.LoadUint32(&reopened.zstdDictID); got != trainedID {
		t.Fatalf("重新 Open 后字典 ID 不一致：期望 %d，实际 %d", trainedID, got)
	}

	messages, _, err := reopened.ListMessages(ctx, "t-reload", 0, 2000)
	if err != nil {
		t.Fatalf("重新 Open 后 ListMessages 失败: %v", err)
	}
	for _, m := range messages {
		if m.ID != "dict-msg" {
			continue
		}
		if m.Text != text {
			t.Fatalf("字典压缩消息还原不一致：%q", m.Text)
		}
		return
	}
	t.Fatal("重新 Open 后没读到 dict-msg")
}
