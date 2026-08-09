package clusterinit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
)

// CheckFrontDoorMatchesOverlay guards the resume path against a front-door
// change that would silently evaporate.
//
// A re-run of `init` against an existing cluster overlay RESUMES: it skips the
// scaffold, and with it postProcessClusterConfig and WriteAddons. That is
// correct for retrying an interrupted install — but it means an operator who
// re-runs with a DIFFERENT --ingress-address-layer gets a clean exit and no
// change whatsoever. The flag is accepted, the plan prints the new layer, and
// the cluster keeps the old front door.
//
// Silently applying it instead would be worse. Flipping the layer rewrites the
// Envoy Service shape and installs or orphans MetalLB — a live-traffic change
// that must not ride along on "resume the install that died at step 9".
//
// So the resume refuses, and points at the day-2 path: edit the fleet and let
// Flux converge, which is where every other post-install change already lives.
func CheckFrontDoorMatchesOverlay(fleetRepo, clusterName, wantLayer string) error {
	want := strings.TrimSpace(wantLayer)
	envPath := filepath.Join(fleetRepo, "clusters", clusterName, "cluster-config.env")
	env, err := config.LoadEnv(envPath)
	if err != nil {
		// errors.Is, NOT os.IsNotExist: config.LoadEnv wraps with %w, and
		// os.IsNotExist does not unwrap — so a MISSING overlay read as
		// "unreadable" and fell into the fail-closed branch below.
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing to compare against
		}
		if want == "" {
			// No front-door request to honour, so an unrelated parse problem
			// must not block an otherwise-fine resume; the callers that need
			// the overlay report it themselves.
			return nil
		}
		// FAIL CLOSED when there IS a request. Treating an unreadable overlay
		// as "no mismatch" is the silent-ignore this guard exists to prevent:
		// the layer would be accepted, the resume would write nothing, and the
		// operator would be told the install succeeded.
		return fmt.Errorf("%w: cannot verify the current front door because %s could not be read (%v), "+
			"and you asked for %q. Fix or remove the overlay, or re-run without --ingress-address-layer",
			ErrFrontDoorChangeOnResume, envPath, err, want)
	}
	m := env.AsMap()
	haveLayer, _ := resolveAddressLayer(m["INGRESS_ADDRESS_LAYER"],
		m["METALLB_FLOATING_IP"], m["METALLB_MODE"])
	if want == "" || want == haveLayer {
		return nil
	}
	return fmt.Errorf("%w: this cluster's overlay already declares the front door as %q "+
		"and you asked for %q. A resume skips the scaffold, so the new layer would be accepted and then "+
		"silently ignored — and applying it here would rewrite the Envoy Service and install or orphan MetalLB "+
		"on a cluster that is already serving traffic. Change the front door as a day-2 edit instead: set "+
		"INGRESS_ADDRESS_LAYER=%s in clusters/%s/cluster-config.env (plus the addons layers if you are moving "+
		"to or from a VIP), commit, and let Flux converge. Re-run this command without "+
		"--ingress-address-layer to finish the resume",
		ErrFrontDoorChangeOnResume, haveLayer, want, want, clusterName)
}
