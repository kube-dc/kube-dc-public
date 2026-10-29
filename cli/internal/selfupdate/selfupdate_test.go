package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// release serves one GitHub release: its metadata, checksums.txt, its
// signature and the linux/amd64 binary. Fields can be broken per test.
type release struct {
	tag, binary, sums, sig string
	pre                    bool
	hits                   map[string]int
}

func newRelease(t *testing.T, priv ed25519.PrivateKey, tag, binary string) *release {
	t.Helper()
	sum := sha256.Sum256([]byte(binary))
	sums := fmt.Sprintf("%s  kube-dc_linux_amd64\n%s  kube-dc_darwin_arm64\n", hex.EncodeToString(sum[:]), strings.Repeat("0", 64))
	return &release{tag: tag, binary: binary, sums: sums,
		sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums))), hits: map[string]int{}}
}

func (r *release) serve(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits[req.URL.Path]++
		switch req.URL.Path {
		case "/releases/latest":
			assets := []Asset{}
			for _, n := range []string{"checksums.txt", "checksums.txt.sig", "kube-dc_linux_amd64"} {
				assets = append(assets, Asset{Name: n, URL: srv.URL + "/download/" + n})
			}
			_ = json.NewEncoder(w).Encode(Release{Tag: r.tag, URL: "https://example.test/" + r.tag, Pre: r.pre, Assets: assets})
		case "/download/checksums.txt":
			fmt.Fprint(w, r.sums)
		case "/download/checksums.txt.sig":
			fmt.Fprint(w, r.sig)
		case "/download/kube-dc_linux_amd64":
			fmt.Fprint(w, r.binary)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func updater(t *testing.T, api string, pub ed25519.PublicKey, current string) *Updater {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "kube-dc")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	return &Updater{Current: current, API: api, Executable: exe, StateFile: filepath.Join(dir, "state", "update.json"),
		PublicKey: pub, GOOS: "linux", GOARCH: "amd64", Client: http.DefaultClient, Now: func() time.Time { return now }}
}

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestInstallsANewerSignedRelease(t *testing.T) {
	pub, priv := keys(t)
	rel := newRelease(t, priv, "v0.9.12", "new binary")
	u := updater(t, rel.serve(t).URL, pub, "0.9.10")
	res, err := u.Run(context.Background(), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || res.From != "0.9.10" || res.To != "v0.9.12" {
		t.Fatalf("result %+v", res)
	}
	got, _ := os.ReadFile(u.Executable)
	if string(got) != "new binary" {
		t.Fatalf("binary not replaced: %q", got)
	}
	if st, _ := os.Stat(u.Executable); st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("not executable: %v", st.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(u.Executable), ".kube-dc-update-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	// Within the interval nothing is asked again.
	if _, err := u.Run(context.Background(), false, false); err != nil || rel.hits["/releases/latest"] != 1 {
		t.Fatalf("checked again within a day: %d %v", rel.hits["/releases/latest"], err)
	}
	// After it, it is.
	u.Now = func() time.Time { return time.Date(2026, 10, 25, 12, 0, 1, 0, time.UTC) }
	if _, err := u.Run(context.Background(), false, false); err != nil || rel.hits["/releases/latest"] != 2 {
		t.Fatalf("not checked after a day: %d %v", rel.hits["/releases/latest"], err)
	}
}

func TestNeverReplacesWithoutAValidSignatureAndChecksum(t *testing.T) {
	pub, priv := keys(t)
	_, otherPriv := keys(t)
	cases := map[string]func(r *release){
		"signed by another key":   func(r *release) { r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, []byte(r.sums))) },
		"checksums changed after": func(r *release) { r.sums = strings.Replace(r.sums, "0000", "1111", 1) },
		"malformed signature":     func(r *release) { r.sig = "not base64!" },
		"binary does not match":   func(r *release) { r.binary = "tampered binary" },
		"binary not in the listing": func(r *release) {
			r.sums = strings.Repeat("0", 64) + "  kube-dc_darwin_arm64\n"
			r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(r.sums)))
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			rel := newRelease(t, priv, "v0.9.12", "new binary")
			breakIt(rel)
			u := updater(t, rel.serve(t).URL, pub, "0.9.10")
			res, err := u.Run(context.Background(), true, false)
			if err == nil || res.Installed {
				t.Fatalf("installed anyway: %+v %v", res, err)
			}
			if got, _ := os.ReadFile(u.Executable); string(got) != "old binary" {
				t.Fatalf("binary changed: %q", got)
			}
		})
	}
}

func TestOnlyNewerPublishedReleasesInstall(t *testing.T) {
	pub, priv := keys(t)
	for _, c := range []struct {
		name, tag, current string
		pre                bool
	}{
		{"same version", "v0.9.10", "0.9.10", false},
		{"older release", "v0.9.9", "v0.9.10", false},
		{"pre-release", "v0.9.12", "0.9.10", true},
		{"odd tag", "v0.9.12-rc.1", "0.9.10", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rel := newRelease(t, priv, c.tag, "new binary")
			rel.pre = c.pre
			u := updater(t, rel.serve(t).URL, pub, c.current)
			res, _ := u.Run(context.Background(), true, false)
			if res.Installed || rel.hits["/download/kube-dc_linux_amd64"] != 0 {
				t.Fatalf("installed %+v", res)
			}
		})
	}
	// --check reports without installing.
	rel := newRelease(t, priv, "v0.9.12", "new binary")
	u := updater(t, rel.serve(t).URL, pub, "0.9.10")
	res, err := u.Run(context.Background(), true, true)
	if err != nil || res.Installed || res.To != "v0.9.12" || rel.hits["/download/kube-dc_linux_amd64"] != 0 {
		t.Fatalf("check only: %+v %v", res, err)
	}
}

func TestAFailedLookupWaitsForTheNextInterval(t *testing.T) {
	pub, _ := keys(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; http.Error(w, "rate limited", 403) }))
	t.Cleanup(srv.Close)
	u := updater(t, srv.URL, pub, "0.9.10")
	if _, err := u.Run(context.Background(), false, false); err == nil {
		t.Fatal("no error")
	}
	_, _ = u.Run(context.Background(), false, false)
	if hits != 1 {
		t.Fatalf("asked %d times", hits)
	}
}

func TestSkipReasons(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "kube-dc")
	none := func(string) string { return "" }
	set := func(k string) func(string) string {
		return func(n string) string {
			if n == k {
				return "1"
			}
			return ""
		}
	}
	for _, c := range []struct {
		name, current string
		args          []string
		env           func(string) string
		tty           bool
		exe, want     string
	}{
		{"release in a terminal", "0.9.10", []string{"secrets", "list"}, none, true, exe, ""},
		{"dev build", "dev", nil, none, true, exe, "development build"},
		{"git describe build", "v0.9.10-3-gabc-dirty", nil, none, true, exe, "development build"},
		{"opted out", "0.9.10", nil, set("KUBE_DC_NO_UPDATE"), true, exe, "KUBE_DC_NO_UPDATE is set"},
		{"CI", "0.9.10", nil, set("CI"), true, exe, "running in CI"},
		{"not a terminal", "0.9.10", nil, none, false, exe, "not an interactive terminal"},
		{"kubectl exec plugin", "0.9.10", []string{"credential"}, none, true, exe, "command credential"},
		{"homebrew", "0.9.10", nil, none, true, "/opt/homebrew/Cellar/kube-dc/0.9.10/bin/kube-dc", "installed by a package manager"},
		{"read-only directory", "0.9.10", nil, none, true, "/proc/kube-dc", "binary directory is not writable"},
	} {
		if got := SkipReason(c.current, c.args, c.env, c.tty, c.exe); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		b, a string
		want bool
	}{{"v0.9.11", "0.9.10", true}, {"v0.10.0", "v0.9.99", true}, {"v1.0.0", "0.99.0", true},
		{"v0.9.10", "0.9.10", false}, {"v0.9.9", "0.9.10", false}, {"v0.9.11-rc.1", "0.9.10", false}, {"v0.9.11", "dev", false}} {
		if got := Newer(c.b, c.a); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.b, c.a, got)
		}
	}
}

func TestTheBuiltInKeyIsEmptyOrAnEd25519PublicKey(t *testing.T) {
	if PublicKey == "" {
		return // signing off
	}
	k, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(k) != ed25519.PublicKeySize {
		t.Fatalf("PublicKey: %d bytes, %v", len(k), err)
	}
}

// Signing off: the binary is checked against checksums.txt alone, and the
// signature is not even fetched.
func TestWithoutAKeyTheChecksumAloneDecides(t *testing.T) {
	_, priv := keys(t)
	rel := newRelease(t, priv, "v0.9.12", "new binary")
	u := updater(t, rel.serve(t).URL, nil, "0.9.10")
	res, err := u.Run(context.Background(), true, false)
	if err != nil || !res.Installed {
		t.Fatalf("%+v %v", res, err)
	}
	if rel.hits["/download/checksums.txt.sig"] != 0 {
		t.Fatal("fetched the signature with signing off")
	}
	// A binary that does not match is still refused.
	bad := newRelease(t, priv, "v0.9.12", "new binary")
	bad.binary = "tampered"
	u2 := updater(t, bad.serve(t).URL, nil, "0.9.10")
	if res, err := u2.Run(context.Background(), true, false); err == nil || res.Installed {
		t.Fatalf("tampered binary installed: %+v", res)
	}
	if got, _ := os.ReadFile(u2.Executable); string(got) != "old binary" {
		t.Fatalf("binary changed: %q", got)
	}
}

// Signing on: a release without a valid signature is refused, so it cannot
// be downgraded to checksum-only by leaving the signature out.
func TestWithAKeyAnUnsignedReleaseIsRefused(t *testing.T) {
	pub, priv := keys(t)
	rel := newRelease(t, priv, "v0.9.12", "new binary")
	u := updater(t, rel.serve(t).URL, pub, "0.9.10")
	rel.sig = ""
	if res, err := u.Run(context.Background(), true, false); err == nil || res.Installed {
		t.Fatalf("unsigned release installed: %+v %v", res, err)
	}
}

func TestAnInterruptedInstallIsRetriedWithinTheHourNotTheDay(t *testing.T) {
	pub, priv := keys(t)
	rel := newRelease(t, priv, "v0.9.12", "new binary")
	rel.binary = "tampered" // the install fails after the lookup
	u := updater(t, rel.serve(t).URL, pub, "0.9.10")
	if _, err := u.Run(context.Background(), false, false); err == nil {
		t.Fatal("installed a tampered binary")
	}
	if u.Due() {
		t.Fatal("retried immediately")
	}
	base := u.Now()
	u.Now = func() time.Time { return base.Add(RetryAfterFailure + time.Second) }
	if !u.Due() {
		t.Fatal("not retried after an hour")
	}
}
