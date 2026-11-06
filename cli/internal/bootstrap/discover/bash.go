package discover

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// The Fleet scripts start through /usr/bin/env bash and need Bash 4.4 or
// later (associative arrays, empty arrays under set -u). macOS ships Bash 3.2
// as /bin/bash; a Homebrew bash must come first in PATH there.
func bashFix() string {
	if runtime.GOOS == "darwin" {
		return "Install Bash with Homebrew (brew install bash) and put Homebrew's bin directory before /bin in PATH, then rerun the checks."
	}
	return "Install Bash 4.4 or later and put it first in PATH, then rerun the checks."
}

// newBashProbe checks the bash first in PATH, which is the one the Fleet
// scripts run, for version 4.4 or later.
func newBashProbe(exec execHook) *ToolProbe {
	p := newSimpleVersionProbe("bash", exec, "--version")
	p.fixHint = bashFix()
	p.timeout = 5 * time.Second
	p.extraFn = func(ctx context.Context, run execHook) *extraResult {
		stdout, _, err := run(ctx, "bash", "-c", "echo ${BASH_VERSINFO[0]} ${BASH_VERSINFO[1]}")
		major, minor, ok := parseBashVersinfo(string(stdout))
		if err != nil || !ok || major < 4 || (major == 4 && minor < 4) {
			detail := "the bash first in PATH is older than 4.4"
			if ok {
				detail = fmt.Sprintf("the bash first in PATH is %d.%d; the Fleet scripts need 4.4 or later", major, minor)
			}
			return &extraResult{status: ports.StatusPartial, severity: ports.SeverityBlocker,
				detail: detail, fixHint: ports.FixHint{Text: bashFix()}}
		}
		return nil
	}
	return p
}

func parseBashVersinfo(out string) (int, int, bool) {
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(fields[0])
	minor, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}
