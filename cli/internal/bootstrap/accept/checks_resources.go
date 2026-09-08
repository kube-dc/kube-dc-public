package accept

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// This file holds the checks that read ordinary Kubernetes objects rather than
// probing the network. Each one exists because a real cluster reached
// `usable` — Flux green, pods Running, front door serving — while the property
// it verifies was false, and the failure surfaced days later somewhere else.

// nested walks a decoded object by dot-path and returns the value at the end.
func nested(obj map[string]any, path ...string) (any, bool) {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func nestedString(obj map[string]any, path ...string) string {
	v, ok := nested(obj, path...)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func nestedSlice(obj map[string]any, path ...string) []any {
	v, ok := nested(obj, path...)
	if !ok {
		return nil
	}
	s, _ := v.([]any)
	return s
}

// nestedInt reads an integer field, tolerating the float64 a decoded JSON
// document may carry.
func nestedInt(obj map[string]any, path ...string) (int64, bool) {
	v, ok := nested(obj, path...)
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	}
	return 0, false
}

// readyCondition returns the status of the Ready condition, and whether that
// condition actually describes the object's CURRENT spec.
//
// A controller writes conditions asynchronously, so immediately after a spec
// change the object still carries the previous generation's verdict. Reading
// Ready=True without checking observedGeneration accepts the OLD state — which
// on an upgrade is precisely the state being replaced, and is how a check like
// this passes a release or a certificate that has not been reconciled yet.
func readyCondition(obj map[string]any) (status string, current bool) {
	gen, hasGen := nestedInt(obj, "metadata", "generation")
	for _, c := range nestedSlice(obj, "status", "conditions") {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		if t, _ := cm["type"].(string); t != "Ready" {
			continue
		}
		status, _ = cm["status"].(string)
		current = true
		// Prefer the condition's own observedGeneration, then the status-level
		// one; when neither is published there is nothing to compare against.
		if obs, ok := nestedInt(cm, "observedGeneration"); ok && hasGen {
			current = obs >= gen
		} else if obs, ok := nestedInt(obj, "status", "observedGeneration"); ok && hasGen {
			current = obs >= gen
		}
		return status, current
	}
	return "", true
}

// objName renders "namespace/name", or just the name for cluster-scoped kinds.
func objName(obj map[string]any) string {
	name := nestedString(obj, "metadata", "name")
	if ns := nestedString(obj, "metadata", "namespace"); ns != "" {
		return ns + "/" + name
	}
	return name
}

// checkDefaultStorageClass verifies that a PersistentVolumeClaim written the way
// every chart and every user writes one — with no storageClassName — will
// actually bind.
//
// This is the failure the package doc names and nothing checked. A cluster with
// no default StorageClass converges perfectly: Flux is green, every controller
// is Running, and the first workload that asks for storage sits in Pending
// forever. The PVC carries no event explaining why, because from the
// scheduler's point of view nothing is wrong — no class was requested and none
// was implied, so there is nothing to provision and nothing to report.
//
// Two defaults is the same failure wearing a different mask: Kubernetes does not
// define which one wins, so identical PVCs land on different backends and the
// cluster works until the day it matters which.
func checkDefaultStorageClass(ctx context.Context, o Options) Check {
	const name = "storage/default-class"
	classes, err := o.K8s.ListResourceObjects(ctx, "storage.k8s.io", "v1", "storageclasses", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list StorageClasses: %v", err)}
	}
	if len(classes) == 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: "the cluster has NO StorageClass at all",
			Fix:    "kubectl get storageclass; the storage layer never installed — every PVC will stay Pending"}
	}
	var defaults []string
	for _, c := range classes {
		ann, _ := nested(c, "metadata", "annotations")
		m, _ := ann.(map[string]any)
		if m == nil {
			continue
		}
		// Both spellings are honoured by Kubernetes; the beta one is still
		// what several storage operators write.
		for _, k := range []string{
			"storageclass.kubernetes.io/is-default-class",
			"storageclass.beta.kubernetes.io/is-default-class",
		} {
			if v, _ := m[k].(string); v == "true" {
				defaults = append(defaults, nestedString(c, "metadata", "name"))
				break
			}
		}
	}
	sort.Strings(defaults)
	switch len(defaults) {
	case 0:
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d StorageClass(es) but NONE is marked default", len(classes)),
			Fix: "kubectl patch storageclass <name> -p " +
				`'{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'` +
				" — a PVC with no storageClassName stays Pending forever and says nothing about why",
		}
	case 1:
		return Check{Name: name, Required: true, Outcome: Pass,
			Detail: fmt.Sprintf("%q is the default of %d class(es)", defaults[0], len(classes))}
	default:
		// Not a hard failure: the DefaultStorageClass admission plugin picks the
		// most recently created default, deterministically, so a cluster
		// mid-migration between backends works. It is still worth surfacing —
		// the winner is decided by creation order, which nobody reads — but it
		// must not block an install that is behaving correctly.
		return Check{Name: name, Outcome: Fail,
			Detail: fmt.Sprintf("%d StorageClasses are marked default: %s", len(defaults), strings.Join(defaults, ", ")),
			Fix: "the newest of them wins; un-default the others so the choice is stated " +
				"rather than decided by creation timestamp"}
	}
}

// checkSopsDecryption catches the install where Flux substituted SOPS ciphertext
// verbatim into the cluster's Secrets and reported success.
//
// When the decryption patch is missing from flux-system, the Kustomization has
// no decryption provider, so it applies the SOPS document as data rather than
// failing. Every Kustomization goes Ready, every HelmRelease installs, and the
// Secrets contain literal ENC[AES256_GCM,...] strings. Nothing downstream can
// authenticate with them, and because Flux is green the operator looks
// everywhere except at the secret values.
//
// It reports the Secret and the KEY only, never any value — a check that prints
// what it found in a Secret is worse than the bug.
// sopsProbeChars bounds how much of a Secret value the SOPS scan decodes. The
// envelope marker is at the very start of the value, so a short prefix decides
// it — and bounding the decode rather than SKIPPING large values means an
// encrypted certificate or keyring is still caught.
const sopsProbeChars = 4096

func checkSopsDecryption(ctx context.Context, o Options) Check {
	const name = "secrets/sops-decryption"
	// Cluster-wide, not just flux-system: the ciphertext ends up wherever the
	// Kustomization writes the Secret, which is the component's own namespace.
	// Scoping this to flux-system would pass the exact cluster it exists to
	// catch.
	//
	// This lists every Secret, which on a very large multi-tenant cluster is the
	// most expensive thing acceptance does. The decode cost is bounded below by
	// skipping the two kinds that are both bulky and incapable of holding
	// substituted config, but the list itself is not — a cluster with tens of
	// thousands of large Secrets will make this slow.
	secrets, err := o.K8s.ListResourceObjects(ctx, "", "v1", "secrets", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list Secrets: %v", err)}
	}
	if len(secrets) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "no Secrets readable to inspect"}
	}
	var bad []string
	scanned := 0
	for _, s := range secrets {
		// Helm stores each release revision as a large gzipped blob. Decoding
		// those is pure cost and they never carry SOPS ciphertext.
		switch nestedString(s, "type") {
		case "helm.sh/release.v1", "kubernetes.io/service-account-token":
			// Release blobs are large and gzipped; SA tokens are generated.
			// Neither can carry a substituted SOPS value.
			continue
		}
		scanned++
		data, _ := nested(s, "data")
		m, _ := data.(map[string]any)
		for k, v := range m {
			enc, _ := v.(string)
			// Decode at most a prefix, trimmed to a whole base64 quantum.
			if len(enc) > sopsProbeChars {
				enc = enc[:sopsProbeChars]
			}
			enc = enc[:len(enc)-len(enc)%4]
			raw, derr := base64.StdEncoding.DecodeString(enc)
			if derr != nil {
				continue
			}
			// The marker is the SOPS ciphertext envelope itself. Matching the
			// envelope rather than a "sops" key also catches a single
			// encrypted value inside an otherwise-plain Secret.
			if strings.Contains(string(raw), "ENC[AES256_GCM") {
				bad = append(bad, objName(s)+":"+k)
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d Secret key(s) hold undecrypted SOPS ciphertext: %s",
				len(bad), strings.Join(bad, ", ")),
			Fix: "flux-system is missing its SOPS decryption patch — check that gotk-patches.yaml " +
				"is in flux-system/kustomization.yaml and that the sops-age Secret exists, then reconcile",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("no SOPS ciphertext in %d Secret(s)", scanned)}
}

// checkClusterConfigPins catches a version pin carrying a trailing comment.
//
// The cluster-config ConfigMap is generated by kustomize's configMapGenerator
// from an env file, and its parser keeps everything after the '=' — including a
// trailing "# rationale". Flux substitutes that whole string into the
// HelmRelease, and Helm's semver parser rejects it with "improper constraint",
// so the release silently stops upgrading while the Kustomization stays Ready.
//
// Image-tag substitutions tolerate the same comment, which is what makes this
// worth checking rather than obvious: the habit looks harmless everywhere it is
// used until it is applied to a chart version.
// isChartVersionPin reports whether a key feeds a Helm chart VERSION constraint
// rather than an image tag.
//
// The distinction decides whether a trailing comment is fatal. Helm parses a
// chart version as a semver constraint and rejects a commented value outright;
// an image tag is only ever a string, and the same comment passes through
// harmlessly. Matching every key containing "VERSION" would block a healthy
// cluster over something like BACKEND_VERSION, which is an image pin.
func isChartVersionPin(key string) bool {
	k := strings.ToUpper(key)
	return strings.HasSuffix(k, "CHART_VERSION") || k == "KUBE_DC_VERSION"
}

func checkClusterConfigPins(ctx context.Context, o Options) Check {
	const name = "config/version-pins"
	cms, err := o.K8s.ListResourceObjects(ctx, "", "v1", "configmaps", "flux-system")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list flux-system ConfigMaps: %v", err)}
	}
	var breaking, cosmetic []string
	found := false
	for _, cm := range cms {
		if nestedString(cm, "metadata", "name") != "cluster-config" {
			continue
		}
		found = true
		data, _ := nested(cm, "data")
		m, _ := data.(map[string]any)
		for k, v := range m {
			val, _ := v.(string)
			// For a chart version ANY '#' is fatal: Helm parses the value as a
			// semver constraint, which never legitimately contains one, so
			// "1.2.3#temporary" fails just as "1.2.3 # temporary" does.
			if isChartVersionPin(k) {
				if strings.Contains(val, "#") {
					breaking = append(breaking, k)
				}
				continue
			}
			// Elsewhere only a '#' preceded by whitespace is a comment; a bare
			// '#' inside a value is legitimate (fragments, generated passwords).
			if i := strings.Index(val, "#"); i > 0 && (val[i-1] == ' ' || val[i-1] == '\t') {
				cosmetic = append(cosmetic, k)
			}
		}
	}
	if !found {
		return Check{Name: name, Outcome: Skipped,
			Detail: "no flux-system/cluster-config ConfigMap on this cluster"}
	}
	sort.Strings(breaking)
	sort.Strings(cosmetic)
	if len(breaking) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("chart-version pin(s) carry an inline comment: %s", strings.Join(breaking, ", ")),
			Fix: "strip the trailing '# ...' from these keys in clusters/<name>/cluster-config.env — " +
				"Helm rejects a commented version as \"improper constraint\" and the release stops upgrading " +
				"while Flux still reports Ready. Put the rationale in the commit message.",
		}
	}
	if len(cosmetic) > 0 {
		return Check{Name: name, Outcome: Fail,
			Detail: fmt.Sprintf("pin(s) carry an inline comment: %s", strings.Join(cosmetic, ", ")),
			Fix:    "harmless for image tags today, but strip them — the same habit breaks any chart-version pin",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: "no cluster-config value carries an inline comment"}
}

// ipRange is a closed address range, used to compare pools.
type ipRange struct{ lo, hi netip.Addr }

// parseIPRange accepts the two forms MetalLB allows in a pool: a CIDR, and an
// inclusive "start-end" range.
func parseIPRange(s string) (ipRange, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return ipRange{}, false
		}
		p = p.Masked()
		lo := p.Addr()
		// Last address in the prefix.
		bs := lo.AsSlice()
		bits := p.Bits()
		for i := bits; i < len(bs)*8; i++ {
			bs[i/8] |= 1 << (7 - uint(i)%8)
		}
		hi, ok := netip.AddrFromSlice(bs)
		if !ok {
			return ipRange{}, false
		}
		return ipRange{lo: lo, hi: hi}, true
	}
	if lo, hi, ok := strings.Cut(s, "-"); ok {
		a, err1 := netip.ParseAddr(strings.TrimSpace(lo))
		b, err2 := netip.ParseAddr(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil {
			return ipRange{}, false
		}
		// Both ends must be the same family: Compare orders IPv4 before IPv6, so
		// a mixed range like 192.0.2.1-2001:db8::1 would otherwise look ordered
		// and valid.
		if a.Is4() != b.Is4() {
			return ipRange{}, false
		}
		// A reversed range is not a range MetalLB will accept, and treating it
		// as valid would silently exclude it from every overlap comparison.
		if a.Compare(b) > 0 {
			return ipRange{}, false
		}
		return ipRange{lo: a, hi: b}, true
	}
	// A bare address is not valid pool syntax — MetalLB wants a CIDR (/32 for a
	// single address) or a start-end range — so it must not be treated as a
	// range this check silently validated.
	return ipRange{}, false
}

func (r ipRange) overlaps(other ipRange) bool {
	if r.lo.Is4() != other.lo.Is4() {
		return false
	}
	return r.lo.Compare(other.hi) <= 0 && other.lo.Compare(r.hi) <= 0
}

// checkLoadBalancerPools catches overlapping MetalLB address pools.
//
// MetalLB validates its pools as a set and rejects the whole configuration when
// two of them claim the same address — it does not drop the offending pool and
// carry on. So adding one overlapping pool takes down address allocation for
// EVERY LoadBalancer Service on the cluster, including the ones that were
// working, and the symptom is Services sitting in <pending> with the cause
// visible only in the controller's log.
//
// The overlap is usually introduced by an example or a second pool written for a
// specific customer range that happens to sit inside the pool the installer
// already created.
func checkLoadBalancerPools(ctx context.Context, o Options) Check {
	const name = "network/loadbalancer-pools"
	pools, err := o.K8s.ListResourceObjects(ctx, "metallb.io", "v1beta1", "ipaddresspools", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list IPAddressPools: %v", err)}
	}
	if len(pools) == 0 {
		return Check{Name: name, Outcome: Skipped,
			Detail: "MetalLB is not in use on this cluster"}
	}
	type entry struct {
		pool string
		r    ipRange
		text string
	}
	var all []entry
	var unparsed []string
	var empty []string
	for _, p := range pools {
		addrs := nestedSlice(p, "spec", "addresses")
		if len(addrs) == 0 {
			// MetalLB rejects a pool that declares no prefixes, and rejecting
			// one pool takes the whole set with it.
			empty = append(empty, objName(p))
		}
		for _, a := range addrs {
			s, _ := a.(string)
			r, ok := parseIPRange(s)
			if !ok {
				unparsed = append(unparsed, objName(p)+":"+s)
				continue
			}
			all = append(all, entry{pool: objName(p), r: r, text: s})
		}
	}
	// Every range is compared with every other, INCLUDING ranges inside the same
	// pool: MetalLB validates addresses as one set, so a pool that overlaps
	// itself takes down allocation exactly as two pools that overlap do.
	var clashes []string
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[i].r.overlaps(all[j].r) {
				clashes = append(clashes, fmt.Sprintf("%s(%s) overlaps %s(%s)",
					all[i].pool, all[i].text, all[j].pool, all[j].text))
			}
		}
	}
	sort.Strings(clashes)
	if len(clashes) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d overlapping address range(s): %s", len(clashes), strings.Join(clashes, "; ")),
			Fix: "MetalLB rejects the whole pool set on an overlap, so NO LoadBalancer Service gets an address — " +
				"narrow one range so they are disjoint",
		}
	}
	if len(empty) > 0 {
		sort.Strings(empty)
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d pool(s) declare no addresses: %s", len(empty), strings.Join(empty, ", ")),
			Fix:    "MetalLB rejects a pool with no prefixes, and that takes the whole pool set with it",
		}
	}
	if len(unparsed) > 0 {
		// Required, not advisory: these are the forms MetalLB itself accepts, so
		// anything this cannot parse is configuration MetalLB will reject —
		// which stops allocation for every Service.
		sort.Strings(unparsed)
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d malformed address entr(ies), NOT checked for overlap: %s",
				len(unparsed), strings.Join(unparsed, ", ")),
			Fix: "MetalLB accepts a CIDR (use /32 for one address) or an inclusive start-end range; " +
				"a bare address and a reversed range are both rejected",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("%d pool(s), %d range(s), no overlap", len(pools), len(all))}
}

// selectorIsEmpty reports whether a label selector constrains nothing.
func selectorIsEmpty(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return true
	}
	if l, _ := m["matchLabels"].(map[string]any); len(l) > 0 {
		return false
	}
	if e, _ := m["matchExpressions"].([]any); len(e) > 0 {
		return false
	}
	return true
}

// webhookIsUnscoped reports whether a webhook applies broadly enough that a
// missing backend certainly blocks writes.
//
// A webhook narrowed by a namespaceSelector, an objectSelector or a
// matchCondition may never be invoked at all — pointed at a namespace that does
// not exist, or gated on an expression that is always false. Deciding that
// properly means evaluating the rules against the cluster, which this cannot do,
// so a scoped webhook is reported without failing the install rather than
// guessed at.
// clusterScopedResources are the built-in kinds that live outside a namespace
// AND that a namespaceSelector therefore cannot constrain. Keys are
// "apiGroup/resource", with the empty group for core.
//
// Qualified by group on purpose: a namespaced CRD is free to call itself
// "nodes.example.com", and matching on the bare plural would treat that
// webhook's selector as inert and report a properly-scoped webhook as blocking.
//
// Note what is NOT here: "namespaces". A Namespace is cluster-scoped, but the
// admission contract makes namespaceSelector match the labels of the Namespace
// OBJECT ITSELF for requests on namespaces — so a selector genuinely does
// constrain such a webhook, and listing it would report a properly-scoped
// webhook as blocking.
//
// A list rather than API discovery: the port has no discovery method. It covers
// the built-in kinds and is deliberately not inverted into a namespaced
// allowlist — most webhook rules name namespaced CRDs, and treating every
// unrecognised resource as cluster-scoped would report those as blocking when a
// selector genuinely constrains them. The residual gap is a cluster-scoped CRD
// nobody listed here, which under-reports rather than crying wolf.
var clusterScopedResources = map[string]bool{
	"/componentstatuses": true,
	"/nodes":             true,
	"/persistentvolumes": true,
	"admissionregistration.k8s.io/mutatingwebhookconfigurations":     true,
	"admissionregistration.k8s.io/validatingadmissionpolicies":       true,
	"admissionregistration.k8s.io/validatingadmissionpolicybindings": true,
	"admissionregistration.k8s.io/validatingwebhookconfigurations":   true,
	"apiextensions.k8s.io/customresourcedefinitions":                 true,
	"apiregistration.k8s.io/apiservices":                             true,
	"cert-manager.io/clusterissuers":                                 true,
	"certificates.k8s.io/certificatesigningrequests":                 true,
	"certificates.k8s.io/clustertrustbundles":                        true,
	"flowcontrol.apiserver.k8s.io/flowschemas":                       true,
	"flowcontrol.apiserver.k8s.io/prioritylevelconfigurations":       true,
	"internal.apiserver.k8s.io/storageversions":                      true,
	"networking.k8s.io/ingressclasses":                               true,
	"networking.k8s.io/ipaddresses":                                  true,
	"networking.k8s.io/servicecidrs":                                 true,
	"node.k8s.io/runtimeclasses":                                     true,
	"rbac.authorization.k8s.io/clusterrolebindings":                  true,
	"rbac.authorization.k8s.io/clusterroles":                         true,
	"resource.k8s.io/deviceclasses":                                  true,
	"resource.k8s.io/resourceslices":                                 true,
	"scheduling.k8s.io/priorityclasses":                              true,
	"snapshot.storage.k8s.io/volumesnapshotclasses":                  true,
	"snapshot.storage.k8s.io/volumesnapshotcontents":                 true,
	"storage.k8s.io/csidrivers":                                      true,
	"storage.k8s.io/csinodes":                                        true,
	"storage.k8s.io/storageclasses":                                  true,
	"storage.k8s.io/volumeattachments":                               true,
	"storage.k8s.io/volumeattributesclasses":                         true,
}

// groupsWithClusterScopedKinds are the API groups that contain at least one
// cluster-scoped kind. It is consulted only for a rule whose resources are "*",
// where the resource name itself says nothing: apiGroups ["apps"] with
// resources ["*"] is entirely namespaced, while ["storage.k8s.io"] is not.
var groupsWithClusterScopedKinds = map[string]bool{
	"*":                            true,
	"":                             true, // core: nodes, persistentvolumes, ...
	"admissionregistration.k8s.io": true,
	"apiextensions.k8s.io":         true,
	"apiregistration.k8s.io":       true,
	"certificates.k8s.io":          true,
	"cert-manager.io":              true,
	"flowcontrol.apiserver.k8s.io": true,
	"internal.apiserver.k8s.io":    true,
	"networking.k8s.io":            true,
	"node.k8s.io":                  true,
	"rbac.authorization.k8s.io":    true,
	"resource.k8s.io":              true,
	"scheduling.k8s.io":            true,
	"snapshot.storage.k8s.io":      true,
	"storage.k8s.io":               true,
}

func webhookIsUnscoped(hm map[string]any) bool {
	// The deciding factor is whether the RULES can match a resource that a
	// namespaceSelector cannot constrain. That is not the scope field on its
	// own: scope "*" and an unset scope both reach cluster-scoped kinds when the
	// rule names one, and even an explicit scope "Cluster" is still selectable
	// when the resource is namespaces.
	matchesClusterScoped := false
	for _, r := range nestedSlice(hm, "rules") {
		rm, _ := r.(map[string]any)
		if rm == nil {
			continue
		}
		scope, _ := rm["scope"].(string)
		if scope == "Namespaced" {
			continue
		}
		for _, res := range nestedSlice(rm, "resources") {
			rs, _ := res.(string)
			// Strip any subresource ("nodes/status" -> "nodes").
			if i := strings.Index(rs, "/"); i >= 0 {
				rs = rs[:i]
			}
			rs = strings.ToLower(rs)
			switch {
			case rs == "namespaces":
				// Checked before scope on purpose: for requests on namespaces
				// the selector matches the Namespace object's own labels, so it
				// constrains the webhook even when the rule says scope Cluster.
				continue
			case scope == "Cluster":
				// Explicitly cluster-scoped, including a CRD not listed below.
				matchesClusterScoped = true
			case rs == "*":
				// A wildcard says nothing on its own; whether it can reach a
				// cluster-scoped kind depends on the groups.
				for _, g := range nestedSlice(rm, "apiGroups") {
					gs, _ := g.(string)
					if groupsWithClusterScopedKinds[strings.ToLower(gs)] {
						matchesClusterScoped = true
						break
					}
				}
			default:
				// Qualified by group, so a namespaced CRD sharing a built-in
				// plural does not inherit its scope.
				for _, g := range nestedSlice(rm, "apiGroups") {
					gs, _ := g.(string)
					if clusterScopedResources[strings.ToLower(gs)+"/"+rs] {
						matchesClusterScoped = true
						break
					}
				}
			}
			if matchesClusterScoped {
				break
			}
		}
		if matchesClusterScoped {
			break
		}
	}
	if !matchesClusterScoped && !selectorIsEmpty(hm["namespaceSelector"]) {
		return false
	}
	if !selectorIsEmpty(hm["objectSelector"]) {
		return false
	}
	if mc, _ := hm["matchConditions"].([]any); len(mc) > 0 {
		return false
	}
	return true
}

// checkAdmissionWebhooks catches a webhook that blocks writes it can never
// answer.
//
// A ValidatingWebhookConfiguration with failurePolicy: Fail whose backing
// Service has no ready endpoints rejects every matching create and update
// cluster-wide. Nothing about that is visible in Flux or in pod status: the
// webhook's own Deployment may be scaled to zero, crash-looping, or simply not
// installed yet, and the resulting error surfaces on unrelated objects as
// "failed calling webhook", usually in whatever workload the user tried next.
//
// Readiness comes from EndpointSlices, with the legacy Endpoints API only as a
// fallback. Endpoints is deprecated and is not the authority on a modern
// cluster; reading it first would report every healthy webhook on such a cluster
// as having no backend, which is the worst outcome a check like this can have.
//
// Only a webhook that is BOTH failurePolicy: Fail and unscoped fails the
// install. Everything else — Ignore policy, or a webhook narrowed by selectors
// this cannot evaluate — is reported without blocking.
func checkAdmissionWebhooks(ctx context.Context, o Options) Check {
	const name = "admission/webhooks"

	// service key -> set of port NAMES that have at least one ready endpoint.
	// Readiness is tracked per port because a multi-port Service can be ready on
	// one port and have nothing listening on another, and the webhook only
	// cares about the one it calls.
	readyPorts := map[string]map[string]bool{}
	slices, err := o.K8s.ListResourceObjects(ctx, "discovery.k8s.io", "v1", "endpointslices", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list EndpointSlices: %v", err)}
	}
	// EndpointSlices are authoritative, with no fallback to the legacy Endpoints
	// API. Endpoints is deprecated, and on a cluster where the EndpointSlice
	// controller has failed the legacy objects can still be present and stale —
	// so falling back to them would report an unreachable webhook as healthy,
	// which is the failure this check exists to catch.
	if len(slices) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "no EndpointSlices exist, so webhook backends cannot be verified",
			Fix:    "kubectl get endpointslices -A; a cluster with Services but no slices has a broken EndpointSlice controller"}
	}
	for _, es := range slices {
		svc := ""
		if lbls, _ := nested(es, "metadata", "labels"); lbls != nil {
			m, _ := lbls.(map[string]any)
			svc, _ = m["kubernetes.io/service-name"].(string)
		}
		if svc == "" {
			continue
		}
		anyReady := false
		for _, e := range nestedSlice(es, "endpoints") {
			em, _ := e.(map[string]any)
			if em == nil {
				continue
			}
			addrs, _ := em["addresses"].([]any)
			if len(addrs) == 0 {
				continue
			}
			// Absent conditions.ready means ready, per the API contract.
			ready := true
			if c, _ := em["conditions"].(map[string]any); c != nil {
				if r, ok := c["ready"].(bool); ok {
					ready = r
				}
			}
			if ready {
				anyReady = true
				break
			}
		}
		if !anyReady {
			continue
		}
		key := nestedString(es, "metadata", "namespace") + "/" + svc
		if readyPorts[key] == nil {
			readyPorts[key] = map[string]bool{}
		}
		// A slice port's name is the SERVICE port's name; an unnamed single
		// port is the empty string on both sides.
		for _, pt := range nestedSlice(es, "ports") {
			pm, _ := pt.(map[string]any)
			if pm == nil {
				continue
			}
			pn, _ := pm["name"].(string)
			readyPorts[key][pn] = true
		}
	}

	// service key -> ports the Service declares.	// service key -> ports the Service declares. The webhook names a SERVICE
	// port, so it is checked against these and not against endpoint ports,
	// which are target ports and routinely differ.
	// The webhook names a SERVICE port; EndpointSlice ports are TARGET ports and
	// routinely differ, so the two are joined through the service port's NAME
	// rather than compared numerically.
	svcPortName := map[string]map[int64]string{}
	svcPortProto := map[string]map[int64]string{}
	svcs, err := o.K8s.ListResourceObjects(ctx, "", "v1", "services", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list Services: %v", err)}
	}
	for _, sv := range svcs {
		key := nestedString(sv, "metadata", "namespace") + "/" + nestedString(sv, "metadata", "name")
		ports := map[int64]string{}
		protos := map[int64]string{}
		for _, pt := range nestedSlice(sv, "spec", "ports") {
			pm, _ := pt.(map[string]any)
			if pm == nil {
				continue
			}
			pn, _ := pm["name"].(string)
			pr, _ := pm["protocol"].(string)
			var num int64
			switch v := pm["port"].(type) {
			case int64:
				num = v
			case float64:
				num = int64(v)
			default:
				continue
			}
			ports[num] = pn
			protos[num] = pr
		}
		svcPortName[key] = ports
		svcPortProto[key] = protos
	}

	var blocking, reported []string
	scan := func(objs []map[string]any) {
		for _, w := range objs {
			cfgName := nestedString(w, "metadata", "name")
			for _, h := range nestedSlice(w, "webhooks") {
				hm, _ := h.(map[string]any)
				if hm == nil {
					continue
				}
				svc, ok := nested(hm, "clientConfig", "service")
				if !ok {
					continue // url-based webhook; nothing local to verify
				}
				sm, _ := svc.(map[string]any)
				ns, _ := sm["namespace"].(string)
				sn, _ := sm["name"].(string)
				if ns == "" || sn == "" {
					continue
				}
				key := ns + "/" + sn
				port := int64(443) // the API default
				switch v := sm["port"].(type) {
				case int64:
					port = v
				case float64:
					port = int64(v)
				}

				var problem string
				declared, svcKnown := svcPortName[key]
				switch {
				case !svcKnown:
					// A ready slice can be left behind by hand or by a deleted
					// Service, so slice readiness alone must not vouch for a
					// Service that does not exist.
					problem = "Service does not exist"
				case len(readyPorts[key]) == 0:
					problem = "no ready endpoints"
				case len(declared) > 0:
					pn, ok := declared[port]
					if !ok {
						problem = fmt.Sprintf("Service declares no port %d", port)
					} else if !readyPorts[key][pn] {
						problem = fmt.Sprintf("port %d has no ready endpoint", port)
					} else if proto := svcPortProto[key][port]; proto != "" && proto != "TCP" {
						// A webhook call is HTTPS over TCP; any other protocol
						// cannot serve it however ready the endpoint looks.
						problem = fmt.Sprintf("port %d is %s, not TCP", port, proto)
					}
				}
				if problem == "" {
					continue
				}
				policy, _ := hm["failurePolicy"].(string)
				where := fmt.Sprintf("%s→%s (%s)", cfgName, key, problem)
				// Fail is also the API default when unset.
				if policy != "Ignore" && webhookIsUnscoped(hm) {
					blocking = append(blocking, where)
				} else {
					reported = append(reported, where)
				}
			}
		}
	}

	vwc, err := o.K8s.ListResourceObjects(ctx, "admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list validating webhooks: %v", err)}
	}
	scan(vwc)
	mwc, err := o.K8s.ListResourceObjects(ctx, "admissionregistration.k8s.io", "v1", "mutatingwebhookconfigurations", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list mutating webhooks: %v", err)}
	}
	scan(mwc)

	sort.Strings(blocking)
	sort.Strings(reported)
	if len(blocking) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d unscoped webhook(s) with failurePolicy=Fail cannot be reached: %s",
				len(blocking), strings.Join(blocking, ", ")),
			Fix: "every matching create/update is being rejected cluster-wide — " +
				"kubectl -n <ns> get pods,endpointslices -l kubernetes.io/service-name=<svc>",
		}
	}
	if len(reported) > 0 {
		return Check{Name: name, Outcome: Fail,
			Detail: fmt.Sprintf("%d webhook(s) cannot be reached: %s", len(reported), strings.Join(reported, ", ")),
			Fix: "these are scoped by selectors, or set failurePolicy=Ignore, so writes are not blocked — " +
				"but the rule each enforces is silently not being enforced",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("all service-backed webhooks in %d configuration(s) are reachable", len(vwc)+len(mwc))}
}

// checkCertificates verifies every cert-manager Certificate actually issued.
//
// The front-door check proves the console's certificate is trusted, which is the
// one an operator looks at. It says nothing about Keycloak, the S3 endpoint, the
// API endpoint or any tenant route, and those are issued by the same ACME
// account against the same DNS. A rate-limited or DNS-blocked issuance leaves
// those Certificates not Ready while the console keeps serving its existing
// certificate, so the install looks finished and individual endpoints fail later
// with a name nobody associates with certificates.
func checkCertificates(ctx context.Context, o Options) Check {
	const name = "certificates/issued"
	certs, err := o.K8s.ListResourceObjects(ctx, "cert-manager.io", "v1", "certificates", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list Certificates: %v", err)}
	}
	if len(certs) == 0 {
		return Check{Name: name, Outcome: Skipped,
			Detail: "cert-manager is not managing any Certificate on this cluster"}
	}
	var notReady []string
	for _, c := range certs {
		ready, current := readyCondition(c)
		switch {
		case ready != "True":
			notReady = append(notReady, objName(c))
		case !current:
			// Ready, but describing a previous revision of the Certificate.
			notReady = append(notReady, objName(c)+" (not yet reissued for its current spec)")
		}
	}
	sort.Strings(notReady)
	if len(notReady) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d of %d Certificate(s) not Ready: %s",
				len(notReady), len(certs), strings.Join(notReady, ", ")),
			Fix: "kubectl describe certificate <name> -n <ns>, then its CertificateRequest — the reason " +
				"depends on the issuer: an ACME order needs public DNS to resolve first, while a CA, " +
				"self-signed or Vault issuer fails on its own credentials or policy",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("all %d Certificate(s) Ready", len(certs))}
}

// checkHelmReleases closes the gap between "Flux converged" and "the charts
// actually installed".
//
// The convergence check reads Kustomizations, and a Kustomization goes Ready
// once it has APPLIED its objects — including a HelmRelease. Whether that
// HelmRelease then installed, upgraded, or wedged is a separate condition on a
// separate object, and nothing in this command was reading it. So a cluster
// whose charts are failing to upgrade reports every Kustomization Ready, which
// is exactly what an operator looks at.
//
// It is the visible half of several distinct failures: a chart version that does
// not exist or does not parse, values that fail schema validation, a stuck
// upgrade retry budget, and a registry the cluster cannot reach.
//
// Suspended releases are reported without failing. Suspending one is a
// deliberate operator act — the stage cluster runs that way by design — and
// treating it as a fault would make the command cry wolf on a cluster that is
// behaving as intended. Reporting it still matters: a suspended release is not
// tracking git, and forgetting that is its own outage.
func checkHelmReleases(ctx context.Context, o Options) Check {
	const name = "flux/helmreleases"

	var hrs []map[string]any
	// The stored version differs across Flux generations; the first that
	// answers with anything wins. A CRD that is not installed lists empty.
	for _, v := range []string{"v2", "v2beta2", "v2beta1"} {
		got, err := o.K8s.ListResourceObjects(ctx, "helm.toolkit.fluxcd.io", v, "helmreleases", "")
		if err != nil {
			return Check{Name: name, Required: true, Outcome: Skipped,
				Detail: fmt.Sprintf("cannot list HelmReleases: %v", err)}
		}
		if len(got) > 0 {
			hrs = got
			break
		}
	}
	if len(hrs) == 0 {
		return Check{Name: name, Outcome: Skipped,
			Detail: "no HelmReleases on this cluster"}
	}

	var failed, suspended []string
	for _, hr := range hrs {
		if s, ok := nested(hr, "spec", "suspend"); ok {
			if b, _ := s.(bool); b {
				suspended = append(suspended, objName(hr))
				continue
			}
		}
		ready, current := readyCondition(hr)
		switch {
		case ready != "True":
			failed = append(failed, objName(hr))
		case !current:
			// The Ready=True belongs to the generation being replaced, so the
			// chart the cluster is running is not the chart git asks for.
			failed = append(failed, objName(hr)+" (has not reconciled its current spec)")
		}
	}
	sort.Strings(failed)
	sort.Strings(suspended)

	if len(failed) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("%d of %d HelmRelease(s) not Ready: %s",
				len(failed), len(hrs), strings.Join(failed, ", ")),
			Fix: "flux get helmreleases -A; the Kustomization that applied these is Ready, " +
				"so the chart itself is failing to install or upgrade",
		}
	}
	detail := fmt.Sprintf("all %d HelmRelease(s) Ready", len(hrs)-len(suspended))
	if len(suspended) > 0 {
		return Check{Name: name, Outcome: Pass,
			Detail: detail + fmt.Sprintf("; %d SUSPENDED and not tracking git: %s",
				len(suspended), strings.Join(suspended, ", "))}
	}
	return Check{Name: name, Required: true, Outcome: Pass, Detail: detail}
}

// checkManagementSnat verifies that kube-ovn has PUBLISHED the management VPC's
// SNAT address — the address every system→tenant connection (cert-manager
// HTTP-01 to a tenant route, CAPI bootstrap, the DB operators) is rewritten to
// before it reaches a tenant router. kube-dc-manager discovers that address
// from the OvnSnatRule for ovn-cluster and refuses to write a tenant firewall
// without it, so an unpublished address means every Project stays NotReady
// with "no management SNAT address available" — on a cluster that otherwise
// looks converged.
//
// The greenfield failure this catches (webdock 2026-08-31): kube-ovn's legacy
// external-gateway handler created the ovn-cluster-<ext> router port but never
// the OvnEip that records its address, so the OvnSnatRule stayed empty AND the
// first tenant EIP was allocated the same address. Fix recipe in the fleet's
// docs/internal/issues/ovn-cluster-ext-cloud-lrp-missing.md.
// nestedBool reads a boolean field that some CRDs serialize as a JSON bool and
// others (or older versions) as the strings "true"/"false".
func nestedBool(obj map[string]any, path ...string) bool {
	v, ok := nested(obj, path...)
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

func checkManagementSnat(ctx context.Context, o Options) Check {
	const name = "network/management-snat"
	rules, err := o.K8s.ListResourceObjects(ctx, "kubeovn.io", "v1", "ovn-snat-rules", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: fmt.Sprintf("cannot list OvnSnatRules: %v", err)}
	}
	var mgmt []map[string]any
	for _, r := range rules {
		if strings.HasPrefix(objName(r), "ovn-cluster-to-") || nestedString(r, "status", "vpc") == "ovn-cluster" {
			mgmt = append(mgmt, r)
		}
	}
	if len(mgmt) == 0 {
		return Check{Name: name, Outcome: Skipped,
			Detail: "no OvnSnatRule for the management VPC (ovn-cluster-to-<ext>) is declared — system→tenant traffic has no SNAT path; expected only on clusters without an external network"}
	}
	eips, _ := o.K8s.ListResourceObjects(ctx, "kubeovn.io", "v1", "ovn-eips", "")
	eipReady := map[string]bool{}
	eipAddr := map[string]string{}
	for _, e := range eips {
		eipReady[objName(e)] = nestedBool(e, "status", "ready")
		eipAddr[objName(e)] = nestedString(e, "status", "v4Ip")
	}
	var published, unpublished []string
	for _, r := range mgmt {
		addr := nestedString(r, "status", "v4Eip")
		ready := nestedBool(r, "status", "ready")
		if addr != "" && ready {
			published = append(published, objName(r)+"="+addr)
			continue
		}
		eip := nestedString(r, "spec", "ovnEip")
		why := "OvnEip " + eip + " is missing"
		if _, ok := eipReady[eip]; ok {
			why = fmt.Sprintf("OvnEip %s exists (ready=%v, v4Ip=%q) but the rule has not published it", eip, eipReady[eip], eipAddr[eip])
		}
		unpublished = append(unpublished, objName(r)+" ("+why+")")
	}
	sort.Strings(published)
	sort.Strings(unpublished)
	if len(unpublished) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: fmt.Sprintf("management SNAT address NOT published: %s", strings.Join(unpublished, "; ")),
			Fix: "every Project will stay NotReady (\"no management SNAT address available\") and system→tenant traffic has no exemption — " +
				"register the router port's address: create OvnEip <spec.ovnEip> {type: lrp, externalSubnet: <ext>, v4Ip/macAddress from " +
				"`ovn-nbctl get logical_router_port ovn-cluster-<ext> networks|mac`} and check the ext-cloud-ovn-cluster switch port exists " +
				"(kube-dc docs/internal/issues/ovn-cluster-ext-cloud-lrp-missing.md)",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: "published: " + strings.Join(published, ", ")}
}

// checkManagementGatewayPair verifies the OVN NORTHBOUND topology behind the
// management SNAT: the router port ovn-cluster-<ext> on ovn-cluster AND its
// peer switch port <ext>-ovn-cluster on the external switch, with the router
// port carrying the published SNAT address. CR status alone cannot see this:
// OvnSnatRule/OvnEip stayed "ready" on stage while the pair was broken, and on
// webdock the router port existed with no switch port — the handler retried a
// constraint violation every second and every system→tenant packet had no
// datapath. Probed by exec'ing ovn-nbctl in an ovn-central pod.
func checkManagementGatewayPair(ctx context.Context, o Options) Check {
	const name = "network/management-gw-pair"
	rules, err := o.K8s.ListResourceObjects(ctx, "kubeovn.io", "v1", "ovn-snat-rules", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped, Detail: fmt.Sprintf("cannot list OvnSnatRules: %v", err)}
	}
	var eip, addr string
	for _, r := range rules {
		if strings.HasPrefix(objName(r), "ovn-cluster-to-") {
			eip = nestedString(r, "spec", "ovnEip")
			addr = nestedString(r, "status", "v4Eip")
			break
		}
	}
	if eip == "" {
		return Check{Name: name, Outcome: Skipped, Detail: "no management OvnSnatRule declared — nothing to verify"}
	}
	ext := strings.TrimPrefix(eip, "ovn-cluster-")
	lrp, lsp := "ovn-cluster-"+ext, ext+"-ovn-cluster"
	pods, err := o.K8s.ListPodNames(ctx, "kube-system", "app=ovn-central")
	if err != nil || len(pods) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "cannot reach an ovn-central pod to read the northbound DB",
			Fix:    "kubectl -n kube-system get pods -l app=ovn-central"}
	}
	// One exec, three facts, machine-readable lines. `--if-exists` keeps a
	// missing port a plain empty line instead of a failed exec.
	script := fmt.Sprintf(`n=ovn-nbctl; $n --no-leader-only --if-exists get logical_router_port %s networks 2>/dev/null | tr -d '[]" ' | sed 's/^/LRP=/'; `+
		`$n --no-leader-only --if-exists get logical_switch_port %s type 2>/dev/null | sed 's/^/LSPTYPE=/'; `+
		`$n --no-leader-only --if-exists get logical_switch_port %s options 2>/dev/null | sed 's/^/LSPOPTS=/'`, lrp, lsp, lsp)
	var out []byte
	for _, pod := range pods {
		b, err := o.K8s.PodExec(ctx, "kube-system", pod, []string{"sh", "-c", script}, nil)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 {
			if alt, altErr := o.K8s.PodExecViaKubectl(ctx, "kube-system", pod, []string{"sh", "-c", script}, nil); altErr == nil && len(strings.TrimSpace(string(alt))) > 0 {
				b, err = alt, nil
			}
		}
		if err == nil && len(strings.TrimSpace(string(b))) > 0 {
			out = b
			break
		}
	}
	text := strings.TrimSpace(string(out))
	if text == "" || !strings.Contains(text, "LRP=") && !strings.Contains(text, "LSPTYPE=") {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "could not read the northbound DB from ovn-central (exec returned nothing usable)",
			Fix:    "kubectl -n kube-system exec deploy/ovn-central -c ovn-central -- ovn-nbctl lrp-list ovn-cluster"}
	}
	var lrpNets, lspType, lspOpts string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "LRP="):
			lrpNets = strings.TrimPrefix(l, "LRP=")
		case strings.HasPrefix(l, "LSPTYPE="):
			lspType = strings.Trim(strings.TrimPrefix(l, "LSPTYPE="), `"`)
		case strings.HasPrefix(l, "LSPOPTS="):
			lspOpts = strings.TrimPrefix(l, "LSPOPTS=")
		}
	}
	var problems []string
	if lrpNets == "" {
		problems = append(problems, "router port "+lrp+" is missing on ovn-cluster")
	} else if addr != "" && !strings.HasPrefix(lrpNets, addr+"/") && lrpNets != addr {
		problems = append(problems, fmt.Sprintf("router port %s carries %s but the OvnSnatRule published %s", lrp, lrpNets, addr))
	}
	if lspType == "" && lspOpts == "" {
		problems = append(problems, "switch port "+lsp+" is missing on "+ext+" — the router has no peer on the external segment")
	} else {
		if lspType != "router" {
			problems = append(problems, fmt.Sprintf("switch port %s has type %q, want router", lsp, lspType))
		}
		if !strings.Contains(lspOpts, "router-port="+lrp) {
			problems = append(problems, fmt.Sprintf("switch port %s is not bound to %s (options %s)", lsp, lrp, lspOpts))
		}
	}
	if len(problems) > 0 {
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: strings.Join(problems, "; "),
			Fix: "system→tenant traffic has no datapath while the pair is incomplete (Projects can still show Ready). " +
				"With the OvnEip in place, remove BOTH halves (`ovn-nbctl --if-exists lsp-del " + lsp + "; ovn-nbctl --if-exists lrp-del " + lrp + "`) " +
				"and kube-ovn's external-gateway handler recreates the pair from the OvnEip within seconds; " +
				"never toggle enable-external-gw or the node list (kube-ovn race #6469). Runbook: kube-dc docs/internal/issues/ovn-cluster-ext-cloud-lrp-missing.md",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("%s=%s ↔ %s (router-port bound)", lrp, lrpNets, lsp)}
}

// checkDefaultVPCPatchPairs verifies, for EVERY Subnet attached to the
// management VPC (ovn-cluster), that OVN holds both halves of its router↔switch
// patch pair. kube-ovn creates the pair in one transaction and, if it ever
// finds the router port alone, retries that transaction forever ("constraint
// violation … Logical_Router_Port") — the Subnet never turns Ready and nothing
// on the switch can reach the router. On webdock (2026-08-31) that was the
// state of BOTH ext-cloud (management SNAT dark) and infra-net (dual-homing
// silently disabled: the Project controller withholds the opt-in label while
// the infra Subnet is not Ready, so tenant pods stay single-homed and Kamaji
// cannot reach a tenant etcd). Subnet.status alone does not show it.
func checkDefaultVPCPatchPairs(ctx context.Context, o Options) Check {
	const name = "network/default-vpc-patch-pairs"
	subnets, err := o.K8s.ListResourceObjects(ctx, "kubeovn.io", "v1", "subnets", "")
	if err != nil {
		return Check{Name: name, Required: true, Outcome: Skipped, Detail: fmt.Sprintf("cannot list Subnets: %v", err)}
	}
	var names []string
	notReady := map[string]bool{}
	for _, s := range subnets {
		vpc := nestedString(s, "spec", "vpc")
		if vpc != "" && vpc != "ovn-cluster" {
			continue
		}
		n := objName(s)
		if n == "" || n == "join" {
			continue // join is the node network, not a workload subnet
		}
		names = append(names, n)
		ready := false
		for _, c := range nestedSlice(s, "status", "conditions") {
			cm, _ := c.(map[string]any)
			if nestedString(cm, "type") == "Ready" && nestedString(cm, "status") == "True" {
				ready = true
			}
		}
		notReady[n] = !ready
	}
	if len(names) == 0 {
		return Check{Name: name, Outcome: Skipped, Detail: "no workload Subnet on the management VPC"}
	}
	sort.Strings(names)
	pods, err := o.K8s.ListPodNames(ctx, "kube-system", "app=ovn-central")
	if err != nil || len(pods) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "cannot reach an ovn-central pod to read the northbound DB",
			Fix:    "kubectl -n kube-system get pods -l app=ovn-central"}
	}
	var sb strings.Builder
	sb.WriteString("n=ovn-nbctl; ")
	for _, s := range names {
		fmt.Fprintf(&sb, `printf '%s LRP=%%s LSP=%%s\n' "$($n --no-leader-only --if-exists get logical_router_port ovn-cluster-%s networks 2>/dev/null | tr -d '[]\" ')" "$($n --no-leader-only --if-exists get logical_switch_port %s-ovn-cluster type 2>/dev/null)"; `, s, s, s)
	}
	var out []byte
	for _, pod := range pods {
		b, err := o.K8s.PodExec(ctx, "kube-system", pod, []string{"sh", "-c", sb.String()}, nil)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 {
			if alt, altErr := o.K8s.PodExecViaKubectl(ctx, "kube-system", pod, []string{"sh", "-c", sb.String()}, nil); altErr == nil && len(strings.TrimSpace(string(alt))) > 0 {
				b, err = alt, nil
			}
		}
		if err == nil && strings.Contains(string(b), "LRP=") {
			out = b
			break
		}
	}
	if len(out) == 0 {
		return Check{Name: name, Required: true, Outcome: Skipped,
			Detail: "could not read the northbound DB from ovn-central",
			Fix:    "kubectl -n kube-system exec deploy/ovn-central -c ovn-central -- ovn-nbctl lrp-list ovn-cluster"}
	}
	var broken, ok []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		s := f[0]
		lrp := strings.TrimPrefix(f[1], "LRP=")
		lsp := strings.TrimPrefix(f[2], "LSP=")
		switch {
		case lrp != "" && lsp == "":
			broken = append(broken, fmt.Sprintf("%s: router port ovn-cluster-%s (%s) has NO switch port %s-ovn-cluster", s, s, lrp, s))
		case lrp == "" && lsp != "":
			broken = append(broken, fmt.Sprintf("%s: switch port %s-ovn-cluster exists but router port ovn-cluster-%s is missing", s, s, s))
		case lrp == "" && lsp == "":
			if notReady[s] {
				broken = append(broken, fmt.Sprintf("%s: no patch pair at all and the Subnet is not Ready", s))
			}
		default:
			if notReady[s] {
				broken = append(broken, fmt.Sprintf("%s: pair present but the Subnet is not Ready", s))
			} else {
				ok = append(ok, s)
			}
		}
	}
	if len(broken) > 0 {
		sort.Strings(broken)
		return Check{Name: name, Required: true, Outcome: Fail,
			Detail: strings.Join(broken, "; "),
			Fix: "kube-ovn retries the pair as one transaction and can never complete it while one half exists: " +
				"`ovn-nbctl --if-exists lsp-del <subnet>-ovn-cluster; ovn-nbctl --if-exists lrp-del ovn-cluster-<subnet>` then nudge the Subnet " +
				"(kubectl annotate subnet <subnet> kube-dc.com/reconcile-nudge=$(date +%s) --overwrite); for the external subnet make sure OvnEip " +
				"ovn-cluster-<ext> exists first. Runbook: kube-dc docs/internal/issues/ovn-cluster-ext-cloud-lrp-missing.md",
		}
	}
	return Check{Name: name, Required: true, Outcome: Pass,
		Detail: fmt.Sprintf("%d subnet(s) wired to ovn-cluster with complete patch pairs: %s", len(ok), strings.Join(ok, ", "))}
}
