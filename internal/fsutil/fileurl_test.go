package fsutil

import "testing"

func TestFileURL(t *testing.T) {
	for _, tc := range []struct{ goos, path, want string }{
		{"windows", `C:\a b\文件.txt`, "file:///C:/a%20b/%E6%96%87%E4%BB%B6.txt"},
		{"linux", "/home/a b", "file:///home/a%20b"},
	} {
		if got := fileURL(tc.path, tc.goos).String(); got != tc.want {
			t.Errorf("%s: %q != %q", tc.goos, got, tc.want)
		}
	}
}

func TestPathFromFileURL(t *testing.T) {
	for _, tc := range []struct {
		goos, raw, want string
		ok              bool
	}{
		{"windows", "file:///C:/a%20b", "C:/a b", true},
		{"windows", "file://localhost/C:/a", "C:/a", true},
		{"linux", "file:///home/a%20b", "/home/a b", true},
		{"linux", "file://example.com/home/a", "", false},
		{"linux", "file:///bad%zz", "", false},
	} {
		got, ok := pathFromFileURL(tc.raw, tc.goos)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %q, %v", tc.raw, got, ok)
		}
	}
}
