package swarm

import "strings"

// Pair 描述一组「主代理 + 子代理」的模型配对：编排把子任务派发到配对里配置的子代理模型上。
// 字段刻意不带 json tag，序列化由调用方（如 config 包）自己负责。
type Pair struct {
	ID   string
	Name string

	LeadProvider string
	LeadModel    string
	LeadEffort   string
	LeadFastMode bool

	BeeProvider string
	BeeModel    string
	BeeEffort   string
	BeeFastMode bool

	BeeTiers map[string]TierOverride
}

// FindPair 按引用查找配对：ref 先去除首尾空白，为空时返回 false；
// 先精确匹配 ID，再按 Name 做大小写不敏感的匹配。
func FindPair(pairs []Pair, ref string) (Pair, bool) {
	want := strings.TrimSpace(ref)
	if want == "" {
		return Pair{}, false
	}
	for _, p := range pairs {
		if p.ID == want {
			return p, true
		}
	}
	for _, p := range pairs {
		if strings.EqualFold(strings.TrimSpace(p.Name), want) {
			return p, true
		}
	}
	return Pair{}, false
}

// MatchPair 按当前的主代理与子代理模型选择推断生效的配对：
// 主导 provider 为空时返回 false；主导与子代理 provider/model 都相等才算命中，
// 配对的主导模型为空视为通配。先扫描主导模型完全一致（非空）的配对，
// 第二轮才接受主导模型为空的通配配对，同一轮内按列表顺序取第一条。
// 子代理 provider/model 为空表示跟随主导（与派发时的回退口径一致），
// 配对里为空的子代理 provider/model 同样回落到配对的主导侧。
func MatchPair(pairs []Pair, leadProvider, leadModel, beeProvider, beeModel string) (Pair, bool) {
	leadProvider = strings.TrimSpace(leadProvider)
	if leadProvider == "" {
		return Pair{}, false
	}
	leadModel = strings.TrimSpace(leadModel)
	beeProvider = strings.TrimSpace(beeProvider)
	if beeProvider == "" {
		beeProvider = leadProvider
	}
	beeModel = strings.TrimSpace(beeModel)
	if beeModel == "" {
		beeModel = leadModel
	}
	for pass := 0; pass < 2; pass++ {
		for _, p := range pairs {
			if strings.TrimSpace(p.LeadProvider) != leadProvider {
				continue
			}
			pairLeadModel := strings.TrimSpace(p.LeadModel)
			if pass == 0 {
				// 第一轮只要主导模型完全一致的配对。
				if pairLeadModel == "" || pairLeadModel != leadModel {
					continue
				}
			} else if pairLeadModel != "" {
				continue
			}
			workerProvider := strings.TrimSpace(p.BeeProvider)
			if workerProvider == "" {
				workerProvider = strings.TrimSpace(p.LeadProvider)
			}
			workerModel := strings.TrimSpace(p.BeeModel)
			if workerModel == "" {
				workerModel = pairLeadModel
			}
			if workerProvider == beeProvider && workerModel == beeModel {
				return p, true
			}
		}
	}
	return Pair{}, false
}

// PairStillMatches 判断一个已选配对是否仍与当前的主导与子代理模型一致：
// 规则与 MatchPair 相同，配对的主导模型为空视为通配。用于判断显式选中的配对是否还适用。
func PairStillMatches(p Pair, leadProvider, leadModel, beeProvider, beeModel string) bool {
	_, ok := MatchPair([]Pair{p}, leadProvider, leadModel, beeProvider, beeModel)
	return ok
}
