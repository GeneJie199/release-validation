//go:build windows

package guard

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func prepareManagedCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	command.WaitDelay = 5 * time.Second
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		killer := exec.Command("taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F")
		if output, err := killer.CombinedOutput(); err != nil {
			if killErr := command.Process.Kill(); killErr != nil {
				return fmt.Errorf("taskkill: %w: %s; kill: %v", err, output, killErr)
			}
		}
		return nil
	}
}
