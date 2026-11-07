//go:build darwin

package startuptrace

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func processStartTime() time.Time {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil || kp == nil {
		return time.Now()
	}
	sec := kp.Proc.P_starttime.Sec
	usec := kp.Proc.P_starttime.Usec
	if sec == 0 && usec == 0 {
		return time.Now()
	}
	return time.Unix(sec, int64(usec)*1000)
}
