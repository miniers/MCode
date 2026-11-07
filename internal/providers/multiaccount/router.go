// Package multiaccount 提供与具体供应商无关的多账号路由与负载均衡能力：
// 账号冷却状态机、失败重试时的排除集合、会话粘性绑定，以及 smart/order/rotate/usage/pace/weight 调度策略。
//
// 其他支持多账号的供应商接入方式：
//  1. 把账号快照转换成 []AccountState（Enabled 表示该账号本轮可参与调度，UsedPercent/ResetAt 为额度视图）；
//  2. 用已持久化的会话绑定和落盘回调构造 NewRouter(bindings, saveBindings)；
//  3. 每轮调用 Pick(cfg, sessionKey, accounts, exclude, now)，限流时调用 MarkLimited(id, resetAt, now)；
//  4. 供应商标记的 token 刷新、隔离目录、额度拉取等逻辑保留在各自包内，不进入本包。
package multiaccount

import (
	"errors"
	"sort"
	"sync"
	"time"
)

type Strategy string

const (
	StrategySmart  Strategy = "smart"
	StrategyOrder  Strategy = "order"
	StrategyRotate Strategy = "rotate"
	StrategyUsage  Strategy = "usage"
	StrategyPace   Strategy = "pace"
	StrategyWeight Strategy = "weight"
)

type Affinity string

const (
	AffinitySession Affinity = "session"
	AffinityTurn    Affinity = "turn"
	AffinityStrict  Affinity = "strict"
)

// Config 是路由调度配置，JSON 字段保持与既有持久化格式一致。
type Config struct {
	Strategy Strategy `json:"strategy"`
	Sink     bool     `json:"sink"`
	Affinity Affinity `json:"affinity"`
}

func DefaultConfig() Config {
	return Config{
		Strategy: StrategySmart,
		Sink:     false,
		Affinity: AffinitySession,
	}
}

func (c Config) Normalize() Config {
	res := c
	switch res.Strategy {
	case StrategySmart, StrategyOrder, StrategyRotate, StrategyUsage, StrategyPace, StrategyWeight:
	default:
		res.Strategy = StrategySmart
	}
	switch res.Affinity {
	case AffinitySession, AffinityTurn, AffinityStrict:
	default:
		res.Affinity = AffinitySession
	}
	return res
}

// AccountState 是供应商账号参与路由决策所需的最小视图。
// Enabled 为 false 的账号仍会参与 strict 亲和性的绑定复用检查，但不会被重新选号。
type AccountState struct {
	ID           string
	Enabled      bool
	Order        int
	Weight       int
	UsedPercent  float64
	ResetAt      time.Time
	RecentTokens float64
}

// candidate 是进入策略排序的可用账号。
type candidate struct {
	ID           string
	Order        int
	Weight       int
	UsedPercent  float64
	ResetAt      time.Time
	RecentTokens float64
}

var ErrNoAccount = errors.New("no available account")

type coolRecord struct {
	until       time.Time
	lastTrigger time.Time
	step        time.Duration
	everCooled  bool
}

// Router 维护冷却状态、轮询计数器、平滑加权轮询权重和会话绑定。
// 所有方法均可并发调用；persist 在持锁期间被调用，回调内不要再次调用 Router。
type Router struct {
	mu            sync.Mutex
	cooling       map[string]*coolRecord
	rotateCounter uint64
	currentWeight map[string]int
	bindings      map[string]string
	persist       func(map[string]string) error
}

// NewRouter 创建 Router。bindings 可为 nil；persist 为 nil 时绑定只保存在内存中。
func NewRouter(bindings map[string]string, persist func(map[string]string) error) *Router {
	if bindings == nil {
		bindings = make(map[string]string)
	}
	return &Router{
		cooling:       make(map[string]*coolRecord),
		currentWeight: make(map[string]int),
		bindings:      bindings,
		persist:       persist,
	}
}

// Pick 为 sessionKey 选择账号。exclude 是本轮已因限流换掉的账号 ID。
// 亲和性为 strict/session 且绑定账号仍可用时复用绑定；turn 模式每轮重新选号。
func (r *Router) Pick(cfg Config, sessionKey string, accounts []AccountState, exclude []string, now time.Time) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg = cfg.Normalize()

	chosenID := r.boundChoiceLocked(cfg, sessionKey, accounts, exclude, now)
	if chosenID == "" {
		picked, err := r.pickLocked(cfg, candidatesFor(accounts, exclude), now, true)
		if err != nil {
			return "", err
		}
		chosenID = picked
	}

	if cfg.Affinity != AffinityTurn && sessionKey != "" && r.bindings[sessionKey] != chosenID {
		r.bindings[sessionKey] = chosenID
		_ = r.persistLocked()
	}

	return chosenID, nil
}

// Peek 与 Pick 的选择逻辑一致，但只读：不推进 rotate 轮询计数、不改平滑加权轮询权重，
// 也不写会话绑定。供额度弹窗这类展示场景预览「下一次真实请求会选到哪个账号」。
func (r *Router) Peek(cfg Config, sessionKey string, accounts []AccountState, exclude []string, now time.Time) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg = cfg.Normalize()

	if chosenID := r.boundChoiceLocked(cfg, sessionKey, accounts, exclude, now); chosenID != "" {
		return chosenID, nil
	}
	return r.pickLocked(cfg, candidatesFor(accounts, exclude), now, false)
}

// boundChoiceLocked 返回可沿用的会话绑定账号，不满足沿用条件时返回空串。
func (r *Router) boundChoiceLocked(cfg Config, sessionKey string, accounts []AccountState, exclude []string, now time.Time) string {
	if sessionKey == "" {
		return ""
	}
	boundID, ok := r.bindings[sessionKey]
	if !ok {
		return ""
	}
	acc, exists := findAccount(accounts, boundID)
	if !exists {
		return ""
	}
	if cfg.Affinity == AffinityStrict {
		// strict：绑定账号仍在账号列表中即沿用，即使冷却中也不换号。
		return boundID
	}
	if cfg.Affinity == AffinitySession {
		// session：绑定账号可用、未冷却且不在排除集合中时沿用。
		if acc.Enabled && acc.UsedPercent < 100 && !r.coolingLocked(boundID, now) && !contains(exclude, boundID) {
			return boundID
		}
	}
	return ""
}

// candidatesFor 拼出参与调度选号的候选账号。
func candidatesFor(accounts []AccountState, exclude []string) []candidate {
	cands := make([]candidate, 0, len(accounts))
	for _, acc := range accounts {
		if !acc.Enabled || contains(exclude, acc.ID) {
			continue
		}
		cands = append(cands, candidate{
			ID:           acc.ID,
			Order:        acc.Order,
			Weight:       acc.Weight,
			UsedPercent:  acc.UsedPercent,
			ResetAt:      acc.ResetAt,
			RecentTokens: acc.RecentTokens,
		})
	}
	return cands
}

// Bind 将会话与指定账号绑定并持久化，空参数直接忽略。
func (r *Router) Bind(sessionKey, accountID string) error {
	if sessionKey == "" || accountID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bindings[sessionKey] = accountID
	return r.persistLocked()
}

// Bound 返回会话当前绑定的账号。
func (r *Router) Bound(sessionKey string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bindings[sessionKey]
	return id, ok
}

// BoundSessions 返回绑定在指定账号上的会话 ID 列表。
func (r *Router) BoundSessions(accountID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []string
	for sessionKey, id := range r.bindings {
		if id == accountID {
			result = append(result, sessionKey)
		}
	}
	return result
}

// ForgetAccount 删除绑定到指定账号的会话并持久化。
func (r *Router) ForgetAccount(accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for sessionKey, id := range r.bindings {
		if id == accountID {
			delete(r.bindings, sessionKey)
			changed = true
		}
	}
	if changed {
		return r.persistLocked()
	}
	return nil
}

func (r *Router) persistLocked() error {
	if r.persist == nil {
		return nil
	}
	snapshot := make(map[string]string, len(r.bindings))
	for k, v := range r.bindings {
		snapshot[k] = v
	}
	return r.persist(snapshot)
}

func (r *Router) MarkLimited(id string, resetAt, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.cooling[id]
	if !ok {
		rec = &coolRecord{}
		r.cooling[id] = rec
	}
	rec.everCooled = true

	if !resetAt.IsZero() && resetAt.After(now) {
		rec.until = resetAt
		rec.lastTrigger = now
	} else {
		if !rec.lastTrigger.IsZero() && now.Sub(rec.lastTrigger) <= 30*time.Minute {
			if rec.step == 0 {
				rec.step = 1 * time.Minute
			} else {
				rec.step *= 2
				if rec.step > 30*time.Minute {
					rec.step = 30 * time.Minute
				}
			}
		} else {
			rec.step = 1 * time.Minute
		}
		rec.until = now.Add(rec.step)
		rec.lastTrigger = now
	}
}

func (r *Router) Cooling(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.coolingLocked(id, now)
}

func (r *Router) CoolingUntil(id string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec := r.cooling[id]; rec != nil {
		return rec.until
	}
	return time.Time{}
}

func (r *Router) coolingLocked(id string, now time.Time) bool {
	rec, ok := r.cooling[id]
	if !ok {
		return false
	}
	return now.Before(rec.until)
}

// pickLocked 按调度策略排序候选账号并返回首选。commit 为 false 时只预览：不推进
// rotate 轮询计数、不改平滑加权轮询的 currentWeight，结果与下一次 Pick 一致。
func (r *Router) pickLocked(cfg Config, cands []candidate, now time.Time, commit bool) (string, error) {
	if len(cands) == 0 {
		return "", ErrNoAccount
	}

	var available []candidate
	for _, c := range cands {
		if r.coolingLocked(c.ID, now) {
			continue
		}
		cand := c
		if cand.Weight <= 0 {
			cand.Weight = 100
		}
		available = append(available, cand)
	}

	if len(available) == 0 {
		return "", ErrNoAccount
	}

	ordered := make([]candidate, len(available))

	switch cfg.Strategy {
	case StrategySmart:
		var fine, low, spent []candidate
		for _, c := range available {
			switch {
			case c.UsedPercent >= 98.0:
				spent = append(spent, c)
			case c.UsedPercent >= 90.0:
				low = append(low, c)
			default:
				fine = append(fine, c)
			}
		}

		sort.SliceStable(fine, func(i, j int) bool {
			a, b := fine[i], fine[j]
			ta := a.ResetAt.Truncate(time.Hour)
			tb := b.ResetAt.Truncate(time.Hour)
			if ta.IsZero() != tb.IsZero() {
				return !ta.IsZero()
			}
			if !ta.Equal(tb) {
				return ta.Before(tb)
			}
			return a.Order < b.Order
		})

		for _, tier := range [][]candidate{low, spent} {
			sort.SliceStable(tier, func(i, j int) bool {
				if tier[i].UsedPercent != tier[j].UsedPercent {
					return tier[i].UsedPercent < tier[j].UsedPercent
				}
				return tier[i].Order < tier[j].Order
			})
		}

		ordered = append(append(fine, low...), spent...)

	case StrategyOrder:
		var room, full []candidate
		for _, c := range available {
			if c.UsedPercent < 100.0 {
				room = append(room, c)
			} else {
				full = append(full, c)
			}
		}
		sort.SliceStable(room, func(i, j int) bool { return room[i].Order < room[j].Order })
		sort.SliceStable(full, func(i, j int) bool { return full[i].Order < full[j].Order })
		ordered = append(room, full...)

	case StrategyRotate:
		n := int(r.rotateCounter % uint64(len(available)))
		if commit {
			r.rotateCounter++
		}
		ordered = append(append([]candidate{available[n]}, available[:n]...), available[n+1:]...)

	case StrategyUsage:
		copy(ordered, available)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].UsedPercent != ordered[j].UsedPercent {
				return ordered[i].UsedPercent < ordered[j].UsedPercent
			}
			if ordered[i].RecentTokens != ordered[j].RecentTokens {
				return ordered[i].RecentTokens < ordered[j].RecentTokens
			}
			return ordered[i].Order < ordered[j].Order
		})

	case StrategyPace:
		calcPace := func(c candidate) float64 {
			remain := 100.0 - c.UsedPercent
			if remain < 0 {
				remain = 0
			}
			if c.ResetAt.IsZero() {
				return 100.0 / 168.0
			}
			hours := c.ResetAt.Sub(now).Hours()
			if hours < 1.0 {
				hours = 1.0
			}
			return remain / hours
		}

		byPace := make([]candidate, len(available))
		copy(byPace, available)
		sort.SliceStable(byPace, func(i, j int) bool {
			return calcPace(byPace[i]) > calcPace(byPace[j])
		})

		band := make(map[string]int)
		var top float64
		currentBand := 0
		for i, c := range byPace {
			p := calcPace(c)
			if i == 0 || p < 0.9*top {
				top = p
				currentBand = i
			}
			band[c.ID] = currentBand
		}

		copy(ordered, available)
		sort.SliceStable(ordered, func(i, j int) bool {
			ba := band[ordered[i].ID]
			bb := band[ordered[j].ID]
			if ba != bb {
				return ba < bb
			}
			if ordered[i].RecentTokens != ordered[j].RecentTokens {
				return ordered[i].RecentTokens < ordered[j].RecentTokens
			}
			return ordered[i].Order < ordered[j].Order
		})

	case StrategyWeight:
		valid := make(map[string]bool, len(available))
		for _, c := range available {
			valid[c.ID] = true
		}
		if commit {
			for k := range r.currentWeight {
				if !valid[k] {
					delete(r.currentWeight, k)
				}
			}
		}

		bestIdx := -1
		totalWeight := 0
		bestScore := 0
		for i, c := range available {
			w := c.Weight
			score := r.currentWeight[c.ID] + w
			if commit {
				r.currentWeight[c.ID] = score
			}
			totalWeight += w
			if bestIdx < 0 || score > bestScore {
				bestIdx = i
				bestScore = score
			}
		}

		if bestIdx >= 0 {
			bestID := available[bestIdx].ID
			if commit {
				r.currentWeight[bestID] -= totalWeight
			}
			ordered = append(append([]candidate{available[bestIdx]}, available[:bestIdx]...), available[bestIdx+1:]...)
		} else {
			copy(ordered, available)
		}
	}

	if cfg.Sink {
		var uncooled, cooled []candidate
		for _, c := range ordered {
			rec := r.cooling[c.ID]
			if rec != nil && rec.everCooled {
				cooled = append(cooled, c)
			} else {
				uncooled = append(uncooled, c)
			}
		}
		ordered = append(uncooled, cooled...)
	}

	return ordered[0].ID, nil
}

func findAccount(accounts []AccountState, id string) (AccountState, bool) {
	for _, acc := range accounts {
		if acc.ID == id {
			return acc, true
		}
	}
	return AccountState{}, false
}

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}
