package claude

import (
	"reflect"
	"testing"

	"mcode/internal/harness"
)

// TestBuildArgsAdditionalDirs 覆盖附加目录映射：每个非空且不等于工作目录的目录生成一对
// --add-dir <dir>，位置在 --permission-mode 之后、extra 之前。
func TestBuildArgsAdditionalDirs(t *testing.T) {
	base := func(rest ...string) []string {
		args := []string{
			"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
			"--disallowed-tools", "Agent,Task", "--permission-mode", "acceptEdits",
		}
		return append(args, rest...)
	}

	tests := []struct {
		name  string
		turn  harness.Turn
		extra []string
		want  []string
	}{
		{
			name: "逐个生成--add-dir并跳过空串与工作目录",
			turn: harness.Turn{
				WorkDir:        "/work/main",
				AdditionalDirs: []string{"/proj/a", "", "/work/main", "/proj/b"},
			},
			extra: []string{"--custom-flag"},
			want:  base("--add-dir", "/proj/a", "--add-dir", "/proj/b", "--custom-flag"),
		},
		{
			name: "附加目录为空时参数与原来完全一致",
			turn: harness.Turn{WorkDir: "/work/main"},
			want: base(),
		},
		{
			name: "只有工作目录自身时也不追加参数",
			turn: harness.Turn{WorkDir: "/work/main", AdditionalDirs: []string{"/work/main"}},
			want: base(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildArgs(tc.turn, tc.extra...); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildArgs() = %v, want %v", got, tc.want)
			}
		})
	}
}
