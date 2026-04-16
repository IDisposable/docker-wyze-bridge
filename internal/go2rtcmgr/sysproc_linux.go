//go:build linux

package go2rtcmgr

import (
	"os/exec"
	"syscall"
)

// configureProcess sets platform-specific process attributes on the
// go2rtc subprocess. On Linux we set Pdeathsig so the subprocess
// receives SIGTERM if the bridge (its parent) dies for any reason —
// including panic, kill -9, or terminal closing. Without this, orphan
// go2rtc processes can linger and hold ports 1984/8554/etc, which
// causes "address already in use" errors on the next startup.
func configureProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
	// Run in a new process group so Ctrl+C at the terminal goes to
	// the bridge, which then relays SIGTERM via Pdeathsig — this
	// prevents go2rtc from receiving SIGINT directly and exiting
	// before we've persisted state.
	cmd.SysProcAttr.Setpgid = true
}
