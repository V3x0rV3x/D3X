//go:build !windows

package desktop

import (
	"os/exec"
	"runtime"
)

// OpenDefault ouvre la page dans le navigateur par défaut.
func OpenDefault(url string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", url)
	} else {
		cmd = exec.Command("xdg-open", url)
	}
	hideConsole(cmd)
	return cmd.Start()
}
