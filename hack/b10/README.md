# B-10 — tenant-VLAN live acceptance

Runnable form of the plan in `docs/prd/tenant-vlan-attachment-implementation.md` §18.

The point of B-10 is that design review is exhausted. Seven cross-review passes drove
*controller* findings to diminishing returns, while the two genuinely dangerous bugs of
the last round were both in what the product **hands a user** — a VM snippet that
detached the tenant VPC NIC, and a Deployment snippet that attached nothing. Neither is
visible without reading generated YAML. That class is settled by running it, not by
reading it again.

So these scripts drive the **product path**: the admin API and manifests taken verbatim
from the tenant API. Where a step could be done more conveniently with a hand-written
CR, it deliberately is not.

## Layout

| Script | What it proves | Disruptive? |
|---|---|---|
| `00-preflight.sh` | cluster is fit to test on: no wedged orgs, IPAM healthy, nodes labelled, health suite idle | no |
| `10-absent.sh` | with the feature off: no API, no nav, no route, no CRD reachability for tenants | no |
| `20-declare.sh` | segment + assignment created **through the admin API**, name derived, exclusivity refused | yes (creates) |
| `30-pod.sh` | a real Pod from the tenant API's own snippet: 3 NICs, default route on the VPC NIC, MTU, external peer | yes |
| `40-vm.sh` | a real VM from the tenant API's own snippet, same assertions | yes |
| `50-breach.sh` | the full breach matrix — every refusal the design claims | yes |
| `60-resilience.sh` | manager roll, cold start, total webhook outage, recovery; ordinary admission survives | **very** |
| `70-teardown.sh` | teardown blocked independently by a terminating Pod and by surviving OVN IPs, then ordered completion | yes |
| `99-cleanup.sh` | remove everything this run created, disable the feature | yes |

`run-all.sh` runs them in order and stops at the first failure.

## Before running

**This is not safe to run on a cluster that is doing other work.** `60-resilience.sh`
takes the admission webhook down entirely; on a shared cluster that means every pod
creation in scope fails while it runs. Check `00-preflight.sh` output and, on stage,
confirm the Playwright health suite is idle and will stay idle.

Do **not** narrow the webhook's namespace selectors to make this safer. That tests a
weaker configuration than the one that ships.

## The precondition no script can check

§18.4: address authority. Kube-OVN IPAM and the customer's own network both allocate on
this wire, and nothing here detects a collision — a binding looks healthy while IPAM
hands out an address their hardware already uses, and the symptom is intermittent ARP
flapping blamed on anything but us.

`00-preflight.sh` will ask you to confirm the range is exclusively ours. That
confirmation is a human one; treat it as a real gate.

## Recording the result

Every script appends to `RESULTS.md` in this directory: what ran, what was observed,
and the RC identity (commit, image digests, component versions). If B-10 fails, fix
only the observed blocker, cut RC2, and rerun the **whole** pass — not the failed step.

## Building the RC (note for whoever cuts RC2)

The manager **cannot** be built inside the in-cluster Dagger engine: it runs in
the deliberately network-restricted `shalb-dev` tenant and cannot reach
`proxy.golang.org` (the failure is a TLS handshake timeout on the first module
fetch). That is why `_buildManager` has a release path that takes a
pre-compiled binary.

Build it on the host and let the engine package it:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o bin/manager cmd/main.go
dagger -m cicd/dagger call build-image --image manager --src . --tag <TAG> \
  --manager-binary bin/manager \
  --manager-cabundle /etc/ssl/certs/ca-certificates.crt \
  --username shalb --token env:DOCKERHUB_TOKEN
```

`CGO_ENABLED=0` is load-bearing. A dynamically linked binary crash-loops on the
distroless base with `exec /manager: no such file or directory`, which reads
like a missing file rather than a linkage problem.

The UI images build in the engine normally — they do not need the Go proxy.
