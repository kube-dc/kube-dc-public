package initform

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// Environment keys behind the Certificates rows: exactly what ExportMap
// writes and ImportMap reads for the TLS flags, so a value set in a row and
// one set in the environment editor or --config are the same input.
const (
	keyTLSMode             = "TLS_MODE"
	keyTLSCert             = clusterinit.InitPrefix + "TLS_CERT"
	keyTLSKey              = clusterinit.InitPrefix + "TLS_KEY"
	keyTrustedCA           = clusterinit.InitPrefix + "TRUSTED_CA_BUNDLE"
	keyRoute53Zone         = "DNS01_ROUTE53_ZONE_ID"
	keyRoute53Region       = "DNS01_ROUTE53_REGION"
	keyRoute53AccessKey    = "DNS01_ROUTE53_ACCESS_KEY_ID"
	keyRoute53SecretFile   = clusterinit.InitPrefix + "DNS01_ROUTE53_SECRET_KEY_FILE"
	keyCloudflareZone      = "DNS01_CLOUDFLARE_ZONE"
	keyCloudflareTokenFile = clusterinit.InitPrefix + "DNS01_CLOUDFLARE_API_TOKEN_FILE"
)

// tlsMode is the selected platform TLS mode; an absent key is the acme default.
func (s *State) tlsMode() string {
	if m := strings.TrimSpace(s.RetainedInputs[keyTLSMode]); m != "" {
		return m
	}
	return clusterinit.TLSModeACME
}

// setRetainedInput writes one retained environment input. Copy-on-write:
// State values are copied by the panel and the editor, and an earlier copy
// must not change with this one.
func (s *State) setRetainedInput(key, value string) {
	next := make(map[string]string, len(s.RetainedInputs)+1)
	for k, v := range s.RetainedInputs {
		next[k] = v
	}
	if v := strings.TrimSpace(value); v == "" {
		delete(next, key)
	} else {
		next[key] = v
	}
	s.RetainedInputs = next
	s.retainedEdited = true
}

func retainedGet(key string) func(*State) string {
	return func(s *State) string { return s.RetainedInputs[key] }
}

// retainedSet writes a row's input. An explicit edit, clearing included,
// resolves a dropped-prefill marker for that key: the operator decided.
func retainedSet(key string) func(*State, string) {
	return func(s *State, v string) {
		s.setRetainedInput(key, v)
		s.resolveDroppedCredentialRef(key)
	}
}

func (s *State) resolveDroppedCredentialRef(key string) {
	kept := s.DroppedCredentialRefs[:0:0]
	for _, k := range s.DroppedCredentialRefs {
		if k != key {
			kept = append(kept, k)
		}
	}
	s.DroppedCredentialRefs = kept
}

// unresolvedCredentialRef is the first dropped prefill the selected mode
// still uses, or "".
func (s *State) unresolvedCredentialRef() string {
	for _, k := range s.DroppedCredentialRefs {
		if s.credentialKeyApplies(k) {
			return k
		}
	}
	return ""
}

func tlsModeIs(mode string) func(*State) bool {
	return func(s *State) bool { return s.tlsMode() == mode }
}

// setTLSMode selects a mode and drops the inputs only another mode uses, so a
// hidden row cannot block Review or reach init. The CA bundle stays: it is
// independent of the mode. Cloudflare inputs stay under acme, where they add
// DNS-01 for the wildcard only.
func (s *State) setTLSMode(mode string) {
	if mode == clusterinit.TLSModeACME {
		mode = ""
	}
	s.setRetainedInput(keyTLSMode, mode)
	if mode != clusterinit.TLSModeBYOWildcard {
		s.setRetainedInput(keyTLSCert, "")
		s.setRetainedInput(keyTLSKey, "")
	}
	if mode != clusterinit.TLSModeACMEDNS01Route53 {
		for _, k := range []string{keyRoute53Zone, keyRoute53Region, keyRoute53AccessKey, keyRoute53SecretFile} {
			s.setRetainedInput(k, "")
		}
	}
	if mode != "" && mode != clusterinit.TLSModeACMEDNS01Cloudflare {
		s.setRetainedInput(keyCloudflareZone, "")
		s.setRetainedInput(keyCloudflareTokenFile, "")
	}
}

func cloudflareRows(s *State) bool {
	m := s.tlsMode()
	return m == clusterinit.TLSModeACME || m == clusterinit.TLSModeACMEDNS01Cloudflare
}

// validateCredentialFile accepts the absolute path of an existing regular
// file only. Certificates, keys and tokens are referenced, never pasted: a
// pasted value would be shown on screen and saved into the draft, and it is
// never the name of an existing file. init reads these files on this
// workstation, so they must exist here.
func validateCredentialFile(v string) error {
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("enter the absolute path of the file, not its contents")
	}
	if fi, err := os.Stat(v); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("no such file on this workstation")
	}
	return nil
}

// validateCephNodesMore checks servers 4 to 16 of a multi-node layout.
func validateCephNodesMore(v string) error {
	entries := strings.Split(v, ",")
	if len(entries) > 13 {
		return fmt.Errorf("at most 16 Ceph nodes")
	}
	for _, e := range entries {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if err := clusterinit.ValidateNodeDevicePairField(e); err != nil {
			return err
		}
	}
	return nil
}

// cloudflareWildcardZoneMissing mirrors LoadDNS01Cloudflare: under acme,
// Cloudflare DNS-01 serves the wildcard only and needs the zone as its
// explicit opt-in.
func (s *State) cloudflareWildcardZoneMissing() bool {
	return s.tlsMode() == clusterinit.TLSModeACME &&
		strings.TrimSpace(s.RetainedInputs[keyCloudflareTokenFile]) != "" &&
		strings.TrimSpace(s.RetainedInputs[keyCloudflareZone]) == ""
}

// maxCephNodes is the multi-node OSD server limit of the flag path.
const maxCephNodes = 16

// cephPairs is every NODE=DEVICE entry of a multi-node layout: the three
// rows, then "More Ceph nodes".
func (s *State) cephPairs() []string {
	var out []string
	for _, p := range append([]string{s.CephNode1, s.CephNode2, s.CephNode3}, strings.Split(s.CephNodesMore, ",")...) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// setCephPairs stores entries in order: the first three in the rows, the
// rest in "More Ceph nodes".
func (s *State) setCephPairs(pairs []string) {
	s.CephNode1, s.CephNode2, s.CephNode3, s.CephNodesMore = "", "", "", ""
	slots := []*string{&s.CephNode1, &s.CephNode2, &s.CephNode3}
	var more []string
	for i, p := range pairs {
		if i < len(slots) {
			*slots[i] = p
		} else {
			more = append(more, p)
		}
	}
	s.CephNodesMore = strings.Join(more, ",")
}

// dropUnverifiedCredentialRefs removes credential file references that do
// not name an existing regular file on this workstation. A prefill (a
// --config file, KUBE_DC_INIT_* or flags) could carry a pasted secret in such
// a key; the panel must neither display nor save it. init reads these files
// here, so a path valid only elsewhere has to be entered again anyway.
func (s *State) dropUnverifiedCredentialRefs() {
	s.DroppedCredentialRefs = nil
	var keys []string
	for k, v := range s.RetainedInputs {
		if clusterinit.IsCredentialFileKey(k) && strings.TrimSpace(v) != "" && validateCredentialFile(v) != nil {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.setRetainedInput(k, "")
	}
	s.DroppedCredentialRefs = keys
}

// credentialKeyApplies reports whether a credential file key is used by the
// selected TLS mode; a dropped prefill of an unused key needs no re-entry.
func (s *State) credentialKeyApplies(k string) bool {
	switch k {
	case keyTLSCert, keyTLSKey:
		return s.tlsMode() == clusterinit.TLSModeBYOWildcard
	case keyRoute53SecretFile:
		return s.tlsMode() == clusterinit.TLSModeACMEDNS01Route53
	case keyCloudflareTokenFile:
		return cloudflareRows(s)
	}
	return true // the private CA bundle applies to every mode
}

// carryDroppedCredentialRefs keeps the unresolved drop markers of prev across
// an environment-editor rebuild, except for the key just edited or removed.
func (s *State) carryDroppedCredentialRefs(prev []string, edited string) {
	seen := map[string]bool{}
	for _, k := range s.DroppedCredentialRefs {
		seen[k] = true
	}
	for _, k := range prev {
		if k != edited && !seen[k] {
			s.DroppedCredentialRefs = append(s.DroppedCredentialRefs, k)
			seen[k] = true
		}
	}
	sort.Strings(s.DroppedCredentialRefs)
}
