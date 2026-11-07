//go:build windows

package accounts

import (
	"context"
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// antigravityCredentialTarget 是 agy CLI 在 Windows 凭据管理器里的条目名。
	// agy 经 zalando/go-keyring（底层 danieljoos/wincred）把凭证写成 Generic Credential，
	// go-keyring 在 Windows 上把 target 拼成 service + ":" + user；agy 的 service 是 "gemini"、
	// user 是 "antigravity"（与 macOS 钥匙串条目一致），所以推断为 "gemini:antigravity"。
	// 这一条是按 go-keyring 的规则推断的，尚未在真机核实，可用 `cmdkey /list` 确认条目名。
	antigravityCredentialTarget = "gemini:antigravity"

	// credTypeGeneric 是 wincred 的 CRED_TYPE_GENERIC，go-keyring 写入的就是这一类。
	credTypeGeneric = 1
)

var (
	// advapi32 提供凭据管理 API（CredReadW / CredFree）。
	advapi32      = syscall.NewLazyDLL("advapi32.dll")
	procCredReadW = advapi32.NewProc("CredReadW")
	procCredFree  = advapi32.NewProc("CredFree")
)

// windowsCredential 对应 wincred.h 的 CREDENTIALW：字段顺序与类型必须与系统结构一致，
// 这里只声明读取时用到的字段，但后面的字段不能省，否则偏移对不上。
type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// readAntigravityCredential 用 CredReadW 读取 agy 的 Generic Credential；
// 成功时返回的句柄必须由 CredFree 释放，调用方负责。
func readAntigravityCredential() (*windowsCredential, error) {
	target, err := syscall.UTF16PtrFromString(antigravityCredentialTarget)
	if err != nil {
		return nil, errors.New("system agy credentials unavailable")
	}
	var credential *windowsCredential
	ok, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if ok == 0 {
		if callErr == windows.ERROR_NOT_FOUND {
			return nil, errors.New("system agy credentials not found")
		}
		return nil, errors.New("system agy credentials unavailable")
	}
	if credential == nil {
		return nil, errors.New("system agy credentials unavailable")
	}
	return credential, nil
}

// readSystemCredentialBytes 读出凭据的 CredentialBlob（go-keyring 存的是密码的 UTF-8 字节）。
// 读到的内容只留在内存，不回写凭据管理器。
func readSystemCredentialBytes(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.New("system agy credentials unavailable")
	}
	credential, err := readAntigravityCredential()
	if err != nil {
		return nil, err
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlob == nil || credential.CredentialBlobSize == 0 {
		return nil, errors.New("system agy credentials unavailable")
	}
	// 拷贝出 Go 堆上的切片：CredFree 之后系统内存不再有效。
	blob := unsafe.Slice(credential.CredentialBlob, int(credential.CredentialBlobSize))
	data := make([]byte, len(blob))
	copy(data, blob)
	return data, nil
}

// systemCredentialHasEntry 只确认凭据管理器里存在该条目，不解析其中的凭证内容。
func systemCredentialHasEntry(ctx context.Context) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	credential, err := readAntigravityCredential()
	if err != nil {
		return false
	}
	procCredFree.Call(uintptr(unsafe.Pointer(credential)))
	return true
}
