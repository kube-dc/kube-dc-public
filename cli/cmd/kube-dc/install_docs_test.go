package main

import (
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The quickstart is the page a first-time operator copy-pastes from, so a stale
// flag there costs more than a stale flag anywhere else: it fails on someone
// else's hardware, with our name on it, at the point where they have the least
// context to recover.
//
// Writing it, three of its commands were wrong — `--node-external-ip` does not
// exist on `bootstrap install` (it is `--external-ip`), `bootstrap install`
// needs `--name`, and break-glass is `bootstrap break-glass adopt <cluster>`
// rather than `bootstrap adopt --break-glass`. Every one was found by running
// the command, not by reading the page. These tests keep it that way.

// installDocs are every operator-facing page that tells someone which command
// to run. All of them are held to the same standard: a stale flag on any of them
// fails on somebody else's hardware with our name on it.
func installDocs() []string {
	return []string{"quickstart.md", "installation-guide.md", "installation-overview.md"}
}

func docBody(t *testing.T, name string) (string, bool) {
	t.Helper()
	p := filepath.Join("..", "..", "..", "docs", "platform", name)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func quickstartBody(t *testing.T) string {
	t.Helper()
	body, ok := docBody(t, "quickstart.md")
	if !ok {
		t.Skip("quickstart not present")
	}
	return body
}

// Every `kube-dc ...` command the quickstart tells the operator to run must
// resolve to a real command in the CLI's own tree.
func TestInstallDocs_EveryCommandExists(t *testing.T) {
	root := newRootCmd()
	for _, doc := range installDocs() {
		body, ok := docBody(t, doc)
		if !ok {
			continue
		}
		t.Run(doc, func(t *testing.T) { assertCommandsExist(t, root, doc, body) })
	}
}

func assertCommandsExist(t *testing.T, root *cobra.Command, doc, body string) {
	t.Helper()

	// Match the command path only: `kube-dc bootstrap oidc-cutover ...` -> the
	// leading non-flag words.
	re := regexp.MustCompile(`(?m)^\s*(?:# )?kube-dc ((?:[a-z0-9][a-z0-9-]*\s*)+)`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		words := strings.Fields(m[1])
		// Drop a trailing positional argument (cluster name, node name) by
		// walking as deep as the command tree allows.
		cmd, _, err := root.Find(words)
		if err != nil || cmd == nil {
			t.Errorf("%s references a command that does not exist: kube-dc %s (%v)", doc,
				strings.Join(words, " "), err)
			continue
		}
		// root.Find falls back to the root command for an unknown first word.
		if cmd == root && len(words) > 0 {
			t.Errorf("%s references unknown command: kube-dc %s", doc, strings.Join(words, " "))
			continue
		}
		seen[cmd.CommandPath()] = true
	}
	// Sanity for the quickstart specifically: it must actually walk the whole
	// install, or this test is asserting nothing.
	if doc == "quickstart.md" {
		for _, want := range []string{
			"kube-dc bootstrap install",
			"kube-dc bootstrap init",
			"kube-dc bootstrap oidc-cutover",
			"kube-dc bootstrap accept",
			"kube-dc bootstrap fetch-kubeconfig",
			"kube-dc login",
		} {
			if !seen[want] {
				t.Errorf("quickstart should walk the operator through %q but does not reference it", want)
			}
		}
	}
}

// Every long flag the quickstart passes must be registered on the command it is
// passed to. This is the check that would have caught --node-external-ip on
// `bootstrap install`.
func TestInstallDocs_EveryFlagIsRegisteredOnItsCommand(t *testing.T) {
	root := newRootCmd()
	for _, doc := range installDocs() {
		body, ok := docBody(t, doc)
		if !ok {
			continue
		}
		t.Run(doc, func(t *testing.T) { assertFlagsRegistered(t, root, body) })
	}
}

func assertFlagsRegistered(t *testing.T, root *cobra.Command, body string) {
	t.Helper()

	// Join backslash-continued lines so a multi-line invocation is one string.
	joined := strings.ReplaceAll(body, "\\\n", " ")
	re := regexp.MustCompile(`(?m)^\s*kube-dc ([^\n|>]+)`)
	for _, m := range re.FindAllStringSubmatch(joined, -1) {
		tokens := strings.Fields(m[1])
		var path []string
		for _, tk := range tokens {
			if strings.HasPrefix(tk, "-") {
				break
			}
			path = append(path, tk)
		}
		cmd, _, err := root.Find(path)
		if err != nil || cmd == nil || cmd == root {
			continue // covered by TestQuickstart_EveryCommandExists
		}
		for _, tk := range tokens {
			if !strings.HasPrefix(tk, "--") {
				continue
			}
			name := strings.TrimPrefix(tk, "--")
			if i := strings.IndexAny(name, "= "); i >= 0 {
				name = name[:i]
			}
			if name == "" {
				continue
			}
			if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil &&
				cmd.PersistentFlags().Lookup(name) == nil && root.PersistentFlags().Lookup(name) == nil {
				t.Errorf("doc passes --%s to %q, which does not accept it",
					name, cmd.CommandPath())
			}
		}
	}
}

// The example config block must use the key names the prefill actually reads.
// The prefix rule is asymmetric and easy to get wrong: orchestration keys KEEP
// the KUBE_DC_INIT_ prefix, cluster-identity keys do NOT take it. Writing
// KUBE_DC_INIT_CLUSTER_NAME (which looks more consistent) silently loads
// nothing and init then reports --name as missing.
func TestQuickstart_ConfigKeysAreRecognised(t *testing.T) {
	body := quickstartBody(t)
	start := strings.Index(body, "cat > dc1.env")
	if start < 0 {
		t.Skip("quickstart has no example config block")
	}
	block := body[start:]
	if end := strings.Index(block, "\nEOF"); end > 0 {
		block = block[:end]
	}

	// These four are cluster identity and must appear WITHOUT the prefix.
	for _, key := range []string{"CLUSTER_NAME", "DOMAIN", "NODE_EXTERNAL_IP", "EMAIL"} {
		if !regexp.MustCompile(`(?m)^` + key + `=`).MatchString(block) {
			t.Errorf("example config must set %s= (plain, no prefix)", key)
		}
		if strings.Contains(block, "KUBE_DC_INIT_"+key+"=") {
			t.Errorf("example config sets KUBE_DC_INIT_%s, which the prefill does not read — "+
				"cluster-identity keys take no prefix", key)
		}
	}
	// And these are orchestration and must KEEP it.
	for _, key := range []string{"MODE", "PRESET", "FLEET_MODE"} {
		if !strings.Contains(block, "KUBE_DC_INIT_"+key+"=") {
			t.Errorf("example config must set KUBE_DC_INIT_%s= (orchestration keys keep the prefix)", key)
		}
	}
}
