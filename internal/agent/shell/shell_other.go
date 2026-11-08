//go:build !unix && !windows

package shell

import "os/exec"

func shellCommand(command string) (string, []string) { return "sh", []string{"-c", command} }
func setProcessGroup(cmd *exec.Cmd) {}
func stopProcess(cmd *exec.Cmd, done <-chan error) error {
	_ = cmd.Process.Kill()
	return <-done
}
