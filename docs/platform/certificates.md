# Platform TLS certificates

How the platform hostnames (`console.`, `login.`, `backend.`, `grafana.`,
`s3.`, `registry.`, …) get the certificate they serve, and how to run a
cluster on **your own wildcard certificate** so it survives a reinstall.

This is about the certificate the platform **serves**. For trusting a private
CA on the *outbound* side (the manager and backend calling Keycloak, OpenBao or
S3 over TLS), see [private-CA enterprise install](private-ca-enterprise-install.md).

## Three modes

`TLS_MODE` in `clusters/<name>/cluster-config.env` records the choice.

| Mode | What issues the certificate | Use when |
|---|---|---|
| `acme` (default) | cert-manager, via the `letsencrypt-prod-http` ClusterIssuer (HTTP-01 through the Gateway) | The domain is publicly resolvable **and** reachable on `:80` for the HTTP-01 challenge |
| `acme-dns01-route53` | cert-manager, same ClusterIssuer, but proving control via **Route53 DNS records** | The zone lives in Route53 but the cluster itself is private/VPN-only (Let's Encrypt cannot reach it) — certificates still **renew automatically** |
| `byo-wildcard` | You do. One wildcard for `*.<DOMAIN>`, committed SOPS-encrypted | Internal/air-gapped domains, corporate PKI, or any cluster ACME cannot validate — **nothing renews this for you** |

`acme` is the default and needs no configuration.

## `acme-dns01-route53`

The default issuer solves HTTP-01, which requires Let's Encrypt to reach the
platform on `:80`. A cluster whose `NODE_EXTERNAL_IP` is a private address can
never pass that — but if its public DNS zone is hosted in Route53, DNS-01
proves domain control by writing TXT records instead, and the cluster only
needs *outbound* internet. Renewal stays fully automatic, which is why this
mode beats `byo-wildcard` whenever it is available.

Prepare a **least-privilege IAM user** scoped to the one zone:
`route53:ChangeResourceRecordSets` + `route53:ListResourceRecordSets` on the
hosted zone, `route53:GetChange` on `*`. Then:

```bash
# the secret key never goes on the command line: file or environment
export KUBE_DC_DNS01_ROUTE53_SECRET_KEY='<secret access key>'

kube-dc bootstrap init … \
  --tls-mode acme-dns01-route53 \
  --dns01-route53-zone-id Z0EXAMPLE12345TEST \
  --dns01-route53-access-key-id AKIAEXAMPLETEST00000
```

What the CLI scaffolds (it never calls AWS itself):

- `clusters/<name>/dns01-route53-credentials.enc.yaml` — the secret access
  key, SOPS-encrypted, for cert-manager's `secretAccessKeySecretRef`;
- a `platform.yaml` patch swapping the `letsencrypt-prod-http` ClusterIssuer's
  solvers to `dns01: {route53: …}` (the issuer keeps its historical name —
  every platform Certificate references it, so nothing else changes);
- `TLS_MODE` + the non-secret solver config in `cluster-config.env`.

The plan pins the credential's SHA-256, so the key that ships at apply is the
key the reviewed plan approved. Temporary STS keys (`ASIA…`) are rejected —
the solver's static-secret shape cannot carry a session token.

Verify after the platform converges:

```bash
kubectl get certificate -A            # all Ready=True within ~2-5 min
kubectl get challenge -A              # empty when done; stuck "pending" =
                                      # wrong zone ID or key — check
                                      # cert-manager logs for the AWS error
```

**Rotation:** create a new IAM key, re-run `init` with the new material (the
SOPS artifact and env records update in place), commit, then disable the old
key in AWS.

Everything below is about `byo-wildcard`.

## Why a BYO certificate needs the ACME Certificates suppressed

This is the trap that makes a hand-rolled BYO setup fragile, so it is worth
stating plainly.

The platform declares cert-manager `Certificate` objects for every platform
hostname. If you simply drop your own Secret in place under the same name,
**cert-manager treats it as material to replace**:

```
Issuing certificate as Secret was previously issued by "Issuer.cert-manager.io/"
```

On an internal domain the HTTP-01 challenge can never validate, so those
Certificates sit permanently `False` and keep retrying — which looks like
harmless noise. It is not: the moment any issuer *did* succeed, cert-manager
would overwrite your wildcard and the platform would start serving a
certificate your clients do not trust.

So `byo-wildcard` has **two** halves, and both must be in Git:

1. your wildcard material, SOPS-encrypted, under every secret name the Gateway
   listeners reference; and
2. suppression of every platform ACME `Certificate`, so nothing competes.

Because both live in Git, a full reinstall restores the certificate exactly.

### Suppression must cover every Kustomization that declares Certificates

A Certificate is deleted from the render by the Flux Kustomization that
**declares** it. On a full platform that is more than one:

| Kustomization | Certificates |
|---|---|
| `platform` | the 12 platform hostname certs (wildcard, login, console, backend, admin, grafana, mimir ×3, loki, flux, openbao) |
| `registry-depot` | `registry-tls` → secret `registry-server-tls` |
| `infra-object-storage` | `s3-tls` → secret `s3-server-tls` |

Suppressing only `platform` leaves the other two alive, and Flux recreates them
after every deletion. Note `s3-tls` issues into `s3-server-tls`, which *is* one
of the BYO secrets — so that one is actively armed against your material.

## Procedure

### 1. Prepare the certificate

A single certificate valid for `*.<DOMAIN>` (add `<DOMAIN>` itself if you serve
the apex). Full chain in `tls.crt`, private key in `tls.key`:

```bash
# leaf + intermediates, LEAF FIRST (intermediate-first is rejected)
cat wildcard.crt intermediate.crt > tls.crt

# the key must match the certificate. Compare PUBLIC KEYS, not RSA moduli —
# `openssl rsa -modulus` fails outright on an ECDSA key, which is supported.
diff <(openssl x509 -in tls.crt -noout -pubkey) \
     <(openssl pkey -in tls.key -pubout) && echo "key matches certificate"

# the SAN must include the wildcard itself, not just one hostname
openssl x509 -noout -subject -dates -ext subjectAltName -in tls.crt
```

`init` rejects material that would half-work: a mismatched key, an expired or
not-yet-valid certificate, a chain whose first block is a CA (intermediate-first),
a malformed intermediate, a certificate whose extended key usage excludes server
auth, and — importantly — a certificate that lacks the `*.<DOMAIN>` SAN. A
certificate for only `console.<DOMAIN>` would otherwise be replicated to all
platform Secrets and then fail on every other hostname.

### 2. Let the CLI scaffold it

```bash
kube-dc bootstrap init \
  --name mycluster --domain example.com \
  --tls-mode byo-wildcard \
  --tls-cert ./tls.crt --tls-key ./tls.key \
  ...
```

`init` validates the material (parses, key matches certificate, not expired,
and the SAN actually covers `*.<DOMAIN>`), then writes:

- `clusters/<name>/wildcard-tls-secrets.enc.yaml` — one SOPS-encrypted
  `kubernetes.io/tls` Secret per platform secret name, each labelled
  `kube-dc.com/byo-wildcard-tls: "true"`;
- the suppression patches in each Kustomization that declares Certificates; and
- `TLS_MODE=byo-wildcard` in `cluster-config.env`.

Commit and push.

### 3. Delete the pre-existing Certificates — once, by hand

**Flux does not do this for you.** These Kustomizations run with `prune: false`,
so `$patch: delete` removes a Certificate from *future* renders but leaves the
object already in the cluster alive and reconciling. Until you delete it, it is
still able to overwrite your Secret.

First confirm the deletion cannot take your Secret with it:

```bash
# must print nothing — with this flag set, deleting a Certificate DELETES its Secret
kubectl -n cert-manager get deploy cert-manager \
  -o jsonpath='{.spec.template.spec.containers[0].args}' | tr ',' '\n' | grep owner-ref

# must print none for every BYO secret
kubectl -n <ns> get secret <name> -o jsonpath='{.metadata.ownerReferences}'
```

If the flag is set, or a Secret carries an ownerReference, do **not** delete —
back the material up first and restore it after, or remove the flag.

Then delete each suppressed Certificate:

```bash
kubectl -n envoy-gateway-system delete certificate wildcard-tls
kubectl -n keycloak            delete certificate login-tls
kubectl -n kube-dc             delete certificate kube-dc-frontend-tls kube-dc-backend-tls kube-dc-admin-frontend-tls registry-tls
kubectl -n monitoring          delete certificate tls-grafana tls-mimir-query tls-loki-query tls-mimir-ruler tls-mimir-alertmanager
kubectl -n flux-system         delete certificate tls-flux
kubectl -n openbao             delete certificate tls-openbao
kubectl -n rook-ceph           delete certificate s3-tls
```

If any reappears, its declaring Kustomization is not yet suppressed — find it
with `kubectl -n <ns> get certificate <name> -o jsonpath='{.metadata.labels}'`
and read `kustomize.toolkit.fluxcd.io/name`.

### 4. Verify

```bash
# ZERO platform Certificates must remain — count them, do not filter by status.
# `grep -v True` is the wrong check: it HIDES a surviving Ready=True Certificate,
# which is exactly the dangerous case (cert-manager succeeded and may already
# have replaced your material).
kubectl get certificate -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name --no-headers \
  | grep -E 'wildcard-tls|login-tls|kube-dc-(frontend|backend|admin-frontend)-tls|tls-(grafana|mimir|loki|flux|openbao)|registry-tls|s3-tls' \
  || echo "OK: no platform ACME Certificates remain"

# the served certificate must be YOURS
echo | openssl s_client -connect console.<DOMAIN>:443 -servername console.<DOMAIN> 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates

# every listener's secret must exist and be labelled BYO
kubectl -n envoy-gateway-system get gateway eg \
  -o jsonpath='{range .spec.listeners[*]}{.tls.certificateRefs[0].namespace}/{.tls.certificateRefs[0].name}{"\n"}{end}' \
  | sort -u
```

## Security note: one key, many namespaces

The same private key is written into every namespace that terminates TLS for a
platform hostname (currently 14 Secrets). That is what a wildcard means, but it
widens the blast radius: anyone able to read Secrets — or to create a Pod that
mounts one — in *any* of those namespaces obtains a key valid for **every**
platform hostname, and the only remedy is rotating the wildcard everywhere.

Treat those namespaces as platform-privileged: no tenant-facing RBAC on their
Secrets, and enable encryption at rest for etcd. If that blast radius is
unacceptable, prefer per-hostname certificates from your own CA over one
wildcard — the same suppression applies, you simply supply distinct material per
secret name.

## Rotating the certificate

Because the secrets are the source of truth, rotation is a Git operation:

1. supply the new material — either way lands the same commit:
   - **re-run `init`** against the existing overlay with the new files:
     `kube-dc bootstrap init … --tls-mode byo-wildcard --tls-cert new.crt --tls-key new.key`.
     `init` resumes (it never re-scaffolds a cluster that already has
     `cluster-config.env`), rewrites only the TLS artifacts, and commits
     `chore(<name>): rotate platform TLS material via kube-dc CLI`; unchanged
     material is a no-op, so a plain resume stays a plain resume — **or**
   - edit the SOPS file directly — `sops clusters/<name>/wildcard-tls-secrets.enc.yaml` —
     replacing every `tls.crt`/`tls.key` pair with the base64 of the new material
     (one certificate, all entries identical);
2. commit and push;
3. `flux reconcile kustomization flux-system --with-source`;
4. Envoy picks the new material up without a restart; confirm with the
   `openssl s_client` check above.

Plan rotation before `notAfter` — nothing renews a BYO certificate for you.
That is the trade for not depending on ACME.

## Adding a hostname later

A wildcard covers any new `*.<DOMAIN>` hostname, but a **new Gateway listener
referencing a new secret name** still needs that secret to exist. Add the name
to the BYO set (re-run `init`, or copy an existing entry in the SOPS file) and
suppress the matching ACME Certificate if the new layer declares one.

## Switching back to ACME

Set `TLS_MODE=acme`, remove the suppression patches and the SOPS secret file,
then delete the BYO secrets so cert-manager can issue fresh ones:

```bash
kubectl -n <ns> delete secret <name>   # for each BYO secret
```

Only do this once the domain genuinely resolves publicly and `:80` reaches the
Gateway, or you will be left with no certificate at all.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Issuing certificate as Secret was previously issued by "Issuer.cert-manager.io/"` | An ACME Certificate still exists for a BYO secret — suppression is missing for the Kustomization that declares it |
| Certificate reappears after you delete it | It is declared by a different Kustomization than the one you patched; suppress it at its source |
| Deleting the Certificate also deleted the Secret | cert-manager runs with `--enable-certificate-owner-ref`; remove that flag, or recreate the secret from Git |
| Browser trusts nothing | The chain in `tls.crt` is leaf-only — append the intermediates |
| Endpoint serves the wrong certificate | The listener references a secret name your BYO set does not cover; list the listener refs with the command above |
