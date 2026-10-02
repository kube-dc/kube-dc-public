package clusterinit

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// A saved install spec restores deliberate overrides. An unmarked cluster
// environment remains a clone source: site addresses and release pins are
// filtered so they cannot silently become another cluster's configuration.
const KeySpecVersion = InitPrefix + "SPEC_VERSION"
const InputSpecVersion = "1"

type inputStringBinding struct {
	key, flag string
	field     func(*InitOptions) *string
}

// Only artifact references and file paths are stored here. Credential contents and approval flags are
// never part of a reusable environment file.
var inputStringBindings = []inputStringBinding{
	{InitPrefix + "INSTALLATION_KIND", "installation-kind", func(o *InitOptions) *string { return &o.InstallationKind }},
	{InitPrefix + "MANAGED_SERVICES", "managed-services", func(o *InitOptions) *string { return &o.ManagedServicesMode }},
	{InitPrefix + "SERVICES_DATABASE_CLASS", "services-database-class", func(o *InitOptions) *string { return &o.ServicesDatabaseClass }},
	{InitPrefix + "SERVICES_STORAGE_BUDGET", "services-storage-budget", func(o *InitOptions) *string { return &o.ServicesStorageBudget }},
	{InitPrefix + "STARTER_REF", "starter-ref", func(o *InitOptions) *string { return &o.StarterRef }},
	{InitPrefix + "TLS_CERT", "tls-cert", func(o *InitOptions) *string { return &o.TLSCert }},
	{InitPrefix + "TLS_KEY", "tls-key", func(o *InitOptions) *string { return &o.TLSKey }},
	{InitPrefix + "TRUSTED_CA_BUNDLE", "trusted-ca-bundle", func(o *InitOptions) *string { return &o.TrustedCABundle }},
	{InitPrefix + "DNS01_ROUTE53_SECRET_KEY_FILE", "dns01-route53-secret-key-file", func(o *InitOptions) *string { return &o.DNS01Route53SecretKeyFile }},
	{InitPrefix + "DNS01_CLOUDFLARE_API_TOKEN_FILE", "dns01-cloudflare-api-token-file", func(o *InitOptions) *string { return &o.DNS01CloudflareAPITokenFile }},
}

// ValidateInputSpec checks the file format without requiring complete cluster
// settings. Errors name keys, never values, because an invalid entry can carry
// a credential. Files are parsed as data and are never sourced by a shell.
func ValidateInputSpec(src map[string]string) error {
	if v, ok := src[KeySpecVersion]; ok && v != InputSpecVersion {
		return fmt.Errorf("unsupported %s; supported version is %s", KeySpecVersion, InputSpecVersion)
	}
	for _, k := range SpecOrderedKeys(src) {
		v := src[k]
		if !isUpperKey(k) {
			return fmt.Errorf("configuration key must use uppercase letters, digits, and underscores")
		}
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s must contain one line without control characters", k)
		}
		if strings.HasPrefix(k, InitPrefix) && !IsOrchestrationKey(k) {
			return fmt.Errorf("unknown installer setting %s", k)
		}
		if IsSecretConfigKey(k) && strings.TrimSpace(v) != "" {
			return fmt.Errorf("%s contains credential material; use a credential file or encrypted Secret", k)
		}
		if err := validateKnownSetting(k, v); err != nil {
			return err
		}
		switch k {
		case KeyPrimaryNode:
			if v != "" {
				if err := ValidateK8sNodeNameField(v); err != nil {
					return fmt.Errorf("%s must be a Kubernetes node name", k)
				}
			}
		case KeyNodeSSHHosts:
			pairs, err := ParseSetPairs(splitCSVList(v))
			if err != nil || len(validateNodeSSHHosts("", pairs)) > 0 {
				return fmt.Errorf("%s must contain valid NODE=SSH_HOST pairs", k)
			}
		case KeyNodeSSHHostKeys:
			pairs, err := ParseSetPairs(splitCSVList(v))
			if err != nil || len(validateNodeSSHHostKeys(pairs)) > 0 {
				return fmt.Errorf("%s must contain valid NODE=SHA256:FINGERPRINT pairs", k)
			}
		case KeyNodeNICs:
			if _, err := ParseSetPairs(splitCSVList(v)); err != nil {
				return fmt.Errorf("%s must contain unique NODE=IFACE pairs", k)
			}
		case "GPU_NODE_MODES":
			if _, err := ParseGPUNodeModes(splitCSVList(v)); err != nil {
				return fmt.Errorf("%s must contain valid NODE=MODE pairs", k)
			}
		case "CEPH_LOCAL_OSD_SIZE_GB", "CEPH_OSD_COUNT", "CEPH_OSD_VOLUME_SIZE_GB":
			if v != "" {
				if _, err := strconv.Atoi(v); err != nil {
					return fmt.Errorf("%s must be an integer", k)
				}
			}
		case KeyAllowDNS, KeyAllowNoKVM, KeyAllowUnpin, KeyNoS3Exposure, KeyNoKubeVirt, KeyPAYG, KeyGPUAllowUnassigned, KeyVGPUSecretReady, "HAMI_ENABLED", "GPU_ENABLED", "GPU_CATALOG_ENABLED":
			switch strings.ToLower(v) {
			case "", "true", "false", "1", "0", "yes", "no", "on", "off":
			default:
				return fmt.Errorf("%s must be a boolean", k)
			}
		}
	}
	return nil
}

// IsSecretConfigKey identifies credential values, not references or boolean
// capability flags. Known installation path fields are explicitly allowed.
func IsSecretConfigKey(k string) bool {
	if IsOrchestrationKey(k) {
		return false
	}
	for _, suffix := range []string{"_REF", "_NAME", "_ENABLED", "_READY", "_FILE"} {
		if strings.HasSuffix(k, suffix) {
			return false
		}
	}
	return strings.Contains(k, "PASSWORD") || strings.HasSuffix(k, "_TOKEN") ||
		strings.HasSuffix(k, "_SECRET") || strings.HasSuffix(k, "_SECRET_KEY") ||
		strings.HasSuffix(k, "_SECRET_ACCESS_KEY") || strings.HasSuffix(k, "_PRIVATE_KEY") ||
		strings.HasPrefix(k, "OPENBAO_UNSEAL_KEY_")
}
