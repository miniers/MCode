package omp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// Model 是 omp 目录里的一个可选模型。
type Model struct {
	// ID 是 <provider>/<model id> 形式的完整 id。
	ID       string
	Name     string
	Provider string
	// Efforts 是模型自带的推理档位，不会推理的模型为空。
	Efforts       []string
	DefaultEffort string
}

// ListModels 运行 `omp models --json --no-extensions` 取当前可用的模型目录：
// 它不需要会话；目录为空表示 omp 里没有登录任何供应商。binary 为空时用默认可执行文件名。
func ListModels(ctx context.Context, binary string) ([]Model, error) {
	if binary == "" {
		binary = DefaultBinary
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := procattr.HideWindow(exec.CommandContext(ctx, binary, "models", "--json", "--no-extensions"))
	cmd.Dir = loginenv.HomeDirectory()
	cmd.Env = loginenv.Environ()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("omp models: %w", err)
	}
	models, err := ParseModels(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("omp models: 没有列出任何模型")
	}
	return models, nil
}

// ParseModels 解析 `omp models --json` 的输出，按 provider、名字排序。
func ParseModels(raw []byte) ([]Model, error) {
	var doc struct {
		Models []struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Provider string   `json:"provider"`
			Thinking []string `json:"thinking"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("omp models: 解析输出: %w", err)
	}
	out := make([]Model, 0, len(doc.Models))
	for _, m := range doc.Models {
		if m.ID == "" || m.Provider == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		def := ""
		for _, e := range m.Thinking {
			if e == "medium" {
				def = e
				break
			}
		}
		if def == "" && len(m.Thinking) > 0 {
			def = m.Thinking[0]
		}
		out = append(out, Model{
			ID: m.Provider + "/" + m.ID, Name: name, Provider: m.Provider,
			Efforts: m.Thinking, DefaultEffort: def,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
