// 本文件的测试改写自 magpie 的 internal/steady/steady_test.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。
package magpiecfg

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestWriteAtomicRenameInjection(t *testing.T) {
	old := rename
	t.Cleanup(func() { rename = old })
	denied := errors.New("重命名失败")
	rename = func(_, _ string) error { return denied }
	if err := WriteAtomic(filepath.Join(t.TempDir(), "settings.json"), []byte("{}")); !errors.Is(err, denied) {
		t.Fatalf("错误: %v", err)
	}
}
