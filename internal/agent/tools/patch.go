package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/agent/patch"
)

// ApplyPatchTool returns the Codex-style patch tool.
func ApplyPatchTool() Tool {
	return Tool{
		Name: "apply_patch",
		Description: "Apply a patch between *** Begin Patch and *** End Patch.\n" +
			"*** Add File: path then + lines; *** Delete File: path.\n" +
			"*** Update File: path, optional *** Move to: path, then hunks.\n" +
			"Hunk lines: space context, - remove, + add.\n" +
			"A hunk starts with @@ and optional locator text.\n" +
			"*** End of File anchors a hunk at the end of the file.\n" +
			"Example:\n" +
			"*** Begin Patch\n" +
			"*** Update File: a.txt\n" +
			"@@ / -old / +new / *** End Patch",
		Parameters: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`),
		ReadOnly:   false,
		Run:        applyPatch,
	}
}

type patchTarget struct {
	op   patch.Op
	path string
	move string
}

func applyPatch(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		Input string `json:"input"`
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	if strings.TrimSpace(a.Input) == "" {
		return failure("input is required")
	}
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	ops, e := patch.Parse(a.Input)
	if e != nil {
		return failure(e)
	}

	targets := make([]patchTarget, 0, len(ops))
	seen := make(map[string]bool, len(ops)*2)
	for _, op := range ops {
		p, e := resolve(env, op.Path)
		if e != nil {
			return failure(e)
		}
		if seen[p] {
			return failure(fmt.Sprintf("duplicate path: %s", op.Path))
		}
		seen[p] = true
		t := patchTarget{op: op, path: p}
		if op.MoveTo != "" {
			m, e := resolve(env, op.MoveTo)
			if e != nil {
				return failure(e)
			}
			if seen[m] {
				return failure(fmt.Sprintf("duplicate path: %s", op.MoveTo))
			}
			seen[m] = true
			t.move = m
		}
		targets = append(targets, t)
	}

	contents := make(map[string]string, len(ops))
	for _, t := range targets {
		switch t.op.Kind {
		case patch.Add:
			if _, err := os.Stat(t.path); err == nil {
				return failure(fmt.Sprintf("%s: file already exists", t.op.Path))
			} else if !os.IsNotExist(err) {
				return failure(err)
			}
		default:
			data, err := os.ReadFile(t.path)
			if err != nil {
				return failure(err)
			}
			contents[t.op.Path] = string(data)
		}
		if t.move != "" {
			if _, err := os.Stat(t.move); err == nil {
				return failure(fmt.Sprintf("%s: file already exists", t.op.MoveTo))
			} else if !os.IsNotExist(err) {
				return failure(err)
			}
		}
	}

	read := func(name string) (string, error) {
		s, ok := contents[name]
		if !ok {
			return "", fmt.Errorf("file not found: %s", name)
		}
		return s, nil
	}
	results, err := patch.Apply(ops, read)
	if err != nil {
		return failure(err)
	}

	for i, r := range results {
		t := targets[i]
		switch {
		case r.Added:
			if e := os.MkdirAll(filepath.Dir(t.path), 0755); e != nil {
				return failure(e)
			}
			if e := os.WriteFile(t.path, []byte(r.Updated), 0644); e != nil {
				return failure(e)
			}
		case r.Deleted:
			if e := os.Remove(t.path); e != nil {
				return failure(e)
			}
		default:
			if t.move == "" && r.Updated == r.Original {
				continue
			}
			info, err := os.Stat(t.path)
			if err != nil {
				return failure(err)
			}
			target := t.path
			if t.move != "" {
				target = t.move
			}
			if e := os.MkdirAll(filepath.Dir(target), 0755); e != nil {
				return failure(e)
			}
			if e := os.WriteFile(target, []byte(r.Updated), info.Mode()); e != nil {
				return failure(e)
			}
			if t.move != "" {
				if e := os.Remove(t.path); e != nil {
					return failure(e)
				}
			}
		}
	}

	var added, updated, deleted []string
	for i, r := range results {
		name := targets[i].op.Path
		switch {
		case r.Added:
			added = append(added, name)
		case r.Deleted:
			deleted = append(deleted, name)
		default:
			if r.MoveTo != "" {
				name += " -> " + r.MoveTo
			}
			updated = append(updated, name)
		}
	}
	var text strings.Builder
	text.WriteString("Success.")
	for _, group := range []struct {
		label string
		items []string
	}{{"Added", added}, {"Updated", updated}, {"Deleted", deleted}} {
		if len(group.items) > 0 {
			fmt.Fprintf(&text, "\n%s: %s", group.label, strings.Join(group.items, ", "))
		}
	}

	var diffs strings.Builder
	for i, r := range results {
		p := targets[i].path
		if r.MoveTo != "" {
			diffs.WriteString(unifiedDiff(p, r.Original, ""))
			diffs.WriteString(unifiedDiff(targets[i].move, "", r.Updated))
			continue
		}
		diffs.WriteString(unifiedDiff(p, r.Original, r.Updated))
	}
	return Result{
		Text:   text.String(),
		Change: &FileChange{Path: targets[0].path, Patch: diffs.String()},
	}, nil
}
