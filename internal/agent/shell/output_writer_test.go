package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputWriterKeepsLastLines(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &outputWriter{file: f}
	want := strings.Repeat("new\n", maxOutputLines)
	input := strings.Repeat("old\n", 10) + want
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if got := string(w.tail); got != want {
		t.Errorf("保留的输出行不符合预期: %d 行", lineCount(w.tail))
	}
}
