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
	modes := make(map[string]os.FileMode, len(ops))
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
			info, err := os.Stat(t.path)
			if err != nil {
				return failure(err)
			}
			modes[t.path] = info.Mode()
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

	type undoStep struct {
		path string
		run  func() error
	}
	var undo []undoStep
	rollback := func(err error) (Result, error) {
		var failed []string
		for i := len(undo) - 1; i >= 0; i-- {
			if e := undo[i].run(); e != nil {
				failed = append(failed, undo[i].path)
			}
		}
		if len(failed) > 0 {
			return failure(fmt.Errorf("%w; rollback failed for: %s", err, strings.Join(failed, ", ")))
		}
		return failure(err)
	}
	mkdirAll := func(dir string) error {
		var missing []string
		for p := dir; ; p = filepath.Dir(p) {
			if _, err := os.Stat(p); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return err
			}
			missing = append(missing, p)
			if filepath.Dir(p) == p {
				break
			}
		}
		err := os.MkdirAll(dir, 0755)
		for i := len(missing) - 1; i >= 0; i-- {
			p := missing[i]
			if info, e := os.Stat(p); e == nil && info.IsDir() {
				undo = append(undo, undoStep{p, func() error { return os.Remove(p) }})
			}
		}
		return err
	}
	for i, r := range results {
		t := targets[i]
		restore := func() error { return writeFileAtomic(t.path, []byte(contents[t.op.Path]), modes[t.path]) }
		switch {
		case r.Added:
			if e := mkdirAll(filepath.Dir(t.path)); e != nil {
				return rollback(e)
			}
			if e := writeFileAtomic(t.path, []byte(r.Updated), 0644); e != nil {
				return rollback(e)
			}
			undo = append(undo, undoStep{t.path, func() error { return os.Remove(t.path) }})
		case r.Deleted:
			if e := os.Remove(t.path); e != nil {
				return rollback(e)
			}
			undo = append(undo, undoStep{t.path, restore})
		default:
			if t.move == "" && r.Updated == r.Original {
				continue
			}
			target := t.path
			if t.move != "" {
				target = t.move
			}
			if e := mkdirAll(filepath.Dir(target)); e != nil {
				return rollback(e)
			}
			if e := writeFileAtomic(target, []byte(r.Updated), modes[t.path]); e != nil {
				return rollback(e)
			}
			if t.move != "" {
				undo = append(undo, undoStep{target, func() error { return os.Remove(target) }})
				if e := os.Remove(t.path); e != nil {
					return rollback(e)
				}
				undo = append(undo, undoStep{t.path, restore})
			} else {
				undo = append(undo, undoStep{t.path, restore})
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

	changes := make([]FileChange, 0, len(results))
	for i, r := range results {
		p := targets[i].path
		if r.MoveTo != "" {
			changes = append(changes, FileChange{
				Path:  targets[i].move,
				Patch: unifiedDiff(p, r.Original, "") + unifiedDiff(targets[i].move, "", r.Updated),
			})
			continue
		}
		changes = append(changes, FileChange{Path: p, Patch: unifiedDiff(p, r.Original, r.Updated)})
	}
	return Result{Text: text.String(), Changes: changes}, nil
}
