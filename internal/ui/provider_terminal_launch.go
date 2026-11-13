package mcodeui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"mcode/internal/procattr"
)

var providerTerminalGOOS = runtime.GOOS

func providerInstallScript(goos, cmd string) string {
	if goos != "windows" {
		return "#!/bin/zsh -l\n" + cmd + "\n"
	}
	if strings.HasPrefix(strings.TrimSpace(cmd), "npm ") || strings.TrimSpace(cmd) == "npm" {
		return `if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
    winget install -e --id OpenJS.NodeJS.LTS --accept-source-agreements --accept-package-agreements
    $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
        Write-Host '需要先安装 Node.js：https://nodejs.org'
        exit 1
    }
}
` + cmd + "\n"
	}
	return cmd + "\n"
}

func launchProviderTerminalAction(st *SettingsState, id, action string) {
	cmd, ok := providerTerminalCommand(st, id, action)
	if !ok {
		return
	}

	// 失败写回行的更新提示：否则点「在终端登录/安装/更新」没反应也没有任何解释。
	fail := func(err error) {
		st.mu.Lock()
		if st.providerRows.updateMsg == nil {
			st.providerRows.updateMsg = make(map[string]string)
		}
		st.providerRows.updateMsg[id] = "打开终端失败：" + err.Error()
		st.mu.Unlock()
		st.invalidate()
	}

	go func() {
		if err := openTerminalScript(fmt.Sprintf("%s-%s", action, id), cmd); err != nil {
			fail(err)
		}
	}()
}

func openTerminalScript(name, cmd string) error {
	dir := filepath.Join(os.TempDir(), "mcode-login")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ext := ".command"
	if runtime.GOOS == "windows" {
		ext = ".ps1"
	}
	scriptPath := filepath.Join(dir, name+ext)
	content := providerInstallScript(runtime.GOOS, cmd)
	if runtime.GOOS == "windows" {
		content = "\ufeff" + content
	}
	if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return procattr.HideWindow(exec.Command("cmd", "/c", "start", "", "powershell.exe", "-NoExit", "-ExecutionPolicy", "Bypass", "-File", scriptPath)).Run()
	case "darwin":
		return exec.Command("open", scriptPath).Run()
	default:
		return mygo.Shell.OpenPath(scriptPath)
	}
}
