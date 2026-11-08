package patch

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func readFiles(files map[string]string) func(string) (string, error) {
	return func(path string) (string, error) {
		content, ok := files[path]
		if !ok {
			return "", fmt.Errorf("file not found: %s", path)
		}
		return content, nil
	}
}

func TestParseAndApply(t *testing.T) {
	text := `*** Begin Patch
*** Add File: new.txt
+hello
*** Delete File: old.txt
*** Update File: a.txt
@@
-old
+new
*** Update File: b.txt
*** Move to: c.txt
@@
-x
+y
*** End Patch`
	ops, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 4 {
		t.Fatalf("got %d operations", len(ops))
	}
	if ops[0].Kind != Add || ops[0].Path != "new.txt" || ops[0].Content != "hello\n" {
		t.Fatalf("add: %+v", ops[0])
	}
	if ops[1].Kind != Delete || ops[1].Path != "old.txt" {
		t.Fatalf("delete: %+v", ops[1])
	}
	if ops[2].Kind != Update || len(ops[2].Hunks) != 1 {
		t.Fatalf("update: %+v", ops[2])
	}
	if ops[3].MoveTo != "c.txt" || ops[3].Path != "b.txt" {
		t.Fatalf("move: %+v", ops[3])
	}

	results, err := Apply(ops, readFiles(map[string]string{
		"old.txt": "bye\n",
		"a.txt":   "old\n",
		"b.txt":   "x\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d results", len(results))
	}
	if !results[0].Added || results[0].Updated != "hello\n" || results[0].Original != "" {
		t.Fatalf("add result: %+v", results[0])
	}
	if !results[1].Deleted || results[1].Original != "bye\n" || results[1].Updated != "" {
		t.Fatalf("delete result: %+v", results[1])
	}
	if results[2].Updated != "new\n" || results[2].Original != "old\n" {
		t.Fatalf("update result: %+v", results[2])
	}
	if results[3].Updated != "y\n" || results[3].MoveTo != "c.txt" {
		t.Fatalf("move result: %+v", results[3])
	}

	paths, err := Paths(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new.txt", "old.txt", "a.txt", "b.txt", "c.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestApplyMultipleHunksAndLocator(t *testing.T) {
	original := "package main\n\nfunc alpha() {\n\toldAlpha()\n}\n\nfunc beta() {\n\toldBeta()\n}\n"
	text := `*** Begin Patch
*** Update File: main.go
@@ func alpha() {
 	oldAlpha()
-}
+	// alpha end
+}
@@ func beta() {
 	oldBeta()
-}
+	// beta end
+}
*** End of File
*** End Patch`
	ops, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Apply(ops, readFiles(map[string]string{"main.go": original}))
	if err != nil {
		t.Fatal(err)
	}
	want := "package main\n\nfunc alpha() {\n\toldAlpha()\n\t// alpha end\n}\n\nfunc beta() {\n\toldBeta()\n\t// beta end\n}\n"
	if results[0].Updated != want {
		t.Fatalf("updated:\n%q\nwant:\n%q", results[0].Updated, want)
	}
}

func TestApplyEndOfFileWithoutNewline(t *testing.T) {
	text := `*** Begin Patch
*** Update File: f
@@
 b
+c
*** End of File
*** End Patch`
	ops, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Apply(ops, readFiles(map[string]string{"f": "a\nb"}))
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Updated != "a\nb\nc\n" {
		t.Fatalf("updated = %q", results[0].Updated)
	}
}

func TestApplyWhitespaceTolerance(t *testing.T) {
	ops, err := Parse(`*** Begin Patch
*** Update File: f
@@
 first
-second
+SECOND
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Apply(ops, readFiles(map[string]string{"f": "first   \nsecond\t\nthird\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Updated != "first   \nSECOND\nthird\n" {
		t.Fatalf("updated = %q", results[0].Updated)
	}

	ops, err = Parse(`*** Begin Patch
*** Update File: f
@@
-  indented
+flat
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	results, err = Apply(ops, readFiles(map[string]string{"f": "    indented  \nkeep\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Updated != "flat\nkeep\n" {
		t.Fatalf("updated = %q", results[0].Updated)
	}
}

func TestParseHeredocAndWhitespace(t *testing.T) {
	text := "\n\napply_patch <<'EOF'\n*** Begin Patch\n*** Add File: x\n+x\n*** End Patch\nEOF\n\n"
	ops, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Content != "x\n" {
		t.Fatalf("ops = %+v", ops)
	}

	ops, err = Parse("\n*** Begin Patch\n*** Add File: y\n+y\n*** End Patch\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Path != "y" {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestApplyErrors(t *testing.T) {
	ops, err := Parse(`*** Begin Patch
*** Update File: file.txt
@@
 missing line
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ops, readFiles(map[string]string{"file.txt": "other\n"}))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "file.txt") || !strings.Contains(err.Error(), "missing line") {
		t.Fatalf("error = %v", err)
	}

	ops, err = Parse(`*** Begin Patch
*** Update File: f
@@ no such locator
 x
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ops, readFiles(map[string]string{"f": "x\n"}))
	if err == nil || !strings.Contains(err.Error(), "locator") {
		t.Fatalf("error = %v", err)
	}

	ops, err = Parse(`*** Begin Patch
*** Update File: f
@@
 a
*** End of File
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ops, readFiles(map[string]string{"f": "a\nb\n"}))
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseSyntaxErrors(t *testing.T) {
	_, err := Parse("hello\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("error = %v", err)
	}

	_, err = Parse("*** Begin Patch\n*** Add File: a\nnope\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("error = %v", err)
	}

	_, err = Parse("*** Begin Patch\n*** Update File: a\n@@\n x\n")
	if err == nil || !strings.Contains(err.Error(), "End Patch") {
		t.Fatalf("error = %v", err)
	}
}
