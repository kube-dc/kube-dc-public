package discover

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestScaffoldPythonChecksSameInterpreterModuleAndFloor(t *testing.T) {
	for _, tc := range []struct {
		name, version         string
		versionErr, importErr error
		want                  ports.Status
		imports               int
	}{
		{"ready", "Python 3.12.3", nil, nil, ports.StatusInstalled, 1},
		{"absent", "", exec.ErrNotFound, nil, ports.StatusMissing, 0},
		{"too-old", "Python 3.8.18", nil, nil, ports.StatusPartial, 0},
		{"module-missing", "Python 3.12.3", nil, errors.New("No module named yaml"), ports.StatusPartial, 1},
		{"unreadable-version", "unknown", nil, nil, ports.StatusPartial, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imports := 0
			p := newPythonScaffoldProbe(func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
				if name != "python3" {
					t.Fatal("module checked through a different interpreter")
				}
				if _, bounded := ctx.Deadline(); !bounded {
					t.Fatal("unbounded Python invocation")
				}
				if args[0] == "--version" {
					return []byte(tc.version), nil, tc.versionErr
				}
				imports++
				if args[0] != "-c" || !strings.Contains(args[1], "yaml.safe_load") {
					t.Fatal("PyYAML API not checked")
				}
				return nil, nil, tc.importErr
			})
			result := p.Run(context.Background())
			if result.Status != tc.want || imports != tc.imports {
				t.Fatalf("%+v, imports=%d", result, imports)
			}
			if tc.want != ports.StatusInstalled && !strings.Contains(result.FixHint.Text, "PyYAML") {
				t.Fatalf("missing repair: %+v", result)
			}
		})
	}
}

func TestScaffoldPythonCancellationDoesNotExecute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	newPythonScaffoldProbe(func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("executed after cancellation")
		return nil, nil, nil
	}).Run(ctx)
}
