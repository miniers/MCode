package kanban

import "testing"

func TestParseExecutorResultLastBlock(t *testing.T) {
	r, err := ParseExecutorResult("```json\n{\"status\":\"failed\"}\n```\n```json\n{\"status\":\"waiting\",\"wait_kind\":\"human\",\"wait_minutes\":999}\n```")
	if err != nil || r.Status != ResultWaiting || r.WaitMinutes != 120 {
		t.Fatalf("结果 %v，错误 %v", r, err)
	}
}

func TestExecutorResultValidate(t *testing.T) {
	cases := []struct {
		result ExecutorResult
		valid  bool
	}{
		{ExecutorResult{Status: ResultDone}, true},
		{ExecutorResult{Status: ResultFailed}, true},
		{ExecutorResult{Status: "unknown"}, false},
		{ExecutorResult{Status: ResultNeedsDecision}, false},
		{ExecutorResult{Status: ResultNeedsDecision, Question: "问题", Options: []DecisionOption{{Key: "A"}}}, false},
		{ExecutorResult{Status: ResultNeedsDecision, Question: "问题", Options: []DecisionOption{{Key: "A"}, {Key: "B"}}}, true},
		{ExecutorResult{Status: ResultDelegated}, false},
		{ExecutorResult{Status: ResultDelegated, Followups: []Followup{{Title: "子任务"}}}, true},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "other"}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine"}, false},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "machine", WaitCheck: "true"}, true},
		{ExecutorResult{Status: ResultWaiting, WaitKind: "human"}, true},
	}
	for _, tc := range cases {
		if (tc.result.Validate() == nil) != tc.valid {
			t.Errorf("结果 %#v 的校验不符合预期", tc.result)
		}
	}
}
