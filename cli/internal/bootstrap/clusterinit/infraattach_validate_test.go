package clusterinit

import (
	"strings"
	"testing"
)

// Every case here is a mistake that is silent or near-silent at install time.
// Catching them at PLAN time is the whole point: the alternative is a cluster
// that installs cleanly and then either crash-loops or misroutes.

func infraErrs(env map[string]string) string {
	var errs []string
	validateInfraAttachment(env, &errs)
	return strings.Join(errs, "; ")
}

func managementAPIErrs(env map[string]string) string {
	var errs []string
	validateManagementAPI(env, &errs)
	return strings.Join(errs, "; ")
}

func TestValidateManagementAPI_ServiceRequiresExactInCIDRIPv4(t *testing.T) {
	valid := map[string]string{
		"MANAGEMENT_API_MODE":      "service",
		"INFRA_ATTACHMENT_ENABLED": "true",
		"K8S_SERVICE_IP":           "10.101.0.1",
		"SVC_CIDR":                 "10.101.0.0/16",
		"INFRA_ATTACHMENT_ROUTES":  "192.0.2.0/24,10.100.0.0/16",
	}
	if got := managementAPIErrs(valid); got != "" {
		t.Fatalf("valid service mode rejected: %s", got)
	}

	for name, mutate := range map[string]func(map[string]string){
		"dual-home-disabled":   func(v map[string]string) { v["INFRA_ATTACHMENT_ENABLED"] = "false" },
		"outside-service-cidr": func(v map[string]string) { v["K8S_SERVICE_IP"] = "10.102.0.1" },
		"ipv6":                 func(v map[string]string) { v["K8S_SERVICE_IP"] = "2001:db8::1" },
		"ipv4-mapped":          func(v map[string]string) { v["K8S_SERVICE_IP"] = "::ffff:10.101.0.1" },
		"route-overlap":        func(v map[string]string) { v["INFRA_ATTACHMENT_ROUTES"] = "10.101.0.0/16" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := make(map[string]string, len(valid))
			for key, value := range valid {
				candidate[key] = value
			}
			mutate(candidate)
			if got := managementAPIErrs(candidate); got == "" {
				t.Fatal("unsafe service mode accepted")
			}
		})
	}
}

func TestValidateManagementAPI_ExplicitModes(t *testing.T) {
	if got := managementAPIErrs(map[string]string{"MANAGEMENT_API_MODE": "external"}); got != "" {
		t.Fatalf("external mode rejected: %s", got)
	}
	// platformVIP is RETIRED (front-door simplification §1.1) — even a FULLY
	// populated config must now be refused, with the migration path in the
	// message. Previously this exact map was the "valid" case.
	if got := managementAPIErrs(map[string]string{
		"MANAGEMENT_API_MODE": "platformVIP", "PLATFORM_ENDPOINT_KUBE_API_ENABLED": "true", "KUBE_API_INTERNAL_VIP": "100.66.0.31",
	}); !strings.Contains(got, "RETIRED") {
		t.Fatalf("fully-populated platformVIP config must still be refused as RETIRED, got %q", got)
	}
	if got := managementAPIErrs(map[string]string{"MANAGEMENT_API_MODE": "platformVIP"}); !strings.Contains(got, "RETIRED") {
		t.Fatalf("platformVIP must be refused as RETIRED, got %q", got)
	}
	if got := managementAPIErrs(map[string]string{"MANAGEMENT_API_MODE": "automatic"}); !strings.Contains(got, "external or service") {
		t.Fatalf("unknown mode did not fail clearly: %q", got)
	}
}

func TestValidateInfraAttachment_AcceptsAWellFormedConfig(t *testing.T) {
	got := infraErrs(map[string]string{
		"INFRA_ATTACHMENT_ENABLED":        "true",
		"INFRA_ATTACHMENT_ROUTES":         "192.168.110.0/24,172.30.0.0/22,10.100.0.0/16",
		"INFRA_ATTACHMENT_SECURITY_GROUP": "infra-lock-{namespace}",
		"INFRA_ATTACHMENT_CIDR":           "100.66.0.0/16",
		"INFRA_ATTACHMENT_GATEWAY":        "100.66.0.1",
	})
	if got != "" {
		t.Fatalf("rejected a valid config: %s", got)
	}
}

// The exact failure that produced a dead cluster: a placeholder is a non-empty
// string, so the chart's required() guard passes and net.ParseCIDR fails later.
func TestValidateInfraAttachment_RejectsAPlaceholderRoute(t *testing.T) {
	got := infraErrs(map[string]string{
		"INFRA_ATTACHMENT_ENABLED": "true",
		"INFRA_ATTACHMENT_ROUTES":  "CHANGEME_NODE_CIDR,172.30.0.0/22,10.100.0.0/16",
	})
	if !strings.Contains(got, "CHANGEME_NODE_CIDR") || !strings.Contains(got, "CrashLoopBackOff") {
		t.Fatalf("placeholder route not caught with an actionable message: %q", got)
	}
}

// A shared security group across every project is a cross-tenant isolation
// failure, not a cosmetic naming issue.
func TestValidateInfraAttachment_RejectsASharedSecurityGroup(t *testing.T) {
	got := infraErrs(map[string]string{
		"INFRA_ATTACHMENT_SECURITY_GROUP": "infra-lock",
	})
	if !strings.Contains(got, "{namespace}") || !strings.Contains(got, "isolation") {
		t.Fatalf("shared SG template not caught: %q", got)
	}
}

func TestValidateInfraAttachment_RejectsEnabledWithoutRoutes(t *testing.T) {
	got := infraErrs(map[string]string{"INFRA_ATTACHMENT_ENABLED": "true"})
	if !strings.Contains(got, "INFRA_ATTACHMENT_ROUTES must be set") {
		t.Fatalf("enabled-without-routes not caught: %q", got)
	}
}

// Unquoted in the HelmRelease, so anything but lowercase true/false changes the
// Helm truthiness silently.
func TestValidateInfraAttachment_RejectsNonBooleanEnabled(t *testing.T) {
	for _, v := range []string{"True", "yes", "1"} {
		if got := infraErrs(map[string]string{"INFRA_ATTACHMENT_ENABLED": v}); got == "" {
			t.Fatalf("%q accepted as a boolean", v)
		}
	}
}

func TestValidateInfraAttachment_RejectsGatewayOutsideTheInfraCIDR(t *testing.T) {
	got := infraErrs(map[string]string{
		"INFRA_ATTACHMENT_CIDR":    "100.66.0.0/16",
		"INFRA_ATTACHMENT_GATEWAY": "10.0.0.1",
	})
	if got == "" {
		t.Fatal("gateway outside the infra CIDR accepted — pods would get an unreachable next hop")
	}
}

// A disabled cluster legitimately carries empty values; validation must not
// force an operator to fill them in just to install without dual-homing.
func TestValidateInfraAttachment_AllowsTheDisabledShape(t *testing.T) {
	got := infraErrs(map[string]string{
		"INFRA_ATTACHMENT_ENABLED": "false",
		"INFRA_ATTACHMENT_ROUTES":  "",
		"NODE_CIDR":                "",
	})
	if got != "" {
		t.Fatalf("rejected the legitimate disabled shape: %s", got)
	}
}

// The platform front door is the one address a managed control plane must
// reach beyond its own etcd (OpenBao Transit for the kms-plugin sidecar, S3 for
// snapshots). Every case below installed cleanly on a lab cluster and then parked four
// control-plane pods with zero workers (2026-09-08). The "must be set" error
// fires only when the front door is ESTABLISHED (announced VIP == arrival on a
// MetalLB layer) and sits inside the routes — never on a guess. The none layer
// and the platform-endpoint overlay are never demanded: the file cannot tell a
// NAT-free node from a 1:1-NAT one, nor whether the overlay's DNS rewrite is
// in place; the scaffold seeds or warns for those.
func TestValidateInfraAttachment_PlatformIngressVIP(t *testing.T) {
	base := map[string]string{
		"INFRA_ATTACHMENT_ENABLED":        "true",
		"INFRA_ATTACHMENT_ROUTES":         "192.168.110.0/24,172.30.0.0/22,10.100.0.0/16",
		"INFRA_ATTACHMENT_SECURITY_GROUP": "infra-lock-{namespace}",
	}
	with := func(mutate func(map[string]string)) map[string]string {
		env := make(map[string]string, len(base)+4)
		for k, v := range base {
			env[k] = v
		}
		mutate(env)
		return env
	}

	// Seeded value inside the node route: the ordinary healthy file.
	if got := infraErrs(with(func(v map[string]string) { v[KeyPlatformIngressVIP] = "192.168.110.180" })); got != "" {
		t.Fatalf("front door inside the routes rejected: %s", got)
	}
	// Front door OUTSIDE the routes with the key empty: the pod reaches it over
	// the tenant route, nothing to grant, nothing to complain about.
	if got := infraErrs(with(func(v map[string]string) {
		v["INGRESS_ADDRESS_LAYER"] = "metallb-l2"
		v["METALLB_FLOATING_IP"], v["KUBE_API_ARRIVAL_IP"] = "203.0.113.10", "203.0.113.10"
	})); got != "" {
		t.Fatalf("tenant-route front door demanded a key: %s", got)
	}
	// Front door INSIDE the routes with the key empty on a MetalLB layer: the
	// live failure. The error must name the exact assignment.
	got := infraErrs(with(func(v map[string]string) {
		v["INGRESS_ADDRESS_LAYER"] = "metallb-l2"
		v["METALLB_FLOATING_IP"], v["KUBE_API_ARRIVAL_IP"] = "192.168.110.180", "192.168.110.180"
	}))
	if !strings.Contains(got, KeyPlatformIngressVIP+"=192.168.110.180") {
		t.Fatalf("infra-NIC front door without the key accepted, or the fix not named: %q", got)
	}
	// none layer: the node's own address is inside the routes, but the file
	// cannot say whether 1:1 NAT stands in front of it (both keys carry the
	// post-NAT address there). Not demanded — the scaffold seeds or warns.
	if got := infraErrs(with(func(v map[string]string) {
		v["INGRESS_ADDRESS_LAYER"] = "none"
		v["NODE_EXTERNAL_IP"], v["KUBE_API_ARRIVAL_IP"] = "192.168.110.180", "192.168.110.180"
	})); got != "" {
		t.Fatalf("none-layer node address demanded as the front door: %s", got)
	}
	// A MetalLB arrival address that no longer matches the announced VIP (a
	// clone's leftover) is likewise not established.
	if got := infraErrs(with(func(v map[string]string) {
		v["INGRESS_ADDRESS_LAYER"] = "metallb-l2"
		v["METALLB_FLOATING_IP"], v["KUBE_API_ARRIVAL_IP"] = "192.168.110.181", "192.168.110.180"
	})); got != "" {
		t.Fatalf("stale arrival address demanded as the front door: %s", got)
	}
	// The platform-endpoint overlay: enabling it does not prove bao. is
	// rewritten to the internal VIP, so nothing is demanded — not the internal
	// VIP, and not the MetalLB VIP that would otherwise be established.
	if got := infraErrs(with(func(v map[string]string) {
		v["INGRESS_ADDRESS_LAYER"] = "metallb-l2"
		v["METALLB_FLOATING_IP"], v["KUBE_API_ARRIVAL_IP"] = "192.168.110.180", "192.168.110.180"
		v["PLATFORM_ENDPOINT_ENVOY_GATEWAY_ENABLED"] = "true"
		v["ENVOY_GATEWAY_INTERNAL_VIP"] = "192.168.110.31"
	})); got != "" {
		t.Fatalf("overlay topology demanded a guess: %s", got)
	}

	for name, vip := range map[string]string{
		"outside-routes": "203.0.113.10",
		"ipv6":           "2001:db8::1",
		"ipv4-mapped":    "::ffff:192.168.110.180",
		"cidr":           "192.168.110.180/32",
		"hostname":       "bao.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			if got := infraErrs(with(func(v map[string]string) { v[KeyPlatformIngressVIP] = vip })); got == "" {
				t.Fatalf("%s=%q accepted", KeyPlatformIngressVIP, vip)
			}
		})
	}

	// The persistable decline is accepted as-is even where the front door is
	// established and inside the routes: the scaffold rewrites it to empty
	// and is the one that warns about the cost.
	if got := infraErrs(with(func(v map[string]string) {
		v["INGRESS_ADDRESS_LAYER"] = "metallb-l2"
		v["METALLB_FLOATING_IP"], v["KUBE_API_ARRIVAL_IP"] = "192.168.110.180", "192.168.110.180"
		v[KeyPlatformIngressVIP] = PlatformIngressVIPNone
	})); got != "" {
		t.Fatalf("deliberate decline rejected: %s", got)
	}

	// Dual-homing off: the chart never renders the key, so only its shape is
	// checked — a value outside routes that do not apply is not an error.
	off := map[string]string{"INFRA_ATTACHMENT_ENABLED": "false", KeyPlatformIngressVIP: "203.0.113.10"}
	if got := infraErrs(off); got != "" {
		t.Fatalf("inert key rejected while dual-homing is off: %s", got)
	}
	if got := infraErrs(map[string]string{"INFRA_ATTACHMENT_ENABLED": "false", KeyPlatformIngressVIP: "nope"}); got == "" {
		t.Fatal("malformed inert key accepted")
	}
}
