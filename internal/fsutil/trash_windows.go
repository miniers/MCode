//go:build windows

package fsutil

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var shellFileOperation = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

type shellFileOperationData struct {
	hwnd          uintptr
	operation     uint32
	from          *uint16
	to            *uint16
	flags         uint16
	aborted       int32
	mappings      uintptr
	progressTitle *uint16
}

func init() { windowsTrashPath = moveToWindowsTrash }

func moveToWindowsTrash(path string) error {
	from, err := windows.UTF16FromString(path)
	if err != nil {
		return err
	}
	from = append(from, 0)
	op := shellFileOperationData{operation: 3, from: &from[0], flags: 0x40 | 0x10 | 0x4 | 0x400}
	result, _, _ := shellFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	if result != 0 {
		return fmt.Errorf("SHFileOperationW: %w", windows.Errno(result))
	}
	if op.aborted != 0 {
		return fmt.Errorf("SHFileOperationW: 操作已取消")
	}
	return nil
}
