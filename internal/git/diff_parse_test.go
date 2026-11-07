package git

import (
	"reflect"
	"testing"
)

func TestParseUnifiedDiff(t *testing.T) {
	tests := []struct {
		name    string
		diff    string
		want    []FileDiff
		wantErr bool
	}{
		{
			name: "普通单 hunk 修改",
			diff: `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
-import "fmt"
+import "log"
+import "os"
 func main() {}
`,
			want: []FileDiff{
				{
					OldPath: "main.go",
					NewPath: "main.go",
					Status:  "modified",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 1,
							OldLines: 3,
							NewStart: 1,
							NewLines: 4,
							Header:   "@@ -1,3 +1,4 @@",
							Lines: []Line{
								{Kind: ' ', Text: "package main", OldNo: 1, NewNo: 1},
								{Kind: '-', Text: "import \"fmt\"", OldNo: 2, NewNo: 0},
								{Kind: '+', Text: "import \"log\"", OldNo: 0, NewNo: 2},
								{Kind: '+', Text: "import \"os\"", OldNo: 0, NewNo: 3},
								{Kind: ' ', Text: "func main() {}", OldNo: 3, NewNo: 4},
							},
						},
					},
				},
			},
		},
		{
			name: "多 hunk 修改",
			diff: `diff --git a/calc.go b/calc.go
--- a/calc.go
+++ b/calc.go
@@ -10,3 +10,3 @@
 func Add(a, b int) int {
-	return a - b
+	return a + b
 }
@@ -50,3 +50,3 @@
 func Mul(a, b int) int {
-	return 0
+	return a * b
 }
`,
			want: []FileDiff{
				{
					OldPath: "calc.go",
					NewPath: "calc.go",
					Status:  "modified",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 10,
							OldLines: 3,
							NewStart: 10,
							NewLines: 3,
							Header:   "@@ -10,3 +10,3 @@",
							Lines: []Line{
								{Kind: ' ', Text: "func Add(a, b int) int {", OldNo: 10, NewNo: 10},
								{Kind: '-', Text: "\treturn a - b", OldNo: 11, NewNo: 0},
								{Kind: '+', Text: "\treturn a + b", OldNo: 0, NewNo: 11},
								{Kind: ' ', Text: "}", OldNo: 12, NewNo: 12},
							},
						},
						{
							OldStart: 50,
							OldLines: 3,
							NewStart: 50,
							NewLines: 3,
							Header:   "@@ -50,3 +50,3 @@",
							Lines: []Line{
								{Kind: ' ', Text: "func Mul(a, b int) int {", OldNo: 50, NewNo: 50},
								{Kind: '-', Text: "\treturn 0", OldNo: 51, NewNo: 0},
								{Kind: '+', Text: "\treturn a * b", OldNo: 0, NewNo: 51},
								{Kind: ' ', Text: "}", OldNo: 52, NewNo: 52},
							},
						},
					},
				},
			},
		},
		{
			name: "新增文件包含 new file mode",
			diff: `diff --git a/newfile.txt b/newfile.txt
new file mode 100644
index 0000000..abcdef1
--- /dev/null
+++ b/newfile.txt
@@ -0,0 +1,2 @@
+hello
+world
`,
			want: []FileDiff{
				{
					OldPath: "",
					NewPath: "newfile.txt",
					Status:  "added",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 0,
							OldLines: 0,
							NewStart: 1,
							NewLines: 2,
							Header:   "@@ -0,0 +1,2 @@",
							Lines: []Line{
								{Kind: '+', Text: "hello", OldNo: 0, NewNo: 1},
								{Kind: '+', Text: "world", OldNo: 0, NewNo: 2},
							},
						},
					},
				},
			},
		},
		{
			name: "删除文件包含 deleted file mode",
			diff: `diff --git a/del.txt b/del.txt
deleted file mode 100644
index abcdef1..0000000
--- a/del.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-line1
-line2
`,
			want: []FileDiff{
				{
					OldPath: "del.txt",
					NewPath: "",
					Status:  "deleted",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 1,
							OldLines: 2,
							NewStart: 0,
							NewLines: 0,
							Header:   "@@ -1,2 +0,0 @@",
							Lines: []Line{
								{Kind: '-', Text: "line1", OldNo: 1, NewNo: 0},
								{Kind: '-', Text: "line2", OldNo: 2, NewNo: 0},
							},
						},
					},
				},
			},
		},
		{
			name: "纯重命名无内容修改",
			diff: `diff --git a/old_name.txt b/new_name.txt
similarity index 100%
rename from old_name.txt
rename to new_name.txt
`,
			want: []FileDiff{
				{
					OldPath: "old_name.txt",
					NewPath: "new_name.txt",
					Status:  "renamed",
					Binary:  false,
					Hunks:   []Hunk{},
				},
			},
		},
		{
			name: "重命名并修改内容",
			diff: `diff --git a/source.txt b/dest.txt
similarity index 85%
rename from source.txt
rename to dest.txt
index 1111111..2222222 100644
--- a/source.txt
+++ b/dest.txt
@@ -1,2 +1,2 @@
 foo
-bar
+baz
`,
			want: []FileDiff{
				{
					OldPath: "source.txt",
					NewPath: "dest.txt",
					Status:  "renamed",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 1,
							OldLines: 2,
							NewStart: 1,
							NewLines: 2,
							Header:   "@@ -1,2 +1,2 @@",
							Lines: []Line{
								{Kind: ' ', Text: "foo", OldNo: 1, NewNo: 1},
								{Kind: '-', Text: "bar", OldNo: 2, NewNo: 0},
								{Kind: '+', Text: "baz", OldNo: 0, NewNo: 2},
							},
						},
					},
				},
			},
		},
		{
			name: "二进制文件 diff",
			diff: `diff --git a/avatar.png b/avatar.png
index 1111111..2222222 100644
Binary files a/avatar.png and b/avatar.png differ
`,
			want: []FileDiff{
				{
					OldPath: "avatar.png",
					NewPath: "avatar.png",
					Status:  "modified",
					Binary:  true,
					Hunks:   []Hunk{},
				},
			},
		},
		{
			name: "带引号与空格的路径解析",
			diff: `diff --git "a/path with space/file name.txt" "b/path with space/file name.txt"
index 1111111..2222222 100644
--- "a/path with space/file name.txt"
+++ "b/path with space/file name.txt"
@@ -1,1 +1,1 @@
-old space
+new space
`,
			want: []FileDiff{
				{
					OldPath: "path with space/file name.txt",
					NewPath: "path with space/file name.txt",
					Status:  "modified",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 1,
							OldLines: 1,
							NewStart: 1,
							NewLines: 1,
							Header:   "@@ -1,1 +1,1 @@",
							Lines: []Line{
								{Kind: '-', Text: "old space", OldNo: 1, NewNo: 0},
								{Kind: '+', Text: "new space", OldNo: 0, NewNo: 1},
							},
						},
					},
				},
			},
		},
		{
			name: "Hunk 头部省略行数（@@ -1 +1 @@）",
			diff: `diff --git a/single.txt b/single.txt
--- a/single.txt
+++ b/single.txt
@@ -1 +1 @@
-line1
+line1_modified
`,
			want: []FileDiff{
				{
					OldPath: "single.txt",
					NewPath: "single.txt",
					Status:  "modified",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 1,
							OldLines: 1,
							NewStart: 1,
							NewLines: 1,
							Header:   "@@ -1 +1 @@",
							Lines: []Line{
								{Kind: '-', Text: "line1", OldNo: 1, NewNo: 0},
								{Kind: '+', Text: "line1_modified", OldNo: 0, NewNo: 1},
							},
						},
					},
				},
			},
		},
		{
			name: "处理 \\ No newline at end of file",
			diff: `diff --git a/noeol.txt b/noeol.txt
--- a/noeol.txt
+++ b/noeol.txt
@@ -1,2 +1,2 @@
 hello
-world
\ No newline at end of file
+world!
\ No newline at end of file
`,
			want: []FileDiff{
				{
					OldPath: "noeol.txt",
					NewPath: "noeol.txt",
					Status:  "modified",
					Binary:  false,
					Hunks: []Hunk{
						{
							OldStart: 1,
							OldLines: 2,
							NewStart: 1,
							NewLines: 2,
							Header:   "@@ -1,2 +1,2 @@",
							Lines: []Line{
								{Kind: ' ', Text: "hello", OldNo: 1, NewNo: 1},
								{Kind: '-', Text: "world", OldNo: 2, NewNo: 0},
								{Kind: '+', Text: "world!", OldNo: 0, NewNo: 2},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUnifiedDiff([]byte(tt.diff))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseUnifiedDiff() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseUnifiedDiff() mismatch:\ngot  = %#v\nwant = %#v", got, tt.want)
			}
		})
	}
}
