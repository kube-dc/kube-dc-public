---
name: manage-certificates
description: Request and manage a Project-scoped ManagedCertificate for private Organization PKI or public ACME issuance, then consume the resulting Kubernetes TLS Secret.
---

## When to Use This Skill

Use `ManagedCertificate` when an application needs an explicitly managed
X.509 certificate for server TLS, client authentication, mTLS, or code signing.

Gateway HTTPS exposure is a separate flow. The `expose-service` skill creates
an HTTPRoute and a raw cert-manager `Certificate` through the Project's
`Issuer`; it does not create a `ManagedCertificate`.

## Prerequisites

- The target Project exists and is Ready.
- Know its backing namespace: `{organization}-{project}`.
- Every DNS SAN is allowed by
  `Organization.spec.security.certificateDomains`.
- Private issuance requires the platform's OpenBao PKI and Organization
  intermediate issuer to be ready.
- Public issuance requires the configured ACME path, DNS, and Gateway
  reachability.

ManagedCertificate currently rejects IP SANs even though the CRD reserves an
`ipAddresses` field. Use allowed DNS names.

## Choose the Trust Root

| Type | Use |
|---|---|
| `private` | Internal TLS, mTLS, clients, or code signing through the Organization intermediate CA |
| `public` | Browser/public trust through the configured ACME issuer |

Private certificates are not publicly trusted. Distribute the Organization
trust chain to each client that must validate them. Managed Clusters do not
receive that trust chain automatically.

## Request a Certificate

```yaml
apiVersion: security.kube-dc.com/v1alpha1
kind: ManagedCertificate
metadata:
  name: api-tls
  namespace: "{backing-namespace}"
spec:
  type: private # private | public
  purpose: server # server | client | mtls | code-signing
  dnsNames:
  - api.production.internal
  duration: 90d
  renewBefore: 15d
  targetSecretName: api-tls
```

Use [managed-certificate-template.yaml](managed-certificate-template.yaml) for
an annotated version.

CLI examples:

```bash
kube-dc certificates request api-tls \
  --type=public \
  --target=api-tls \
  --dns=api.example.com

kube-dc certificates request worker-mtls \
  --type=private \
  --purpose=mtls \
  --target=worker-mtls \
  --dns=worker.production.internal
```

If `targetSecretName` is omitted from a raw resource, admission defaults it to
`{managed-certificate-name}-tls`.

## Permissions

| Role | View | Request | Renew existing | Delete |
|---|---|---|---|---|
| `admin` | Yes | Yes | Yes | Yes |
| `developer` | Yes | Yes | Yes | Yes |
| `project-manager` | Yes | No | Yes | No |
| `user` | Yes | No | No | No |

Issuance happens in the platform controller; users never receive the issuing CA
private key.

## Consume the TLS Secret

A Ready certificate produces a `kubernetes.io/tls` Secret with `tls.crt`
and `tls.key`. Private certificates also include `ca.crt`.

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

For a Gateway resource you manage yourself, reference this Secret from the
listener's `certificateRefs`. For Kube-DC Service exposure, set
`service.nlb.kube-dc.com/tls-secret` to consume an existing compatible Secret.

## Inspect and Renew

```bash
kube-dc certificates list
kube-dc certificates get api-tls
kubectl get mcert api-tls -n {backing-namespace} -o yaml

# Request an early reissuance.
kube-dc certificates renew api-tls
```

The controller normally renews `renewBefore` ahead of expiry. cert-manager
updates the Secret in place. Applications reading a Secret volume must reload
the certificate; environment variables do not update in running containers.

## Delete

```bash
kube-dc certificates delete api-tls --yes
```

Deleting a ManagedCertificate removes its owned cert-manager Certificate. The
target Secret can remain because cert-manager Secret owner references are not
enabled on every installation. Move consumers first, then verify and delete any
retained Secret explicitly when it is no longer needed.

## Verification

```bash
kubectl get mcert api-tls -n {backing-namespace} \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\t"}{.status.notAfter}{"\n"}'

kubectl get secret api-tls -n {backing-namespace} \
  -o jsonpath='{.type}{"\n"}'
```

On failure, inspect ManagedCertificate conditions, events, and the underlying
cert-manager Certificate. Typical causes are:

- a SAN outside the Organization's allowed certificate domains;
- unavailable Organization PKI for a private certificate;
- DNS, Gateway, or ACME challenge failure for a public certificate;
- `renewBefore` not shorter than `duration`.

## Safety

- Do not request a name the Organization does not control.
- Do not promise a fixed ACME issuance time or rate-limit allowance.
- Do not claim private certificates are automatically trusted.
- ManagedCertificate does not support bring-your-own private keys.
- Review consumers before renewal after a security event or before deletion.
