# GPU security threat model

**Status:** ready for Security and Product approval; open residual risks are
listed below and tenant GPU creation remains independently gated.

**Scope:** Shared GPU containers, Dedicated GPU VMs, catalog/discovery, quota,
billing add-ons, operator installation, node-mode transitions, monitoring, and
the tenant/admin user interfaces.

This model complements the platform [security model](security-model.md). It is
the review artifact for GPU tracker task G9-T01; it does not turn a cooperative
software-sharing mechanism into a hardware security boundary.

## Security objectives

The GPU design must preserve these invariants:

1. A tenant cannot gain GPU entitlement by creating or editing ordinary
   Kubernetes objects.
2. A GPU request is accepted only for an enabled stable catalog profile and
   within organization plus project quota.
3. A Shared GPU Pod cannot bypass the selected scheduler, request bounds, or
   admission-visible runtime restrictions.
4. Failure of the Shared GPU mutating webhook fails closed for GPU Pods without
   blocking ordinary Pods.
5. A Dedicated GPU VM can receive only a catalogued whole device through the
   KubeVirt controller and cannot be live-migrated or directly node-steered.
6. Shared-container and VM device plugins never own the same physical device at
   the same time.
7. Tenant responses never expose node names, native device-resource names,
   PCI selectors, device UUIDs, or other tenants' holders.
8. Privileged reads or writes happen only after authorization with the caller's
   identity and organization/project scope.
9. Add-on, annotation, HRQ, status, UI, and invoice quantity converge before a
   GPU product is shown as ready.
10. Entitlement reductions, cancellation, and suspension never undercut active
    authoritative usage.
11. Mode changes and upgrades stop on holders, unsupported version tuples, or
    incomplete rollback/monitoring evidence.
12. Every failure either denies a new allocation, leaves an existing holder
    intact for controlled release, or keeps the affected node cordoned.

## Trust boundaries and actors

| Actor/component | Trust | Relevant authority |
|---|---|---|
| Project member/admin | Untrusted for platform integrity | Creates namespaced workloads and some ordinary ResourceQuotas/ConfigMaps |
| Organization admin | Partially trusted | Requests project-cap and add-on changes only inside the JWT-bound organization |
| Browser/UI | Untrusted client | Presents stable profile IDs; never supplies authoritative entitlement or device identity |
| kube-dc backend | Privileged after caller checks | Reads aggregate cluster inventory and writes reserved project quotas |
| kube-dc manager | Privileged controller | Reconciles billing annotations, HRQ, status, and release state |
| HNC and KubeVirt controllers | Trusted system identities | Propagate HRQ; create VMIs and launcher Pods |
| GPU scheduler/webhooks/plugins | Highly privileged | Mutate/schedule GPU Pods and access host devices/runtime state |
| Fleet/GitOps operator | Trusted administrative boundary | Owns drivers, plugins, monitoring, modes, upgrades, and rollback |
| Billing provider/webhook | External partially trusted input | Supplies provider item IDs/quantities; cannot directly write quota |
| Guest/container image | Untrusted workload code | Runs inside the selected Pod or VM isolation boundary |

The strongest host-risk boundary is the privileged GPU software supply chain.
The strongest tenant boundary offered in the first release is the whole-device
VM. Shared GPU is appropriate only for approved cooperative code.

## Threat register

| ID | Threat and impact | Controls and verification | Residual/owner |
|---|---|---|---|
| GPU-T01 | A compromised GPU Operator, scheduler, webhook, device plugin, or driver DaemonSet takes over a node through host mounts/privilege | Components are GitOps-owned and version-pinned; nodes/modes are explicit; tenant roles cannot modify them; upgrade and transition commands require reviewed state and rollback evidence | Image provenance/SBOM/CVE policy remains G9-T02; Platform + Security |
| GPU-T02 | A tenant bypasses Shared GPU memory/compute policy through alternate resources, scheduler, node steering, host access, runtime class, privilege, `envFrom`, or `CUDA_DISABLE_CONTROL` | Catalog-derived Pod admission validates request/limit equality and steps, exact scheduler, profile, node/runtime/security fields, volumes/devices, environment and ephemeral-container attach; the live matrix includes these reject paths | Shell/arguments and CUDA-library behavior cannot be completely inspected; D-003/B-004 Product + Security decision |
| GPU-T03 | The mutating webhook or scheduler fails and an unmodified GPU Pod reaches the default scheduler | The webhook failure policy is fail-closed only for profile-labelled GPU Pods; admission requires the injected GPU scheduler; ordinary Pods bypass the selector. Controlled outage and recovery passed live | Frozen-but-listening plugin requires monitoring; the allocation-canary alert is mandatory |
| GPU-T04 | A tenant requests a native whole-device resource directly or spoofs a KubeVirt launcher owner | VM/VMI admission requires stable profile propagation and exact catalog device mapping; launcher admission allows the native resource only for the configured KubeVirt controller identity with a real VMI owner; generic `hostDevices` are denied | KubeVirt controller compromise is cluster-admin impact; monitor controller/supply chain |
| GPU-T05 | VM attachment weakens the promised boundary through migration, node steering, or uncatalogued devices | Effective eviction strategy must be non-migrating; node name/selector/affinity and generic host devices are denied; catalog, KubeVirt allowlist, external provider, PCI selector and node capacity must agree | Guest driver remains tenant-controlled; no device identity stability promise |
| GPU-T06 | Shared and VM plugins claim one GPU concurrently, corrupting allocation or exposing a device twice | One expected/active mode label selects exactly one plugin; conflict/wrong-mode alerts, holder-safe transitions, exact plugin checks and rollback are implemented; live wrong-mode and both-direction driver handoff were exercised | A transition after cordon failure stays cordoned for operator recovery |
| GPU-T07 | A project admin creates a ResourceQuota that inflates entitlement or changes controller-owned quota | Only exact reserved names are trusted; Kubernetes-effective hard is the minimum and used is the maximum; a VAP denies reserved-name writes except backend/HNC and deletion controllers; tenant RBAC is read-only for quota | Backend/controller service-account compromise can change quota; audit those identities |
| GPU-T08 | An org admin uses the backend service account to edit another organization/project | Every request first checks the caller JWT role, reads the Project with the caller token inside the token-derived organization, validates identifiers, then performs the narrow privileged write; route tests cover cross-org rejection | Cluster-wide core RBAC cannot restrict create-by-name; authorization ordering is the compensating control |
| GPU-T09 | Discovery leaks node, PCI, UUID, native resource, or other-tenant holder data | Tenant access uses per-request SSAR and org annotation scope; only a field-allowlisted aggregate is cached/returned; admin details use a separate superadmin route; errors are static; URL segments are encoded | Tenant dashboards/recording rules must be separately reviewed under G9-T05 |
| GPU-T10 | A tenant amplifies cluster-wide Node/Pod/KubeVirt discovery into API-server denial of service or cache poisoning | Discovery uses a 30-second single-flight refresh and stores only the immutable redacted aggregate; authorization stays outside the cache; namespace quota reads remain scoped | Add endpoint request metrics/rate policy if pilot load shows abuse; API/SRE |
| GPU-T11 | Catalog drift maps a stable profile to the wrong native resource or unsafe request bounds | Go, backend, Helm and admission validate profile shape, unique resource names, numeric ranges/steps, billing eligibility and passthrough consistency; invalid catalogs fail render/reconcile/discovery closed | Rules exist across languages; hardware-free fixture CI and G6 consistency tests are drift controls |
| GPU-T12 | Billing provider quantity or webhook replay grants unintended quota | Provider price mapping is catalog-owned; placeholder/missing IDs fail before mutation; one subscription item quantity maps to one stable add-on; controller, not provider, reconciles HRQ; readiness requires annotation/HRQ/status agreement | External Stripe acceptance and provider audit remain G7-T08/G10-T07 |
| GPU-T13 | Reduction/cancel/suspend removes quota under active Pods/VMIs, causing accounting or service inconsistency | Authoritative HRQ usage and named Pod/VMI blockers fail reductions closed across quota-only, Stripe and WHMCS; unsafe trial expiry defers release, emits warnings and retries | Live holder lifecycle acceptance remains G7-T13/T14 |
| GPU-T14 | A user mistakes quota for reserved capacity or software compute percentage for hard performance/isolation | UI separates entitlement, project cap, and physical capacity; copy says quota is not reservation; Shared GPU disclosure describes cooperative isolation and compute convergence/library limitations; whole-device VM is the hard-boundary option | D-003 and D-006 require explicit Product approval before beta/reservation sales |
| GPU-T15 | Pending/error UI shows another project's cached data or stale data as live | Project identity keys the hook state; project changes clear attribution; polling preserves stale/error state instead of relabelling it live; unknown reason codes have safe copy | Browser RBAC/accessibility E2E remains G4-T08/G9-T15 |
| GPU-T16 | Operator upgrade/mode commands disrupt holders or leave two owners active | Commands require exact live/fleet agreement, clean Git, both creation gates off, zero holders before and after cordon, qualification tuple/canary evidence, atomic Git operations, target plugin/label/Ready checks, and explicit cordoned resume | Real Wave-0-gated transition and selected upgrade canary remain G8-T11/T12 |
| GPU-T17 | A failed device/node/plugin continues accepting new work or silently loses allocations | Unhealthy/conflicting/stale states remove readiness or fail discovery closed; allocation canary, plugin/scheduler/webhook/mode alerts and quota guards cover known paths | ECC/XID/thermal and complete failure matrix remain G9-T03/T04/T06/T10 |
| GPU-T18 | Admin monitoring data leaks physical or cross-tenant identity into a tenant Grafana organization | Admin inventory and tenant capability contracts are separate; public fields are allowlisted; tenant metrics must use org/project scope and omit node/device labels | Recording rules and cross-tenant dashboard proof remain G9-T03/G9-T05 |
| GPU-T19 | Secrets or licensed vGPU credentials enter ordinary config, logs, plans, or tenant APIs | Current installer accepts only a secret-readiness boolean and rejects secret/license/UUID-shaped output; vGPU is deferred | SOPS wiring and licensed vGPU review remain G8-T06/G11 |

## Control ownership

| Control plane | Authoritative controls |
|---|---|
| Admission | Shared Pod VAP; VM/VMI VAP; launcher Pod rules; reserved ResourceQuota VAP |
| Authorization | Tenant RBAC; caller-token SSAR; JWT organization scope; superadmin inventory role |
| Entitlement | Billing catalog/annotation; manager reconciliation; organization HRQ; optional project cap |
| Discovery | Validated catalog; redacted single-flight aggregate; exact KubeVirt/resource consistency |
| Runtime | One GPU mode/plugin owner; scheduler/webhook; whole-device VM attachment |
| Operations | Flux ownership; allocation canary/alerts; upgrade and transition gates; rollback runbooks |
| Product | Independent discovery/billing/shared-create/VM-create flags; isolation and capacity disclosure |

No browser decision, Pod annotation, tenant-created quota, provider callback, or
Node label alone is sufficient to grant entitlement or prove readiness.

## Required security evidence

Before the first tenant beta, the release record must retain:

- rendered admission policies and their reject/allow matrix, including live
  webhook failure and controller-created launcher behavior;
- caller-token authorization, cross-org rejection, reserved-quota protection,
  and tenant/admin redaction tests;
- exact image/chart/driver provenance plus scan disposition;
- zero-entitlement baseline, one controlled grant, HRQ/status convergence, and
  holder-safe release evidence;
- firing and recovery evidence for frozen plugin, wrong mode, health, and quota
  conditions;
- a whole-device VM create/run/stop/start/migration-denial and guest-driver
  qualification record;
- manual review of Shared GPU isolation copy and D-003 acceptance;
- cross-tenant monitoring/dashboard tests with no node/device identity leakage;
- rollback timing ending with zero holders and one authoritative plugin owner.

## Approval gates and residual risks

Security approval must not be inferred from tests alone. G9-T01 can close only
when Security and Product explicitly accept or defer all of the following:

1. **Shared compute is cooperative.** HAMi memory/control injection is not a
   hostile-tenant boundary, startup can exceed a requested compute percentage,
   and some CUDA library paths can remain above it (D-003/B-004).
2. **Privileged supply chain.** GPU drivers and operators have node-compromise
   blast radius. Exact installer pins and the runtime digest audit are in the
   [GPU supply-chain policy](gpu-supply-chain.md); SBOM/scan disposition remains
   required under G9-T02.
3. **Capacity is not reservation.** Quota/add-on entitlement does not guarantee
   a free physical device; any reserved product needs the separate G7-T15
   operational contract (D-006).
4. **Guest support is still gated.** The VM attachment lifecycle is proven, but
   Ubuntu/Windows driver and CUDA qualification remains G0-T08/G6-T09.
5. **Observability is incomplete.** Hardware health recording rules, tenant
   dashboards and cross-tenant leakage proof remain G9-T03–T06.

Until those decisions are recorded, keep Shared GPU and Dedicated GPU VM tenant
creation disabled and use operator-owned validation only.
