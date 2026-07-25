package main

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// TestClassifyExecuteErr pins the exit-code/reporting contract of the
// Execute() error branch.
//
// Regression guard: the branch used to match an ANONYMOUS
// `interface{ ExitCode() int }`. *exec.ExitError satisfies that too, so every
// failing child process (sops, kubectl, bao, ssh) exited with the child's
// status and printed NOTHING — `openbao setup-controller-auth` showed up as
// exit=100 with zero bytes of output. Reinstating the anonymous interface
// must fail the exec.ExitError case below.
func TestClassifyExecuteErr(t *testing.T) {
	// A distinct type that also implements ExitCode() int — stands in for
	// any third-party error that happens to carry that method.
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  bool // true = the error must be printed
	}{
		{
			name:     "nil error",
			err:      nil,
			wantCode: 0,
			wantMsg:  false,
		},
		{
			name:     "doctorExitCodeErr keeps its code and prints nothing",
			err:      &doctorExitCodeErr{code: 3},
			wantCode: 3,
			wantMsg:  false,
		},
		{
			name:     "wrapped doctorExitCodeErr behaves identically",
			err:      fmt.Errorf("doctor run: %w", &doctorExitCodeErr{code: 2}),
			wantCode: 2,
			wantMsg:  false,
		},
		{
			name:     "exec.ExitError must NOT be silenced (the original bug)",
			err:      fmt.Errorf("sops decrypt: %w", &exec.ExitError{ProcessState: nil}),
			wantCode: 1,
			wantMsg:  true,
		},
		{
			name:     "unrelated ExitCode() implementor is not silenced either",
			err:      fmt.Errorf("wrapped: %w", otherExitCoder{code: 100}),
			wantCode: 1,
			wantMsg:  true,
		},
		{
			name:     "plain error prints and exits 1",
			err:      errors.New("boom"),
			wantCode: 1,
			wantMsg:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := classifyExecuteErr(tc.err)
			if code != tc.wantCode {
				t.Errorf("code = %d, want %d", code, tc.wantCode)
			}
			if got := msg != ""; got != tc.wantMsg {
				t.Errorf("printed = %v (msg=%q), want printed = %v", got, msg, tc.wantMsg)
			}
		})
	}
}

// otherExitCoder implements ExitCode() int without being the doctor's type.
type otherExitCoder struct{ code int }

func (e otherExitCoder) Error() string { return fmt.Sprintf("child exited %d", e.code) }
func (e otherExitCoder) ExitCode() int { return e.code }
