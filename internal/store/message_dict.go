package store

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
)

// 消息正文 zstd 字典：大量短的 tool / thinking 消息单独压缩时，共享历史窗口为空，压缩率很差。
// 在本地数据上训练一个字典，落库到 message_zstd_dicts，之后压缩都带上它。
//
// 设计要点：
//   - 字典 ID 由 zstd 帧头携带，解码器按 ID 自动选用，text_encoding 仍只写 "zstd"，不新增编码值。
//   - 字典只增不删：旧压缩行可能依赖旧字典，删掉就读不回来。
//   - 每次进程启动都从库里加载全部字典再重建解码器；加载失败必须让 Open 报错，否则读不回历史消息。

const (
	// messageDictMaxSamples 是训练样本上限（最近的消息优先）。
	messageDictMaxSamples = 4000
	// messageDictMinSamples / messageDictMinTotalBytes 是训练门槛，样本太少训出来的字典反而更差。
	messageDictMinSamples    = 500
	messageDictMinTotalBytes = 1 << 20
	// 单条样本的正文长度区间：太短没有统计意义，太长会主导字典内容。
	messageDictMinSampleBytes = 256
	messageDictMaxSampleBytes = 65536
	// messageDictHistoryMaxBytes 是字典历史窗口上限，对齐参考实现默认的 112KB 内容体积。
	messageDictHistoryMaxBytes = 112 << 10
	// messageDictValidationDivisor 表示随机划出 1/10 的样本做验证集。
	messageDictValidationDivisor = 10
	// messageDictMinSavingPercent 是写入字典的最低收益：验证集总字节至少省 10%。
	messageDictMinSavingPercent = 10
	// 字典 ID 取值区间 [1<<15, 1<<31)，避开小 ID 与 0（0 表示无字典）。
	messageDictMinID = 1 << 15
	messageDictMaxID = 1 << 31
)

// 包级字典注册表：编码器缓存与解码器都从这里读。
var (
	messageDictMu         sync.RWMutex
	messageDictRegistry   = map[uint32][]byte{}
	messageDictDecoder    *zstd.Decoder
	messageDictDecoderErr error

	// messageDictEncoders 缓存非 0 字典 ID 的编码器；0 走 message_content.go 里的无字典编码器。
	messageDictEncoderMu sync.Mutex
	messageDictEncoders  = map[uint32]*zstd.Encoder{}
)

// buildMessageZstdDecoderLocked 用当前注册的全部字典重建解码器；调用方必须持有 messageDictMu 写锁。
// 旧解码器不关闭：它可能正被其他 goroutine 的 DecodeAll 使用，关闭会打断解码。
func buildMessageZstdDecoderLocked() error {
	opts := []zstd.DOption{
		zstd.WithDecoderMaxMemory(uint64(messageContentMaxBytes)),
		zstd.WithDecoderConcurrency(1),
	}
	if len(messageDictRegistry) > 0 {
		ids := make([]uint32, 0, len(messageDictRegistry))
		for id := range messageDictRegistry {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		dicts := make([][]byte, 0, len(ids))
		for _, id := range ids {
			dicts = append(dicts, messageDictRegistry[id])
		}
		opts = append(opts, zstd.WithDecoderDicts(dicts...))
	}
	dec, err := zstd.NewReader(nil, opts...)
	if err != nil {
		return err
	}
	messageDictDecoder = dec
	messageDictDecoderErr = nil
	return nil
}

// messageZstdDecoder 返回带全部已注册字典的包级解码器（懒初始化）。
// zstd 帧头带字典 ID，解码器会自动选用；无字典的帧照常解压。
func messageZstdDecoder() (*zstd.Decoder, error) {
	messageDictMu.RLock()
	dec, err := messageDictDecoder, messageDictDecoderErr
	messageDictMu.RUnlock()
	if dec != nil || err != nil {
		return dec, err
	}

	messageDictMu.Lock()
	defer messageDictMu.Unlock()
	if messageDictDecoder != nil || messageDictDecoderErr != nil {
		return messageDictDecoder, messageDictDecoderErr
	}
	if buildErr := buildMessageZstdDecoderLocked(); buildErr != nil {
		messageDictDecoderErr = buildErr
		return nil, buildErr
	}
	return messageDictDecoder, nil
}

// registerMessageDicts 注册一批字典并重建解码器；失败时回滚注册表，保持旧字典可用。
func registerMessageDicts(dicts map[uint32][]byte) error {
	if len(dicts) == 0 {
		return nil
	}
	messageDictMu.Lock()
	defer messageDictMu.Unlock()

	previous := make(map[uint32][]byte, len(dicts))
	added := make([]uint32, 0, len(dicts))
	for id, dict := range dicts {
		if old, ok := messageDictRegistry[id]; ok {
			previous[id] = old
		} else {
			added = append(added, id)
		}
		messageDictRegistry[id] = dict
	}
	if err := buildMessageZstdDecoderLocked(); err != nil {
		for _, id := range added {
			delete(messageDictRegistry, id)
		}
		for id, dict := range previous {
			messageDictRegistry[id] = dict
		}
		// 尽力恢复旧解码器；即使失败也要把错误返回给调用方。
		_ = buildMessageZstdDecoderLocked()
		return fmt.Errorf("build zstd decoder with %d dicts: %w", len(dicts), err)
	}
	return nil
}

// newMessageDictEncoder 新建一个绑定字典的编码器；顶点参数与无字典编码器保持一致，便于比较收益。
func newMessageDictEncoder(dict []byte) (*zstd.Encoder, error) {
	return zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderDict(dict),
	)
}

// messageEncoderForDict 返回指定字典 ID 的包级编码器；dictID 为 0 时返回无字典编码器。
func messageEncoderForDict(dictID uint32) (*zstd.Encoder, error) {
	if dictID == 0 {
		return zstdMessageEncoder()
	}

	messageDictEncoderMu.Lock()
	defer messageDictEncoderMu.Unlock()
	if enc, ok := messageDictEncoders[dictID]; ok {
		return enc, nil
	}

	messageDictMu.RLock()
	dict, ok := messageDictRegistry[dictID]
	messageDictMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("zstd 字典 %d 未加载", dictID)
	}
	enc, err := newMessageDictEncoder(dict)
	if err != nil {
		return nil, fmt.Errorf("init zstd encoder with dict %d: %w", dictID, err)
	}
	messageDictEncoders[dictID] = enc
	return enc, nil
}

// loadMessageDicts 读出库里的全部字典并注册，把最大的 ID 记为当前写入用的字典。
// 加载失败时返回错误：缺字典的解码器读不回历史消息，必须让 Open 失败而不是静默降级。
func (s *Store) loadMessageDicts(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id, dict FROM message_zstd_dicts ORDER BY id")
	if err != nil {
		return fmt.Errorf("query message zstd dicts: %w", err)
	}
	defer rows.Close()

	dicts := make(map[uint32][]byte)
	var newest uint32
	for rows.Next() {
		var id int64
		var dict []byte
		if err := rows.Scan(&id, &dict); err != nil {
			return fmt.Errorf("scan message zstd dict: %w", err)
		}
		if len(dict) == 0 || id <= 0 {
			continue
		}
		dictID := uint32(id)
		dicts[dictID] = dict
		if dictID > newest {
			newest = dictID
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate message zstd dicts: %w", err)
	}
	if len(dicts) == 0 {
		return nil
	}
	if err := registerMessageDicts(dicts); err != nil {
		return err
	}
	atomic.StoreUint32(&s.zstdDictID, newest)
	return nil
}

// messageDictSamples 取最近的最多 4000 条 tool / thinking 消息作为训练样本。
// 已压缩的行先解出明文；只保留正文长度在 [256, 65536] 的样本。
func (s *Store) messageDictSamples(ctx context.Context) ([][]byte, int64, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT text, tool_input, text_encoding, text_z, tool_input_z FROM messages
	WHERE role = 'tool' OR kind = 'thinking'
	ORDER BY rowid DESC
	LIMIT ?
	`, messageDictMaxSamples)
	if err != nil {
		return nil, 0, fmt.Errorf("query message dict samples: %w", err)
	}
	defer rows.Close()

	samples := make([][]byte, 0, messageDictMaxSamples)
	var totalBytes int64
	for rows.Next() {
		var text, toolInput, encoding string
		var textZ, toolInputZ []byte
		if err := rows.Scan(&text, &toolInput, &encoding, &textZ, &toolInputZ); err != nil {
			return nil, 0, fmt.Errorf("scan message dict sample: %w", err)
		}
		if encoding != "" {
			decodedText, decodedInput, err := decodeMessageContent(encoding, textZ, toolInputZ)
			if err != nil {
				// 解不开的行直接跳过训练，不影响压缩任务本身。
				log.Printf("store: 字典训练样本解压失败，跳过: %v", err)
				continue
			}
			text, toolInput = decodedText, decodedInput
		}
		// 部分 tool 消息的正文在 tool_input 里，正文为空时用它补齐。
		if text == "" {
			text = toolInput
		}
		if len(text) < messageDictMinSampleBytes || len(text) > messageDictMaxSampleBytes {
			continue
		}
		samples = append(samples, []byte(text))
		totalBytes += int64(len(text))
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate message dict samples: %w", err)
	}
	return samples, totalBytes, nil
}

// buildMessageDictHistory 把训练样本拼成字典历史，截到 112KB 以内（保留较新的样本）。
func buildMessageDictHistory(samples [][]byte) []byte {
	var buf bytes.Buffer
	for _, sample := range samples {
		if buf.Len()+len(sample) > messageDictHistoryMaxBytes {
			break
		}
		buf.Write(sample)
	}
	return buf.Bytes()
}

// compareMessageDictCompression 用验证集比较带字典与不带字典的压缩总字节。
func compareMessageDictCompression(dict []byte, samples [][]byte) (plainBytes, dictBytes int, err error) {
	plainEnc, err := zstdMessageEncoder()
	if err != nil {
		return 0, 0, fmt.Errorf("init zstd encoder: %w", err)
	}
	dictEnc, err := newMessageDictEncoder(dict)
	if err != nil {
		return 0, 0, err
	}
	defer dictEnc.Close()

	for _, sample := range samples {
		plainBytes += len(plainEnc.EncodeAll(sample, nil))
		dictBytes += len(dictEnc.EncodeAll(sample, nil))
	}
	return plainBytes, dictBytes, nil
}

// newMessageDictID 在 [1<<15, 1<<31) 内挑一个尚未注册的随机 ID。
func newMessageDictID() uint32 {
	messageDictMu.RLock()
	defer messageDictMu.RUnlock()
	for {
		id := uint32(messageDictMinID + rand.Int63n(messageDictMaxID-messageDictMinID))
		if _, exists := messageDictRegistry[id]; !exists {
			return id
		}
	}
}

// ensureMessageDict 确保当前 Store 有一个可用的字典：已有就直接返回，没有就训练一个。
// 训练门槛不满足或收益不足时只记日志并保留无字典状态，不返回错误。
func (s *Store) ensureMessageDict(ctx context.Context) error {
	if atomic.LoadUint32(&s.zstdDictID) != 0 {
		return nil
	}

	samples, totalBytes, err := s.messageDictSamples(ctx)
	if err != nil {
		return err
	}
	if len(samples) < messageDictMinSamples || totalBytes < messageDictMinTotalBytes {
		log.Printf("store: 消息样本不足（%d 条 / %d 字节），跳过 zstd 字典训练", len(samples), totalBytes)
		return nil
	}

	// 随机打乱后划出 1/10 做验证集，其余参与训练。
	rand.Shuffle(len(samples), func(i, j int) { samples[i], samples[j] = samples[j], samples[i] })
	validCount := len(samples) / messageDictValidationDivisor
	if validCount < 1 {
		validCount = 1
	}
	if validCount >= len(samples) {
		validCount = len(samples) - 1
	}
	validSamples := samples[:validCount]
	trainSamples := samples[validCount:]

	dictID := newMessageDictID()
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       dictID,
		Contents: trainSamples,
		History:  buildMessageDictHistory(trainSamples),
		Offsets:  [3]int{1, 4, 8},
		Level:    zstd.SpeedBetterCompression,
	})
	if err != nil {
		return fmt.Errorf("build zstd dict: %w", err)
	}

	plainBytes, dictBytes, err := compareMessageDictCompression(dict, validSamples)
	if err != nil {
		return err
	}
	// 至少省 10% 才值得落库；否则白占空间，还让编码器多背一份历史。
	if dictBytes*(100-messageDictMinSavingPercent) > plainBytes*100 {
		log.Printf("store: zstd 字典收益不足（验证集 %d → %d 字节），放弃写入", plainBytes, dictBytes)
		return nil
	}

	if _, err := s.db.ExecContext(ctx, `
	INSERT INTO message_zstd_dicts (id, dict, sample_count, created_at)
	VALUES (?, ?, ?, ?)
	`, int64(dictID), dict, len(trainSamples), formatTime(time.Now().UTC())); err != nil {
		return fmt.Errorf("insert message zstd dict: %w", err)
	}
	if err := registerMessageDicts(map[uint32][]byte{dictID: dict}); err != nil {
		return err
	}
	// 保留任务在后台 goroutine 跑，写入路径可能并发读，统一用原子操作。
	atomic.StoreUint32(&s.zstdDictID, dictID)
	log.Printf("store: 已训练 zstd 字典 %d（%d 条训练样本，验证集 %d → %d 字节）", dictID, len(trainSamples), plainBytes, dictBytes)
	return nil
}

// resetMessageZstdDictsForTest 清空内存里的字典注册表与编码器缓存，模拟进程重启。
// 只供测试使用；库里的字典不受影响，重新 Open 后会再加载回来。
func resetMessageZstdDictsForTest() {
	messageDictMu.Lock()
	messageDictRegistry = map[uint32][]byte{}
	messageDictDecoder = nil
	messageDictDecoderErr = nil
	_ = buildMessageZstdDecoderLocked()
	messageDictMu.Unlock()

	messageDictEncoderMu.Lock()
	for _, enc := range messageDictEncoders {
		enc.Close()
	}
	messageDictEncoders = map[uint32]*zstd.Encoder{}
	messageDictEncoderMu.Unlock()
}
