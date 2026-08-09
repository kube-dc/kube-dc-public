# Greenfield installer E2E harness

Codifies the manual customer-flow acceptance run (installer-bugs "D3") into a
repeatable, assertion-driven script so the greenfield install can't silently
regress. It drives the exact documented customer flow — `bootstrap install` →
`fetch-kubeconfig` → `bootstrap init` — and asserts the outcome.

## What it asserts

1. `INSTALL_RC=0` — RKE2 comes up over SSH.
2. `FETCH_RC=0` + the fetched kubeconfig reaches the apiserver.
3. **`storageprofiles.cdi.kubevirt.io` CRD absent before `init`** — proves a genuine greenfield cluster (the CDI-split fix's whole premise).
4. `INIT_RC=0` — with **zero manual `kubectl` between steps** (the governing rule from [`docs/internal/e2e-installer-test-plan.md`](../../../docs/internal/e2e-installer-test-plan.md) §8: any required manual step is a *finding*, not a workaround).
5. `platform` **and** `platform-cdi-storage` Kustomizations reach Ready; the CDI operator registers the `storageprofiles` CRD at runtime and `StorageProfile local-path` is applied — i.e. the greenfield CDI deadlock stays fixed with no manual bridge.
6. OpenBao `storage_type=raft` (the `file` backend 405s generate-root → no controller-auth/KMS — must never recur).

## Running it

Everything real is supplied via env — nothing operator/cluster-specific is
hardcoded (enforced by `cli/internal/lint/no_real_infra_test.go`, which scans
this tree). Copy the example config **out of the repo**, fill it in, source it:

```bash
cp tests/e2e/installer/installer-e2e.env.example ~/installer-e2e.env
$EDITOR ~/installer-e2e.env          # real IP / domain / kubeconfig / init-config paths
source ~/installer-e2e.env
tests/e2e/installer/run-install-e2e.sh
```

Exits non-zero on the first failed gate, so it doubles as an acceptance gate.

## Post-install health (optional)

The script stops at "platform converged + INIT_RC=0". For deeper health —
KMSKey Ready, org/project smoke — reuse the Ginkgo suite against the fresh
cluster:

```bash
KUBE_DC_E2E_DOMAIN=<domain> KUBE_DC_E2E_ORG=<org> KUBE_DC_E2E_PROJECT=<proj> \
  make test-e2e-focus FOCUS='KMSKey'
```

## Operator execution

There is no GitHub Actions wrapper for this hardware-backed test. Provision a
fresh RKE2-capable node out-of-band, populate the environment file described
above, and invoke `tests/e2e/installer/run-install-e2e.sh` directly from a
trusted operator host.
