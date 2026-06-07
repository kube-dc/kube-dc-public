---
name: manage-certificates
description: Request and manage x509 certificates in a Kube-DC project — ManagedCertificate resources backed by either the Organization's private intermediate CA (mTLS, internal services, code signing) or a public ACME issuer (Let's Encrypt by default). Certificates auto-renew before expiry; the result lands in a Kubernetes Secret your workloads mount. For HTTPS via Gateway / HTTPRoute use the expose-service skill — it auto-creates the ManagedCertificate. Use this skill directly only for mTLS, client certs, code signing, or custom durations.
---

## Prerequisites
- Target project must exist and be Ready
- Project namespace: `{org}-{project}`
- For `type: public` — the requested `dnsNames` must resolve to your project's external IP and be on the Organization's allowed-domains list (admission webhook enforces)
- For `type: private` — the Organization's intermediate CA must be provisioned (auto-done at first request)

## Key Concepts

- **ManagedCertificate** — Project-scoped CRD that wraps cert-manager + the OpenBao PKI engine. You name a target Kubernetes Secret and a SAN list; the platform issues + renews + writes `tls.crt` / `tls.key` / `ca.crt` into the Secret.
- **type** — `private` (Org intermediate CA — trusted inside the platform) or `public` (ACME — trusted by browsers).
- **purpose** — `server` (server TLS auth, the default), `client` (client TLS auth for mTLS), `mtls` (both), `code-signing` (code-signing EKU).
- **Auto-renewal** — Renews `renewBefore` ahead of expiry (default 15d for a 90d cert). No tenant action required.

## Decide: type=private or type=public?

| Need | Pick |
|------|------|
| HTTPS endpoint reachable from the internet | `public` |
| Internal service mTLS (service-to-service in the project / org) | `private` |
| Client certs you ship to partners or workloads | `private` |
| Code signing (signing images, OCI artifacts) | `private` |
| Short-lived certs (< 90d) for batch jobs | `private` |

`public` certs cost an ACME issuance — don't churn them. `private` is free + fast (< 10s issuance) but not internet-trusted.

## Request a Certificate

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedCertificate
metadata:
  name: {cert-name}
  namespace: {project-namespace}        # {org}-{project}
spec:
  type: private                         # private | public
  purpose: server                       # server | client | mtls | code-signing
  dnsNames:
    - {hostname-or-san-1}
    - {hostname-or-san-2}
  duration: 90d                         # validity period
  renewBefore: 15d                      # renew this far ahead of expiry
  targetSecretName: {cert-name}         # Kubernetes Secret to project into
```

See @managed-certificate-template.yaml for a fully-annotated template.

### Common shapes

```bash
# Public server cert for an HTTPS endpoint
kube-dc certificates request {api-name} \
  --type=public \
  --dns={api.your-domain.com}

# Private mTLS cert for an internal service
kube-dc certificates request {svc-name} \
  --type=private --purpose=mtls \
  --dns={svc.internal} \
  --dns={svc.{project-namespace}.svc.cluster.local}

# Private client cert (handed to a partner / off-cluster client)
kube-dc certificates request {partner-name} \
  --type=private --purpose=client \
  --dns={partner-name}.partners.internal

# Short-lived cert for a batch job
kube-dc certificates request {batch-name} \
  --type=private \
  --dns={batch-job.internal} \
  --duration=30d --renew-before=5d
```

## Use the Certificate

The platform writes a standard `kubernetes.io/tls` Secret with `tls.crt`, `tls.key`, and (for `type: private`) `ca.crt` containing the Org intermediate CA chain.

```yaml
spec:
  containers:
  - name: app
    image: my-app
    volumeMounts:
    - name: tls
      mountPath: /etc/tls
      readOnly: true
  volumes:
  - name: tls
    secret:
      secretName: {cert-name}
```

For Gateway / HTTPRoute use the secret directly in `certificateRefs`:

```yaml
spec:
  listeners:
  - name: https
    port: 443
    protocol: HTTPS
    tls:
      certificateRefs:
      - kind: Secret
        name: {cert-name}
```

## Inspect / Renew / Delete

```bash
kube-dc certificates list
kube-dc certificates get {cert-name}

# Force re-issuance (e.g. after a security event)
kube-dc certificates renew {cert-name}

# Delete CRD + cert-manager Certificate + Kubernetes Secret
# (--yes is required; the CLI refuses without it)
kube-dc certificates delete {cert-name} --yes
```

`kubectl` short name is `mcert`:

```bash
kubectl get mcert -n {project-namespace}
kubectl describe mcert {cert-name} -n {project-namespace}
```

## Verification

After requesting a certificate:

```bash
# 1. ManagedCertificate is Ready
kubectl get managedcertificate {name} -n {project-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# Expected: True

# 2. Target Secret exists with valid TLS data
kubectl get secret {targetSecretName} -n {project-namespace} \
  -o jsonpath='{.type}'
# Expected: kubernetes.io/tls

# 3. Expiry is in the future
kubectl get managedcertificate {name} -n {project-namespace} \
  -o jsonpath='{.status.notAfter}'
# Expected: future RFC3339 timestamp

# 4. Renewal is scheduled
kubectl get managedcertificate {name} -n {project-namespace} \
  -o jsonpath='{.status.renewalTime}'
# Expected: future timestamp, earlier than notAfter by renewBefore
```

**Success**: Ready=True, target Secret has `tls.crt`/`tls.key`, notAfter in the future.
**Failure**:
- Stuck `CertificateRequested` for `type=public`: ACME HTTP-01 challenge can't complete — check DNS points at the cluster's ingress IP, and that the project's HTTPRoute / Gateway is up.
- Stuck `CertificateRequested` for `type=private`: PKI engine missing for the Org — check the cluster admin enabled OpenBao.
- `dnsNames not allowed`: an admission webhook rejected one of the SANs. Verify against the Org's allowed-domains list.

## Safety

- `dnsNames` are validated against the Organization's allowed-domains list. Don't try to issue for a domain you don't control — the request will be admission-rejected.
- `type: public` issuance hits Let's Encrypt rate limits (50 certs / domain / week by default). Don't churn them.
- Deleting a ManagedCertificate also deletes the target Kubernetes Secret. Workloads currently mounting it will fail to restart until you fix references. Run `kubectl get pods -n {project-namespace} -o yaml | grep -A2 secretName.*{cert-name}` to find users first.
- The platform does NOT support BYO private key. Every issuance generates a fresh key pair. If you need to bring your own key, use cert-manager primitives directly (not this skill).
- For HTTPS exposure (the common case), use the `expose-service` skill — it creates the right Gateway / HTTPRoute and auto-provisions the ManagedCertificate so you don't have to do both halves by hand.
