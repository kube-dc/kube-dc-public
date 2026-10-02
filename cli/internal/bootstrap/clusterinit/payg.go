package clusterinit

// payg.go — `kube-dc bootstrap init --payg`: opt-in PAYG usage billing for a
// NEW installation.
//
// PAYG is off by default. It runs only on an installation that bills through
// Kube-DC (BILLING_PROVIDER stripe or whmcs, set for this installation) and
// never on one billed by a partner. The rule is enforced twice: at validation
// against the reviewed inputs, and again at apply against the rendered
// cluster-config.env, so nothing between the two can slip PAYG in.
//
// Without --payg the CLI writes no PAYG switch, identity, layer or login. (The
// starter's release pins still carry PAYG_METERING_IMAGE into every
// cluster-config.env; nothing references that key until payg.yaml exists, so
// it is inert.)
//
// With --payg the scaffold wires what the fleet's platform/payg README
// describes:
//
//  1. cluster-config.env — PAYG_ENABLED=true (the chart's billing.payg.enabled
//     switch), a freshly generated PAYG_INSTALLATION_UID bound to this
//     installation by PAYG_INSTALLATION_BINDING=<name>/<domain>, and
//     PAYG_PRODUCTS_REVISION=<cluster>-<yyyymmdd>. PAYG_METERING_IMAGE comes
//     from the release pins or an explicit --set, and must carry a digest;
//  2. clusters/<name>/payg.yaml — the Flux Kustomization over ./platform/payg,
//     listed in clusters/<name>/kustomization.yaml;
//  3. clusters/<name>/metering-db.enc.yaml — the kube-dc-metering-db login
//     Secret (username/password/uri), SOPS-encrypted with the fleet's own
//     recipients through the same hardened flow as the other generated
//     secrets, listed in the cluster kustomization;
//  4. platform.yaml — the kube_dc_metering role in the CNPG Cluster's
//     spec.managed.roles, appended to whatever the base Cluster declares.
//
// The installation UID is part of every usage fact's identity: two
// installations sharing one merge their billing histories. It is generated
// here from crypto/rand, written together with its binding, never
// regenerated, refused on --set, deny-listed from clone imports (prefill.go),
// and refused when its binding names another installation or another cluster
// directory of the fleet carries the same UID.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

const (
	// paygBundlePath is the shared fleet bundle the per-cluster layer points
	// at. It ships in the fleet-starter (cicd/release/starter-tree-allowlist.txt).
	paygBundlePath = "platform/payg"
	// paygLayerFileName is the per-cluster Flux Kustomization.
	paygLayerFileName = "payg.yaml"
	// paygDBSecretsFileName matches .sops.yaml's \.enc\.yaml$ creation rule.
	paygDBSecretsFileName = "metering-db.enc.yaml"

	paygDBSecretName = "kube-dc-metering-db"
	paygNamespace    = "monitoring"
	paygDBRole       = "kube_dc_metering"
	paygDBName       = "kube_dc_metering"

	// Defaults mirror the bundle's own ${VAR:=default} fallbacks, so the role
	// patch and the rendered Database agree when the operator sets neither.
	paygDefaultDBCluster       = "grafana-pg"
	paygDefaultConnectionLimit = 8
	paygNewConnectionLimit     = 30

	// paygDBRoleMarker identifies the owned platform.yaml patch entry so a
	// re-run is a no-op and the other platform.yaml writers can compose with
	// it (ownedPlatformPatchMarkers).
	paygDBRoleMarker = "# kube-dc: PAYG metering database role"

	// cluster-config.env keys.
	PAYGEnabledKey             = "PAYG_ENABLED"
	PAYGInstallationUIDKey     = "PAYG_INSTALLATION_UID"
	PAYGInstallationBindingKey = "PAYG_INSTALLATION_BINDING"
	PAYGMeteringImageKey       = "PAYG_METERING_IMAGE"
	PAYGProductsRevisionKey    = "PAYG_PRODUCTS_REVISION"
	paygDBClusterKey           = "PAYG_DB_CLUSTER"
	paygConnectionLimitKey     = "PAYG_DB_CONNECTION_LIMIT"
	billingProviderKey         = "BILLING_PROVIDER"

	// paygPartnerMarkerPrefix is the configuration namespace of a partner
	// billing integration the starter and fleet already know. An
	// installation carrying any such key is billed by that partner.
	paygPartnerMarkerPrefix = "CLOUDSIGMA_"
)

// paygBillingProviders are the billing providers Kube-DC bills through. PAYG
// is refused for every other value, including none and unset.
var paygBillingProviders = map[string]bool{"stripe": true, "whmcs": true}

// ErrPAYGChangeOnResume fires when --payg is asked of a resume whose overlay
// has no PAYG wiring at all. A resume skips the scaffold; turning PAYG on for
// an installed cluster is a day-2 change.
var ErrPAYGChangeOnResume = errors.New("init: --payg cannot be added to an existing overlay by a resume")

// ErrPAYGIdentity fires when an overlay's PAYG installation UID does not
// belong to this installation. The CLI refuses instead of regenerating it:
// replacing an identity re-identifies every usage fact already recorded.
var ErrPAYGIdentity = errors.New("init: the PAYG installation identity does not belong to this installation")

// ErrPAYGRefused fires when PAYG is asked of an installation that does not
// bill through Kube-DC.
var ErrPAYGRefused = errors.New("init: PAYG refused for this installation")

// paygNow and paygNewUID are seams for tests; production uses the wall clock
// and crypto/rand.
var (
	paygNow    = time.Now
	paygNewUID = newPAYGInstallationUID
)

var (
	// paygImageRegex requires a digest-pinned reference: a tag alone can be
	// moved under an installation that bills from what the image measures.
	paygImageRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9._/-]*[a-z0-9])?(:[A-Za-z0-9._-]+)?@sha256:[a-f0-9]{64}$`)
	// paygUUIDRegex accepts any RFC 4122 textual UUID for an EXISTING value;
	// new ones are always v4.
	paygUUIDRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// paygSOPSValueRegex is one SOPS-encrypted scalar.
	paygSOPSValueRegex = regexp.MustCompile(`^ENC\[AES256_GCM,data:[^\]]*\]$`)
)

// newPAYGInstallationUID returns a random (version 4) UUID from crypto/rand.
func newPAYGInstallationUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate installation UID: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// paygProductsRevision names the installation's first resource-products
// statement: <cluster>-<yyyymmdd>. A nested fleet name (eu/dc1) becomes
// eu-dc1 so the revision stays a single token.
func paygProductsRevision(clusterName string, now time.Time) string {
	return strings.ReplaceAll(clusterName, "/", "-") + "-" + now.UTC().Format("20060102")
}

// paygBinding is the installation the UID was minted for: the fleet cluster
// name plus the domain it was scaffolded with.
func paygBinding(clusterName, domain string) string {
	return clusterName + "/" + strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

// paygBillingRefusal returns why an installation with this provider and these
// configuration keys must not run PAYG, or "" when it may.
func paygBillingRefusal(provider string, keys []string) string {
	for _, k := range keys {
		if strings.HasPrefix(k, paygPartnerMarkerPrefix) {
			return fmt.Sprintf("%s marks an installation billed by a partner, which never runs Kube-DC PAYG", k)
		}
	}
	p := strings.ToLower(strings.TrimSpace(provider))
	if paygBillingProviders[p] {
		return ""
	}
	have := billingProviderKey + " is not set"
	if p != "" {
		have = billingProviderKey + "=" + p
	}
	return have + "; PAYG runs only on an installation that bills through Kube-DC — set " + billingProviderKey + "=stripe or whmcs for this installation (--set)"
}

// validatePAYG is the cobra-time half of the PAYG contract. The switch and the
// identity have one owner each (--payg and the scaffold writer); --set would
// give either a second, unvalidated source.
func validatePAYG(o *InitOptions) []string {
	var errs []string
	if _, ok := o.Sets[PAYGEnabledKey]; ok {
		errs = append(errs, "--set "+PAYGEnabledKey+" is not accepted — use --payg, which also scaffolds the metering bundle, the installation UID and its database login (the chart switch alone points the console at a billing service that does not exist)")
	}
	for _, k := range []string{PAYGInstallationUIDKey, PAYGInstallationBindingKey} {
		if _, ok := o.Sets[k]; ok {
			errs = append(errs, "--set "+k+" is not accepted — --payg generates the installation identity once, and it is never copied from another installation (it is part of every usage fact's identity)")
		}
	}
	if v, ok := o.Sets[PAYGMeteringImageKey]; ok && v != "" && !paygImageRegex.MatchString(v) {
		errs = append(errs, fmt.Sprintf("--set %s=%q must be a digest-pinned image reference (repo:tag@sha256:<64 hex>)", PAYGMeteringImageKey, v))
	}
	if o.PAYG {
		if reason := paygBillingRefusal(o.Sets[billingProviderKey], SpecOrderedKeys(o.Sets)); reason != "" {
			errs = append(errs, "--payg is refused: "+reason)
		}
	}
	return errs
}

// paygLayerYAML is clusters/<name>/payg.yaml, as platform/payg/README.md
// specifies it.
const paygLayerYAML = `# PAYG usage metering and the billing service (opt-in; written by
# ` + "`kube-dc bootstrap init --payg`" + `). Pulls in the shared bundle platform/payg,
# configured by the PAYG_* keys in cluster-config.env — see
# platform/payg/README.md. The database login is metering-db.enc.yaml and the
# CNPG role is the "PAYG metering database role" patch in platform.yaml.
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: payg
  namespace: flux-system
spec:
  dependsOn:
    - name: platform
  interval: 10m
  retryInterval: 2m
  timeout: 10m
  path: ./platform/payg
  prune: true
  wait: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: cluster-config
`

// renderPAYGDBSecret is the plaintext of metering-db.enc.yaml. CNPG reads
// username/password for the managed role; the collector reads uri.
func renderPAYGDBSecret(dbCluster, password string) string {
	uri := "postgresql://" + paygDBRole + ":" + password + "@" + dbCluster + "-rw." + paygNamespace +
		".svc:5432/" + paygDBName + "?sslmode=verify-full&sslrootcert=/etc/postgres/ca.crt"
	return `# PAYG metering database login. Generated by ` + "`kube-dc bootstrap init --payg`" + `;
# SOPS-encrypted with this fleet's age recipients.
#
# CNPG sets the password of the managed role ` + paygDBRole + ` from
# username/password (the "PAYG metering database role" patch in platform.yaml);
# the collector connects with uri. The ` + paygNamespace + ` namespace exists before
# the first reconcile: flux-install.sh installs the prometheus-operator CRDs
# into it.
apiVersion: v1
kind: Secret
metadata:
    name: ` + paygDBSecretName + `
    namespace: ` + paygNamespace + `
type: kubernetes.io/basic-auth
stringData:
    username: "` + paygDBRole + `"
    password: "` + password + `"
    uri: "` + uri + `"
`
}

// paygRoleSpec is the managed role the collector logs in with.
func paygRoleSpec(connectionLimit int) string {
	return "name: " + paygDBRole + "\n" +
		"ensure: present\n" +
		"login: true\n" +
		"superuser: false\n" +
		"createdb: false\n" +
		"createrole: false\n" +
		"connectionLimit: " + strconv.Itoa(connectionLimit) + "\n" +
		"passwordSecret:\n" +
		"  name: " + paygDBSecretName + "\n"
}

// indentLines prefixes every non-empty line of s.
func indentLines(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// paygManagedShape says what the base CNPG Cluster already declares under
// spec.managed, which decides the JSON6902 path: append to an existing roles
// list, create the list, or create the whole managed block.
type paygManagedShape int

const (
	paygNoManaged paygManagedShape = iota
	paygManagedNoRoles
	paygManagedRoles
)

// renderPAYGRolePatch is the platform.yaml spec.patches entry. It never
// replaces what the base Cluster declares: with an existing roles list it
// appends (/spec/managed/roles/-); it creates the list, or the managed
// block, only when the base has none.
func renderPAYGRolePatch(dbCluster string, connectionLimit int, shape paygManagedShape) string {
	role := paygRoleSpec(connectionLimit)
	var op string
	switch shape {
	case paygManagedRoles:
		op = "- op: add\n  path: /spec/managed/roles/-\n  value:\n" + indentLines(role, "    ")
	case paygManagedNoRoles:
		op = "- op: add\n  path: /spec/managed/roles\n  value:\n" + indentLines("- "+strings.ReplaceAll(strings.TrimRight(role, "\n"), "\n", "\n  ")+"\n", "    ")
	default:
		op = "- op: add\n  path: /spec/managed\n  value:\n    roles:\n" + indentLines("- "+strings.ReplaceAll(strings.TrimRight(role, "\n"), "\n", "\n  ")+"\n", "      ")
	}
	return "    " + paygDBRoleMarker + "\n" +
		"    # The PAYG collector's own PostgreSQL login (password in " + paygDBSecretsFileName + "),\n" +
		"    # added to the roles the base Cluster declares without replacing them.\n" +
		"    - target:\n" +
		"        group: postgresql.cnpg.io\n" +
		"        kind: Cluster\n" +
		"        name: " + dbCluster + "\n" +
		"        namespace: " + paygNamespace + "\n" +
		"      patch: |\n" +
		indentLines(op, "        ")
}

// patchPlatformPAYGRole appends the role entry to platform.yaml's
// spec.patches, creating the list when absent. Composition is structural,
// not marker-based: add-cluster.sh already emits a patches list (the
// managed-K8s backend values) with no kube-dc marker, and appending at EOF is
// only valid while `patches:` is the LAST key of spec. Conflicting patches of
// spec.managed are refused before this runs (paygRoleWiring).
func patchPlatformPAYGRole(entry string) func([]string) ([]string, bool, error) {
	return func(lines []string) ([]string, bool, error) {
		for _, l := range lines {
			if strings.Contains(l, paygDBRoleMarker) {
				return lines, false, nil // already wired
			}
		}
		end := len(lines)
		for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		patchesAt := -1
		for i := 0; i < end; i++ {
			t := strings.TrimRight(lines[i], " ")
			if t == "  patches:" || strings.HasPrefix(t, "  patches: #") {
				patchesAt = i
			}
		}
		if patchesAt >= 0 {
			for _, l := range lines[patchesAt+1 : end] {
				if strings.TrimSpace(l) == "" {
					continue
				}
				if indent := len(l) - len(strings.TrimLeft(l, " ")); indent <= 2 {
					return nil, false, fmt.Errorf("spec.patches is not the last key of spec (hand-edited?) — add the PAYG role patch by hand (platform/payg/README.md step 2, marker %q)", paygDBRoleMarker)
				}
			}
		}
		block := strings.TrimRight(entry, "\n")
		if patchesAt < 0 {
			block = "  patches:\n" + block
		}
		out := make([]string, 0, end+32)
		out = append(out, lines[:end]...)
		out = append(out, strings.Split(block, "\n")...)
		out = append(out, "")
		return out, true, nil
	}
}

// paygEnvValue reads a cluster-config.env value without its inline comment.
func paygEnvValue(env *config.Env, key string) string {
	v, _ := env.Get(key)
	return stripInlineComment(v)
}

// paygUIDElsewhere returns a file in ANOTHER cluster directory of the fleet
// that carries uid, or "". Ownership is the nearest ancestor directory with a
// cluster-config.env, so a nested cluster (eu/dc1) inside eu/ is still
// "another cluster" for eu.
func paygUIDElsewhere(fleetRepo, clusterDir, uid string) (string, error) {
	clustersRoot := filepath.Join(fleetRepo, "clusters")
	owner := map[string]string{} // directory → owning cluster directory ("" = none)
	ownerOf := func(dir string) string {
		if o, ok := owner[dir]; ok {
			return o
		}
		o := ""
		for d := dir; strings.HasPrefix(d, clustersRoot); d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "cluster-config.env")); err == nil {
				o = d
				break
			}
			if d == clustersRoot {
				break
			}
		}
		owner[dir] = o
		return o
	}
	var hit string
	err := filepath.WalkDir(clustersRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if hit != "" {
			return fs.SkipAll
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".env") || strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			return nil
		}
		if ownerOf(filepath.Dir(path)) == clusterDir {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 4<<20 {
			return nil
		}
		body, err := os.ReadFile(path)
		if err == nil && bytes.Contains(body, []byte(uid)) {
			rel, _ := filepath.Rel(fleetRepo, path)
			hit = rel
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan the fleet for the installation UID: %w", err)
	}
	return hit, nil
}

// checkPAYGIdentity verifies that the overlay's UID (if any) belongs to this
// installation: a well-formed UUID, bound to <name>/<domain>, and present in
// no other cluster directory. It never repairs anything.
func checkPAYGIdentity(fleetRepo, clusterName, domain string, env *config.Env) error {
	uid := paygEnvValue(env, PAYGInstallationUIDKey)
	have := paygEnvValue(env, PAYGInstallationBindingKey)
	want := paygBinding(clusterName, domain)
	switch {
	case uid == "" && have == "":
		return nil
	case uid == "":
		return fmt.Errorf("%w: %s=%s is set without %s — fix clusters/%s/cluster-config.env by hand", ErrPAYGIdentity, PAYGInstallationBindingKey, have, PAYGInstallationUIDKey, clusterName)
	case !paygUUIDRegex.MatchString(uid):
		return fmt.Errorf("%w: %s=%q is not a UUID — fix it by hand; the CLI never replaces an installation's identity", ErrPAYGIdentity, PAYGInstallationUIDKey, uid)
	case have == "":
		return fmt.Errorf("%w: %s=%s has no %s, so nothing says it was minted for this installation (a copied overlay?). If it is this installation's own, add %s=%s by hand; never reuse another installation's UID",
			ErrPAYGIdentity, PAYGInstallationUIDKey, uid, PAYGInstallationBindingKey, PAYGInstallationBindingKey, want)
	case have != want:
		return fmt.Errorf("%w: %s=%s is bound to %s, and this installation is %s (a copied overlay?). Remove the PAYG identity from this overlay rather than reuse it",
			ErrPAYGIdentity, PAYGInstallationUIDKey, uid, have, want)
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	dup, err := paygUIDElsewhere(fleetRepo, clusterDir, uid)
	if err != nil {
		return err
	}
	if dup != "" {
		return fmt.Errorf("%w: %s=%s also appears in %s — two installations must never share a UID", ErrPAYGIdentity, PAYGInstallationUIDKey, uid, dup)
	}
	return nil
}

// WritePAYG wires PAYG usage billing into a freshly scaffolded overlay, or
// completes a partial PAYG scaffold on a resume. A no-op when disabled.
func WritePAYG(fleetRepo, clusterName, domain string, enabled bool, out io.Writer) error {
	_, err := writePAYG(fleetRepo, clusterName, domain, enabled, out)
	return err
}

// writePAYG reports whether it changed the overlay. Idempotent: the existing
// identity, revision, layer file, login and role patch are all kept, so a
// re-run never re-identifies the installation or rotates its database
// password; missing pieces are written.
//
// Same staged flow as the other secret writers: validate and stage every
// plain-file change in memory, write the SOPS artifact, then commit the
// staged writes. A failure after the login was minted removes it again, so a
// failed run leaves the overlay as it found it.
func writePAYG(fleetRepo, clusterName, domain string, enabled bool, out io.Writer) (bool, error) {
	if out == nil {
		out = io.Discard
	}
	if !enabled {
		return false, nil
	}
	if strings.TrimSpace(domain) == "" {
		return false, fmt.Errorf("payg: no domain to bind the installation identity to")
	}
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)

	// PHASE 1 — every precondition, before touching a file.
	if _, err := os.Stat(filepath.Join(fleetRepo, paygBundlePath, "kustomization.yaml")); err != nil {
		return false, fmt.Errorf("payg: the fleet checkout has no %s/kustomization.yaml — --payg needs a fleet-starter that ships the PAYG bundle (update the starter/checkout to the CLI release): %w", paygBundlePath, err)
	}
	envPath := filepath.Join(clusterDir, "cluster-config.env")
	env, err := config.LoadEnv(envPath)
	if err != nil {
		return false, fmt.Errorf("payg: %w", err)
	}
	// The rendered configuration, not only the reviewed flags: this is the
	// second enforcement point of the billing rule.
	if reason := paygBillingRefusal(paygEnvValue(env, billingProviderKey), env.Keys()); reason != "" {
		return false, fmt.Errorf("payg: %w: %s (clusters/%s/cluster-config.env)", ErrPAYGRefused, reason, clusterName)
	}
	image := paygEnvValue(env, PAYGMeteringImageKey)
	if image == "" {
		return false, fmt.Errorf("payg: %s is not pinned — this starter's release pins predate PAYG; pass --set %s=shalb/kube-dc-metering:<tag>@sha256:<digest> (check the digest on Docker Hub first)", PAYGMeteringImageKey, PAYGMeteringImageKey)
	}
	if !paygImageRegex.MatchString(image) {
		return false, fmt.Errorf("payg: %s=%q is not digest-pinned (want repo:tag@sha256:<64 hex>)", PAYGMeteringImageKey, image)
	}
	if err := checkPAYGIdentity(fleetRepo, clusterName, domain, env); err != nil {
		return false, fmt.Errorf("payg: %w", err)
	}
	uid := paygEnvValue(env, PAYGInstallationUIDKey)
	newUID := uid == ""
	if newUID {
		if uid, err = paygNewUID(); err != nil {
			return false, fmt.Errorf("payg: %w", err)
		}
		if dup, err := paygUIDElsewhere(fleetRepo, clusterDir, uid); err != nil || dup != "" {
			return false, fmt.Errorf("payg: generated UID collides with %s (%v) — re-run", dup, err)
		}
	}
	binding := paygBinding(clusterName, domain)
	revision := paygEnvValue(env, PAYGProductsRevisionKey)
	if revision == "" {
		revision = paygProductsRevision(clusterName, paygNow())
	}
	dbCluster := paygEnvValue(env, paygDBClusterKey)
	if dbCluster == "" {
		dbCluster = paygDefaultDBCluster
	}
	if !k8sNodeNameRegex.MatchString(dbCluster) {
		return false, fmt.Errorf("payg: %s=%q is not a valid CNPG cluster name", paygDBClusterKey, dbCluster)
	}
	connLimit := paygDefaultConnectionLimit
	// Existing installations without an explicit setting retain their
	// original eight-connection role. New installations record their larger
	// limit so a later resume and the Database bundle see the same value.
	if newUID && paygEnvValue(env, paygConnectionLimitKey) == "" {
		connLimit = paygNewConnectionLimit
	}
	if v := paygEnvValue(env, paygConnectionLimitKey); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return false, fmt.Errorf("payg: %s=%q must be an integer from 1 to 1000", paygConnectionLimitKey, v)
		}
		connLimit = n
	}

	secretPath := filepath.Join(clusterDir, paygDBSecretsFileName)
	mintSecret := true
	if fi, err := os.Lstat(secretPath); err == nil {
		if !fi.Mode().IsRegular() {
			return false, fmt.Errorf("payg: %s exists but is not a regular file", secretPath)
		}
		b, err := os.ReadFile(secretPath)
		if err != nil {
			return false, fmt.Errorf("payg: read %s: %w", secretPath, err)
		}
		if err := checkPAYGDBSecret(b); err != nil {
			return false, fmt.Errorf("payg: %s is not a SOPS-encrypted login (%v) — refusing to keep or overwrite it; inspect and remove it by hand", secretPath, err)
		}
		mintSecret = false // never rotate the login on a re-run
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("payg: stat %s: %w", secretPath, err)
	}

	type stagedWrite struct {
		path     string
		original []byte // nil = the file does not exist yet
		content  string
	}
	var writes []stagedWrite
	stage := func(path string, patch func([]string) ([]string, bool, error)) error {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		patched, changed, err := patch(strings.Split(string(body), "\n"))
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if changed {
			writes = append(writes, stagedWrite{path, body, strings.Join(patched, "\n")})
		}
		return nil
	}
	if err := stage(filepath.Join(clusterDir, "kustomization.yaml"), func(lines []string) ([]string, bool, error) {
		changed := false
		for _, res := range []string{paygLayerFileName, paygDBSecretsFileName} {
			next, c, err := patchKustomizationResource(res)(lines)
			if err != nil {
				return nil, false, err
			}
			lines, changed = next, changed || c
		}
		return lines, changed, nil
	}); err != nil {
		return false, fmt.Errorf("payg: %w", err)
	}
	platformPath := filepath.Join(clusterDir, "platform.yaml")
	platformBody, err := os.ReadFile(platformPath)
	if err != nil {
		// add-cluster.sh writes platform.yaml unconditionally; its absence
		// means the scaffold shape changed under us.
		return false, fmt.Errorf("payg: %s: %w — cannot declare the CNPG role", platformPath, err)
	}
	// The shared tree and the base Cluster are inspected on every write,
	// wired or not; the role is wired only by the exact patch the CLI renders
	// for that base, and any other patch that might select the Cluster
	// refuses.
	shape, err := findPAYGBaseCluster(fleetRepo, dbCluster)
	if err != nil {
		return false, fmt.Errorf("payg: %w", err)
	}
	wantRole, err := paygExpectedRolePatch(dbCluster, connLimit, shape)
	if err != nil {
		return false, fmt.Errorf("payg: %w", err)
	}
	roleWired, err := paygRoleWiring(platformBody, dbCluster, wantRole)
	if err != nil {
		return false, fmt.Errorf("payg: %w", err)
	}
	if !roleWired {
		if err := stage(platformPath, patchPlatformPAYGRole(renderPAYGRolePatch(dbCluster, connLimit, shape))); err != nil {
			return false, fmt.Errorf("payg: %w", err)
		}
	}
	if err := paygClusterBuildConflicts(fleetRepo, clusterDir); err != nil {
		return false, fmt.Errorf("payg: %w", err)
	}
	layerPath := filepath.Join(clusterDir, paygLayerFileName)
	if body, err := os.ReadFile(layerPath); os.IsNotExist(err) {
		writes = append(writes, stagedWrite{layerPath, nil, paygLayerYAML})
	} else if err != nil {
		return false, fmt.Errorf("payg: read %s: %w", layerPath, err)
	} else if err := checkPAYGLayer(body); err != nil {
		// Never clobbered, and never customised: anything beyond what the CLI
		// renders could override the identity or the image.
		return false, fmt.Errorf("payg: %s is not the PAYG layer: %v", layerPath, err)
	}

	envChanged := paygEnvValue(env, PAYGEnabledKey) != "true" || newUID ||
		paygEnvValue(env, PAYGInstallationBindingKey) != binding ||
		paygEnvValue(env, PAYGProductsRevisionKey) != revision

	// PHASE 2 — the SOPS artifact.
	if mintSecret {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return false, fmt.Errorf("payg: generate database password: %w", err)
		}
		password := hex.EncodeToString(raw) // URI- and YAML-safe as-is
		if err := sopsEncryptToFile(fleetRepo, secretPath, renderPAYGDBSecret(dbCluster, password), []string{password}); err != nil {
			return false, fmt.Errorf("payg: %w", err)
		}
		enc, err := os.ReadFile(secretPath)
		if err == nil {
			err = checkPAYGDBSecret(enc)
		}
		if err != nil {
			_ = os.Remove(secretPath)
			return false, fmt.Errorf("payg: sops output for %s is not a fully encrypted login (%v) — check .sops.yaml encrypted_regex", paygDBSecretsFileName, err)
		}
	}
	// A failure from here on must not leave a login this run minted behind:
	// the retry would then find an overlay it did not write.
	fail := func(err error) (bool, error) {
		if mintSecret {
			_ = os.Remove(secretPath)
		}
		return false, err
	}

	// PHASE 3 — the staged plain-file writes, re-read first so a cooperating
	// writer that changed a target during the SOPS shell-out surfaces as an
	// error instead of being silently overwritten.
	for _, w := range writes {
		current, err := os.ReadFile(w.path)
		switch {
		case w.original == nil && err == nil:
			return fail(fmt.Errorf("payg: %s appeared while init was running — re-run init against the current state", filepath.Base(w.path)))
		case w.original == nil && !os.IsNotExist(err):
			return fail(fmt.Errorf("payg: stat %s: %w", w.path, err))
		case w.original != nil && err != nil:
			return fail(fmt.Errorf("payg: re-read %s: %w", w.path, err))
		case w.original != nil && string(current) != string(w.original):
			return fail(fmt.Errorf("payg: %s changed while init was running — re-run init against the current state", filepath.Base(w.path)))
		}
	}
	for _, w := range writes {
		if err := os.WriteFile(w.path, []byte(w.content), 0o644); err != nil {
			return fail(fmt.Errorf("payg: write %s: %w", w.path, err))
		}
	}
	if envChanged {
		env.Set(PAYGEnabledKey, "true")
		env.Set(PAYGInstallationUIDKey, uid)
		env.Set(PAYGInstallationBindingKey, binding)
		env.Set(PAYGProductsRevisionKey, revision)
		if newUID && paygEnvValue(env, paygConnectionLimitKey) == "" {
			env.Set(paygConnectionLimitKey, strconv.Itoa(connLimit))
		}
		if err := env.Write(""); err != nil {
			return fail(fmt.Errorf("payg: write cluster-config.env: %w", err))
		}
	}
	changed := mintSecret || len(writes) > 0 || envChanged
	if !changed {
		return false, nil
	}

	uidNote := "kept"
	if newUID {
		uidNote = "generated"
	}
	fmt.Fprintf(out, "[scaffold] PAYG usage billing wired: %s (./%s), %s=true, %s=%s (%s, bound to %s — record it, it never changes), %s=%s, %s=%s\n",
		paygLayerFileName, paygBundlePath, PAYGEnabledKey, PAYGInstallationUIDKey, uid, uidNote, binding,
		PAYGProductsRevisionKey, revision, PAYGMeteringImageKey, image)
	if mintSecret {
		fmt.Fprintf(out, "[scaffold] PAYG database login minted: %s/%s (SOPS, %s) + CNPG managed role %s on %s in platform.yaml\n",
			paygNamespace, paygDBSecretName, paygDBSecretsFileName, paygDBRole, dbCluster)
	}
	fmt.Fprintf(out, "[scaffold] PAYG next: PAYG_REGION and PAYG_DECLARED_PRODUCERS are per-installation choices (cluster-config.env); once Flux reconciles `payg`, the collector logs `billing schema migrated` and `billing API serving addr=:8443` (platform/payg/README.md)\n")
	return true, nil
}

// paygOverlayState is what a resume finds of the PAYG contract in an overlay.
type paygOverlayState struct {
	present bool     // any PAYG piece exists
	missing []string // pieces writePAYG can complete
	// legacy: PAYG_ENABLED=true with no CLI-minted identity — a hand-made or
	// pre-CLI setup. Completing it would mint a SECOND identity for an
	// installation that already bills, so it is never completed.
	legacy bool
}

// inspectPAYGOverlay reads every PAYG piece of an overlay and checks each one
// that is present, structurally: the identity (binding + uniqueness), the
// billing rule, the login Secret as exactly one fully encrypted document,
// payg.yaml as the bundle's Flux Kustomization, the kustomization entries
// (parsed), and the CNPG role as a real patch — never the marker comment.
// Problems writePAYG can complete are reported in missing; anything it must
// not repair is an error.
func inspectPAYGOverlay(fleetRepo, clusterName, domain string, env *config.Env) (paygOverlayState, error) {
	var st paygOverlayState
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	enabled := strings.EqualFold(paygEnvValue(env, PAYGEnabledKey), "true")
	uid := paygEnvValue(env, PAYGInstallationUIDKey)
	binding := paygEnvValue(env, PAYGInstallationBindingKey)
	layer, layerErr := os.ReadFile(filepath.Join(clusterDir, paygLayerFileName))
	secret, secretErr := os.ReadFile(filepath.Join(clusterDir, paygDBSecretsFileName))
	kust, _ := os.ReadFile(filepath.Join(clusterDir, "kustomization.yaml"))
	listsLayer := paygKustomizationHas(kust, paygLayerFileName)
	listsSecret := paygKustomizationHas(kust, paygDBSecretsFileName)
	dbCluster := paygEnvValue(env, paygDBClusterKey)
	if dbCluster == "" {
		dbCluster = paygDefaultDBCluster
	}
	platform, platformErr := os.ReadFile(filepath.Join(clusterDir, "platform.yaml"))

	// Presence is detected generously (a mention of the role counts): it only
	// decides whether the checks below run, and running them is the safe side.
	st.present = enabled || uid != "" || binding != "" || layerErr == nil || secretErr == nil ||
		listsLayer || listsSecret || bytes.Contains(platform, []byte(paygDBRole))
	if !st.present {
		return st, nil
	}
	// Every present piece is checked, --payg or not.
	if err := checkPAYGIdentity(fleetRepo, clusterName, domain, env); err != nil {
		return st, err
	}
	if reason := paygBillingRefusal(paygEnvValue(env, billingProviderKey), env.Keys()); reason != "" {
		return st, fmt.Errorf("%w: clusters/%s carries PAYG, but %s", ErrPAYGRefused, clusterName, reason)
	}
	if secretErr == nil {
		if err := checkPAYGDBSecret(secret); err != nil {
			return st, fmt.Errorf("%w: clusters/%s/%s is not a single, fully encrypted login (%v) — inspect and remove it by hand", ErrPAYGRefused, clusterName, paygDBSecretsFileName, err)
		}
	}
	if layerErr == nil {
		if err := checkPAYGLayer(layer); err != nil {
			return st, fmt.Errorf("%w: clusters/%s/%s is not the PAYG layer: %v", ErrPAYGRefused, clusterName, paygLayerFileName, err)
		}
		// The layer consumes the image: once it is wired, the pin must be
		// digest-pinned. A resume does not apply --set, so this is by hand.
		if img := paygEnvValue(env, PAYGMeteringImageKey); !paygImageRegex.MatchString(img) {
			return st, fmt.Errorf("%w: %s=%q in clusters/%s/cluster-config.env is not a digest-pinned image — set it by hand, a resume does not apply --set", ErrPAYGRefused, PAYGMeteringImageKey, img, clusterName)
		}
	}

	if platformErr != nil {
		return st, fmt.Errorf("%w: clusters/%s/platform.yaml: %v", ErrPAYGRefused, clusterName, platformErr)
	}
	connLimit := paygDefaultConnectionLimit
	if v := paygEnvValue(env, paygConnectionLimitKey); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return st, fmt.Errorf("%w: %s=%q must be an integer from 1 to 1000", ErrPAYGRefused, paygConnectionLimitKey, v)
		}
		connLimit = n
	}
	// The shared tree and base Cluster, on every resume, wired or not.
	shape, err := findPAYGBaseCluster(fleetRepo, dbCluster)
	if err != nil {
		return st, fmt.Errorf("%w: %v", ErrPAYGRefused, err)
	}
	wantRole, err := paygExpectedRolePatch(dbCluster, connLimit, shape)
	if err != nil {
		return st, err
	}
	roleWired, roleErr := paygRoleWiring(platform, dbCluster, wantRole)
	if err := paygClusterBuildConflicts(fleetRepo, clusterDir); err != nil {
		return st, fmt.Errorf("%w: %v", ErrPAYGRefused, err)
	}
	if roleErr != nil {
		return st, fmt.Errorf("%w: clusters/%s: %v", ErrPAYGRefused, clusterName, roleErr)
	}

	missing := func(ok bool, what string) {
		if !ok {
			st.missing = append(st.missing, what)
		}
	}
	missing(enabled, PAYGEnabledKey+"=true")
	missing(uid != "", "installation identity ("+PAYGInstallationUIDKey+" + "+PAYGInstallationBindingKey+")")
	missing(paygEnvValue(env, PAYGProductsRevisionKey) != "", PAYGProductsRevisionKey)
	missing(layerErr == nil, paygLayerFileName)
	missing(secretErr == nil, paygDBSecretsFileName)
	missing(listsLayer, "kustomization.yaml entry "+paygLayerFileName)
	missing(listsSecret, "kustomization.yaml entry "+paygDBSecretsFileName)
	missing(roleWired, "CNPG role patch in platform.yaml")
	st.legacy = enabled && uid == ""
	return st, nil
}

// CheckPAYGOnResume guards a resume, which skips the scaffold.
//
//   - Whenever the overlay carries any PAYG piece, every present piece is
//     checked — identity, billing rule, login encryption, layer and role
//     wiring — --payg or not: a resume must never push PAYG onto an
//     installation that does not bill through Kube-DC, a readable login, or
//     another installation's identity.
//   - --payg only authorizes COMPLETING a partial setup. It is refused for an
//     overlay with no PAYG at all (turning PAYG on for an installed cluster is
//     a day-2 change) and for a hand-made setup with no CLI-minted identity.
//     complete=true means pieces are missing that writePAYG can finish.
func CheckPAYGOnResume(fleetRepo, clusterName, domain string, requested bool) (complete bool, err error) {
	clusterDir := filepath.Join(fleetRepo, "clusters", clusterName)
	env, err := config.LoadEnv(filepath.Join(clusterDir, "cluster-config.env"))
	if err != nil {
		if requested {
			return false, fmt.Errorf("%w: cannot read clusters/%s/cluster-config.env (%v)", ErrPAYGChangeOnResume, clusterName, err)
		}
		for _, f := range []string{paygLayerFileName, paygDBSecretsFileName} {
			if _, statErr := os.Stat(filepath.Join(clusterDir, f)); statErr == nil {
				return false, fmt.Errorf("%w: clusters/%s carries %s but its cluster-config.env cannot be read (%v), so the billing rule cannot be checked", ErrPAYGRefused, clusterName, f, err)
			}
		}
		return false, nil // no PAYG here; other guards report the overlay
	}
	st, err := inspectPAYGOverlay(fleetRepo, clusterName, domain, env)
	if err != nil {
		return false, err
	}
	if !requested {
		return false, nil
	}
	if !st.present {
		return false, fmt.Errorf("%w: clusters/%s already exists without PAYG, and a resume writes nothing. Enable PAYG on an installed cluster as a day-2 change (platform/payg/README.md: cluster-config.env keys, the database login, then payg.yaml), or re-run without --payg to finish the resume",
			ErrPAYGChangeOnResume, clusterName)
	}
	if len(st.missing) == 0 {
		return false, nil
	}
	if st.legacy {
		return false, fmt.Errorf("%w: clusters/%s has %s=true but no installation identity minted by the CLI (a hand-made or earlier PAYG setup); completing it would mint a second identity for an installation that already bills. Hand it over by hand (platform/payg/README.md), or re-run without --payg",
			ErrPAYGChangeOnResume, clusterName, PAYGEnabledKey)
	}
	return true, nil
}

// CompletePAYGOnResume finishes a partial PAYG scaffold and then requires the
// whole contract to hold. It reports whether it changed the overlay.
func CompletePAYGOnResume(fleetRepo, clusterName, domain string, out io.Writer) (bool, error) {
	changed, err := writePAYG(fleetRepo, clusterName, domain, true, out)
	if err != nil {
		return false, err
	}
	env, err := config.LoadEnv(filepath.Join(fleetRepo, "clusters", clusterName, "cluster-config.env"))
	if err != nil {
		return changed, fmt.Errorf("payg: %w", err)
	}
	st, err := inspectPAYGOverlay(fleetRepo, clusterName, domain, env)
	if err != nil {
		return changed, err
	}
	if len(st.missing) > 0 {
		return changed, fmt.Errorf("payg: the PAYG wiring of clusters/%s is still incomplete after completion: %s", clusterName, strings.Join(st.missing, ", "))
	}
	return changed, nil
}
