package commandcode

// 本文件复刻 Command Code CLI 自己的模型准入判断（cli.mjs 的 evaluateModelAccess）：
// `cmd --list-models` 会列出整个目录，订阅装不下的模型选了也只会被服务端拒绝，
// 所以目录要按订阅过滤。数据来源：
//   - 方案：GET /alpha/billing/subscriptions 的 data.planId（订阅有效时）；
//   - 额度：GET /alpha/billing/credits，有购买或赠送额度时任何模型都能用；
//   - 模型要求：CLI 缓存的模型目录 ~/.commandcode/cache/model-catalog/v1/catalog-*.json
//     里每个模型的 minimumPlan 与 category。
// 任何一环读不到都按“可用”处理，宁可多列也不把账号能用的模型藏起来。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// planRank 是 CLI 的方案由低到高排序（Hr）。
var planRank = []string{
	"individual-go", "individual-go-v1", "individual-goat", "individual-pro", "individual-pro-v1",
	"individual-provider", "individual-max", "individual-ultra", "teams-pro",
}

// planCategories 是各方案可用的模型类别（Wr）。
var planCategories = map[string][]string{
	"individual-go":       {"opensource"},
	"individual-go-v1":    {"opensource"},
	"individual-goat":     {"opensource"},
	"individual-pro":      {"premium", "opensource"},
	"individual-pro-v1":   {"premium", "opensource"},
	"individual-provider": {"premium", "opensource"},
	"individual-max":      {"premium", "opensource"},
	"individual-ultra":    {"premium", "opensource"},
	"teams-pro":           {"premium", "opensource"},
}

// activeSubscriptionStatus 是 CLI 认可的订阅状态（Yr）。
var activeSubscriptionStatus = map[string]bool{"active": true, "trialing": true, "past_due": true}

// modelAccess 是模型目录里一个模型的准入要求。
type modelAccess struct {
	MinimumPlan string
	Category    string
}

// account 是账号的订阅状态。
type account struct {
	PlanID string
	// HasCredits 为真表示有购买或赠送额度，此时任何模型都可用。
	HasCredits bool
}

// allows 返回账号能否使用要求为 req 的模型。
func (a account) allows(req modelAccess, known bool) bool {
	if a.HasCredits || a.PlanID == "" {
		return true
	}
	categories, ok := planCategories[a.PlanID]
	if !ok {
		return true
	}
	if !known || req.Category == "" {
		return true
	}
	if req.MinimumPlan != "" {
		have, need := rankIndex(a.PlanID), rankIndex(req.MinimumPlan)
		if have >= 0 && need >= 0 {
			return have >= need
		}
	}
	for _, c := range categories {
		if c == req.Category {
			return true
		}
	}
	return false
}

func rankIndex(plan string) int {
	for i, p := range planRank {
		if p == plan {
			return i
		}
	}
	return -1
}

// readCLICatalog 读 CLI 缓存的模型目录，返回按模型 id 与别名（小写）索引的准入要求。
// 缓存不存在或损坏时返回 nil。多份缓存取最近修改的一份。
func readCLICatalog(home string) map[string]modelAccess {
	files, _ := filepath.Glob(filepath.Join(home, ".commandcode", "cache", "model-catalog", "v1", "catalog-*.json"))
	if len(files) == 0 {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		a, errA := os.Stat(files[i])
		b, errB := os.Stat(files[j])
		if errA != nil || errB != nil {
			return files[i] < files[j]
		}
		return a.ModTime().After(b.ModTime())
	})
	data, err := os.ReadFile(files[0])
	if err != nil {
		return nil
	}
	var file struct {
		Body struct {
			Data []struct {
				ID          string   `json:"id"`
				Aliases     []string `json:"aliases"`
				Category    string   `json:"category"`
				MinimumPlan string   `json:"minimumPlan"`
			} `json:"data"`
		} `json:"body"`
	}
	if json.Unmarshal(data, &file) != nil || len(file.Body.Data) == 0 {
		return nil
	}
	out := make(map[string]modelAccess, len(file.Body.Data))
	for _, m := range file.Body.Data {
		req := modelAccess{MinimumPlan: m.MinimumPlan, Category: m.Category}
		out[strings.ToLower(m.ID)] = req
		for _, alias := range m.Aliases {
			out[strings.ToLower(alias)] = req
		}
	}
	return out
}

// FilterByPlan 只保留账号订阅可用的模型。读不到方案、额度或模型目录时原样返回；
// 过滤后为空（目录与方案对不上）也原样返回，避免目录整列消失。
func FilterByPlan(ctx context.Context, models []Model, opts PlanLimitsOptions) []Model {
	key := opts.apiKey()
	if key == "" {
		return models
	}
	catalog := readCLICatalog(opts.home())
	if len(catalog) == 0 {
		return models
	}
	acct, ok := fetchAccount(ctx, opts, key)
	if !ok {
		return models
	}
	return filterModels(models, catalog, acct)
}

func filterModels(models []Model, catalog map[string]modelAccess, acct account) []Model {
	out := make([]Model, 0, len(models))
	for _, m := range models {
		req, known := catalog[strings.ToLower(m.ID)]
		if acct.allows(req, known) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return models
	}
	hasDefault := false
	for _, m := range out {
		hasDefault = hasDefault || m.Default
	}
	if !hasDefault {
		out[0].Default = true
	}
	return out
}

// fetchAccount 读订阅方案与额度；任何一个请求失败返回 ok=false。
func fetchAccount(ctx context.Context, opts PlanLimitsOptions, key string) (account, bool) {
	subscriptions, err := getCommandCodeJSON(ctx, opts, "/alpha/billing/subscriptions", key)
	if err != nil {
		return account{}, false
	}
	creditsBody, err := getCommandCodeJSON(ctx, opts, commandCodePlanLimitsPath, key)
	if err != nil {
		return account{}, false
	}
	var acct account
	var sub struct {
		Data struct {
			PlanID string `json:"planId"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(subscriptions, &sub) == nil && activeSubscriptionStatus[sub.Data.Status] {
		acct.PlanID = sub.Data.PlanID
	}
	var credits struct {
		Credits struct {
			Purchased float64 `json:"purchasedCredits"`
			Free      float64 `json:"freeCredits"`
		} `json:"credits"`
	}
	if json.Unmarshal(creditsBody, &credits) == nil {
		acct.HasCredits = credits.Credits.Purchased > 0 || credits.Credits.Free > 0
	}
	return acct, true
}
