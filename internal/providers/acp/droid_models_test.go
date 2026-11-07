package acp

import (
	"reflect"
	"testing"
)

func TestParseDroidModels(t *testing.T) {
	out := []byte(`{"type":"event","id":"other"}
{"jsonrpc":"2.0","type":"response","id":"models","result":{"models":[{"id":"auto","displayName":"Auto Model","modelProvider":"factory","supportedReasoningEfforts":["none"],"defaultReasoningEffort":"none"},{"id":"claude-x","displayName":"Claude X","modelProvider":"anthropic","supportedReasoningEfforts":["high","low","max","medium"],"defaultReasoningEffort":"medium"},{"id":"old","displayName":"Old","deprecated":true},{"id":"off","displayName":"Off","disabled":true}]}}`)
	models := parseDroidModels(out)
	if len(models) != 2 {
		t.Fatalf("deprecated/disabled models must be dropped: %+v", models)
	}
	if !models[0].Default || models[0].Efforts != nil {
		t.Fatalf("auto should be the default without an effort scale: %+v", models[0])
	}
	if want := []string{"low", "medium", "high", "max"}; !reflect.DeepEqual(models[1].Efforts, want) {
		t.Fatalf("efforts not ordered weakest first: %v", models[1].Efforts)
	}
}
