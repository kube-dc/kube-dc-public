package clusterinit

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

var dnsSubdomainLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

// ManagedServicesSelection is the reviewed, non-secret scaffold input. The
// Fleet helper still validates every value before writing an overlay.
type ManagedServicesSelection struct {
	InstallationKind         string
	Mode                     string // on or off
	DatabaseClass            string
	StorageBudget            string
	EgressProbeURLs          []string
	NeedsClassExpansionCheck bool
	Warnings                 []string
}

// ResolveManagedServices determines whether a new site can offer managed
// services from its declared storage topology. An unknown physical capacity
// is never guessed: the operator must supply a budget before enabling it.
func ResolveManagedServices(o *InitOptions) (ManagedServicesSelection, error) {
	if o == nil {
		return ManagedServicesSelection{}, fmt.Errorf("managed services: nil install options")
	}
	result := ManagedServicesSelection{InstallationKind: o.InstallationKind, Mode: "off"}
	if o.InstallationKind == "" {
		return result, nil // Older engine callers; the CLI gate requires the kind.
	}
	if o.InstallationKind != "kube-dc" && o.InstallationKind != "cloudsigma" {
		return result, fmt.Errorf("--installation-kind must be kube-dc or cloudsigma")
	}
	mode := o.ManagedServicesMode
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "on" && mode != "off" {
		return result, fmt.Errorf("--managed-services must be auto, on or off")
	}
	if o.InstallationKind == "cloudsigma" {
		if mode == "on" {
			return result, fmt.Errorf("managed services are not qualified for CloudSigma installations")
		}
		return result, nil
	}
	class := o.ServicesDatabaseClass
	if class == "" {
		switch {
		case o.RookMode == RookCephLocal || o.RookMode == RookCephPVC:
			class = "ceph-block"
		case o.RookMode == RookCephMultiNode && o.VMStorageMode == VMStorageSharedRBD:
			class = "rbd-vm"
		}
	} else {
		result.NeedsClassExpansionCheck = true
	}
	if class != "" && !validStorageClassName(class) {
		return result, fmt.Errorf("--services-database-class must be a DNS subdomain with labels of at most 63 characters")
	}
	budget := o.ServicesStorageBudget
	if budget == "" && o.RookMode == RookCephPVC {
		count, size := o.CephOSDCount, o.CephOSDVolumeSizeGB
		if count == 0 {
			count = 2
		}
		if size == 0 {
			size = 200
		}
		replicas := o.ObjectStorage().replication()
		if replicas > 0 {
			if size > 0 && count > math.MaxInt/size {
				return result, fmt.Errorf("managed-services PVC capacity exceeds supported range")
			}
			budget = fmt.Sprintf("%dGi", count*size/replicas/4)
		}
	}
	if budget != "" {
		quantity, err := resource.ParseQuantity(budget)
		if err != nil || quantity.Sign() <= 0 {
			return result, fmt.Errorf("--services-storage-budget must be a positive Kubernetes quantity")
		}
	}
	probes := append([]string(nil), o.ServicesEgressProbeURLs...)
	if len(probes) == 0 {
		probes = []string{"https://ghcr.io/v2/"}
	}
	for _, probe := range probes {
		parsed, err := url.Parse(probe)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
			parsed.User != nil || strings.Contains(strings.ToLower(parsed.Hostname()), "cloudsigma.com") {
			return result, fmt.Errorf("--services-egress-probe must be an HTTPS URL with a host outside CloudSigma")
		}
	}
	availableBucket := o.RookMode == RookCephLocal || o.RookMode == RookCephPVC || o.RookMode == RookCephMultiNode
	qualified := availableBucket && class != "" && budget != "" && (!result.NeedsClassExpansionCheck || o.ServicesClassExpansionVerified)
	if mode == "on" && !qualified {
		return result, fmt.Errorf("--managed-services=on needs a rook-ceph bucket, an expandable database class and a bounded storage budget")
	}
	if mode == "off" || (mode == "auto" && !qualified) {
		if mode == "auto" {
			result.Warnings = append(result.Warnings, "managed services remain off until an expandable database class, Project bucket provider and bounded storage budget are qualified")
		}
		return result, nil
	}
	result.Mode, result.DatabaseClass, result.StorageBudget, result.EgressProbeURLs = "on", class, budget, probes
	if amount, _ := resource.ParseQuantity(budget); amount.Cmp(resource.MustParse("52Gi")) < 0 {
		result.Warnings = append(result.Warnings, "managed-services storage budget is below the combined Development defaults (52Gi)")
	} else if amount.Cmp(resource.MustParse("60Gi")) < 0 {
		result.Warnings = append(result.Warnings, "managed-services storage budget is below one PostgreSQL Production allocation (60Gi)")
	}
	return result, nil
}

func validStorageClassName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || !dnsSubdomainLabel.MatchString(label) {
			return false
		}
	}
	return true
}
