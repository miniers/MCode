//go:build !unix

package mcp

import "mcode/internal/procutil"

var killProcessGroup = procutil.KillProcessGroup
