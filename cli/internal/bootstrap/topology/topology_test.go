package topology

import (
	"strings"
	"testing"
)

// TestClassify drives the classifier with hand-authored Signal lists,
// matching the per-probe shapes that probeForkEServices /
// probeCloudProvider / probeEnvoyExternalIPs / probeEnvoyHostNetwork
// produce against real clusters. The probes themselves shell out to
// kubectl and are smoke-tested at the command level — this test
// covers the decision matrix exhaustively without needing a live
// cluster.
func TestClassify(t *testing.T) {
	tests := []struct {
		name           string
		signals        []Signal
		wantClass      Class
		wantVerdict    Verdict
		wantConfidence string
		wantReasonHas  string // substring expected in Reasoning
	}{
		{
			name: "Fork E already deployed → already-enabled wins over everything",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "both kube-api-platform + envoy-gateway-platform deployed", Hint: ClassA, Confidence: "high"},
				{Probe: probeNameCloudProvider, Detail: "providerID scheme: aws", Hint: ClassC, Confidence: "medium"},
				{Probe: probeNameEnvoyExtIPs, Detail: "externalIPs=[1.2.3.4]", Hint: ClassB, Confidence: "high"},
			},
			wantClass:      ClassA,
			wantVerdict:    VerdictAlreadyEnabled,
			wantConfidence: "high",
			wantReasonHas:  "currently enabled",
		},
		{
			name: "Fork E partial (only kube-api-platform) — still already-enabled",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "kube-api-platform deployed (envoy-gateway-platform not yet)", Hint: ClassA, Confidence: "high"},
			},
			wantClass:   ClassA,
			wantVerdict: VerdictAlreadyEnabled,
		},
		{
			name: "Cloud-provider takes precedence over Envoy externalIPs",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "neither platform-endpoint Service present", Confidence: "high"},
				{Probe: probeNameCloudProvider, Detail: "providerID scheme: aws", Hint: ClassC, Confidence: "medium"},
				{Probe: probeNameEnvoyExtIPs, Detail: "externalIPs=[1.2.3.4]", Hint: ClassB, Confidence: "high"},
			},
			wantClass:      ClassC,
			wantVerdict:    VerdictNotNeeded,
			wantConfidence: "medium",
			wantReasonHas:  "Cloud-provider",
		},
		{
			name: "Envoy externalIPs without cloud-provider → Class B",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "neither platform-endpoint Service present", Confidence: "high"},
				{Probe: probeNameCloudProvider, Detail: "no providerID on any node", Confidence: "high"},
				{Probe: probeNameEnvoyExtIPs, Detail: "externalIPs=[213.111.154.229]", Hint: ClassB, Confidence: "high"},
				{Probe: probeNameHostNetwork, Detail: "false or unset"},
			},
			wantClass:      ClassB,
			wantVerdict:    VerdictNotNeeded,
			wantConfidence: "high",
			wantReasonHas:  "flat-L2",
		},
		{
			name: "Envoy hostNetwork + ClusterIP, no externalIPs → Class A",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "neither platform-endpoint Service present", Confidence: "high"},
				{Probe: probeNameCloudProvider, Detail: "no providerID on any node", Confidence: "high"},
				{Probe: probeNameEnvoyExtIPs, Detail: "type=ClusterIP (probable hostNetwork bypass)", Hint: ClassA, Confidence: "medium"},
				{Probe: probeNameHostNetwork, Detail: "true (Envoy on host network)", Hint: ClassA, Confidence: "medium"},
			},
			wantClass:      ClassA,
			wantVerdict:    VerdictRequired,
			wantConfidence: "medium",
			wantReasonHas:  "1:1-NAT",
		},
		{
			name: "MetalLB-only Envoy service → Class A",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "neither platform-endpoint Service present", Confidence: "high"},
				{Probe: probeNameCloudProvider, Detail: "no providerID on any node", Confidence: "high"},
				{Probe: probeNameEnvoyExtIPs, Detail: "type=LoadBalancer, lb-class=metallb", Hint: ClassA, Confidence: "medium"},
				{Probe: probeNameHostNetwork, Detail: "false or unset"},
			},
			wantClass:   ClassA,
			wantVerdict: VerdictRequired,
		},
		{
			name: "B and A signals tied → B wins (per-node externalIPs is the load-bearing structural fact)",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "neither platform-endpoint Service present", Confidence: "high"},
				{Probe: probeNameCloudProvider, Detail: "no providerID on any node", Confidence: "high"},
				{Probe: probeNameEnvoyExtIPs, Detail: "externalIPs=[1.2.3.4]", Hint: ClassB, Confidence: "high"},
				{Probe: probeNameHostNetwork, Detail: "true", Hint: ClassA, Confidence: "medium"},
			},
			wantClass:   ClassB,
			wantVerdict: VerdictNotNeeded,
		},
		{
			name: "No signals at all → Unknown / Ambiguous",
			signals: []Signal{
				{Probe: probeNameForkE, Detail: "neither platform-endpoint Service present", Confidence: "high"},
				{Probe: probeNameCloudProvider, Detail: "no providerID on any node", Confidence: "high"},
				{Probe: probeNameEnvoyExtIPs, Detail: "EnvoyProxy CR not found"},
				{Probe: probeNameHostNetwork, Detail: "probe error: connection refused"},
			},
			wantClass:      ClassUnknown,
			wantVerdict:    VerdictAmbiguous,
			wantConfidence: "low",
			wantReasonHas:  "No probe produced",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Result{Signals: tc.signals}
			Classify(&r)

			if r.Class != tc.wantClass {
				t.Errorf("Class = %q, want %q", r.Class, tc.wantClass)
			}
			if r.Verdict != tc.wantVerdict {
				t.Errorf("Verdict = %q, want %q", r.Verdict, tc.wantVerdict)
			}
			if tc.wantConfidence != "" && r.Confidence != tc.wantConfidence {
				t.Errorf("Confidence = %q, want %q", r.Confidence, tc.wantConfidence)
			}
			if tc.wantReasonHas != "" && !strings.Contains(r.Reasoning, tc.wantReasonHas) {
				t.Errorf("Reasoning = %q, want substring %q", r.Reasoning, tc.wantReasonHas)
			}
			if r.Recommendation == "" {
				t.Error("Recommendation must always be set")
			}
		})
	}
}

// TestClassifyForkEDetailMatching verifies the probe-name-based
// foundEnabled detection in classify(). The "Fork E Services" probe
// name is matched case-sensitively; the detail string is searched
// for "kube-api-platform" / "envoy-gateway-platform" substrings.
func TestClassifyForkEDetailMatching(t *testing.T) {
	tests := []struct {
		name              string
		detail            string
		wantAlreadyOnPath bool
	}{
		{"both deployed", "both kube-api-platform + envoy-gateway-platform deployed", true},
		{"only kube-api-platform", "kube-api-platform deployed (envoy-gateway-platform not yet)", true},
		{"only envoy-gateway-platform", "envoy-gateway-platform deployed (kube-api-platform not yet)", true},
		{"neither present", "neither platform-endpoint Service present", false},
		{"unrelated detail string", "some other text", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Result{Signals: []Signal{
				{Probe: probeNameForkE, Detail: tc.detail, Confidence: "high"},
			}}
			Classify(&r)
			gotAlreadyEnabled := r.Verdict == VerdictAlreadyEnabled
			if gotAlreadyEnabled != tc.wantAlreadyOnPath {
				t.Errorf("foundEnabled detection: got AlreadyEnabled=%v, want %v (detail=%q)", gotAlreadyEnabled, tc.wantAlreadyOnPath, tc.detail)
			}
		})
	}
}
