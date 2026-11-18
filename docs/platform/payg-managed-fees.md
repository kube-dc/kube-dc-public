# Managed-service fees (optional)

A managed service can carry an hourly management fee on top of the resources
it uses. Kube-DC charges that fee from evidence, not from configuration: every
five minutes a census records which managed services the Services hub and
runner have committed, and a fee hour is charged only when thirteen of those
captures agree and show the writers the fee source was enrolled for. A missing
or inconsistent capture leaves the hour uncharged; it never becomes a guess.

Use [the optional billable profile](payg-installation-profile.md) for the full
installation preflight and statement workflow.

Managed-service fees are off by default. They need
[PAYG billing](payg-billing.md) and managed services on the same installation.

## How it is set up

| Step | When | Who | What it does |
|------|------|-----|--------------|
| 1. Prepare the layer | `kube-dc bootstrap init` | CLI | Writes the fee layer, suspended, and pins the census image. Nothing runs. |
| 2. Render the scope | Once Services runs | Operator | Reads the running installation and prints the scope lines for `cluster-config.env`. |
| 3. Review and commit | After step 2 | Operator | The committed scope is what the census trusts. Unsuspend the layer: capture starts. |
| 4. Enroll the first fee period | When fees should start | Billing operator | Publishes and activates the fee terms for a period. Fee hours start. |
| 5. Renew on each Services release | Every release | Automatic | When a release moves the hub and runner images, the fee source follows them by itself. |

### 1. Prepare the layer

Pass `--payg-managed-fees` with `--payg` and managed services on:

```bash
kube-dc bootstrap init ... --payg --managed-services=on --payg-managed-fees
```

The scaffold writes `clusters/<name>/payg-managed-fees.yaml`, the Flux
Kustomization of the shared `platform/payg-managed-fee` bundle, with
`suspend: true`, lists it in `clusters/<name>/kustomization.yaml`, and adds
`PAYG_FEE_CENSUS_IMAGE` (from the release pins, digest-pinned) and
`PAYG_FEE_CENSUS_SUSPEND=false` to `cluster-config.env`. The layer stays
suspended because its scope does not exist yet: Flux does not build a
suspended Kustomization, so its unset variables cannot fail the reconcile.

`--payg-managed-fees` is refused without `--payg` or without managed services.
`--set` of any `PAYG_FEE_SCOPE_*` or other scope key is refused: the scope
describes the running installation, which does not exist at bootstrap.
Override the census image with
`--set PAYG_FEE_CENSUS_IMAGE=repo:tag@sha256:...`. A resume with the flag
adds a missing layer, again suspended.

### 2. Render the scope

The scope names this installation's cluster, namespaces, Services data plane,
the hub and runner Deployments with their digest-pinned images, the Services
cell's public signing keys, and which service families carry a management fee.
Render it from the running installation with the census image and a
kubeconfig that verifies the API server's TLS certificate:

```bash
FEE_CENSUS_IMAGE='REPOSITORY:TAG@sha256:DIGEST'
docker run --rm --read-only --user "$(id -u):$(id -g)" \
  --entrypoint /payg-managed-fee-evidence \
  -v "$HOME/.kube/<name>_config:/kubeconfig:ro" \
  "$FEE_CENSUS_IMAGE" \
  -render-scope -kubeconfig /kubeconfig \
  -data-plane <service-data-plane> \
  -fee-families postgresql,mysql,mariadb,valkey \
  -unpriced-families github-runner
```

Set `FEE_CENSUS_IMAGE` to the exact `PAYG_FEE_CENSUS_IMAGE` pin, including
its repository and digest. Use a self-contained kubeconfig; for a local API
tunnel, make that tunnel reachable from the utility container (for example,
`--network host` on Linux). The user option lets the utility read your mounted
kubeconfig without changing its file permissions. `-data-plane` is the name of the
installation's `ServiceDataPlane` (`kubectl get servicedataplanes`). Choose
`-fee-families` and `-unpriced-families` from the families your price list
covers: a fee family is charged its management fee; an unpriced family is
recognized and carries none. Use `-hub-namespace` if the Services hub does
not run in `kube-dc-services`.

The render reads no Secret and writes nothing. It takes the signing keys from
the trust ConfigMap the hub publishes to the runner, recomputes each key ID
from its key, and refuses when the hub, the `ServiceDataPlane` and the trust
ConfigMap disagree about the cell, the audience, the runner namespace or the
enrolled key, or when a writer image is not pinned by digest. It prints six
lines:

```text
PAYG_FEE_SCOPE_JSON=...
PAYG_FEE_SCOPE_REVISION=...
PAYG_FEE_HUB_NAMESPACE=...
PAYG_FEE_RUNNER_NAMESPACE=...
PAYG_FEE_DATA_PLANE_NAME=...
PAYG_FEE_API_CIDRS=...
```

The render supports a hub and data plane in the same cluster, which is what
the fee bundle connects to. It reads only the cell's current signing key, so
render a scope for a new installation, not to replace the scope of one that
already has fee evidence.

### 3. Review and commit

Read the scope before you commit it: the commit, not the render, decides what
the census trusts. Check the cluster and namespace UIDs, the data plane, the
two writer images (they are the Services release you run), the key ID and the
fee families. Add the six lines to `clusters/<name>/cluster-config.env`,
change `suspend: true` to `suspend: false` in
`clusters/<name>/payg-managed-fees.yaml`, and push.

Within a few minutes the `kube-dc-managed-fee-census` CronJob in `monitoring`
captures every five minutes. Each successful Job prints an acknowledgement
with the census digest and the scope digest. Nothing is charged yet.

### 4. Enroll the first fee period

Fee hours need an enrolled fee period: the terms (SKUs and their period), a
baseline census, and an activation recorded before the first hour. This is a
reviewed billing step, done with the census image's `-enroll-source` and
`-activate-source` actions by whoever operates billing for the installation.
Then declare the period in `cluster-config.env`:

```text
PAYG_FEE_PAYABLE_ENROLLMENT=sha256:<activated enrollment digest>
PAYG_FEE_PAYABLE_SUSPEND=false
```

Enable automatic writer renewal after the root is activated:

```text
PAYG_FEE_RENEWAL_SUSPEND=false
```

Each period has an end. The installation's standing settlement authorization
can continue configured sources into the next month. Without source
continuation, enroll and activate the next period before the old one ends.
The payable worker follows successors from the declared root; do not replace
that root for a normal writer renewal.

### 5. Automatic renewal on Services releases

A Services release replaces the hub and runner images, and a fee source only
trusts the writers it was enrolled for. The `kube-dc-managed-fee-writer-renewal`
CronJob follows such a release by itself: when the scope's writer pins change
(the release tooling updates them with the image pins), it publishes a successor
for the new writers at the next whole hour at least fifteen minutes ahead and
activates it, after checking the census history since the current source. The
hours between the release and that hour are uncharged.

Each pass prints its outcome:

| Outcome | Meaning |
|---------|---------|
| `disabled` | No fee period is declared yet (`PAYG_FEE_PAYABLE_ENROLLMENT` unset). |
| `current` | The fee source already follows the running writers. |
| `waiting` | The new writers are not captured yet, or a capture was still importing. The next pass checks again. |
| `renewed` | A successor for the new writers was published and activated. |
| `activated` | A successor published by an interrupted pass was activated. |

Renewal follows compatible writer image changes and additions of recognized
unpriced families. Existing fee families, SKUs, authority and keys remain
fixed. Removing a family or changing a priced family, SKU or another source
term stops the pass with an error and needs the billing operator. The Job then fails, which your alerting sees, and the
affected hours stay uncharged until it is resolved.

## Turning it off

Set these values in `cluster-config.env`, commit and reconcile the layer:

```text
PAYG_FEE_CENSUS_SUSPEND=true
PAYG_FEE_PAYABLE_SUSPEND=true
PAYG_FEE_RENEWAL_SUSPEND=true
```

Confirm all three CronJobs show `spec.suspend: true` and let any active Jobs
finish. This stops future Jobs and preserves recorded evidence and charged
hours. Suspending the Flux Kustomization alone only stops reconciliation; it
does not stop CronJobs that are already installed. Customer telemetry and
managed-service workloads continue independently.
