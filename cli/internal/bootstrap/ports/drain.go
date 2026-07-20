package ports

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Drain consumes a ScriptRunner stream and enforces the contract the
// ScriptRunner interface already documents: "the final Line has
// Stream=StreamExit and Text=<exitCode>".
//
// It exists because that contract used to be re-implemented, slightly
// differently, by every engine that ran a script — and every one of them
// got the same case wrong. Each initialised `exit := 0` and returned it
// untouched if the channel closed without ever carrying a StreamExit
// record, so a killed script, a dead runner or a dropped connection was
// indistinguishable from a clean success. For stateful ceremonies that
// meant declaring the work done on the strength of output nobody
// received: openbao/init.go asserted the vault was initialized,
// clusterinit/scaffold.go continued into post-processing,
// clusterinit/apply.go treated Flux as installed, keycloak/init.go
// printed "init complete".
//
// Drain rejects four violations rather than papering over them:
//
//   - no exit record at all           -> ErrStreamTruncated
//   - a second exit record            -> ErrStreamDuplicateExit
//   - any record AFTER the exit       -> ErrStreamOutputAfterExit
//   - an exit code that is not an int -> ErrStreamBadExitCode
//
// The last three matter because "an exit appeared somewhere" is weaker
// than the contract: a stream that continues past its exit record has
// either been interleaved with another process's output or been
// truncated mid-restart, and in both cases the code we captured may not
// describe the run we are about to act on.
//
// onLine receives every non-exit Line in order and may be nil. Callers
// own their own formatting and redaction; Drain deliberately does no
// writing of its own.
//
// The returned exit code is meaningful only when err is nil.
func Drain(lines <-chan Line, onLine func(Line)) (int, error) {
	exit := 0
	sawExit := false

	for ln := range lines {
		if ln.Stream == StreamExit {
			if sawExit {
				// Keep draining: the contract obliges us to consume the
				// channel to closure or the adapter may leak the process.
				drainRest(lines)
				return 0, fmt.Errorf("%w (second exit record %q)", ErrStreamDuplicateExit, ln.Text)
			}
			n, err := ParseExitCode(ln.Text)
			if err != nil {
				drainRest(lines)
				return 0, err
			}
			exit = n
			sawExit = true
			continue
		}
		if sawExit {
			drainRest(lines)
			return 0, fmt.Errorf("%w (%s: %q)", ErrStreamOutputAfterExit, ln.Stream, ln.Text)
		}
		if onLine != nil {
			onLine(ln)
		}
	}

	if !sawExit {
		return 0, ErrStreamTruncated
	}
	return exit, nil
}

// drainRest consumes whatever is left so the adapter can close down its
// process even though we have already decided the stream is invalid.
func drainRest(lines <-chan Line) {
	for range lines { //nolint:revive // intentional drain-to-close
	}
}

// ParseExitCode converts a StreamExit record's Text to an exit code.
//
// It is strict on purpose. The previous implementations used
// fmt.Sscanf("%d"), which stops at the first non-digit and reports
// success — so "0garbage" parsed as a clean exit 0. A malformed exit
// record means we do not know how the script ended, and guessing "0"
// is the one answer that silently continues a stateful ceremony.
func ParseExitCode(s string) (int, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("%w: empty exit record", ErrStreamBadExitCode)
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrStreamBadExitCode, s)
	}
	return n, nil
}

// ErrStreamTruncated reports a stream that closed without a terminal
// exit record. Engines running stateful ceremonies must treat this as a
// failure and preserve whatever the run already produced — the script
// may well have completed its irreversible work before the stream died.
var ErrStreamTruncated = errors.New("script output ended without an exit status (script killed, or the runner died mid-run)")

// ErrStreamDuplicateExit reports more than one exit record in a stream.
var ErrStreamDuplicateExit = errors.New("script emitted more than one exit record")

// ErrStreamOutputAfterExit reports output following the exit record,
// which the ScriptRunner contract forbids (the exit Line is final).
var ErrStreamOutputAfterExit = errors.New("script emitted output after its exit record")

// ErrStreamBadExitCode reports an exit record whose text is not a
// decimal integer.
var ErrStreamBadExitCode = errors.New("script emitted a malformed exit code")
