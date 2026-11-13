package fsutil

import (
	"errors"
	"os"
	"testing"
)

func TestRenameRetry(t *testing.T) {
	denied := errors.New("占用")
	for _, tc := range []struct {
		name           string
		windows        bool
		failure        error
		failures, want int
	}{
		{"windows", true, denied, 2, 3},
		{"非 windows", false, denied, 2, 1},
		{"不存在", true, os.ErrNotExist, 2, 1},
		{"达到上限", true, denied, 60, 51},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := renameWithRetry(func() error {
				calls++
				if calls <= tc.failures {
					return tc.failure
				}
				return nil
			}, tc.windows)
			if calls != tc.want || (calls <= tc.failures && !errors.Is(err, tc.failure)) || (calls > tc.failures && err != nil) {
				t.Fatalf("调用 %d 次，错误 %v", calls, err)
			}
		})
	}
}
