//go:build !linux

package go2rtcmgr

import "os/exec"

// configureProcess is a no-op on non-Linux platforms. Pdeathsig is a
// Linux-specific feature; on Windows/macOS the subprocess lifecycle
// relies on exec.CommandContext's Kill-on-cancel behavior, which is
// less reliable if the parent dies abruptly.
func configureProcess(cmd *exec.Cmd) {
	_ = cmd
}
