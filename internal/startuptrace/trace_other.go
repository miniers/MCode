//go:build !darwin

package startuptrace

import "time"

func processStartTime() time.Time {
	return time.Now()
}
