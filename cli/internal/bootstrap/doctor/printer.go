// Package doctor renders ports.Result lists as the three-section
// pre-flight report from installer-ux §4:
//
//	Physical world          - things the operator must wire up
//	                          (DNS, NICs, host kernel modules, ...)
//	Auto-handled by CLI     - prereqs the CLI offers to install
//	                          (kubectl/flux/sops binaries, RKE2 unit, ...)
//	CLI verifies + suggests - settings the CLI checks but cannot
//	                          fix without operator decisions
//	                          (gh auth scopes, age key recipient set, ...)
//
// The Printer is plain-text-first; TTY mode adds lipgloss colour from
// the alerts-TUI palette. NO_COLOR=1 disables colour even on a TTY
// (https://no-color.org).
//
// **Exit code is derived from the highest Severity** across all
// probed Results (`SeverityInfo=0`, `SeverityWarn=1`,
// `SeverityBlocker=2`, `SeverityFatal=3`). The printer NEVER
// invents severity from Status — it reads what each probe produced.
package doctor

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// Category is the section bucket every NamedResult lands in. The
// three values are stable + ordered so iteration produces the same
// section sequence on every Print call.
type Category int

const (
	CategoryPhysical Category = iota
	CategoryAutoHandled
	CategoryVerifiesSuggests
)

func (c Category) String() string {
	switch c {
	case CategoryPhysical:
		return "Physical world"
	case CategoryAutoHandled:
		return "Auto-handled by CLI"
	case CategoryVerifiesSuggests:
		return "CLI verifies + suggests"
	}
	return "Unknown"
}

// orderedCategories ensures sections render top-to-bottom in the
// installer-ux §4 order regardless of how callers built the input.
var orderedCategories = []Category{
	CategoryPhysical,
	CategoryAutoHandled,
	CategoryVerifiesSuggests,
}

// NamedResult pairs a probe's name + category with its Result. The
// printer never inspects the probe object directly — Result is the
// only carrier of state + Severity + FixHint.
type NamedResult struct {
	Category Category
	Name     string
	Result   ports.Result
}

// Printer renders a slice of NamedResults. Zero value is usable;
// New() exists for symmetry + future config knobs.
type Printer struct {
	// Out is where the report writes. Defaults to os.Stdout.
	Out io.Writer

	// NoTTY forces plain (uncoloured, deterministic width) output
	// regardless of whether Out happens to be a terminal. Set by
	// `--no-tty` and by CI invocations.
	NoTTY bool

	// NoColor disables lipgloss styling even on a TTY. Honoured
	// when the operator sets `NO_COLOR=1` (per no-color.org).
	NoColor bool

	// NextCommand is the suggested follow-up command shown in the
	// footer (e.g. "kube-dc bootstrap init --domain ..."). Empty
	// → no Next-line is printed.
	NextCommand string
}

// New returns a Printer wired to os.Stdout with TTY mode auto-
// detected from `Out`. Callers can override fields after
// construction (NoTTY, NextCommand).
func New() *Printer {
	return &Printer{Out: os.Stdout}
}

// Print renders `results` and returns the exit code derived from
// the max Severity across them. The exit code maps directly to the
// doctor command's process exit:
//
//	SeverityInfo    -> 0
//	SeverityWarn    -> 1
//	SeverityBlocker -> 2
//	SeverityFatal   -> 3
func (p *Printer) Print(results []NamedResult) int {
	out := p.Out
	if out == nil {
		out = os.Stdout
	}
	useColor := !p.NoTTY && !p.NoColor && os.Getenv("NO_COLOR") == ""

	// Group by category for the three-section layout.
	bucketed := bucketByCategory(results)
	width := computeColumnWidth(results)

	for _, cat := range orderedCategories {
		section := bucketed[cat]
		if len(section) == 0 {
			continue
		}
		// Section header. Bold on TTY, plain on no-TTY.
		fmt.Fprintln(out, sectionHeader(cat.String(), useColor))
		for _, nr := range section {
			fmt.Fprintln(out, formatRow(nr, width, useColor))
			// Surface FixHint on its own indented line(s) when
			// present. Records (DNS-style record-set blocks) get a
			// dedicated copy-paste block.
			renderFixHint(out, nr.Result.FixHint, useColor)
		}
		fmt.Fprintln(out)
	}

	// Footer summary.
	blockers, warnings, info := countBySeverity(results)
	fmt.Fprintln(out, footerSummary(blockers, warnings, info, p.NextCommand, useColor))

	return exitCodeFor(maxSeverity(results))
}

// ---------- layout helpers ----------

func bucketByCategory(results []NamedResult) map[Category][]NamedResult {
	out := make(map[Category][]NamedResult, 3)
	for _, nr := range results {
		out[nr.Category] = append(out[nr.Category], nr)
	}
	// Stable within-section ordering by probe name so snapshots
	// don't churn.
	for cat := range out {
		sort.SliceStable(out[cat], func(i, j int) bool {
			return out[cat][i].Name < out[cat][j].Name
		})
	}
	return out
}

func computeColumnWidth(results []NamedResult) int {
	const min = 18
	w := min
	for _, nr := range results {
		if l := len(nr.Name); l > w {
			w = l
		}
	}
	return w
}

func formatRow(nr NamedResult, nameWidth int, useColor bool) string {
	icon := statusIcon(nr.Result, useColor)
	name := padRight(nr.Name, nameWidth)
	detail := nr.Result.Detail
	if nr.Result.Version != "" && !strings.Contains(detail, nr.Result.Version) {
		// Keep the version visible even when Detail doesn't include
		// it — operators scan this column for "is the version in
		// range?".
		detail = nr.Result.Version + "  " + detail
	}
	return fmt.Sprintf("  %s  %s  %s", icon, name, detail)
}

// statusIcon returns the per-Severity glyph. The icon set matches
// the alerts-TUI palette referenced in installer-ux §4.
func statusIcon(r ports.Result, useColor bool) string {
	var glyph, colour string
	switch r.Severity {
	case ports.SeverityInfo:
		switch r.Status {
		case ports.StatusInstalled, ports.StatusManaged:
			glyph, colour = "✓", "10" // accent green
		default:
			glyph, colour = "·", "8" // muted grey
		}
	case ports.SeverityWarn:
		glyph, colour = "⚠", "11" // orange
	case ports.SeverityBlocker:
		glyph, colour = "✗", "9" // red
	case ports.SeverityFatal:
		glyph, colour = "✗", "9"
	default:
		glyph, colour = "·", "8"
	}
	if !useColor {
		return glyph
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colour)).Render(glyph)
}

func renderFixHint(out io.Writer, hint ports.FixHint, useColor bool) {
	if hint.Text == "" && len(hint.Records) == 0 {
		return
	}
	// Indent two spaces under the row. Multi-line hints reflow
	// with the same indent so the layout stays clean.
	prefix := "      "
	if hint.Text != "" {
		for _, line := range strings.Split(hint.Text, "\n") {
			fmt.Fprintln(out, prefix+line)
		}
	}
	if len(hint.Records) > 0 {
		fmt.Fprintln(out, prefix+"Add these DNS records:")
		for _, r := range hint.Records {
			fmt.Fprintf(out, "%s  %s   %s   %s   %d\n", prefix, r.Type, r.Name, r.Value, r.TTL)
		}
	}
	_ = useColor // reserved for a future "muted" hint style
}

func sectionHeader(name string, useColor bool) string {
	if !useColor {
		return name + ":"
	}
	return lipgloss.NewStyle().Bold(true).Render(name + ":")
}

func footerSummary(blockers, warnings, info int, next string, useColor bool) string {
	parts := []string{
		fmt.Sprintf("%d blockers", blockers),
		fmt.Sprintf("%d warnings", warnings),
		fmt.Sprintf("%d info", info),
	}
	line := strings.Join(parts, ", ")
	if next != "" {
		line += "; Next: " + next
	}
	if !useColor {
		return line
	}
	return lipgloss.NewStyle().Bold(true).Render(line)
}

func countBySeverity(results []NamedResult) (blockers, warnings, info int) {
	for _, nr := range results {
		switch nr.Result.Severity {
		case ports.SeverityBlocker, ports.SeverityFatal:
			blockers++
		case ports.SeverityWarn:
			warnings++
		case ports.SeverityInfo:
			info++
		}
	}
	return
}

func maxSeverity(results []NamedResult) ports.Severity {
	max := ports.SeverityInfo
	for _, nr := range results {
		if nr.Result.Severity > max {
			max = nr.Result.Severity
		}
	}
	return max
}

// exitCodeFor maps Severity onto the doctor command's process exit
// code. See M1 severity model + exit codes in
// `installer-agentic-implementation-plan.md` §6.
func exitCodeFor(s ports.Severity) int {
	switch s {
	case ports.SeverityFatal:
		return 3
	case ports.SeverityBlocker:
		return 2
	case ports.SeverityWarn:
		return 1
	default:
		return 0
	}
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
