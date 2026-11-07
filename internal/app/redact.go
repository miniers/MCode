package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"mcode/internal/remote"
)

// Redact 掩码文本中的凭证；规则实现在 remote.RedactSecrets，避免两份正则。
func Redact(s string) string {
	return remote.RedactSecrets(s)
}

// redactWriter 在写入前对日志内容脱敏，供 log.SetOutput 使用。
type redactWriter struct{ w io.Writer }

func (r redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, Redact(string(p))); err != nil {
		return 0, err
	}
	// 调用方只关心原始字节是否都已处理。
	return len(p), nil
}

var ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// ExportDiagnostics 把日志文件合并写入 dst：逐行脱敏凭证，用户 HOME 路径替换为 ~，IPv4 地址替换为 x.x.x.x。
// 不存在的日志文件会被跳过。
func ExportDiagnostics(dst string, logPaths []string) error {
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create diagnostics file: %w", err)
	}
	defer out.Close()

	home, _ := os.UserHomeDir()
	w := bufio.NewWriter(out)
	for _, path := range logPaths {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("open log %s: %w", path, err)
		}
		fmt.Fprintf(w, "===== %s =====\n", sanitizeDiagLine(path, home))
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			fmt.Fprintln(w, sanitizeDiagLine(sc.Text(), home))
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return fmt.Errorf("read log %s: %w", path, err)
		}
	}
	return w.Flush()
}

func sanitizeDiagLine(line, home string) string {
	line = Redact(line)
	if home != "" && home != "/" {
		line = strings.ReplaceAll(line, home, "~")
	}
	return ipv4Re.ReplaceAllString(line, "x.x.x.x")
}
