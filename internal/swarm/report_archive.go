package swarm

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"mcode/internal/store"
)

// ReportSummaryLimit 是汇报消息里每个 bee 汇报总结保留的最大 rune 数，超出部分只在存档里。
const ReportSummaryLimit = 1500

// ReportIDPrefix 是汇报总结被截断时追加的 bee ID 字段前缀，主代理凭它调用 swarm_report_get。
const ReportIDPrefix = "- ID："

// ReportTruncatedPrefix 是截断提示行的固定开头，界面据此识别并去掉这一行。
const ReportTruncatedPrefix = "（汇报总结已截断："

// truncateReportSummary 把超过 ReportSummaryLimit 的汇报总结截到前 ReportSummaryLimit 个 rune，
// 并在末尾另起一行写明全文字数与取回方式；未超出时原样返回、truncated 为 false。
func truncateReportSummary(summary string) (text string, truncated bool) {
	total := utf8.RuneCountInString(summary)
	if total <= ReportSummaryLimit {
		return summary, false
	}
	runes := []rune(summary)
	head := strings.TrimRight(string(runes[:ReportSummaryLimit]), " \t\r\n")
	return fmt.Sprintf("%s\n%s全文共 %d 字，这里只保留前 %d 字；可用 swarm_report_get 工具按 bee ID 取回全文。）", head, ReportTruncatedPrefix, total, ReportSummaryLimit), true
}

// archiveReports 在拼汇报之前把每个已结束 bee 的完整汇报总结存档；存档失败只记日志，不阻断汇报。
func (m *Manager) archiveReports(ctx context.Context, leadThreadID string, bees []Bee) {
	if m.opts.ArchiveReport == nil {
		return
	}
	for _, h := range bees {
		if h.ThreadID == "" {
			continue
		}
		rec := store.SwarmReport{
			BeeID:        h.ThreadID,
			LeadThreadID: leadThreadID,
			Name:         h.Name,
			Task:         h.Task,
			Summary:      strings.TrimSpace(h.Summary),
		}
		if err := m.opts.ArchiveReport(ctx, rec); err != nil {
			log.Printf("swarm: archive report of bee %s failed: %v", h.ThreadID, err)
		}
	}
}
