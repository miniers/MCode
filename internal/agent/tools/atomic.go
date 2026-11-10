package tools

import (
	"os"

	"mcode/internal/fsutil"
)

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return fsutil.WriteFileAtomic(path, data, mode)
}
