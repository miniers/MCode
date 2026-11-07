package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// DroidModel 是 `droid.list_models` 返回的一个可选模型。
type DroidModel struct {
	ID            string
	Name          string
	Provider      string
	Efforts       []string
	DefaultEffort string
	Default       bool // 路由模型 auto 为默认
}

// droidListModelsRequest 是 Factory 原生协议的取模型请求：
// 除 JSON-RPC 外还要带 factoryApiVersion / factoryProtocolVersion 信封。
const droidListModelsRequest = `{"jsonrpc":"2.0","type":"request","id":"models","factoryApiVersion":"1.0.0","factoryProtocolVersion":"1.221.0","method":"droid.list_models","params":{"includeDisabled":false}}`

// ListDroidModels 用不建会话的 `droid exec` 取账号可用的模型目录：
// 它不触发 ACP 的设备配对，并带出每个模型的推理强度刻度。binary 为空时用默认可执行文件名。
func ListDroidModels(ctx context.Context, binary string) ([]DroidModel, error) {
	if binary == "" {
		binary = DefaultBinary(AgentDroid)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := procattr.HideWindow(exec.CommandContext(ctx, binary, "exec", "--input-format", "stream-jsonrpc", "--output-format", "stream-jsonrpc"))
	cmd.Dir = os.TempDir()
	cmd.Env = loginenv.Environ()
	cmd.Stdin = strings.NewReader(droidListModelsRequest + "\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return nil, fmt.Errorf("droid models: %w", err)
	}
	models := parseDroidModels(stdout.Bytes())
	if len(models) == 0 {
		return nil, fmt.Errorf("droid models: 没有返回可用模型")
	}
	return models, nil
}

// parseDroidModels 逐行找 id 为 models 的应答，丢弃禁用与过期的模型。
func parseDroidModels(output []byte) []DroidModel {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var resp struct {
			ID     string `json:"id"`
			Result struct {
				Models []struct {
					ID          string   `json:"id"`
					DisplayName string   `json:"displayName"`
					Provider    string   `json:"modelProvider"`
					Efforts     []string `json:"supportedReasoningEfforts"`
					DefaultEff  string   `json:"defaultReasoningEffort"`
					Disabled    bool     `json:"disabled"`
					Deprecated  bool     `json:"deprecated"`
				} `json:"models"`
			} `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &resp) != nil || resp.ID != "models" {
			continue
		}
		var out []DroidModel
		for _, m := range resp.Result.Models {
			if m.ID == "" || m.Disabled || m.Deprecated {
				continue
			}
			name := strings.TrimSpace(m.DisplayName)
			if name == "" {
				name = m.ID
			}
			efforts := orderedEfforts(m.Efforts)
			// 只有 none / off 一档等于没有可调强度（路由模型 auto 就是这样）。
			if len(efforts) == 1 && (efforts[0] == "none" || efforts[0] == "off") {
				efforts = nil
			}
			out = append(out, DroidModel{
				ID: m.ID, Name: name, Provider: m.Provider,
				Efforts: efforts, DefaultEffort: m.DefaultEff, Default: m.ID == "auto",
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// effortRanks 是各渠道推理档位由弱到强的次序。
var effortRanks = map[string]int{
	"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4,
	"xhigh": 5, "extra-high": 5, "extra_high": 5, "extra high": 5, "max": 6, "ultra": 6,
}

// orderedEfforts 把档位按强弱升序排列；认识的档位不足两个时保持原序，不认识的档位留在原位附近。
func orderedEfforts(values []string) []string {
	known := 0
	for _, v := range values {
		if _, ok := effortRanks[strings.ToLower(v)]; ok {
			known++
		}
	}
	out := append([]string(nil), values...)
	if known < 2 {
		return out
	}
	idx := make(map[string]int, len(values))
	for i, v := range values {
		idx[v] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		l, lok := effortRanks[strings.ToLower(out[i])]
		r, rok := effortRanks[strings.ToLower(out[j])]
		if lok && rok && l != r {
			return l < r
		}
		return idx[out[i]] < idx[out[j]]
	})
	return out
}
