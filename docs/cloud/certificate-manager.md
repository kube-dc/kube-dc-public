# Certificate Manager

Kube-DC's Certificate Manager gives every Project on-demand X.509
certificates without making you reach for ACME accounts, CA private
keys, or raw `cert-manager` resources. You ask for a cert with a name
and a SAN list; the platform issues it, projects it into a
Kubernetes `Secret` your workloads can mount, and renews it before
it expires.

Two trust roots are available:

- **Private** — issued by your Organization's intermediate CA for internal
  TLS, mTLS, client authentication, or code signing. It is not trusted by the
  public internet. Distribute the Organization trust chain to every client
  that must validate it; Managed Clusters do not receive it automatically.
- **Public** — issued via ACME (Let's Encrypt by default) through
  Kube-DC's existing cert-manager path. Trusted by any browser; only
  usable for SANs the Organization is allowed to issue under.

Console and CLI API actions are audit-logged. Certificate status and
cert-manager events show controller-side issuance and renewal.

## Concepts

A **ManagedCertificate** is a CRD in your project:

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedCertificate
metadata:
  name: api-tls
  namespace: acme-production
spec:
  type: public                     # private | public
  purpose: server                  # server | client | mtls | code-signing
  dnsNames:
    - api.example.com
  duration: 90d
  renewBefore: 15d
  targetSecretName: api-tls
```

Three fields drive what's issued:

- **type** — `private` (Organization intermediate CA) or `public` (ACME).
- **purpose** — picks the x509 key-usages bundle:
  - `server` — server TLS auth
  - `client` — client TLS auth (for mTLS clients)
  - `mtls` — both server + client (for services that do both)
  - `code-signing` — code-signing extended key usage
- **dnsNames** — SANs. Validated against your Organization's allowed
  certificate domains by an admission webhook; you can't issue a cert
  for someone else's domain.

For Project `production`, private certificates allow `production.internal`
and one-label names below it, such as `worker.production.internal`, by
default. Public certificates have no default domain allowance: an Organization
admin must add the exact name or wildcard to
`Organization.spec.security.certificateDomains` before you request it.

The controller creates and owns a `cert-manager` `Certificate` under
the hood, fulfills the request, and writes `tls.crt` + `tls.key` into
the `Secret` named by `targetSecretName`. Your workloads mount that
Secret like any other.

You never touch raw `cert-manager` `Issuer`s, ACME challenges, or
intermediate CA private keys. The platform owns those.

## Permissions

| Role | View | Request | Renew existing | Delete |
|---|---|---|---|---|
| `admin` | Yes | Yes | Yes | Yes |
| `developer` | Yes | Yes | Yes | Yes |
| `project-manager` | Yes | No | Yes | No |
| `user` | Yes | No | No | No |

Certificate issuance runs through the platform controller. Users never receive
access to the issuing CA private key.

## Request a certificate

### Via the CLI

```bash
# Public server cert (default purpose=server, duration=90d, renewBefore=15d).
# Repeat --dns to add multiple SANs.
kube-dc certificates request api-tls \
  --type=public \
  --target=api-tls \
  --dns=api.example.com

# Private mTLS cert for an internal service
kube-dc certificates request worker-mtls \
  --type=private \
  --purpose=mtls \
  --target=worker-mtls \
  --dns=worker.production.internal

# Custom duration / renewBefore
kube-dc certificates request short-cert \
  --type=private \
  --dns=batch-job.production.internal \
  --duration=30d --renew-before=5d
```

### Via kubectl

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedCertificate
metadata:
  name: worker-mtls
  namespace: acme-production
spec:
  type: private
  purpose: mtls
  dnsNames:
    - worker.production.internal
  duration: 90d
  renewBefore: 15d
  targetSecretName: worker-mtls
```

```bash
kubectl apply -f cert.yaml
```

Wait for the ManagedCertificate `Ready` condition. Public issuance also waits
for DNS, Gateway reachability, and the ACME challenge; private issuance waits
for the configured Organization issuer.

### What you get

A Kubernetes `Secret` of type `kubernetes.io/tls`:

```bash
kubectl get secret api-tls -o yaml
# data:
#   tls.crt: <base64 PEM cert chain>
#   tls.key: <base64 PEM private key>
#   ca.crt:  <base64 PEM issuing CA chain — present for type=private>
```

Mount it like any TLS secret:

```yaml
spec:
  containers:
  - name: app
    volumeMounts:
    - name: tls
      mountPath: /etc/tls
      readOnly: true
  volumes:
  - name: tls
    secret:
      secretName: api-tls
```

For a Gateway you manage, reference the projected Secret in the Listener's
`certificateRefs`. Kube-DC [Service Exposure](service-exposure.md) is a
separate flow: `expose-route: "https"` creates a raw cert-manager
`Certificate` through the Project's `letsencrypt` Issuer. It does not create a
`ManagedCertificate`. Use `tls-secret` when you want a route to consume a
certificate Secret you manage explicitly.

## Inspect

```bash
kube-dc certificates list
# NAME         TYPE     PURPOSE  DNS                         SECRET       READY  EXPIRES  AGE
# api-tls      public   server   api.example.com             api-tls      True   in 89d   1m
# worker-mtls  private  mtls     worker.production.internal  worker-mtls  True   in 89d   1m

kube-dc certificates get api-tls
# Name:           acme-production/api-tls
# Type:           public
# Purpose:        server
# DNS Names:      api.example.com
# Target Secret:  api-tls
# Duration:       90d
# Renew Before:   15d
# Created:        2026-07-31T10:00:00Z
# Issuer:         letsencrypt-prod-http
# Not Before:     2026-07-31T10:00:00Z
# Not After:      2026-10-29T10:00:00Z
# Renewal:        2026-10-14T10:00:00Z
# Ready:          True
```

Or with kubectl (note the `mcert` short name):

```bash
kubectl get mcert
kubectl get mcert api-tls -o yaml
```

## Renew

The controller requests renewal `renewBefore` ahead of expiry. To request an
earlier renewal, for example after a security event:

```bash
kube-dc certificates renew api-tls
```

Watch the ManagedCertificate condition and the underlying cert-manager
Certificate until the new revision is Ready. cert-manager updates the Secret
in place. Secret-volume mounts are eventually refreshed by Kubernetes, but the
application must reload the certificate. Environment variables never update
inside an existing container.

## Delete

```bash
kube-dc certificates delete api-tls --yes
```

(`--yes` is required — the CLI refuses to delete without explicit
confirmation.)

Deleting the ManagedCertificate removes its owned cert-manager Certificate.
The projected `targetSecretName` Secret can remain because cert-manager Secret
owner references are not enabled on every installation. Move workloads away
from the Secret first, then verify and delete any retained Secret explicitly
when it is no longer needed.

## Use with Service Exposure

Most TLS use is for HTTPS / mTLS endpoints reached through the
[Service Exposure](service-exposure.md) layer. Set
`service.nlb.kube-dc.com/expose-route: "https"` on a LoadBalancer Service
after creating the Project's `letsencrypt` Issuer. The route controller creates
and owns a cert-manager `Certificate`; it does not create a
`ManagedCertificate`. See the exposure guide for the complete route workflow.

Direct ManagedCertificate use is for cases where you need:

- mTLS between two of your services with neither facing the internet
- Code-signing certs for image / artifact signing pipelines
- Client certs you ship to external partners
- Custom SANs / durations the exposure layer doesn't cover

## Audit

Certificate API actions made through the Kube-DC service are available in the
audit log:

```bash
kube-dc audit list --service certificates
```

The omitted time range uses the audit backend's default window. To set an
explicit boundary, pass epoch seconds or an RFC 3339 timestamp, for example
`--since=2026-07-30T12:00:00Z`.

Use the ManagedCertificate conditions, Kubernetes events, and the underlying
cert-manager Certificate for controller-side issuance details. Automatic
renewals are not recorded as user API actions.

## Limits

- **dnsNames must be allowed by the Organization.** The admission
  webhook checks each SAN against the Organization's allowed certificate
  domains list. If you need a SAN that's not on the list, ask your
  Organization admin to expand it.
- **`public` type costs an ACME issuance.** Don't churn certs against
  Let's Encrypt because its upstream rate limits apply. Kube-DC does not expose
  a separate rate-limit counter. Use `private` for non-internet-facing trust.
- **Key algorithm defaults come from cert-manager and the configured issuer.**
  ManagedCertificate does not currently expose a private-key algorithm field;
  inspect the resulting Certificate when the exact key profile matters.
- **No CSR-based mode.** You can't bring your own private key today;
  cert-manager generates one for every issuance. Use the underlying
  cert-manager primitives directly if you need this.

## Reference

- [Service Exposure](service-exposure.md) — Issuer-backed TLS for Gateway
  routes
- [KMS](kms.md) — encryption keys (separate from x509)
- [Secrets Manager](secrets-manager.md) — storing the cert + key pair
  yourself if needed
- cert-manager docs: [cert-manager.io/docs/](https://cert-manager.io/docs/)
