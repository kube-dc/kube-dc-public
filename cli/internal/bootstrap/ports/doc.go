// Package ports is the interface contract between `kube-dc bootstrap` and
// everything outside the TUI engine — Kubernetes API, OpenBao, SOPS, Git, the
// fleet-repo bash scripts, the operator's host, DNS, SSH, systemctl, netplan.
//
// The TUI and the command engine import ONLY this package; concrete
// implementations live under `cli/internal/bootstrap/adapters/` (real) and
// `cli/internal/bootstrap/mock/` (test/demo fixtures). A small wiring layer
// (`cli/internal/bootstrap/wire.go`) picks one based on the `KUBE_DC_MOCK`
// env var.
//
// Why this split exists (see docs/prd/installer-agentic-implementation-plan.md
// agent rules 3-5 + §11 of the engineering PRD):
//
//   - **Mock-first**: any screen / command must be runnable with
//     `KUBE_DC_MOCK=<scenario> kube-dc bootstrap …`, no real cluster needed.
//     The mock adapter for each port returns canned data from a YAML fixture.
//   - **`--no-tty` first**: every cobra command exposes a plain-stdout path.
//     Both `--no-tty` and TUI share the same ports, so the engine doesn't
//     fork.
//   - **No hidden deps from the engine**: a reviewer can grep for
//     `kubernetes/client-go`, `os/exec`, `bao` in the engine package and
//     find zero hits. They live in adapters.
//
// **Stability**: this package is the public contract for adapter authors.
// Changes here ripple to every adapter + every mock. Treat interfaces as
// versioned; add new methods on new interfaces (e.g. `OpenBaoClientV2`)
// rather than mutating existing ones once adapters ship.
//
// **What MUST NOT appear in this package's imports**:
//   - kubernetes/client-go, apimachinery, controller-runtime (drag in MB of
//     apimachinery code and force every consumer to know about k8s types)
//   - os/exec, syscall (those are adapter concerns)
//   - github.com/hashicorp/vault/api or github.com/openbao/openbao/api
//   - any cloud-provider SDK (AWS, GCP, Azure, Cloudflare, …)
//
// What IS allowed: Go standard library (`context`, `time`, `io`, `errors`)
// plus tightly-typed simple structs we own.
//
// Each .go file in this package owns one port (one interface + the structs
// it returns/accepts). Splitting per file makes the contract easier to
// review one piece at a time.
package ports
