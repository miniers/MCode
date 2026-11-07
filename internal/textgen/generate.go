// Package textgen 提供单次、不带工具调用的轻量文本生成能力，
// 用于会话标题、Git 提交信息以及分支名 slug 的生成。
package textgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// CheapestModel 返回指定 provider 最便宜的已知模型名称（依据 internal/remote/models.go 的静态列表）。
func CheapestModel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return "haiku"
	case "codex":
		return "gpt-5.4-mini"
	case "memory":
		return "echo"
	default:
		return ""
	}
}

// Options 描述一次一次性文本生成的参数。
// Effort 为推理强度（low、medium、high 等），为空时交给各引擎的默认值；
// 各引擎的传参方式不同：claude 用 --effort，pi 用 --thinking，codex 用 model_reasoning_effort。
type Options struct {
	Provider   string
	Model      string
	Effort     string
	BinaryPath string
	// ExtraEnv 是追加给 CLI 子进程的环境变量（KEY=VALUE）。
	ExtraEnv []string
	// ExtraArgs 是追加给 codex 的参数，用于 Magpie 的 -c 配置覆盖。
	ExtraArgs []string
	// Extensions 是 pi 要显式 -e 加载的扩展路径（如 Magpie 桥）。
	// --no-extensions 仍然保留：它只屏蔽自动发现与内置扩展，显式 -e 照常加载。
	Extensions []string
}

// Generate 单次、不带工具地调用模型生成文本。
// 超时 60 秒，输出去掉首尾空白。
// model 为空时，自动使用该 provider 最便宜的已知模型。
// binaryPath 为可选参数，未指定时分别使用 "claude"、"codex"、"pi"。
func Generate(ctx context.Context, provider, model, prompt string, binaryPath ...string) (string, error) {
	opts := Options{Provider: provider, Model: model}
	if len(binaryPath) > 0 {
		opts.BinaryPath = binaryPath[0]
	}
	return GenerateWithOptions(ctx, opts, prompt)
}

// GenerateWithOptions 与 Generate 相同，但可以透传推理强度等参数。
func GenerateWithOptions(ctx context.Context, opts Options, prompt string) (string, error) {
	prov := strings.ToLower(strings.TrimSpace(opts.Provider))
	if prov == "" {
		prov = "claude"
	}

	resolvedModel := strings.TrimSpace(opts.Model)
	if resolvedModel == "" {
		resolvedModel = CheapestModel(prov)
	}
	effort := strings.TrimSpace(opts.Effort)
	bin := opts.BinaryPath

	switch prov {
	case "claude":
		if bin == "" {
			bin = "claude"
		}
		return runClaude(ctx, bin, resolvedModel, effort, prompt, opts.ExtraEnv)
	case "codex":
		if bin == "" {
			bin = "codex"
		}
		return runCodex(ctx, bin, resolvedModel, effort, prompt, opts.ExtraArgs, opts.ExtraEnv)
	case "pi":
		if bin == "" {
			bin = "pi"
		}
		return runPi(ctx, bin, resolvedModel, effort, prompt, opts.ExtraEnv, opts.Extensions)
	case "memory":
		return runMemory(prompt), nil
	default:
		return "", fmt.Errorf("unsupported provider for textgen: %q", opts.Provider)
	}
}

// resolveCLI 在登录 Shell 的 PATH 中解析 CLI 的绝对路径。
// GUI 进程自己的 PATH 通常不含 ~/.local/bin、node 全局 bin 等目录；
// 解析失败时原样返回 bin，让 exec 照常给出 not found 错误。
func resolveCLI(bin string) string {
	if path, err := loginenv.LookPath(bin); err == nil && path != "" {
		return path
	}
	return bin
}

// cliEnv 返回 CLI 子进程使用的环境变量：登录 Shell 环境。
// 其中 PATH 已由 loginenv 按 augmentedPath 扩充，pi 等脚本的 #!/usr/bin/env node 才能找到 node。
func cliEnv() []string {
	return loginenv.Environ()
}

func runClaude(ctx context.Context, bin, model, effort, prompt string, extraEnv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	args := []string{
		"-p",
		"--output-format", "text",
		"--tools", "",
		"--disable-slash-commands",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--no-session-persistence",
		"--settings", `{"disableAllHooks":true}`,
	}
	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}

	bin = resolveCLI(bin)
	cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
	cmd.Env = append(cliEnv(), extraEnv...)
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errStr := strings.TrimSpace(stderr.String())
		if errStr != "" {
			return "", fmt.Errorf("claude textgen failed: %w (%s)", err, errStr)
		}
		return "", fmt.Errorf("claude textgen failed: %w", err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// runPi 以一次性打印模式调用 pi。extensions 逐个以 -e 追加在 --no-extensions 之后，
// extraEnv 追加到登录 Shell 环境之后（同名变量以后者为准）。
// --model 保持整串 "provider/id"：pi 的 resolveCliModel 本身支持这种写法，不必拆成 --provider。
func runPi(ctx context.Context, bin, model, effort, prompt string, extraEnv, extensions []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	args := []string{
		"-p",
		"--no-tools",
		"--no-session",
		"--no-extensions",
		"--no-skills",
	}
	for _, ext := range extensions {
		if ext = strings.TrimSpace(ext); ext != "" {
			args = append(args, "-e", ext)
		}
	}
	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--thinking", effort)
	}

	bin = resolveCLI(bin)
	cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
	cmd.Env = append(cliEnv(), extraEnv...)
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errStr := strings.TrimSpace(stderr.String())
		if errStr != "" {
			return "", fmt.Errorf("pi textgen failed: %w (%s)", err, errStr)
		}
		return "", fmt.Errorf("pi textgen failed: %w", err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

func runCodex(ctx context.Context, bin, model, effort, prompt string, extraArgs, extraEnv []string) (string, error) {
	tmpFile, err := os.CreateTemp("", "mcode-codex-textgen-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file for codex textgen: %w", err)
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpPath)

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	args := []string{
		"exec",
		"--ephemeral",
		"--skip-git-repo-check",
		"-s", "read-only",
	}
	args = append(args, extraArgs...)
	if model != "" {
		args = append(args, "--model", model)
	}
	// codex 的推理强度只能通过 config 注入；未指定时保持原来的 low。
	if effort == "" {
		effort = "low"
	}
	args = append(args, "--config", fmt.Sprintf("model_reasoning_effort=%q", effort), "--output-last-message", tmpPath, "-")

	bin = resolveCLI(bin)
	cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
	cmd.Env = append(cliEnv(), extraEnv...)
	cmd.Stdin = strings.NewReader(prompt)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errStr := strings.TrimSpace(stderr.String())
		if errStr != "" {
			return "", fmt.Errorf("codex textgen failed: %w (%s)", err, errStr)
		}
		return "", fmt.Errorf("codex textgen failed: %w", err)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", fmt.Errorf("read codex output file: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

func runMemory(prompt string) string {
	// memory provider 模拟回显，用于单元测试或模拟模式
	return "测试生成的标题或文本"
}

// OpenAICompatibleOptions 描述一次 OpenAI 兼容 chat/completions 调用的参数。
// Effort 非空时作为 reasoning_effort 写入请求体。
type OpenAICompatibleOptions struct {
	BaseURL string
	APIKey  string
	Model   string
	Effort  string
}

// chatCompletionsURL 规范化 chat/completions 地址：
// BaseURL 已经指向 /chat/completions 时直接使用，避免重复拼接路径。
func chatCompletionsURL(baseURL string) string {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(u, "/chat/completions") {
		return u
	}
	return u + "/chat/completions"
}

// GenerateOpenAICompatible 以非流式方式调用 OpenAI 兼容的 chat/completions 接口。
// 超时 30 秒；非 2xx 时错误仅含状态码，避免把响应体中的凭证带入日志。
func GenerateOpenAICompatible(ctx context.Context, baseURL, apiKey, model, prompt string) (string, error) {
	return GenerateOpenAICompatibleWithOptions(ctx, OpenAICompatibleOptions{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	}, prompt)
}

// GenerateOpenAICompatibleWithOptions 与 GenerateOpenAICompatible 相同，并支持透传 reasoning effort。
func GenerateOpenAICompatibleWithOptions(ctx context.Context, opts OpenAICompatibleOptions, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	payload := map[string]any{
		"model":       opts.Model,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"stream":      false,
		"temperature": 0.2,
	}
	if effort := strings.TrimSpace(opts.Effort); effort != "" {
		payload["reasoning_effort"] = effort
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	url := chatCompletionsURL(opts.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("openai-compatible textgen: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+opts.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai-compatible textgen: request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("openai-compatible textgen: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("openai-compatible textgen: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("openai-compatible textgen: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("openai-compatible textgen: empty choices")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// BranchPrefix 是分支名前缀。
const BranchPrefix = "mcode/"

var asciiWordRe = regexp.MustCompile(`[A-Za-z0-9]+`)

// LocalBranchSlug 对 taskText 取 ASCII 单词，转 kebab-case，取前 5 个词，限 40 字符。
// 没有可用单词时返回空串。
func LocalBranchSlug(taskText string) string {
	words := asciiWordRe.FindAllString(strings.ToLower(taskText), -1)
	if len(words) > 5 {
		words = words[:5]
	}
	slug := strings.Join(words, "-")
	if len(slug) > 40 {
		// 按单词边界截断，只保留完整单词；首词本身超长时才硬截到 40。
		cut := strings.LastIndex(slug[:41], "-")
		if cut <= 0 {
			cut = 40
		}
		slug = strings.Trim(slug[:cut], "-")
	}
	return slug
}

// BranchName 生成带 mcode/ 前缀的分支名：先走模型，失败或结果为空时降级为本地 slug。
// 两者都为空时使用 "task"。CleanBranchName 本身不加前缀，这里统一补上。
func BranchName(ctx context.Context, provider, model, taskText string, binaryPath ...string) string {
	slug := ""
	if out, err := Generate(ctx, provider, model, BranchNamePrompt(taskText), binaryPath...); err == nil {
		slug = CleanBranchName(out)
	}
	if slug == "" {
		slug = LocalBranchSlug(taskText)
	}
	if slug == "" {
		slug = "task"
	}
	return BranchPrefix + slug
}

// CommitMessage 根据 diff 生成提交信息；patch 截断到 12000 字节（按 UTF-8 字符边界）。
func CommitMessage(ctx context.Context, provider, model, diffStat, diffPatch, instructions string, binaryPath ...string) (string, error) {
	opts := Options{Provider: provider, Model: model}
	if len(binaryPath) > 0 {
		opts.BinaryPath = binaryPath[0]
	}
	return CommitMessageWithOptions(ctx, opts, diffStat, diffPatch, instructions)
}

// CommitMessageWithOptions 与 CommitMessage 相同，但可以透传额外环境变量、扩展等参数。
func CommitMessageWithOptions(ctx context.Context, opts Options, diffStat, diffPatch, instructions string) (string, error) {
	diffPatch = truncateUTF8(diffPatch, 12000)
	out, err := GenerateWithOptions(ctx, opts, CommitMessagePrompt(diffStat, diffPatch, instructions))
	if err != nil {
		return "", err
	}
	msg := CleanCommitMessage(out)
	if msg == "" {
		return "", fmt.Errorf("textgen: empty commit message")
	}
	return msg, nil
}
