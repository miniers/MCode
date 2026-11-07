package mcodeui

import (
	"context"
	"sync"
	"time"
)

// swarmReportFullKey 标识一份存档汇报：主会话 ID 与 bee ID 一起参与，避免不同会话串数据。
type swarmReportFullKey struct {
	lead string
	bee  string
}

// swarmReportFullCache 缓存展开卡片时从存档读到的汇报全文：键存在即已发起读取，
// 值为空表示还在读或存档读不到；每份汇报只读一次，读不到也不重试。
var (
	swarmReportFullMu    sync.Mutex
	swarmReportFullCache = make(map[swarmReportFullKey]string)
)

// swarmReportFullMaxCache 是全文缓存的最大条目数，超过后整表清空重来。
const swarmReportFullMaxCache = 256

// swarmReportDisplaySummary 返回卡片里要显示的汇报总结：没被截断时直接用消息里的文本；
// 被截断时首次调用在后台从存档读全文，读到之前或读不到时退回消息里的截断文本。
func swarmReportDisplaySummary(s *chatState, e swarmReportEntry) string {
	if !e.Truncated || e.BeeID == "" || s == nil || s.client == nil {
		return e.Summary
	}
	key := swarmReportFullKey{lead: s.swarmTargetLeadID(), bee: e.BeeID}
	swarmReportFullMu.Lock()
	text, started := swarmReportFullCache[key]
	if !started {
		if len(swarmReportFullCache) >= swarmReportFullMaxCache {
			swarmReportFullCache = make(map[swarmReportFullKey]string)
		}
		swarmReportFullCache[key] = ""
	}
	swarmReportFullMu.Unlock()
	if text != "" {
		return text
	}
	if !started {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			full, err := s.client.SwarmReportSummary(ctx, key.lead, key.bee)
			if err != nil || full == "" {
				return
			}
			swarmReportFullMu.Lock()
			swarmReportFullCache[key] = full
			swarmReportFullMu.Unlock()
			s.invalidate()
		}()
	}
	return e.Summary
}
