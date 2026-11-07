package mcodeui

import (
	"testing"
)

func TestEditorLineURL(t *testing.T) {
	cases := []struct {
		name  string
		app   openInApp
		path  string
		line  int
		col   int
		want  string
		valid bool
	}{
		{
			name: "vscode 带列号",
			app:  openInApp{Name: "Visual Studio Code", BundleID: "com.microsoft.VSCode", AppPath: "/Applications/Visual Studio Code.app"},
			path: "/Users/me/project/main.go", line: 12, col: 5,
			want: "vscode://file/Users/me/project/main.go:12:5", valid: true,
		},
		{
			name: "cursor 路径含空格",
			app:  openInApp{Name: "Cursor", AppPath: "/Applications/Cursor.app"},
			path: "/Users/me/My Project/main.go", line: 7, col: 0,
			want: "cursor://file/Users/me/My%20Project/main.go:7", valid: true,
		},
		{
			name: "windsurf 无列号",
			app:  openInApp{Name: "Windsurf", AppPath: "/Applications/Windsurf.app"},
			path: "/tmp/a.go", line: 3, col: 0,
			want: "windsurf://file/tmp/a.go:3", valid: true,
		},
		{
			name: "zed 按 BundleID 识别",
			app:  openInApp{Name: "Zed", BundleID: "dev.zed.Zed", AppPath: "/Applications/Zed.app"},
			path: "/tmp/a.go", line: 9, col: 2,
			want: "zed://file/tmp/a.go:9:2", valid: true,
		},
		{
			name: "忽略大小写",
			app:  openInApp{Name: "cursor"},
			path: "/tmp/a.go", line: 1, col: 1,
			want: "cursor://file/tmp/a.go:1:1", valid: true,
		},
		{
			name: "Xcode 不支持",
			app:  openInApp{Name: "Xcode", BundleID: "com.apple.dt.Xcode", AppPath: "/Applications/Xcode.app"},
			path: "/tmp/a.go", line: 1, col: 1,
			valid: false,
		},
		{
			name: "Sublime 不支持",
			app:  openInApp{Name: "Sublime Text", AppPath: "/Applications/Sublime Text.app"},
			path: "/tmp/a.go", line: 1, col: 1,
			valid: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := editorLineURL(tc.app, tc.path, tc.line, tc.col)
			if ok != tc.valid {
				t.Fatalf("支持性判断不符，期望 %v 实际 %v（%q）", tc.valid, ok, got)
			}
			if tc.valid && got != tc.want {
				t.Fatalf("URL 不符，期望 %q 实际 %q", tc.want, got)
			}
		})
	}
}

// withOpenLineStubs 临时替换打开编辑器相关的包级变量。
func withOpenLineStubs(t *testing.T, goos string, apps []openInApp, runner func(string) error) {
	t.Helper()
	origGOOS := openLineGOOS
	origApps := openLineInstalledApps
	origRunner := openEditorURLRunner
	t.Cleanup(func() {
		openLineGOOS = origGOOS
		openLineInstalledApps = origApps
		openEditorURLRunner = origRunner
	})
	openLineGOOS = goos
	openLineInstalledApps = func(*chatState) []openInApp { return apps }
	openEditorURLRunner = runner
}

func TestOpenPathAtLineUsesCursorURL(t *testing.T) {
	var got []string
	apps := []openInApp{
		{Name: "Xcode", AppPath: "/Applications/Xcode.app"},
		{Name: "Cursor", AppPath: "/Applications/Cursor.app"},
		{Name: "Zed", AppPath: "/Applications/Zed.app"},
	}
	withOpenLineStubs(t, "darwin", apps, func(target string) error {
		got = append(got, target)
		return nil
	})

	if !openPathAtLine(nil, "/Users/me/My Project/main.go", 42, 3) {
		t.Fatal("期望打开成功")
	}
	if len(got) != 1 {
		t.Fatalf("应只调用一次，实际 %d 次: %v", len(got), got)
	}
	if want := "cursor://file/Users/me/My%20Project/main.go:42:3"; got[0] != want {
		t.Fatalf("URL 不符，期望 %q 实际 %q", want, got[0])
	}
}

func TestOpenPathAtLineSkipsWithoutLine(t *testing.T) {
	called := 0
	apps := []openInApp{{Name: "Cursor", AppPath: "/Applications/Cursor.app"}}
	withOpenLineStubs(t, "darwin", apps, func(string) error {
		called++
		return nil
	})

	if openPathAtLine(nil, "/tmp/a.go", 0, 0) {
		t.Fatal("没有行号时不应打开")
	}
	if called != 0 {
		t.Fatalf("没有行号时不应调用打开命令，实际 %d 次", called)
	}
}

func TestOpenPathAtLineWithoutEditor(t *testing.T) {
	apps := []openInApp{{Name: "Xcode", AppPath: "/Applications/Xcode.app"}}
	withOpenLineStubs(t, "darwin", apps, func(string) error {
		t.Fatal("不支持跳行的应用不应被调用")
		return nil
	})

	if openPathAtLine(nil, "/tmp/a.go", 5, 1) {
		t.Fatal("没有支持跳行的编辑器时应返回 false")
	}
}

func TestOpenPathAtLineNonDarwin(t *testing.T) {
	apps := []openInApp{{Name: "Cursor", AppPath: "/Applications/Cursor.app"}}
	withOpenLineStubs(t, "linux", apps, func(string) error {
		t.Fatal("非 darwin 平台不应调用打开命令")
		return nil
	})

	if openPathAtLine(nil, "/tmp/a.go", 5, 1) {
		t.Fatal("非 darwin 平台应返回 false")
	}
}
