package credential

import (
	"strings"
	"testing"
)

// Inside the console's cloud shell there is no browser, so "run kube-dc login"
// is advice the user cannot act on — the OAuth redirect has nowhere to land.
// The shell's credential is seeded at startup from the browser session and
// cannot be renewed in place, so the only real remedy is a fresh shell.
func TestReloginCmd_InCloudShellDoesNotSuggestBrowserLogin(t *testing.T) {
	t.Setenv("REFRESH_TOKEN", "seeded-by-the-console")
	t.Setenv("SERVER_ENDPOINT", "https://kube-api.example.com:6443")

	got := reloginCmd("https://kube-api.example.com:6443", "acme")
	if strings.Contains(got, "kube-dc login") {
		t.Errorf("must not tell a browser-less shell to run a browser login; got: %s", got)
	}
	if !strings.Contains(got, "reopen") && !strings.Contains(got, "open a new one") {
		t.Errorf("should tell the user to get a fresh shell; got: %s", got)
	}
}

// On a workstation the browser login IS the right answer, and must keep the
// realm so the user does not have to remember which identity expired.
func TestReloginCmd_OnWorkstationKeepsLoginCommand(t *testing.T) {
	t.Setenv("REFRESH_TOKEN", "")
	t.Setenv("SERVER_ENDPOINT", "")

	got := reloginCmd("https://kube-api.example.com:6443", "acme")
	if !strings.Contains(got, "kube-dc login --domain example.com --org acme") {
		t.Errorf("want the tenant login command, got: %s", got)
	}
}

func TestReloginCmd_AdminRealmUsesAdminFlag(t *testing.T) {
	t.Setenv("REFRESH_TOKEN", "")
	t.Setenv("SERVER_ENDPOINT", "")

	got := reloginCmd("https://kube-api.example.com:6443", "master")
	if !strings.Contains(got, "--admin") {
		t.Errorf("master realm must suggest --admin, got: %s", got)
	}
}

// Only BOTH variables together mean "cloud shell". A stray REFRESH_TOKEN in a
// workstation environment must not suppress the actionable login hint.
func TestInCloudShell_RequiresBothSignals(t *testing.T) {
	t.Setenv("REFRESH_TOKEN", "x")
	t.Setenv("SERVER_ENDPOINT", "")
	if inCloudShell() {
		t.Error("REFRESH_TOKEN alone must not be treated as a cloud shell")
	}
}
