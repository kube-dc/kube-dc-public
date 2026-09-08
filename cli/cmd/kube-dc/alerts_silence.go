package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/shalb/kube-dc/cli/internal/alerts"
	"github.com/spf13/cobra"
)

// The alert-suppression surface: `alerts silence`, `alerts ack`,
// `alerts silences` and `alerts unsilence`.
//
// A NOTE ON PRECISION. Targeting by fingerprint builds matchers from the
// alert's full label set, which is the NARROWEST silence Alertmanager can
// express — but not "this alert instance and nothing else". Alertmanager
// silences match when all their matchers match; an alert carrying the same
// labels PLUS extra ones matches too, and so does a future recurrence of the
// same alert after it resolves. Alertmanager has no instance identity to
// target (fingerprints are derived from labels and are not accepted as
// matchers), so this is the floor of what the API can do, and the help says
// so rather than promising exactness.
//
// ack is a silence with a short default and a mandatory comment. Alertmanager
// has no acknowledgement concept of its own, so the alternative was inventing
// state and somewhere to keep it; a silence is already durable, already shows
// up in the web UI, and already expires on its own.

const (
	// defaultSilenceDuration is long enough to cover a maintenance window
	// without being long enough to forget about.
	defaultSilenceDuration = 2 * time.Hour
	// defaultAckDuration is deliberately short. An acknowledgement says "I am
	// looking at this now", not "hide it for the rest of the week" — if the
	// work outlasts it the alert comes back, which is the correct outcome.
	defaultAckDuration = 1 * time.Hour
)

type silenceCmdOpts struct {
	fingerprint string
	matchers    []string
	duration    time.Duration
	comment     string
	author      string
	// alertmanager plumbing, shared with `alerts`
	url         string
	portForward bool
}

// resolveAlertmanager returns a client plus a cleanup func, reusing the same
// endpoint resolution (flag → env → port-forward → localhost) as `alerts`.
func resolveAlertmanager(ctx context.Context, explicitURL string, portForward bool) (*alerts.AlertmanagerClient, func(), error) {
	url := explicitURL
	if url == "" {
		url = os.Getenv("ALERTMANAGER_URL")
	}
	cleanup := func() {}
	if url == "" && portForward {
		pf := alerts.NewAlertmanagerPortForward()
		pfCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := pf.Start(pfCtx); err != nil {
			return nil, cleanup, fmt.Errorf("port-forward to alertmanager failed: %w\n\nHint: set --alertmanager-url or pre-run\n  kubectl port-forward -n monitoring svc/prom-operator-alertmanager 9093:9093", err)
		}
		cleanup = func() { _ = pf.Stop() }
		url = pf.URL()
	}
	if url == "" {
		url = "http://localhost:9093"
	}
	return alerts.NewAlertmanagerClient(url), cleanup, nil
}

// currentAuthor fills createdBy. This is SELF-REPORTED: --author accepts any
// string and the fallback is the local OS username, not an authenticated
// identity. It is a courtesy label for the next operator reading the silence
// list, not evidence of who acted — real attribution needs authenticated API
// logging, which also covers who EXPIRED a silence (Alertmanager does not
// record that on the object at all).
func currentAuthor(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "kube-dc-cli"
}

// buildSilence turns the flags into a silence, choosing between "silence this
// one alert" (fingerprint) and "silence anything matching these labels".
func buildSilence(ctx context.Context, c *alerts.AlertmanagerClient, o silenceCmdOpts, now time.Time) (alerts.Silence, error) {
	if o.fingerprint != "" && len(o.matchers) > 0 {
		return alerts.Silence{}, fmt.Errorf("pass either --fingerprint (silence one firing alert) or --matcher (silence by label), not both")
	}

	var ms []alerts.Matcher
	switch {
	case o.fingerprint != "":
		// Resolve the fingerprint to the alert's real labels: Alertmanager
		// derives fingerprints from labels and will not accept one as a
		// matcher, so the silence has to be expressed in those labels.
		list, err := c.GetAlerts(ctx)
		if err != nil {
			return alerts.Silence{}, fmt.Errorf("fetch alerts to resolve the fingerprint: %w", err)
		}
		a, err := alerts.FindAlertByFingerprint(list, o.fingerprint)
		if err != nil {
			return alerts.Silence{}, err
		}
		ms = alerts.MatchersForAlert(a)
	default:
		var err error
		if ms, err = alerts.MatchersFromLabelSelectors(o.matchers); err != nil {
			return alerts.Silence{}, err
		}
	}

	if strings.TrimSpace(o.comment) == "" {
		return alerts.Silence{}, fmt.Errorf("--comment is required: a silence with no reason is indistinguishable from a mistake when someone finds it later")
	}
	if o.duration <= 0 {
		return alerts.Silence{}, fmt.Errorf("--duration must be positive, got %s", o.duration)
	}

	return alerts.Silence{
		Matchers:  ms,
		StartsAt:  now,
		EndsAt:    now.Add(o.duration),
		CreatedBy: currentAuthor(o.author),
		Comment:   o.comment,
	}, nil
}

func runSilence(ctx context.Context, out io.Writer, o silenceCmdOpts) error {
	c, cleanup, err := resolveAlertmanager(ctx, o.url, o.portForward)
	if err != nil {
		return err
	}
	defer cleanup()

	s, err := buildSilence(ctx, c, o, time.Now())
	if err != nil {
		return err
	}
	id, err := c.CreateSilence(ctx, s)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Silenced until %s (%s)\n", s.EndsAt.Format(time.RFC3339), s.Remaining())
	for _, m := range s.Matchers {
		fmt.Fprintf(out, "  %s\n", sanitizeForTable(m.String()))
	}
	fmt.Fprintf(out, "id: %s\n", id)
	fmt.Fprintf(out, "\nEnds early with: kube-dc alerts unsilence %s\n", id)
	return nil
}

func runListSilences(ctx context.Context, out io.Writer, o silenceCmdOpts, all bool) error {
	c, cleanup, err := resolveAlertmanager(ctx, o.url, o.portForward)
	if err != nil {
		return err
	}
	defer cleanup()

	list, err := c.ListSilences(ctx)
	if err != nil {
		return err
	}
	shown := make([]alerts.Silence, 0, len(list))
	for _, s := range list {
		// Pending counts as shown: a silence scheduled to start in ten minutes
		// is precisely what someone deciding "is this cluster quiet?" needs.
		if all || s.Live() {
			shown = append(shown, s)
		}
	}
	if len(shown) == 0 {
		if all {
			fmt.Fprintln(out, "No silences.")
		} else {
			fmt.Fprintln(out, "No active or pending silences. (--all includes expired ones.)")
		}
		return nil
	}
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].EndsAt.Before(shown[j].EndsAt) })

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tWHEN\tBY\tMATCHERS\tCOMMENT")
	for _, s := range shown {
		parts := make([]string, 0, len(s.Matchers))
		for _, m := range s.Matchers {
			parts = append(parts, sanitizeForTable(m.String()))
		}
		when := s.Remaining() + " left"
		if s.Pending() {
			when = "starts in " + s.StartsIn()
		} else if !s.Active() {
			when = "ended"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Status.State, when, sanitizeForTable(s.CreatedBy),
			strings.Join(parts, ","), sanitizeForTable(s.Comment))
	}
	return tw.Flush()
}

// sanitizeForTable strips control characters from values that came off the
// wire. Matchers and comments are operator-supplied strings from a shared
// service: a tab or newline breaks the column alignment, and a terminal escape
// can move the cursor and rewrite what the reader already saw.
func sanitizeForTable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func runUnsilence(ctx context.Context, out io.Writer, o silenceCmdOpts, id string) error {
	c, cleanup, err := resolveAlertmanager(ctx, o.url, o.portForward)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := c.ExpireSilence(ctx, id); err != nil {
		return err
	}
	// Deliberately not "will page again": another silence, an inhibition rule,
	// a missing receiver or the alert having resolved can all still stop it.
	fmt.Fprintf(out, "Silence %s expired — it no longer suppresses matching alerts.\n", id)
	return nil
}

// addAlertSuppressionCommands hangs the four subcommands off `alerts`.
func addAlertSuppressionCommands(parent *cobra.Command) {
	// EACH command owns its options. A single shared struct looks tidy and is
	// wrong: pflag writes a flag's default into the bound variable at BIND
	// time, so binding `ack --duration` (1h) after `silence --duration` (2h)
	// left `silence` running with ack's default while its help still advertised
	// 2h. Separate storage makes that unrepresentable.
	var silenceOpts, ackOpts, listOpts, expireOpts silenceCmdOpts

	bindCommon := func(c *cobra.Command, o *silenceCmdOpts) {
		c.Flags().StringVar(&o.url, "alertmanager-url", "", "Alertmanager base URL (default: $ALERTMANAGER_URL, else a port-forward)")
		c.Flags().BoolVar(&o.portForward, "port-forward", true, "Port-forward to Alertmanager when no URL is given")
	}

	silence := &cobra.Command{
		Use:   "silence",
		Short: "Stop an alert paging for a while",
		Long: `Create an Alertmanager silence.

Target it either at one firing alert (--fingerprint, taken from
` + "`kube-dc alerts --output json`" + `) or at anything matching a set of labels
(--matcher, repeatable).

A fingerprint silence is built from that alert's FULL label set — the narrowest
silence Alertmanager can express. It is not exact instance targeting: a silence
matches whenever all its matchers match, so an alert carrying those labels plus
extra ones matches too, as does a later recurrence of the same alert.`,
		Example: `  # silence one firing alert for two hours
  kube-dc alerts silence --fingerprint 7f3c2a1b --comment "disk replacement, ticket 412"

  # silence a whole class during a maintenance window
  kube-dc alerts silence --matcher alertname=KubeNodeNotReady --matcher node=worker-3 \
      --duration 6h --comment "planned reboot"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSilence(cmd.Context(), cmd.OutOrStdout(), silenceOpts)
		},
	}
	silence.Flags().StringVar(&silenceOpts.fingerprint, "fingerprint", "", "Fingerprint of a firing alert to silence")
	silence.Flags().StringArrayVar(&silenceOpts.matchers, "matcher", nil, "Label matcher, repeatable: key=value or key=~regex")
	silence.Flags().DurationVar(&silenceOpts.duration, "duration", defaultSilenceDuration, "How long to silence for")
	silence.Flags().StringVar(&silenceOpts.comment, "comment", "", "Why (required — an unexplained silence is indistinguishable from a mistake)")
	silence.Flags().StringVar(&silenceOpts.author, "author", "", "Who to record as the creator (self-reported; defaults to your username)")
	bindCommon(silence, &silenceOpts)

	ack := &cobra.Command{
		Use:   "ack",
		Short: "Acknowledge one firing alert (a short silence saying you are on it)",
		Long: `Acknowledge a firing alert.

Alertmanager has no acknowledgement of its own, so this is a silence with a
short default (` + defaultAckDuration.String() + `) and your comment. That is
deliberate: an ack says "I am looking at this now", not "hide it for the rest of
the week" — if the work outlasts it, the alert comes back, which is what you
want.

ack takes --fingerprint only. Acknowledging by label selector would suppress a
CLASS of current and future alerts, which is a silence, not an acknowledgement —
use ` + "`kube-dc alerts silence`" + ` for that and say so in the comment.`,
		Example: `  kube-dc alerts ack --fingerprint 7f3c2a1b --comment "investigating, ticket 412"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ackOpts.fingerprint == "" {
				return fmt.Errorf("ack needs --fingerprint (from `kube-dc alerts --output json`); to suppress a class of alerts use `kube-dc alerts silence --matcher ...`")
			}
			return runSilence(cmd.Context(), cmd.OutOrStdout(), ackOpts)
		},
	}
	ack.Flags().StringVar(&ackOpts.fingerprint, "fingerprint", "", "Fingerprint of the alert being acknowledged (required)")
	ack.Flags().DurationVar(&ackOpts.duration, "duration", defaultAckDuration, "How long the acknowledgement holds")
	ack.Flags().StringVar(&ackOpts.comment, "comment", "", "What you are doing about it (required)")
	ack.Flags().StringVar(&ackOpts.author, "author", "", "Who to record as the creator (self-reported; defaults to your username)")
	bindCommon(ack, &ackOpts)

	var all bool
	list := &cobra.Command{
		Use:   "silences",
		Short: "List silences",
		Long: `List silences.

Active AND pending silences are shown by default: a silence scheduled to start
later is exactly what you need to see before concluding a cluster is quiet.
--all adds expired ones.`,
		Example: "  kube-dc alerts silences\n  kube-dc alerts silences --all",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListSilences(cmd.Context(), cmd.OutOrStdout(), listOpts, all)
		},
	}
	list.Flags().BoolVar(&all, "all", false, "Include expired silences")
	bindCommon(list, &listOpts)

	unsilence := &cobra.Command{
		Use:     "unsilence <silence-id>",
		Short:   "End a silence now",
		Args:    cobra.ExactArgs(1),
		Example: "  kube-dc alerts unsilence 3f9a1c22-...",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnsilence(cmd.Context(), cmd.OutOrStdout(), expireOpts, args[0])
		},
	}
	bindCommon(unsilence, &expireOpts)

	parent.AddCommand(silence, ack, list, unsilence)
}
