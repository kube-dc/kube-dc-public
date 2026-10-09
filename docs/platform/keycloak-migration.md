# Migrate Keycloak from the Bitnami chart to upstream Keycloak 26

New installations run the upstream Keycloak image (`quay.io/keycloak/keycloak`
26.8) with a CloudNativePG database. Clusters installed earlier run the Bitnami
chart, whose free images (`bitnamilegacy/keycloak`) stopped at 26.3 and no
longer receive security fixes. This page moves an installed cluster to the
upstream deployment.

## How it works

All Keycloak state is in its database. The migration therefore copies the
database and swaps the runtime:

1. The Bitnami Keycloak is scaled to zero (write freeze). Sign-in is
   unavailable from here until step 4.
2. The fleet adds three components after the cluster's theme component:
   `keycloak-26` (Deployment, Service, admin Secret, CNPG `keycloak-db`),
   `keycloak-26-import-bitnami` (CNPG creates `keycloak-db` by copying the
   Bitnami database with `pg_dump`), and `keycloak-26-remove-bitnami` (drops
   the Bitnami HelmRelease from the render).
3. CNPG imports the data into `keycloak-db`.
4. Keycloak 26.8 starts and migrates the **copy's** schema.

The Bitnami PostgreSQL is only read. Until it is removed it is a complete
rollback: Keycloak cannot downgrade a migrated schema, but the original
database never sees the new schema.

What carries over unchanged: realms, users and password hashes, clients and
their secrets, identity providers, and the realm signing keys, so tokens
issued before the migration stay valid for the Kubernetes API, Grafana and
OpenBao. The `keycloak` Service, the `keycloak` admin Secret and the login
theme keep their names.

## Prerequisites

- The cluster renders the components (fleet `platform/keycloak/components/`).
- The CNPG operator is installed (it is part of infra-core).
- The Bitnami PostgreSQL is PostgreSQL 17 or older. `keycloak-db` runs 17;
  `pg_dump` refuses to dump a newer server.
- A maintenance window; sign-in is unavailable while it runs. Measured on
  the 2026-10-08 rollouts: 2 min with images pre-pulled and the objects
  applied directly; 4-5 min when waiting for Flux (Keycloak databases of
  48-490 MB); 10 min where the platform reconcile itself is slow. Pre-pull `docker.io/shalb/keycloak` and
  `ghcr.io/cloudnative-pg/postgresql:17` on the nodes first.
- An admin kubeconfig that does **not** sign in through this Keycloak. A
  `kube-dc login --admin` session expires during the freeze and cannot be
  refreshed until Keycloak is back; use the certificate kubeconfig.
- The cluster's theme reaches keycloak-26 through a theme component:
  `ui-*` for release themes, `kube-dc-theme-base` for the shared base theme.
  A theme set by a cluster overlay patch is not visible to keycloak-26
  (components run before overlay patches); move it into a component first.
- Branding comes from cluster-config (`KUBEDC_BRAND_NAME`,
  `KUBE_DC_UI_ENVIRONMENT_LABEL`, logo/favicon), not from the theme
  component's HelmRelease env. Where a theme component carried branding,
  patch the Deployment's env in the cluster (webdock does this for its brand
  name). Do not set `KUBEDC_BRAND_NAME` in cluster-config: the kube-dc chart
  nests it inside another default, which Flux substitution does not resolve.
- CloudSigma installations set
  `KEYCLOAK_FEATURES=preview,organization,admin-fine-grained-authz:v1`.
  cs-bff impersonates users through legacy token exchange, whose grants are
  v1 fine-grained permissions; 26.8's `preview` profile enables v2 instead
  and the exchange fails with `unknown_error`.

## Procedure

Set `KUBECONFIG` to the cluster, then:

```bash
# 0. Record the baseline (realms, user counts, signing key ids).
#    The Keycloak admin password is in the `keycloak` Secret.

# 1. Stop Flux from reverting the Bitnami release, then freeze writes.
flux suspend hr keycloak -n keycloak
kubectl -n keycloak scale statefulset keycloak --replicas=0
kubectl -n keycloak wait --for=delete pod/keycloak-0 --timeout=180s

# 2. Hand over the Service. The Bitnami Service selects on
#    app.kubernetes.io/instance and part-of; a server-side-apply takeover
#    would keep those keys and select no pods. Delete it so Flux creates the
#    new one.
kubectl -n keycloak delete svc keycloak keycloak-headless keycloak-metrics

# 3. In the fleet, list the components after the theme component in
#    clusters/<name>/platform.yaml, commit and push:
#      - ./keycloak/components/keycloak-26
#      - ./keycloak/components/keycloak-26-import-bitnami
#      - ./keycloak/components/keycloak-26-remove-bitnami
#    Remove any cluster patch that targets the Bitnami HelmRelease.
flux reconcile source git flux-system
flux reconcile kustomization platform

#    Faster: apply the keycloak-namespace Service, Deployment, Cluster and
#    ServiceMonitor from `scripts/render_as_flux.sh <cluster>` with
#    `kubectl apply --server-side --field-manager=kustomize-controller`
#    before pushing, so the import starts at once. Not the Secret: a local
#    render cannot decrypt cluster-secrets and would blank admin-password;
#    add only `admin-user: admin` to the existing Secret instead.

# 4. Wait for the import and the schema migration.
kubectl -n keycloak wait cluster.postgresql.cnpg.io/keycloak-db --for=condition=Ready --timeout=20m
kubectl -n keycloak rollout status deploy/keycloak --timeout=15m
```

## Verify

Compare a before/after listing of every realm's users, clients, groups,
identity providers and signing key ids; they must be identical. Then:

- `https://login.<domain>/realms/master/.well-known/openid-configuration`
  returns the public issuer.
- Realm list, user counts and signing key ids equal the baseline.
- A tenant signs in to the console; `kube-dc login` (browser and
  `--device-code`) works; Grafana opens from the console.
- The kube-dc manager logs no Keycloak errors after Keycloak is back. Its
  SMTP sync logs "Keycloak authentication failed" during the freeze; that
  stops once Keycloak answers.
- Prometheus scrapes the new pod (`up{namespace="keycloak"}`), and both
  `keycloak-db` instances report.
- On CloudSigma: cs-bff's exchange (client `cs-partner-admin`,
  `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`,
  `requested_subject`, `audience=kube-dc`) returns a token, and the BOC
  `cs-hosting-*` managed clusters are unchanged.

## Roll back (before cleanup)

```bash
# In the fleet: revert the components commit, push, reconcile.
kubectl -n keycloak delete deploy keycloak
kubectl -n keycloak delete svc keycloak
flux resume hr keycloak -n keycloak     # recreates the Service, scales Keycloak up
```

Then force the upgrade and scale up: resuming alone does nothing when the
values did not change, so the Service is not recreated.

```bash
flux reconcile hr keycloak -n keycloak --force
kubectl -n keycloak scale statefulset keycloak --replicas=1
```

The Bitnami release comes back on its original, unmigrated database (cs/crk
was rolled back this way on 2026-10-08 in about two minutes). Writes made
while 26.8 ran are lost. Delete `cluster/keycloak-db` once you no longer need
it for comparison.

## Clean up (after a soak period)

From the fleet repo, `scripts/keycloak-bitnami-cleanup.sh` does all of the
below with checks: it refuses unless the new Deployment and `keycloak-db` are
healthy, the Bitnami HelmRelease is suspended and `service/keycloak` no longer
has the Bitnami selector. Dry run by default:

```bash
KUBECONFIG=<cluster> scripts/keycloak-bitnami-cleanup.sh            # checks + plan
KUBECONFIG=<cluster> scripts/keycloak-bitnami-cleanup.sh --apply
KUBECONFIG=<cluster> scripts/keycloak-bitnami-cleanup.sh --apply --delete-data   # once backups cover keycloak-db
```

The same steps by hand:

Do **not** delete the Bitnami HelmRelease with its finalizer in place: Helm
would uninstall everything in its release manifest, which includes the
`keycloak` Secret and `keycloak` ServiceMonitor that the new deployment now
shares. Those two still carry Helm labels, so delete by **name**, never by
`app.kubernetes.io/managed-by=Helm`.

```bash
kubectl -n keycloak patch hr keycloak --type=merge -p '{"metadata":{"finalizers":null}}'
kubectl -n keycloak delete hr keycloak
kubectl -n keycloak delete secret -l owner=helm,name=keycloak     # release records only
kubectl -n keycloak delete statefulset keycloak keycloak-postgresql
kubectl -n keycloak delete svc keycloak-postgresql keycloak-postgresql-hl
kubectl -n keycloak delete pdb keycloak keycloak-postgresql
kubectl -n keycloak delete networkpolicy keycloak keycloak-postgresql
kubectl -n keycloak delete serviceaccount keycloak keycloak-postgresql
kubectl -n keycloak delete configmap keycloak-env-vars keycloak-values
# Keep: secret/keycloak and servicemonitor/keycloak (shared), the
# keycloak-postgresql Secret (keycloak-db's import source references it) and
# the data-keycloak-postgresql-0 PVC until backups cover keycloak-db.
```

`keycloak-26-import-bitnami` can stay listed: CNPG reads the bootstrap once,
when it creates `keycloak-db`.
