// Exported single-field validators for interactive surfaces (T6/OS-5).
//
// The huh.Form wizard validates fields as the operator types; per the
// OS-5 design rule those validators MUST be the same checks the flag
// path runs — a form that accepts what Validate() later rejects (or
// vice versa) would make the wizard a second, drifting contract.
// These wrappers expose the internal per-field rules as one-value
// `func(string) error` shapes huh consumes directly.
//
// Validate() remains the single source of truth for CROSS-field rules
// (mode companions, owner/repo requirements, gates) — the wizard runs
// it on the assembled InitOptions after submit, exactly like the flag
// path does.
package clusterinit

import (
	"fmt"
	"net"
	"strings"
)

// ValidateClusterNameField checks the clusters/<name> shape
// (lowercase, digits, '-', nested via '/').
func ValidateClusterNameField(s string) error {
	if s == "" {
		return fmt.Errorf("required")
	}
	if !clusterNameRegex.MatchString(s) {
		return fmt.Errorf("lowercase letters, digits, '-' (nested: eu/dc1)")
	}
	return nil
}

// ValidateDomainField checks the bare-FQDN shape used by --domain and
// --s3-hostname.
func ValidateDomainField(s string) error {
	if s == "" {
		return fmt.Errorf("required")
	}
	if !domainRegex.MatchString(s) {
		return fmt.Errorf("bare FQDN (no scheme/path), e.g. kdc.example.com")
	}
	return nil
}

// ValidateOptionalDomainField is ValidateDomainField with empty
// allowed (defaulted fields, e.g. S3 hostname → s3.<domain>).
func ValidateOptionalDomainField(s string) error {
	if s == "" {
		return nil
	}
	return ValidateDomainField(s)
}

// ValidateNodeIPField checks --node-external-ip's shape.
func ValidateNodeIPField(s string) error {
	if s == "" {
		return fmt.Errorf("required")
	}
	if net.ParseIP(s) == nil {
		return fmt.Errorf("not a valid IP address")
	}
	return nil
}

// ValidateEmailField mirrors validateEmail's contract.
func ValidateEmailField(s string) error {
	if s == "" {
		return fmt.Errorf("required")
	}
	if errs := validateEmail(s); len(errs) > 0 {
		return fmt.Errorf("not a valid email address")
	}
	return nil
}

// ValidateK8sNodeNameField checks the lowercase RFC 1123 node-name
// shape (--rook-osd-node, --ceph-node keys).
func ValidateK8sNodeNameField(s string) error {
	if s == "" {
		return fmt.Errorf("required")
	}
	if !k8sNodeNameRegex.MatchString(s) {
		return fmt.Errorf("lowercase RFC 1123 node name (a-z, 0-9, '-', '.')")
	}
	return nil
}

// ValidateStorageClassField checks the RFC 1123 object-name shape.
func ValidateStorageClassField(s string) error {
	return ValidateK8sNodeNameField(s) // same shape, same message class
}

// ValidateDeviceNameField checks the Linux device-identifier shape
// (--rook-osd-device, --ceph-node values). Empty allowed (defaults).
func ValidateDeviceNameField(s string) error {
	if s == "" {
		return nil
	}
	if !deviceNameRegex.MatchString(s) {
		return fmt.Errorf("device name like sdb, nvme0n1, loop0")
	}
	return nil
}

// ValidateNodeDevicePairField checks one NODE=DEVICE entry (the
// --ceph-node shape) with both halves validated.
func ValidateNodeDevicePairField(s string) error {
	node, dev, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("expected NODE=DEVICE, e.g. host5-a=sdb")
	}
	if err := ValidateK8sNodeNameField(strings.TrimSpace(node)); err != nil {
		return fmt.Errorf("node: %v", err)
	}
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return fmt.Errorf("device required, e.g. sdb")
	}
	return ValidateDeviceNameField(dev)
}
