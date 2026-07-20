package ports

import (
	"errors"
	"testing"
)

func chanOf(lines ...Line) <-chan Line {
	ch := make(chan Line, len(lines))
	for _, l := range lines {
		ch <- l
	}
	close(ch)
	return ch
}

func out(text string) Line  { return Line{Stream: StreamStdout, Text: text} }
func exit(text string) Line { return Line{Stream: StreamExit, Text: text} }

func TestDrain_ValidStreams(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []Line
		want  int
	}{
		{"exit 0", []Line{out("working"), exit("0")}, 0},
		{"exit 7", []Line{out("boom"), exit("7")}, 7},
		{"exit only", []Line{exit("0")}, 0},
		{"whitespace tolerated", []Line{exit(" 3\n")}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Drain(chanOf(tc.lines...), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

// The core regression: EOF with no exit record must never be reported as
// a clean exit 0. Every engine used to do exactly that.
func TestDrain_TruncatedStream(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []Line
	}{
		{"output then EOF", []Line{out("working")}},
		{"empty stream", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Drain(chanOf(tc.lines...), nil); !errors.Is(err, ErrStreamTruncated) {
				t.Errorf("want ErrStreamTruncated, got %v", err)
			}
		})
	}
}

// "An exit appeared somewhere" is weaker than the contract, which says
// the exit record is the LAST record.
func TestDrain_RejectsContractViolations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []Line
		want  error
	}{
		{"output after exit", []Line{exit("0"), out("late")}, ErrStreamOutputAfterExit},
		{"stderr after exit", []Line{exit("0"), {Stream: StreamStderr, Text: "late"}}, ErrStreamOutputAfterExit},
		{"duplicate exit", []Line{exit("0"), exit("1")}, ErrStreamDuplicateExit},
		// A trailing exit 0 after a real failure would otherwise mask it.
		{"failure then success exit", []Line{exit("1"), exit("0")}, ErrStreamDuplicateExit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Drain(chanOf(tc.lines...), nil); !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// fmt.Sscanf("%d") stopped at the first non-digit and reported success,
// so a corrupt record like "0garbage" became a clean exit 0.
func TestParseExitCode_Strict(t *testing.T) {
	for _, bad := range []string{"0garbage", "1abc", "", "   ", "abc", "0x1f", "1.5", "--1", "0 1"} {
		if _, err := ParseExitCode(bad); !errors.Is(err, ErrStreamBadExitCode) {
			t.Errorf("ParseExitCode(%q) should be rejected, got %v", bad, err)
		}
	}
	for _, good := range []struct {
		in   string
		want int
	}{{"0", 0}, {"7", 7}, {"130", 130}, {" 42 ", 42}, {"-1", -1}} {
		got, err := ParseExitCode(good.in)
		if err != nil {
			t.Errorf("ParseExitCode(%q): %v", good.in, err)
		}
		if got != good.want {
			t.Errorf("ParseExitCode(%q) = %d, want %d", good.in, got, good.want)
		}
	}
}

func TestDrain_ForwardsNonExitLinesInOrder(t *testing.T) {
	var seen []string
	code, err := Drain(chanOf(out("a"), Line{Stream: StreamStderr, Text: "b"}, out("c"), exit("0")),
		func(l Line) { seen = append(seen, string(l.Stream)+":"+l.Text) })
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("exit = %d", code)
	}
	want := []string{"stdout:a", "stderr:b", "stdout:c"}
	if len(seen) != len(want) {
		t.Fatalf("forwarded %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

// The ScriptRunner contract obliges callers to drain to closure or the
// adapter may leak the process — including when we bail out early.
func TestDrain_ConsumesChannelEvenOnViolation(t *testing.T) {
	ch := chanOf(exit("0"), out("late"), out("later"), out("latest"))
	if _, err := Drain(ch, nil); !errors.Is(err, ErrStreamOutputAfterExit) {
		t.Fatalf("want ErrStreamOutputAfterExit, got %v", err)
	}
	if _, open := <-ch; open {
		t.Error("Drain returned with lines still buffered; the adapter can leak its process")
	}
}
