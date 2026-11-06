package discover

import (
	"context"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestParseBashVersinfo(t *testing.T) {
	for in, want := range map[string][3]int{
		"5 2\n": {5, 2, 1}, "4 4": {4, 4, 1}, "3 2\n": {3, 2, 1}, "": {0, 0, 0}, "x y": {0, 0, 0},
	} {
		major, minor, ok := parseBashVersinfo(in)
		if major != want[0] || minor != want[1] || ok != (want[2] == 1) {
			t.Errorf("parseBashVersinfo(%q) = %d %d %v", in, major, minor, ok)
		}
	}
}

func TestBashProbeNeedsBash44(t *testing.T) {
	for _, tc := range []struct {
		versinfo string
		blocker  bool
	}{{"5 2", false}, {"4 4", false}, {"4 3", true}, {"3 2", true}} {
		exec := func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			if len(args) > 0 && args[0] == "--version" {
				return []byte("GNU bash, version " + map[string]string{"5 2": "5.2.21", "4 4": "4.4.23", "4 3": "4.3.48", "3 2": "3.2.57"}[tc.versinfo] + "(1)-release"), nil, nil
			}
			return []byte(tc.versinfo + "\n"), nil, nil
		}
		result := newBashProbe(exec).Run(context.Background())
		if blocked := result.Severity == ports.SeverityBlocker; blocked != tc.blocker {
			t.Errorf("bash %s: severity %v, want blocker=%v (%+v)", tc.versinfo, result.Severity, tc.blocker, result)
		}
	}
}
