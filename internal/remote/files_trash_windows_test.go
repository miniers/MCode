package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/remote/wire"
)

func TestFilesTrash_Windows(t *testing.T) {
	b, _, dir, _, threadID := setupWorkspaceTestEnv(t)
	path := filepath.Join(dir, "trash-test.txt")
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.FilesTrash(context.Background(), wire.FilesTrashParams{ThreadID: threadID, Path: "trash-test.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("回收后原路径仍存在或无法访问: %v", err)
	}
}
