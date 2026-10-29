// Package selfupdate keeps the kube-dc CLI current.
//
// On start the CLI checks, at most once a day, whether kube-dc/kube-dc-public
// has published a newer release. If so it downloads the release's
// checksums.txt, downloads the binary for this OS and architecture, checks its
// SHA-256 against that list, and atomically replaces the running executable.
// The user is told after their command finished.
//
// Signing is prepared but off: with PublicKey set, the release must also
// carry checksums.txt.sig, an ed25519 signature of checksums.txt
// (cli/hack/release-signing), and an unsigned or wrongly signed release is
// refused. A build with a key never accepts an unsigned release, so turning
// signing on cannot be undone by leaving the signature out.
//
// Nothing is changed when the build is a development build, when
// KUBE_DC_NO_UPDATE is set, in CI or when the terminal is not interactive,
// when the binary belongs to a package manager (Homebrew, Nix, Snap), or
// when the binary's directory is not writable. Every failure is silent on
// the automatic path and leaves the binary as it was; `kube-dc update`
// reports it.
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// PublicKey, when set (base64 ed25519), makes signed checksums required.
// Empty for now: releases are verified against checksums.txt over HTTPS only.
// To turn signing on, follow cli/hack/release-signing/README.md: the release
// workflow signs with the matching private key, and this constant gets its
// public half.
const PublicKey = ""

// DefaultAPI is the GitHub API base; KUBE_DC_UPDATE_URL overrides it (tests).
const DefaultAPI = "https://api.github.com/repos/kube-dc/kube-dc-public"

// CheckInterval is how often the automatic path asks for a new release.
const CheckInterval = 24 * time.Hour

// RetryAfterFailure is how soon an interrupted or refused install is tried again.
const RetryAfterFailure = time.Hour

// Release is the part of a GitHub release this package reads.
type Release struct {
	Tag    string  `json:"tag_name"`
	URL    string  `json:"html_url"`
	Draft  bool    `json:"draft"`
	Pre    bool    `json:"prerelease"`
	Assets []Asset `json:"assets"`
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Updater holds what a check needs; tests replace any field.
type Updater struct {
	Current    string // the running version, e.g. "0.9.10" or "v0.9.10"
	API        string
	Executable string // the file to replace
	StateFile  string
	PublicKey  ed25519.PublicKey
	GOOS       string
	GOARCH     string
	Client     *http.Client
	Now        func() time.Time
}

// New returns an Updater for this process.
func New(current string) (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var key ed25519.PublicKey
	if PublicKey != "" {
		raw, err := base64.StdEncoding.DecodeString(PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("selfupdate: the release signing key in this build is malformed")
		}
		key = ed25519.PublicKey(raw)
	}
	api := DefaultAPI
	if v := os.Getenv("KUBE_DC_UPDATE_URL"); v != "" {
		api = strings.TrimRight(v, "/")
	}
	return &Updater{
		Current: current, API: api, Executable: exe,
		StateFile: filepath.Join(home, ".kube-dc", "update.json"),
		PublicKey: key, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Client: &http.Client{Timeout: 60 * time.Second}, Now: time.Now,
	}, nil
}

// SkipReason says why the automatic path must not run, or "" when it may.
// `args` are the command-line arguments after the program name.
func SkipReason(current string, args []string, env func(string) string, interactive bool, exe string) string {
	switch {
	case !Released(current):
		return "development build"
	case env("KUBE_DC_NO_UPDATE") != "":
		return "KUBE_DC_NO_UPDATE is set"
	case env("CI") != "":
		return "running in CI"
	case !interactive:
		return "not an interactive terminal"
	}
	if len(args) > 0 {
		switch args[0] {
		// The kubectl exec plugin: its stdout is read by kubectl, it runs on
		// every kubectl call, and it must stay fast and quiet.
		case "credential", "completion", "__complete", "__completeNoDesc", "update", "help":
			return "command " + args[0]
		}
	}
	if managedPath(exe) {
		return "installed by a package manager"
	}
	if !writableDir(filepath.Dir(exe)) {
		return "binary directory is not writable"
	}
	return ""
}

func managedPath(exe string) bool {
	for _, p := range []string{"/Cellar/", "/homebrew/", "/nix/store/", "/snap/"} {
		if strings.Contains(exe, p) {
			return true
		}
	}
	return false
}

func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".kube-dc-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// Released reports whether v is a release version (vX.Y.Z or X.Y.Z, no
// suffix), the only kind that updates itself.
func Released(v string) bool {
	_, ok := parse(v)
	return ok
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Newer reports whether release version b is newer than a.
func Newer(b, a string) bool {
	x, ok1 := parse(b)
	y, ok2 := parse(a)
	if !ok1 || !ok2 {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

type state struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest,omitempty"`
}

func (u *Updater) readState() state {
	var s state
	if b, err := os.ReadFile(u.StateFile); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (u *Updater) writeState(s state) {
	if err := os.MkdirAll(filepath.Dir(u.StateFile), 0o700); err != nil {
		return
	}
	b, _ := json.Marshal(s)
	_ = os.WriteFile(u.StateFile, b, 0o600)
}

// Due reports whether the automatic path should ask GitHub now.
func (u *Updater) Due() bool {
	return u.Now().Sub(u.readState().CheckedAt) >= CheckInterval
}

// Latest asks GitHub for the newest published release.
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.API+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kube-dc-cli/"+u.Current)
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release lookup: %s", resp.Status)
	}
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("release lookup: %w", err)
	}
	if r.Draft || r.Pre || !Released(r.Tag) {
		return nil, fmt.Errorf("release lookup: %q is not a published release", r.Tag)
	}
	return &r, nil
}

// AssetName is the release file for this OS and architecture.
func (u *Updater) AssetName() string {
	name := "kube-dc_" + u.GOOS + "_" + u.GOARCH
	if u.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func (u *Updater) download(ctx context.Context, r *Release, name string, limit int64) ([]byte, error) {
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "kube-dc-cli/"+u.Current)
		resp, err := u.Client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download %s: %s", name, resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", name, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("download %s: larger than %d bytes", name, limit)
		}
		return b, nil
	}
	return nil, fmt.Errorf("release %s has no %s", r.Tag, name)
}

// Verified returns the SHA-256 the checksums list gives for name. With a key
// the list must carry its valid signature; without one (signing off) the
// signature is not read.
func Verified(checksums, signature []byte, key ed25519.PublicKey, name string) (string, error) {
	if key != nil {
		sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
		if err != nil || len(sig) != ed25519.SignatureSize {
			return "", errors.New("checksums signature is malformed")
		}
		if !ed25519.Verify(key, checksums, sig) {
			return "", errors.New("checksums signature does not verify")
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name && len(fields[0]) == 64 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return strings.ToLower(fields[0]), nil
			}
		}
	}
	return "", fmt.Errorf("checksums do not list %s", name)
}

// Install downloads, verifies and swaps in release r. It returns nothing
// changed on any error.
func (u *Updater) Install(ctx context.Context, r *Release) error {
	sums, err := u.download(ctx, r, "checksums.txt", 64<<10)
	if err != nil {
		return err
	}
	var sig []byte
	if u.PublicKey != nil {
		// Signing on: an unsigned release is refused here.
		if sig, err = u.download(ctx, r, "checksums.txt.sig", 4<<10); err != nil {
			return err
		}
	}
	want, err := Verified(sums, sig, u.PublicKey, u.AssetName())
	if err != nil {
		return err
	}
	bin, err := u.download(ctx, r, u.AssetName(), 256<<20)
	if err != nil {
		return err
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s does not match its signed checksum", u.AssetName())
	}
	return replace(u.Executable, bin, u.GOOS)
}

// replace writes bin next to exe and renames it over exe, so a reader sees
// the old or the new file and never a partial one. Windows cannot replace a
// running executable: the old one is moved aside first and removed on the
// next update.
func replace(exe string, bin []byte, goos string) error {
	dir := filepath.Dir(exe)
	mode := os.FileMode(0o755)
	if st, err := os.Stat(exe); err == nil {
		mode = st.Mode().Perm() | 0o100
	}
	tmp, err := os.CreateTemp(dir, ".kube-dc-update-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if goos == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(name, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(name, exe)
}

// Result is what one check did.
type Result struct {
	From, To, URL string
	Installed     bool
}

// Run checks for a newer release and installs it. force skips the daily
// interval (kube-dc update); checkOnly reports without installing.
func (u *Updater) Run(ctx context.Context, force, checkOnly bool) (Result, error) {
	res := Result{From: u.Current}
	s := u.readState()
	if !force && u.Now().Sub(s.CheckedAt) < CheckInterval {
		return res, nil
	}
	r, err := u.Latest(ctx)
	if err != nil {
		// Offline or rate limited: do not ask again on every start.
		u.writeState(state{CheckedAt: u.Now(), Latest: s.Latest})
		return res, err
	}
	res.To, res.URL = r.Tag, r.URL
	if !Newer(r.Tag, u.Current) || checkOnly {
		u.writeState(state{CheckedAt: u.Now(), Latest: r.Tag})
		return res, nil
	}
	if err := u.Install(ctx, r); err != nil {
		// Interrupted (the command finished first) or refused: try again in
		// an hour rather than a day, and not on every start.
		u.writeState(state{CheckedAt: u.Now().Add(-CheckInterval + RetryAfterFailure), Latest: r.Tag})
		return res, err
	}
	u.writeState(state{CheckedAt: u.Now(), Latest: r.Tag})
	res.Installed = true
	return res, nil
}
