package patch

import (
	"strings"
	"testing"
)

func TestAmbiguousWhitespaceHunk(t *testing.T) {
	ops := []Op{{Kind: Update, Path: "x", Hunks: []Hunk{{Lines: []HunkLine{{Kind: '-', Text: "  repeated"}, {Kind: '+', Text: "changed"}}}}}}
	_, err := Apply(ops, func(string) (string, error) { return "    repeated\n\trepeated\n", nil })
	if err == nil || !strings.Contains(err.Error(), "ambiguous hunk: context matches multiple locations when ignoring whitespace") { t.Fatalf("error: %v", err) }
}

func TestPatchPreservesBOM(t *testing.T) {
	ops := []Op{{Kind: Update, Path: "x", Hunks: []Hunk{{Lines: []HunkLine{{Kind: '-', Text: "old"}, {Kind: '+', Text: "new"}}}}}}
	results, err := Apply(ops, func(string) (string, error) { return "\xef\xbb\xbfold\n", nil })
	if err != nil || len(results) != 1 || results[0].Updated != "\xef\xbb\xbfnew\n" { t.Fatalf("results: %+v %v", results, err) }
}
