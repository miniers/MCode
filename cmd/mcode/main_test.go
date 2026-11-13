package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAugmentedPath(t *testing.T) {
	for _, tc := range []struct {
		name        string
		path        string
		home        string
		goos        string
		wantPrefix  string
		wantContain []string
		absent      []string
		exactlyOnce []string
	}{
		{
			name:        "empty path gets defaults first",
			path:        "",
			home:        "/home/u",
			goos:        "darwin",
			wantPrefix:  "/opt/homebrew/bin",
			wantContain: []string{"/home/u/.local/bin", "/home/u/.version-fox/sdks/nodejs/bin"},
			exactlyOnce: []string{"/home/u/.version-fox/sdks/golang/bin", "/usr/local/go/bin"},
		},
		{
			name:        "existing entries keep order and are not duplicated",
			path:        "/usr/bin:/custom",
			home:        "/home/u",
			goos:        "darwin",
			wantPrefix:  "/usr/bin:/custom:",
			wantContain: []string{"/custom"},
			exactlyOnce: []string{"/usr/bin"},
		},
		{
			name:       "empty home skips home entries",
			path:       "/usr/bin",
			home:       "",
			goos:       "darwin",
			wantPrefix: "/usr/bin:",
			absent: []string{
				".local/bin", ".bun/bin", ".cargo/bin", ".npm-global/bin", ".volta/bin",
				".version-fox/sdks/nodejs/bin", ".version-fox/sdks/golang/bin", ".version-fox/shims", ".local/share/mise/shims", ".asdf/shims",
			},
		},
		{
			// Windows 的 PATH 用分号分隔、路径里带盘符冒号，按冒号拆会把 C:\Windows\System32 拆坏。
			name:       "windows keeps drive letters and joins with semicolons",
			path:       `C:\Windows\System32;C:\Tools`,
			home:       `C:\Users\u`,
			goos:       "windows",
			wantPrefix: `C:\Windows\System32;C:\Tools;`,
			wantContain: []string{
				filepath.Join(`C:\Users\u`, ".local", "bin"),
				filepath.Join(`C:\Users\u`, ".bun", "bin"),
				filepath.Join(`C:\Users\u`, ".cargo", "bin"),
			},
			absent:      []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/go/bin", "/usr/bin", "/usr/sbin"},
			exactlyOnce: []string{`C:\Windows\System32`, `C:\Tools`},
		},
		{
			name:        "windows does not duplicate existing entries",
			path:        `C:\Windows\System32;` + filepath.Join(`C:\Users\u`, ".local", "bin"),
			home:        `C:\Users\u`,
			goos:        "windows",
			wantPrefix:  `C:\Windows\System32;` + filepath.Join(`C:\Users\u`, ".local", "bin") + `;`,
			exactlyOnce: []string{filepath.Join(`C:\Users\u`, ".local", "bin")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.goos != "windows" {
				t.Skip("darwin/linux 路径分隔符用例不适用于 Windows 宿主")
			}
			got := augmentedPath(tc.path, tc.home, tc.goos)
			entries := strings.Split(got, pathListSeparator(tc.goos))

			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("前缀不符: got %q, want prefix %q", got, tc.wantPrefix)
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("结果 %q 缺少 %q", got, want)
				}
			}
			for _, bad := range tc.absent {
				for _, entry := range entries {
					if strings.HasSuffix(entry, bad) {
						t.Errorf("结果 %q 不应包含以 %q 结尾的项", got, bad)
					}
				}
			}
			for _, want := range tc.exactlyOnce {
				count := 0
				for _, entry := range entries {
					if entry == want {
						count++
					}
				}
				if count != 1 {
					t.Errorf("项 %q 出现 %d 次，期望 1 次: %q", want, count, got)
				}
			}
		})
	}
}

// TestAugmentedPathWindowsAppData 覆盖 %APPDATA%\npm 的补充逻辑：APPDATA 由环境变量提供，
// 单独用 t.Setenv 设置，避免表驱动用例依赖宿主机环境。
func TestAugmentedPathWindowsAppData(t *testing.T) {
	t.Setenv("APPDATA", `C:\Users\u\AppData\Roaming`)

	got := augmentedPath(`C:\Windows\System32`, `C:\Users\u`, "windows")
	want := filepath.Join(`C:\Users\u\AppData\Roaming`, "npm")
	if !strings.Contains(got, want) {
		t.Fatalf("结果 %q 缺少 %q", got, want)
	}
	if strings.Count(got, want) != 1 {
		t.Fatalf("项 %q 应只出现一次: %q", want, got)
	}
}

// TestUseSingleInstanceLock 覆盖单实例锁的平台差异：macOS 由 LaunchServices 保证，
// 其他平台必须自己抢锁。
func TestUseSingleInstanceLock(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want bool
	}{
		{"darwin", false},
		{"windows", true},
		{"linux", true},
	} {
		if got := useSingleInstanceLock(tc.goos); got != tc.want {
			t.Errorf("goos %q: got %v, want %v", tc.goos, got, tc.want)
		}
	}
}

// TestShouldQuitOnAllWindowsClosed 覆盖窗口全关后的行为：macOS 始终留在后台，
// 其他平台只在没有托盘可唤回时退出。
func TestShouldQuitOnAllWindowsClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		goos       string
		trayActive bool
		want       bool
	}{
		{"darwin stays resident", "darwin", true, false},
		{"darwin stays resident without tray", "darwin", false, false},
		{"windows quits without tray", "windows", false, true},
		{"windows stays resident with tray", "windows", true, false},
		{"linux quits without tray", "linux", false, true},
		{"linux stays resident with tray", "linux", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldQuitOnAllWindowsClosed(tc.goos, tc.trayActive); got != tc.want {
				t.Fatalf("goos %q tray %v: got %v, want %v", tc.goos, tc.trayActive, got, tc.want)
			}
		})
	}
}

// TestTrayKeepsAppAlive 覆盖「最小化到托盘」的前置条件：macOS 靠 Dock 图标就能唤回，
// 其他平台必须托盘真的建起来了。
func TestTrayKeepsAppAlive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		goos       string
		trayActive bool
		want       bool
	}{
		{"darwin with tray", "darwin", true, true},
		{"darwin without tray", "darwin", false, true},
		{"windows with tray", "windows", true, true},
		{"windows without tray", "windows", false, false},
		{"linux without tray", "linux", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trayKeepsAppAlive(tc.goos, tc.trayActive); got != tc.want {
				t.Fatalf("goos %q tray %v: got %v, want %v", tc.goos, tc.trayActive, got, tc.want)
			}
		})
	}
}
