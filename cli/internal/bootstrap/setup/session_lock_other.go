//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package setup

import (
	"fmt"
	"os"
)

func lockSessionFile(*os.File) error {
	return fmt.Errorf("protected session writers are not supported on this workstation OS")
}
