package steward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 本文件实现总管主会话的账本：主会话会定期换新，换新后只剩账本、话题索引与最近几条对话，
// 因此要跨会话记住的在办事项、已定决策与待用户确认的问题都记在这里。账本整体以 JSON 数组
// 存在应用级设置里，读写方式与话题表一致：一次读改写就是一次整表替换。

// settingLedger 是账本的持久化设置键。
const settingLedger = "steward.ledger"

// 账本分区：取值同时是工具参数 section 的合法值，不要随意改名。
const (
	// LedgerInProgress 进行中：还在办的事。
	LedgerInProgress = "in_progress"
	// LedgerDecision 已定决策：已经拍板、后面要照着做的事。
	LedgerDecision = "decision"
	// LedgerPendingUser 等用户确认：需要用户回答或拍板的问题。
	LedgerPendingUser = "pending_user"
)

// 账本的长度上限。
const (
	// LedgerTextMaxRunes 是单条账本正文的上限：一条就是一行，写不下说明该拆开或精简。
	LedgerTextMaxRunes = 300
	// ledgerRenderMaxRunes 是渲染给模型看的账本文本总长上限，超出时从最旧的条目截起。
	ledgerRenderMaxRunes = 6000
)

// ledgerSectionOrder 是渲染顺序；ledgerSectionTitles 与 ledgerSectionLimits 是各分区的小标题与条数上限。
var (
	ledgerSectionOrder  = []string{LedgerInProgress, LedgerDecision, LedgerPendingUser}
	ledgerSectionTitles = map[string]string{
		LedgerInProgress:  "进行中",
		LedgerDecision:    "已定决策",
		LedgerPendingUser: "等用户确认",
	}
	// decision 满了淘汰最旧的一条（旧决策多半已经落实）；另外两个分区满了拒绝新增，
	// 逼总管先清掉办完或作废的条目，避免在办事项悄悄丢失。
	ledgerSectionLimits = map[string]int{
		LedgerInProgress:  30,
		LedgerDecision:    50,
		LedgerPendingUser: 20,
	}
)

// LedgerSubject 描述账本条目关联的待处理事件对象，例如看板任务、会话或自动化运行。
// 记 pending_user 时带上它，对象对应的待处理事件被消解后，这一条会自动关闭。
type LedgerSubject struct {
	// Type 是对象类型，取值与 attention.SubjectType 一致：thread、kanban_task、automation_run 或 bee。
	Type string
	// ID 是对象标识，例如看板任务 ID、会话 ID 或自动化运行 ID。
	ID string
}

// LedgerEntry 是账本里的一条记录。
type LedgerEntry struct {
	// ID 是条目标识，改与删都按它定位。
	ID string `json:"id"`
	// Section 是所属分区：in_progress、decision 或 pending_user。
	Section string `json:"section"`
	// Text 是正文，一行，不超过 LedgerTextMaxRunes 个字符。
	Text string `json:"text"`
	// TopicThreadID 是关联的话题会话 ID；为空表示不属于某个话题。
	TopicThreadID string `json:"topicThreadId,omitempty"`
	// SubjectType 与 SubjectID 关联待处理事件的对象（看板任务、会话、自动化运行等）；
	// 为空表示这条记录不跟某个具体对象绑定。旧数据没有这两个字段，读出来为空值。
	SubjectType string `json:"subjectType,omitempty"`
	SubjectID   string `json:"subjectId,omitempty"`
	// CreatedAt 是新增时刻；UpdatedAt 是最近一次改动的时刻。
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Ledger 返回账本全部条目，按分区顺序排列，同一分区内按新增先后排列。
func (r *Runtime) Ledger(ctx context.Context) ([]LedgerEntry, error) {
	entries, err := r.loadLedger(ctx)
	if err != nil {
		return nil, err
	}
	sortLedger(entries)
	return entries, nil
}

// AddLedgerEntry 新增一条账本记录并返回它。decision 已满时淘汰最旧的一条；
// in_progress 与 pending_user 已满时拒绝新增。可选的 subject 用来关联待处理事件的对象：
// pending_user 等的是某个具体看板任务、会话或自动化运行时传它，对象处理后条目会自动关闭。
func (r *Runtime) AddLedgerEntry(ctx context.Context, section, text, topicThreadID string, subject ...LedgerSubject) (LedgerEntry, error) {
	section = strings.TrimSpace(section)
	text, err := normalizeLedgerText(text)
	if err != nil {
		return LedgerEntry{}, err
	}
	if _, ok := ledgerSectionLimits[section]; !ok {
		return LedgerEntry{}, ledgerSectionError(section)
	}
	r.ledgerMu.Lock()
	defer r.ledgerMu.Unlock()
	entries, err := r.loadLedger(ctx)
	if err != nil {
		return LedgerEntry{}, err
	}
	var subjectType, subjectID string
	if len(subject) > 0 {
		subjectType = strings.TrimSpace(subject[0].Type)
		subjectID = strings.TrimSpace(subject[0].ID)
		if (subjectType == "") != (subjectID == "") {
			return LedgerEntry{}, errors.New("steward: subject 的类型与 ID 要么都填，要么都不填")
		}
	}
	now := r.now()
	entry := LedgerEntry{
		ID:            newLedgerID(),
		Section:       section,
		Text:          text,
		TopicThreadID: strings.TrimSpace(topicThreadID),
		SubjectType:   subjectType,
		SubjectID:     subjectID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	entries, err = placeLedgerEntry(entries, entry)
	if err != nil {
		return LedgerEntry{}, err
	}
	if err := r.saveLedger(ctx, entries); err != nil {
		return LedgerEntry{}, err
	}
	return entry, nil
}

// UpdateLedgerEntry 按 id 改写一条记录：section 或 text 为空表示保持原值。
// 改分区时按目标分区的上限处理（decision 满了淘汰最旧，其余满了拒绝）。
func (r *Runtime) UpdateLedgerEntry(ctx context.Context, id, section, text string) (LedgerEntry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return LedgerEntry{}, errors.New("steward: 缺少账本条目 ID")
	}
	section = strings.TrimSpace(section)
	if section != "" {
		if _, ok := ledgerSectionLimits[section]; !ok {
			return LedgerEntry{}, ledgerSectionError(section)
		}
	}
	if strings.TrimSpace(text) != "" {
		normalized, err := normalizeLedgerText(text)
		if err != nil {
			return LedgerEntry{}, err
		}
		text = normalized
	} else {
		text = ""
	}
	if section == "" && text == "" {
		return LedgerEntry{}, errors.New("steward: 改账本条目至少要给出新的 section 或 text")
	}
	r.ledgerMu.Lock()
	defer r.ledgerMu.Unlock()
	entries, err := r.loadLedger(ctx)
	if err != nil {
		return LedgerEntry{}, err
	}
	idx := ledgerIndex(entries, id)
	if idx < 0 {
		return LedgerEntry{}, fmt.Errorf("steward: 账本里没有条目 %s", id)
	}
	entry := entries[idx]
	if text != "" {
		entry.Text = text
	}
	entry.UpdatedAt = r.now()
	if section != "" && section != entry.Section {
		// 换分区：先摘下再按目标分区的上限放回去。
		entry.Section = section
		rest := append(append([]LedgerEntry(nil), entries[:idx]...), entries[idx+1:]...)
		if entries, err = placeLedgerEntry(rest, entry); err != nil {
			return LedgerEntry{}, err
		}
	} else {
		entries[idx] = entry
	}
	if err := r.saveLedger(ctx, entries); err != nil {
		return LedgerEntry{}, err
	}
	return entry, nil
}

// RemoveLedgerEntry 按 id 删除一条记录。
func (r *Runtime) RemoveLedgerEntry(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("steward: 缺少账本条目 ID")
	}
	r.ledgerMu.Lock()
	defer r.ledgerMu.Unlock()
	entries, err := r.loadLedger(ctx)
	if err != nil {
		return err
	}
	idx := ledgerIndex(entries, id)
	if idx < 0 {
		return fmt.Errorf("steward: 账本里没有条目 %s", id)
	}
	entries = append(entries[:idx], entries[idx+1:]...)
	return r.saveLedger(ctx, entries)
}

// closePendingUserForSubject 关闭账本里关联某个待处理事件对象的 pending_user 条目：
// 对象已经被处理（对应事件被消解），这些条目不必再等用户。返回被关闭条目的正文。
// subjectType 与 subjectID 都为空或没有匹配条目时什么也不做。
func (r *Runtime) closePendingUserForSubject(ctx context.Context, subjectType, subjectID string) ([]string, error) {
	subjectType = strings.TrimSpace(subjectType)
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return nil, nil
	}
	r.ledgerMu.Lock()
	defer r.ledgerMu.Unlock()
	entries, err := r.loadLedger(ctx)
	if err != nil {
		return nil, err
	}
	kept := entries[:0]
	var closed []string
	for _, entry := range entries {
		if entry.Section == LedgerPendingUser && entry.SubjectType == subjectType && entry.SubjectID == subjectID {
			closed = append(closed, entry.Text)
			continue
		}
		kept = append(kept, entry)
	}
	if len(closed) == 0 {
		return nil, nil
	}
	if err := r.saveLedger(ctx, kept); err != nil {
		return nil, err
	}
	return closed, nil
}

// RenderLedger 把账本渲染成给模型看的中文文本：三个小标题，每条一行，总长不超过
// ledgerRenderMaxRunes 个字符；超出时按 UpdatedAt 从旧到新截掉，并注明省略了几条。
func (r *Runtime) RenderLedger(ctx context.Context) (string, error) {
	entries, err := r.Ledger(ctx)
	if err != nil {
		return "", err
	}
	return renderLedger(entries, ledgerRenderMaxRunes), nil
}

// receiptLedger 在一次读改写里完成话题回执的账本部分：删掉该话题的 in_progress 条目与旧的回执决策，
// 再写入一条新的 decision。旧决策一并替换，重复回执也不会在账本里留下两条。
func (r *Runtime) receiptLedger(ctx context.Context, topicThreadID, text string) error {
	text, err := normalizeLedgerText(text)
	if err != nil {
		return err
	}
	r.ledgerMu.Lock()
	defer r.ledgerMu.Unlock()
	entries, err := r.loadLedger(ctx)
	if err != nil {
		return err
	}
	kept := entries[:0]
	for _, entry := range entries {
		if entry.TopicThreadID == topicThreadID && (entry.Section == LedgerInProgress || entry.Section == LedgerDecision) {
			continue
		}
		kept = append(kept, entry)
	}
	now := r.now()
	kept, err = placeLedgerEntry(kept, LedgerEntry{
		ID:            newLedgerID(),
		Section:       LedgerDecision,
		Text:          text,
		TopicThreadID: topicThreadID,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		return err
	}
	return r.saveLedger(ctx, kept)
}

// placeLedgerEntry 按分区上限把 entry 放进 entries：decision 满了淘汰 CreatedAt 最早的一条，
// 其余分区满了返回中文错误。
func placeLedgerEntry(entries []LedgerEntry, entry LedgerEntry) ([]LedgerEntry, error) {
	limit := ledgerSectionLimits[entry.Section]
	count, oldest := 0, -1
	for i, item := range entries {
		if item.Section != entry.Section {
			continue
		}
		count++
		if oldest < 0 || item.CreatedAt.Before(entries[oldest].CreatedAt) {
			oldest = i
		}
	}
	if count >= limit {
		if entry.Section != LedgerDecision {
			return nil, fmt.Errorf("steward: 账本「%s」已满 %d 条，先删掉办完或作废的条目再新增",
				ledgerSectionTitles[entry.Section], limit)
		}
		entries = append(entries[:oldest], entries[oldest+1:]...)
	}
	return append(entries, entry), nil
}

// renderLedger 是 RenderLedger 的纯函数部分，budget 是总长上限（按 rune 计）。
func renderLedger(entries []LedgerEntry, budget int) string {
	// 截断顺序：UpdatedAt 最旧的先截；每截一条重渲染一次，条数有上限，代价可以忽略。
	byAge := append([]LedgerEntry(nil), entries...)
	sort.SliceStable(byAge, func(i, j int) bool { return byAge[i].UpdatedAt.Before(byAge[j].UpdatedAt) })
	for dropped := 0; dropped <= len(byAge); dropped++ {
		dropIDs := make(map[string]bool, dropped)
		for _, entry := range byAge[:dropped] {
			dropIDs[entry.ID] = true
		}
		text := renderLedgerSections(entries, dropIDs)
		if utf8.RuneCountInString(text) <= budget {
			return text
		}
	}
	// 连空账本都放不下只可能是 budget 过小，退回空账本的文本。
	return renderLedgerSections(nil, nil)
}

// ledgerEntryTimeSuffix 生成条目的记录时间后缀：新增时间必写，改动过再补更新时间的说明；
// 时间为零值（老数据）时省略，避免出现 0001-01-01 这类无意义的日期。
func ledgerEntryTimeSuffix(entry LedgerEntry) string {
	if entry.CreatedAt.IsZero() {
		return ""
	}
	created := entry.CreatedAt.Local().Format("2006-01-02 15:04")
	if !entry.UpdatedAt.IsZero() && !entry.UpdatedAt.Equal(entry.CreatedAt) {
		return fmt.Sprintf("（记于 %s，更新于 %s）", created, entry.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf("（记于 %s）", created)
}

// renderLedgerSections 渲染三个小标题与各自的条目；dropIDs 中的条目不渲染，只计入省略条数。
func renderLedgerSections(entries []LedgerEntry, dropIDs map[string]bool) string {
	var b strings.Builder
	omitted := 0
	for i, section := range ledgerSectionOrder {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "### %s\n", ledgerSectionTitles[section])
		shown := 0
		for _, entry := range entries {
			if entry.Section != section {
				continue
			}
			if dropIDs[entry.ID] {
				omitted++
				continue
			}
			fmt.Fprintf(&b, "- [%s] %s%s", entry.ID, entry.Text, ledgerEntryTimeSuffix(entry))
			if entry.TopicThreadID != "" {
				fmt.Fprintf(&b, "（话题 %s）", entry.TopicThreadID)
			}
			b.WriteString("\n")
			shown++
		}
		if shown == 0 {
			b.WriteString("（无）\n")
		}
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n（篇幅所限，省略了 %d 条较旧的条目，用 steward_ledger_get 查看全部。）\n", omitted)
	}
	return strings.TrimRight(b.String(), "\n")
}

// sortLedger 按分区顺序、再按新增先后排序，渲染与工具输出都用这一个顺序。
func sortLedger(entries []LedgerEntry) {
	rank := make(map[string]int, len(ledgerSectionOrder))
	for i, section := range ledgerSectionOrder {
		rank[section] = i
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if rank[entries[i].Section] != rank[entries[j].Section] {
			return rank[entries[i].Section] < rank[entries[j].Section]
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
}

// normalizeLedgerText 把正文压成一行并校验长度：账本每条一行，多行内容渲染时会挤乱小标题。
func normalizeLedgerText(text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", errors.New("steward: 账本条目正文不能为空")
	}
	if n := utf8.RuneCountInString(text); n > LedgerTextMaxRunes {
		return "", fmt.Errorf("steward: 账本条目正文 %d 个字符，最多 %d 个，请精简后重试", n, LedgerTextMaxRunes)
	}
	return text, nil
}

// ledgerSectionError 说明 section 的合法取值。
func ledgerSectionError(section string) error {
	return fmt.Errorf("steward: 账本分区 %q 不合法，只能是 in_progress（进行中）、decision（已定决策）或 pending_user（等用户确认）", section)
}

// ledgerIndex 返回 id 所在下标，找不到时为 -1。
func ledgerIndex(entries []LedgerEntry, id string) int {
	for i, entry := range entries {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

// newLedgerID 生成一个短的随机条目 ID：模型要在工具参数里抄写它，越短越不容易抄错。
func newLedgerID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("l%x", time.Now().UnixNano())
	}
	return "l" + hex.EncodeToString(buf[:])
}

// loadLedger 从设置里读整本账；键不存在或为空时返回空表。
func (r *Runtime) loadLedger(ctx context.Context) ([]LedgerEntry, error) {
	if r.settings == nil {
		return nil, errors.New("steward: 未提供设置存储，无法读取账本")
	}
	raw, ok, err := r.settings.GetSetting(ctx, settingLedger)
	if err != nil {
		return nil, fmt.Errorf("steward: 读取账本失败: %w", err)
	}
	entries := make([]LedgerEntry, 0)
	if ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return nil, fmt.Errorf("steward: 解析账本失败: %w", err)
		}
	}
	if entries == nil {
		entries = make([]LedgerEntry, 0)
	}
	return entries, nil
}

// saveLedger 把整本账写回设置。
func (r *Runtime) saveLedger(ctx context.Context, entries []LedgerEntry) error {
	if r.settings == nil {
		return errors.New("steward: 未提供设置存储，无法保存账本")
	}
	if entries == nil {
		entries = make([]LedgerEntry, 0)
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("steward: 序列化账本失败: %w", err)
	}
	if err := r.settings.SetSetting(ctx, settingLedger, string(payload)); err != nil {
		return fmt.Errorf("steward: 保存账本失败: %w", err)
	}
	return nil
}
