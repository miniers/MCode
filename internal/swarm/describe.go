package swarm

import (
	"context"
	"errors"
	"strings"

	"mcode/internal/textgen"
)

// TextgenDescriber 返回基于 textgen 的提交说明生成器，供 internal/app/app.go 在
// swarm.NewPipeline 时接线到 PipelineOptions.Describe。
// model 为空时使用该 provider 最便宜的已知模型（textgen.CheapestModel）；
// provider 为空时调用直接返回错误；生成失败时由流水线回退 defaultCommitMessage 兜底。
func TextgenDescriber(provider, model string) func(ctx context.Context, diff string) (string, error) {
	prov := strings.ToLower(strings.TrimSpace(provider))
	mdl := strings.TrimSpace(model)
	if mdl == "" {
		mdl = textgen.CheapestModel(prov)
	}
	return func(ctx context.Context, diff string) (string, error) {
		if prov == "" {
			return "", errors.New("swarm: TextgenDescriber 需要非空 provider")
		}
		return textgen.CommitMessage(ctx, prov, mdl, "", diff, "")
	}
}
