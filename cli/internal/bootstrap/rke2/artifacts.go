package rke2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// ArtifactSet pins the bytes used by the guided host engine. Legacy callers
// retain their existing acquisition path when this explicit input is absent.
type ArtifactSet struct {
	ArchiveSize     int64  `json:"archiveSize"`
	Version         string `json:"version"`
	Architecture    string `json:"architecture"`
	InstallerPath   string `json:"installerPath"`
	InstallerSHA256 string `json:"installerSHA256"`
	ArchiveURL      string `json:"archiveURL"`
	ArchiveSHA256   string `json:"archiveSHA256"`
}

var artifactSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var privateArtifactDirectory = regexp.MustCompile(`^/var/lib/kube-dc-rke2\.[A-Za-z0-9]+$`)

const artifactCleanupTimeout = 30 * time.Second

func (a ArtifactSet) Validate(version string) error { _, err := a.installer(version); return err }

func EmbeddedScripts() map[string]string {
	return map[string]string{"install-server.sh": string(installServerScript), "install-agent.sh": string(installAgentScript)}
}

// EmbeddedScriptHashes binds the host effects to the actual embedded consumers.
func EmbeddedScriptHashes() map[string]string {
	out := map[string]string{}
	for name, body := range map[string][]byte{"install-server.sh": installServerScript, "install-agent.sh": installAgentScript} {
		h := sha256.Sum256(body)
		out[name] = hex.EncodeToString(h[:])
	}
	return out
}

func (a ArtifactSet) installer(version string) ([]byte, error) {
	u, err := url.Parse(a.ArchiveURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(a.ArchiveURL, "\r\n") || !artifactSHA256.MatchString(a.InstallerSHA256) || !artifactSHA256.MatchString(a.ArchiveSHA256) || a.ArchiveSize < 1 || a.ArchiveSize > 4<<30 || a.Version != version || (a.Architecture != "amd64" && a.Architecture != "arm64") {
		return nil, fmt.Errorf("invalid reviewed RKE2 artifacts")
	}
	info, err := os.Lstat(a.InstallerPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 4<<20 {
		return nil, fmt.Errorf("reviewed RKE2 installer is unavailable")
	}
	file, err := os.Open(a.InstallerPath)
	if err != nil {
		return nil, fmt.Errorf("open reviewed RKE2 installer")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return nil, fmt.Errorf("read reviewed RKE2 installer")
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != a.InstallerSHA256 {
		return nil, fmt.Errorf("reviewed RKE2 installer bytes changed")
	}
	if os.Getenv("RKE2_DNS_PUBLIC_FALLBACK") == "true" {
		return nil, fmt.Errorf("reviewed RKE2 installation does not permit resolver replacement")
	}
	return body, nil
}

// stageArtifacts verifies downloaded bytes before either embedded script
// changes config, sysctls, trust, or services. The private directory prevents
// another host user from replacing checked bytes between verification and use.
func stageArtifacts(ctx context.Context, ssh ports.SSHClient, host ports.SSHHost, a ArtifactSet, version string, ca *TrustedCAMaterial) (map[string]string, func() error, error) {
	body, err := a.installer(version)
	if err != nil {
		return nil, nil, err
	}
	response, err := ssh.Run(ctx, host, "sudo -n mktemp -d /var/lib/kube-dc-rke2.XXXXXXXXXX")
	if err != nil {
		return nil, nil, fmt.Errorf("create protected RKE2 artifact directory")
	}
	dir := strings.TrimSpace(string(response))
	if !privateArtifactDirectory.MatchString(dir) {
		return nil, nil, fmt.Errorf("invalid protected RKE2 artifact directory")
	}
	cleanup := func() error {
		// Cleanup still runs after cancellation; it is bounded independently.
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactCleanupTimeout)
		defer cancel()
		if _, err := ssh.Run(cleanCtx, host, "sudo -n rm -rf -- "+shellQuote(dir)); err != nil {
			return fmt.Errorf("RKE2 artifact cleanup needs attention")
		}
		return nil
	}
	fail := func(err error) (map[string]string, func() error, error) { return nil, cleanup, err }
	installer := dir + "/install.sh"
	if err := ssh.Put(ctx, host, installer, body, 0o600); err != nil {
		return fail(fmt.Errorf("stage reviewed RKE2 installer"))
	}
	archive := "rke2.linux-" + a.Architecture + ".tar.gz"
	checksum := []byte(a.ArchiveSHA256 + "  " + archive + "\n")
	if err := ssh.Put(ctx, host, dir+"/sha256sum-"+a.Architecture+".txt", checksum, 0o600); err != nil {
		return fail(fmt.Errorf("stage reviewed RKE2 checksum"))
	}
	curlCA := ""
	if ca != nil {
		if err := ssh.Put(ctx, host, dir+"/ca.pem", ca.PEM, 0o600); err != nil {
			return fail(fmt.Errorf("stage artifact download CA"))
		}
		curlCA = " --cacert " + shellQuote(dir+"/ca.pem")
	}
	transfer := "set -eu; umask 077; test \"$(uname -m)\" = " + shellQuote(map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[a.Architecture]) + "; curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --max-time 600 --max-filesize " + strconv.FormatInt(a.ArchiveSize, 10) + curlCA + " " + shellQuote(a.ArchiveURL) + " | head -c " + strconv.FormatInt(a.ArchiveSize+1, 10) + " > " + shellQuote(dir+"/"+archive) + "; test \"$(stat -c %s " + shellQuote(dir+"/"+archive) + ")\" = " + strconv.FormatInt(a.ArchiveSize, 10) + "; printf '%s  %s\\n' " + shellQuote(a.InstallerSHA256) + " " + shellQuote(installer) + " | sha256sum --check --status; cd " + shellQuote(dir) + "; sha256sum --check --status " + shellQuote("sha256sum-"+a.Architecture+".txt")
	command := "sudo -n bash -o pipefail -c " + shellQuote(transfer)
	if _, err := ssh.Run(ctx, host, command); err != nil {
		return fail(fmt.Errorf("downloaded RKE2 bytes do not match reviewed artifacts or the host architecture"))
	}
	return map[string]string{"KDC_RKE2_INSTALLER": installer, "KDC_RKE2_INSTALLER_SHA256": a.InstallerSHA256, "INSTALL_RKE2_ARTIFACT_PATH": dir, "INSTALL_RKE2_METHOD": "tar", "RKE2_DNS_PUBLIC_FALLBACK": "false"}, cleanup, nil
}

func reviewedScriptCommand(env map[string]string, path string, body []byte, reviewed bool, args ...string) string {
	command := remoteInstallCmd(env, path)
	for _, arg := range args {
		command += " " + shellQuote(arg)
	}
	if !reviewed {
		return command
	}
	hash := sha256.Sum256(body)
	check := "printf '%s  %s\\n' " + shellQuote(hex.EncodeToString(hash[:])) + " " + shellQuote(path) + " | sha256sum --check --status"
	return "sudo -n bash -o pipefail -c " + shellQuote("set -eu; "+check+"; exec "+strings.TrimPrefix(command, "sudo -n "))
}
func reviewedAgentCommand(env map[string]string, path, token, host, ip string, reviewed bool) string {
	if !reviewed {
		return remoteAgentCmd(env, path, token, host, ip)
	}
	command := remoteAgentCmd(env, path, token, host, ip)
	hash := sha256.Sum256(installAgentScript)
	check := "printf '%s  %s\\n' " + shellQuote(hex.EncodeToString(hash[:])) + " " + shellQuote(path) + " | sha256sum --check --status"
	return "sudo -n bash -o pipefail -c " + shellQuote("set -eu; "+check+"; exec "+strings.TrimPrefix(command, "sudo -n "))
}
