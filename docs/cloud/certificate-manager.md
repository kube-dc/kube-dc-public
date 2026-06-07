# Certificate Manager

Kube-DC's Certificate Manager gives every project on-demand x509
certificates without making you reach for ACME accounts, CA private
keys, or raw `cert-manager` resources. You ask for a cert with a name
and a SAN list; the platform issues it, projects it into a
Kubernetes `Secret` your workloads can mount, and renews it before
it expires.

Two trust roots are available:

- **Private** — your Organization's own intermediate CA. The CA cert
  is auto-distributed inside every tenant cluster so internal services
  can mutually trust each other (mTLS), workloads can validate signed
  artifacts (code signing), and clients can be authenticated against
  the same CA. Not trusted by the public internet.
- **Public** — issued via ACME (Let's Encrypt by default) through
  Kube-DC's existing cert-manager path. Trusted by any browser; only
  usable for SANs the Organization is allowed to issue under.

Every issuance is audit-logged. Renewals happen automatically.

## Concepts

A **ManagedCertificate** is a CRD in your project:

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedCertificate
metadata:
  name: api-tls
  namespace: my-project
spec:
  type: public                     # private | public
  purpose: server                  # server | client | mtls | code-signing
  dnsNames:
    - api.my-project.example.com
  duration: 90d
  renewBefore: 15d
  targetSecretName: api-tls
```

Three fields drive what's issued:

- **type** — `private` (Org intermediate CA) or `public` (ACME).
- **purpose** — picks the x509 key-usages bundle:
  - `server` — server TLS auth
  - `client` — client TLS auth (for mTLS clients)
  - `mtls` — both server + client (for services that do both)
  - `code-signing` — code-signing extended key usage
- **dnsNames** — SANs. Validated against your Organization's allowed
  certificate domains by an admission webhook; you can't issue a cert
  for someone else's domain.

The controller creates and owns a `cert-manager` `Certificate` under
the hood, fulfills the request, and writes `tls.crt` + `tls.key` into
the `Secret` named by `targetSecretName`. Your workloads mount that
Secret like any other.

You never touch raw `cert-manager` `Issuer`s, ACME challenges, or
intermediate CA private keys. The platform owns those.

## Permissions

| Role | ManagedCertificate CRD | Request | Renew | Delete |
|---|---|---|---|---|
| Project Manager | full | ✅ | ✅ | ✅ |
| Developer | read | ✅ | ✅ | ✅ (own only) |
| Viewer | read | — | — | — |

Developer-tier policies ship with the right OpenBao PKI grants for
the `private` CA path, so application code doesn't need project-
manager credentials.

## Request a certificate

### Via the CLI

```bash
# Public server cert (default purpose=server, duration=90d, renewBefore=15d)
kube-dc certificates request api-tls \
  --type=public \
  --dns-name=api.my-project.example.com

# Private mTLS cert for an internal service
kube-dc certificates request worker-mtls \
  --type=private \
  --purpose=mtls \
  --dns-name=worker.internal \
  --dns-name=worker.my-project.svc.cluster.local

# Custom duration / renewBefore
kube-dc certificates request short-cert \
  --type=private \
  --dns-name=batch-job.internal \
  --duration=30d --renew-before=5d
```

### Via kubectl

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedCertificate
metadata:
  name: worker-mtls
  namespace: my-project
spec:
  type: private
  purpose: mtls
  dnsNames:
    - worker.internal
    - worker.my-project.svc.cluster.local
  duration: 90d
  renewBefore: 15d
  targetSecretName: worker-mtls
```

```bash
kubectl apply -f cert.yaml
```

Status reaches `Ready=True` within ~10s for the `private` path; the
`public` path takes as long as the ACME HTTP-01 challenge needs (~30s
for a fresh domain, ~5s for a renewal of an already-validated one).

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

For HTTP services exposed through a Kubernetes Gateway, reference the
Secret directly in the Listener's `certificateRefs`. Kube-DC's
[service exposure](service-exposure.md) layer can also generate the
ManagedCertificate for you when you set up an HTTPRoute.

## Inspect

```bash
kube-dc certificates list
# NAME            TYPE     PURPOSE   EXPIRES                  STATUS
# api-tls         public   server    2026-09-05 14:00:00 UTC  Ready
# worker-mtls     private  mtls      2026-09-05 14:00:00 UTC  Ready

kube-dc certificates get api-tls
# DnsNames:           api.my-project.example.com
# Issued:             2026-06-07 14:00:00 UTC
# Expires:            2026-09-05 14:00:00 UTC
# Renew at:           2026-08-21 14:00:00 UTC
# Target secret:      api-tls (Ready)
# Conditions:
#   Ready=True (Issued)
```

Or with kubectl (note the `mcert` short name):

```bash
kubectl get mcert
kubectl get mcert api-tls -o yaml
```

## Renew

Renewals happen **automatically** `renewBefore` ahead of expiry — no
action required. For a hands-on renewal (e.g. after a security event):

```bash
kube-dc certificates renew api-tls
```

This triggers cert-manager to re-issue immediately; the `Secret` is
updated in place and your workloads pick up the new cert on the next
SIGHUP / TLS reload (which most Ingress and Gateway controllers do
automatically).

## Delete

```bash
kube-dc certificates delete api-tls
```

The CRD, the owned `cert-manager` `Certificate`, AND the
`targetSecretName` Secret are all deleted. Pods currently mounting
the Secret will fail to restart until you fix references — that's
intentional, so a deleted cert can't keep serving traffic.

## Use with Service Exposure

Most TLS use is for HTTPS / mTLS endpoints reached through the
[Service Exposure](service-exposure.md) layer. The shortcut there is
to set `kube-dc.com/expose-route: "true"` annotations + a hostname,
and Kube-DC will auto-create the ManagedCertificate for you. The doc
in question covers the full UX.

Direct ManagedCertificate use is for cases where you need:

- mTLS between two of your services with neither facing the internet
- Code-signing certs for image / artifact signing pipelines
- Client certs you ship to external partners
- Custom SANs / durations the exposure layer doesn't cover

## Audit

Every `Request`, `Renew`, `Issued`, and `Delete` emits a structured
audit event:

```bash
kube-dc audit list --resource=ManagedCertificate --since=24h
```

Logs the calling identity, the cert name, the DnsNames, and the
issuing CA path.

## Limits

- **dnsNames must be allowed by the Organization.** The admission
  webhook checks each SAN against the Org's allowed certificate
  domains list. If you need a SAN that's not on the list, ask your
  Org admin to expand it.
- **`public` type costs an ACME issuance.** Don't churn certs against
  Let's Encrypt — there are rate limits, and Kube-DC tracks them.
  Use `private` for any non-internet-facing service.
- **Phase-1 algorithms.** Issuance is fixed to `RSA-2048` for `public`
  and `ECDSA-P256` for `private`. Configurable algorithms are a future
  enhancement.
- **No CSR-based mode.** You can't bring your own private key today;
  the controller generates one for every issuance. Use the underlying
  cert-manager primitives directly if you need this.

## Reference

- [Service Exposure](service-exposure.md) — auto-managed TLS for
  HTTPRoutes
- [KMS](kms.md) — encryption keys (separate from x509)
- [Secrets Manager](secrets-manager.md) — storing the cert + key pair
  yourself if needed
- cert-manager docs: <https://cert-manager.io/docs/>
