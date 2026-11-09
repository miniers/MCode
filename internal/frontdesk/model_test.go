package frontdesk

import (
	"testing"

	"mcode/internal/apisource"
)

func TestSelectSourceRemovedModel(t *testing.T) {
	sources := []apisource.Source{{Kind: "openai", Models: []string{"gpt-mini"}, DefaultModel: "gpt-mini"}}
	_, model, err := SelectSource(sources, nil, "openai", "removed")
	if err != nil || model != "gpt-mini" {
		t.Fatalf("下架模型应回退到默认值：%q，%v", model, err)
	}
}
