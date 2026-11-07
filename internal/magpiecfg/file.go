// 本文件移植自 magpie 的 internal/edit/file.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。
//
// 与 magpie 的差异：去掉了 filememo 缓存（每次都直接读盘）、appdir 的目录
// 策略与写入过滤器；日志调用改为返回错误。

// Package magpiecfg 提供按点分键路径修改 JSONC 与 TOML 配置文件、并保留
// 注释和原有格式的底层能力。
package magpiecfg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Read 返回文件内容；文件不存在时返回 (nil, nil)。返回值归调用方所有。
// 开头的 UTF-8 字节序标记不属于返回值：Windows 工具（PowerShell 的 UTF8
// 编码、记事本等）会在配置文件开头写一个 BOM，而 JSON/TOML 解析器会拒绝
// 它。WriteAtomic 会把它写回去，见 keepBOM。
func Read(path string) ([]byte, error) {
	b, err := ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return bytes.Clone(TrimBOM(b)), nil
}

// bom 是 UTF-8 字节序标记。
var bom = []byte("\xef\xbb\xbf")

// TrimBOM 返回去掉开头 UTF-8 字节序标记后的 b。
func TrimBOM(b []byte) []byte { return bytes.TrimPrefix(b, bom) }

// keepBOM 给 data 加上 path 原有文件开头的字节序标记（当文件有、data 没有
// 时）：用户的文件保持它被保存时的编码，读它的工具也继续看到这个标记。
func keepBOM(path string, data []byte) []byte {
	if len(data) == 0 || bytes.HasPrefix(data, bom) {
		return data
	}
	f, err := os.Open(path)
	if err != nil {
		return data
	}
	defer f.Close()
	head := make([]byte, len(bom))
	if n, _ := io.ReadFull(f, head); n < len(bom) || !bytes.Equal(head, bom) {
		return data
	}
	return append(append(make([]byte, 0, len(bom)+len(data)), bom...), data...)
}

// WriteAtomic 通过临时文件加 rename 写入数据，崩溃时不会留下半截配置。
// 保留文件权限和所属组；path 是符号链接时写它指向的文件、保留链接；文件有
// 其它硬链接时就地写，见 writeInPlace。写完后清掉更早的写入残留下来的临时
// 文件，见 removeStaleTemps。
func WriteAtomic(path string, data []byte) error {
	path, err := Target(path)
	if err != nil {
		return err
	}
	data = keepBOM(path, data)
	// 内容相同就不写：新的 mtime 会唤醒所有监听它的程序，rename 还会在
	// 别的程序读取时把文件换掉，而这一切毫无必要。
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	if hardLinked(path) {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			if err := writeInPlace(f, data); err != nil {
				return err
			}
			removeStaleTemps(path)
			return nil
		}
		// 打不开供写入的文件（权限 0444，或 Windows 上的只读文件）仍按
		// 老办法 rename 覆盖，这会让它和其它硬链接各走各的；先尝试打开，
		// 所以失败时不会在它旁边多出文件。
		if !errors.Is(err, fs.ErrPermission) {
			return err
		}
	}
	mode := fs.FileMode(0o644)
	st, statErr := os.Stat(path)
	if statErr == nil {
		mode = st.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if statErr == nil {
		keepGroup(tmp, st)
	}
	// 先把数据刷到磁盘再 rename：两者之间机器掉电，文件会以完整长度留成
	// 全零。
	if err := Sync(tmp); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	removeStaleTemps(path)
	return nil
}

// writeInPlace 把 data 写进已打开供写入的 f 并关闭它：用于还有其它名字的
// 文件（硬链接），rename 覆盖其中一个名字会把它和其余名字拆开，各留一份
// 旧文本。就地写不原子，所以文本先写到它旁边的临时文件并刷盘，再写进目标
// 文件：中途崩溃时整份新文本都留在临时文件里，写失败时也会保留，并在错误
// 里给出它的路径。文件保留权限、所有者和全部名字。
func writeInPlace(f *os.File, data []byte) error {
	path := f.Name()
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		f.Close()
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err != nil {
		f.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	// 覆盖写再按新长度截断：读的人可能看到新文本开头盖在旧文本上，或在新
	// 文本更短时看到新文本后面跟着旧文本的尾巴。先清空文件并不能关掉这个
	// 窗口，只会把窗口里显示的东西换成空文件或半截文件——工具可能把它当成
	// 一份什么都没配的配置加载并照此行动，而乱掉的文本只会解析失败。
	_, err = f.Write(data)
	if err == nil {
		err = f.Truncate(int64(len(data)))
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return fmt.Errorf("%w; the new text is kept in %s", err, tmp.Name())
	}
	_ = os.Remove(tmp.Name())
	return nil
}

// staleTemp 是配置文件旁的临时文件多久没被碰过才算崩溃残留。
const staleTemp = 10 * time.Minute

// removeStaleTemps 删除更早某次写入崩溃或失败时留在 path 旁边的临时文件：
// 那时 path 已经写入了更新的文本。比 staleTemp 年轻的临时文件保留，因为
// 另一次写入可能还在用它。
func removeStaleTemps(path string) {
	dir, prefix := filepath.Dir(path), "."+filepath.Base(path)+"."
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range es {
		mid, ok := strings.CutPrefix(e.Name(), prefix)
		if ok {
			mid, ok = strings.CutSuffix(mid, ".tmp")
		}
		// os.CreateTemp 用数字填 *；别的名字是别的文件的，例如
		// .models.yml.bak.1.tmp 属于 models.yml.bak。
		if !ok || mid == "" || strings.Trim(mid, "0123456789") != "" {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleTemp {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Target 是写 path 时应该替换的文件：path 本身，或 path 是符号链接时链尾
// 的文件——即使那里还什么都没有，也是它指向的路径。直接 rename 覆盖链接
// 会把它变成一个普通文件，而它指向的目标保持原样。
func Target(path string) (string, error) {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p, nil
	}
	p := path
	for range 255 {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return p, nil
		}
		dest, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			// 不用 Join：它的清理会把 ".." 带过被链接的目录。
			dest = filepath.Dir(p) + string(filepath.Separator) + dest
		}
		p = dest
	}
	return "", fmt.Errorf("%s: too many links", path)
}

// Atomically 运行可能分几步写多个文件的 fn，并在 fn 失败时把 paths 里的每
// 个文件恢复原样——原来的字节和权限，或它原本不存在——这样中途失败的编辑
// 不会留下写了一半的文件。事先读不出来的文件按原样留给 fn。
func Atomically(fn func() error, paths ...string) error {
	type saved struct {
		path   string
		data   []byte
		mode   fs.FileMode
		exists bool
	}
	var before []saved
	for _, p := range paths {
		st, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			if t, err := Target(p); err == nil {
				p = t // 指向尚不存在目标的链接保留，只删 fn 造出来的东西
			}
			before = append(before, saved{path: p})
			continue
		}
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		before = append(before, saved{p, b, st.Mode().Perm(), true})
	}
	err := fn()
	if err == nil {
		return nil
	}
	for _, s := range before {
		now, rerr := os.ReadFile(s.path)
		switch {
		case !s.exists:
			if !errors.Is(rerr, fs.ErrNotExist) {
				if e := os.Remove(s.path); e != nil && !errors.Is(e, fs.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
				}
			}
		case rerr != nil || !bytes.Equal(now, s.data):
			if e := WriteAtomic(s.path, s.data); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			} else if e := os.Chmod(s.path, s.mode); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			}
		}
	}
	return err
}
