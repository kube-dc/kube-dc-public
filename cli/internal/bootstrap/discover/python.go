package discover

import (
	"context"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const pythonScaffoldFix = "Install Python 3.9 or later and PyYAML in the environment used by python3, then rerun the checks."

// newPythonScaffoldProbe checks the interpreter and the module in the same
// environment that Fleet executes. No pip or package installation is attempted.
func newPythonScaffoldProbe(exec execHook) *ToolProbe {
	p := newSimpleVersionProbe("python3", exec, "--version")
	p.fixHint = pythonScaffoldFix
	p.timeout = 5 * time.Second
	p.extraFn = func(ctx context.Context, run execHook) *extraResult {
		_, _, err := run(ctx, "python3", "-c", "import yaml; assert callable(yaml.safe_load) and callable(yaml.safe_dump)")
		if err != nil {
			return &extraResult{status: ports.StatusPartial, severity: ports.SeverityBlocker,
				detail: "Python 3 cannot import the required PyYAML API", fixHint: ports.FixHint{Text: pythonScaffoldFix}}
		}
		return nil
	}
	return p
}
