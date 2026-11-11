package gate

import (
	"encoding/json"
	"testing"
)

func TestLeadPlansOnlyDenial(t *testing.T) {
	for _, tc := range []struct {
		tool, command string
		allow         bool
	}{
		{"Read", "", true}, {"Glob", "", true}, {"Grep", "", true}, {"LS", "", true}, {"WebFetch", "", true}, {"WebSearch", "", true},
		{"mcp__mcode__swarm_delegate", "", true}, {"mcp__mcode__swarm_report_get", "", true}, {"mcp__mcode__memory", "", true},
		{"Edit", "", false}, {"Write", "", false}, {"NotebookEdit", "", false}, {"apply_patch", "", false}, {"mcp__other__read", "", false},
		{"Bash", "rg foo . && git diff", true}, {"exec_command", "go build ./...", true}, {"Bash", "go test -timeout 60s -run TestName -count=1 -race -v -tags unit ./...", true}, {"Bash", "go vet ./...", true},
		{"Bash", "python -c 'print(1)'", false}, {"Bash", "echo hi > file", false}, {"Bash", "sed -i x file", false}, {"Bash", "git commit -m x", false}, {"Bash", "go test ./... && git commit -m x", false},
		{"Bash", "go test -o out ./...", false}, {"Bash", "go test -c ./...", false}, {"Bash", "go test -exec=x ./...", false}, {"Bash", "go test -toolexec x ./...", false}, {"Bash", "go test -overlay=x ./...", false}, {"Bash", "go test -modfile x ./...", false}, {"Bash", "go test -coverprofile=x ./...", false}, {"Bash", "go test -cpuprofile x ./...", false}, {"Bash", "go test -memprofile=x ./...", false}, {"Bash", "go test -trace x ./...", false}, {"Bash", "go test -blockprofile=x ./...", false}, {"Bash", "go test -mutexprofile x ./...", false}, {"Bash", "go test -outputdir=x ./...", false}, {"Bash", "go test -args x", false}, {"Bash", "go test -mod=mod ./...", false}, {"Bash", "go run x.go", false}, {"Bash", "go generate ./...", false}, {"Bash", "go test -unknown ./...", false},
	} {
		args, _ := json.Marshal(map[string]string{"command": tc.command})
		if got := LeadPlansOnlyDenial(tc.tool, args); (got == "") != tc.allow {
			t.Errorf("%s %q: denial %q", tc.tool, tc.command, got)
		}
	}
	if LeadPlansOnlyDenial("Bash", json.RawMessage(`{`)) == "" || LeadPlansOnlyDenial("Bash", json.RawMessage(`{}`)) == "" {
		t.Fatal("invalid input allowed")
	}
	if OnlyReads("go test ./...") {
		t.Fatal("OnlyReads allowed go test")
	}
}
