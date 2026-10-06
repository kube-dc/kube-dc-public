# Qualify exposure source policies

This guide is for platform operators preparing client IPv4 restrictions for
direct LoadBalancer Services, shared Gateway routes, and Floating IPs. The chart
keeps availability flags disabled by default. Published controller artifacts
and saved CIDRs do not establish installation qualification.

For tenant manifests and list semantics, see
[Service exposure](../cloud/service-exposure.md#restrict-client-addresses) and
[Floating IP client restrictions](../cloud/public-floating-ips.md#restrict-floating-ip-clients).

## Controls and their scope

| Chart value | Purpose |
|---|---|
| `manager.fipSourcePolicy.enabled` | Enable Floating IP source enforcement after traffic and lifecycle acceptance |
| `manager.fipSourcePolicy.namespaceUIDs` | Optionally limit restrictive Floating IP qualification to exact current namespaces |
| `backend.exposureSourcePolicy` | Publish console/API availability for each qualified method; these flags do not configure OVN or Envoy |

| `manager.defaultTLSIssuerRef` | Select the platform issuer for Service certificates, for example `ClusterIssuer/letsencrypt-prod-http` |

Floating IP availability requires its enforcement gate. A Service with both a
Gateway route and a direct LoadBalancer needs acceptance of both paths before
publishing its client-list control. Keep unsupported consumer modes unavailable.

## Tenant routes and certificates

Tenants never write routes on the shared Gateway. A Service with
`service.nlb.kube-dc.com/expose-route` gets its route from the platform
controller. Project roles grant only read access to `httproutes`, `grpcroutes`
and `tlsroutes`. Two always-on ValidatingAdmissionPolicies apply in Project
namespaces:

| Policy | Rule |
|---|---|
| `kube-dc-tenant-service-exposure` | Only the core controller changes the enforcement acknowledgment and hostname ownership annotations; the HTTPS redirect annotation must be valid |
| `kube-dc-tenant-certificate-issuers` | Tenant Issuers cannot solve ACME HTTP-01 on the platform Gateway, and tenant Certificates cannot use ClusterIssuers |

An HTTP-01 Issuer on the platform Gateway or a platform ClusterIssuer in tenant
hands would issue certificates for hostnames the tenant does not own. Tenants
keep dns01, CA, self-signed and own-ingress HTTP-01 Issuers. The policies only check creation and spec changes, so they
don't affect Issuers created before the upgrade. Before publishing Gateway
availability:

1. Set `manager.defaultTLSIssuerRef` to the platform ACME ClusterIssuer. The
   controller then issues every Service certificate from it.
2. Confirm that each Service certificate references that issuer and is Ready.
3. Retire Project Issuers with HTTP-01 solvers. The inventory helper lists
   HTTP01 certificates and their issuers. Move tenants with their own
   Certificates on such an Issuer to `expose-route` first.

A tenant `tls-issuer` annotation can still name the tenant's own Issuer. The
controller uses the default instead when the annotation names a ClusterIssuer
that isn't ACME, a Project Issuer that solves HTTP-01 on the platform Gateway,
or a Project Issuer that no longer exists. Without a configured default, a
Project Issuer name is kept.

A `ManagedCertificate` is issued by the manager, which the policy exempts. Its
admission limits public names to the Organization's
`spec.security.certificateDomains[].publicDnsNames`, which allows no names by
default.

## Gather inventory

Run the read-only helper with an explicitly selected installation:
Install Python 3 and `kubectl`, run from the repository root, and set
`INSTALLATION_KUBECONFIG` to the reviewed kubeconfig path.

```sh
python3 hack/audit-exposure-installation.py \
  --kubeconfig "$INSTALLATION_KUBECONFIG" \
  --output /tmp/exposure-installation-inventory.json
```

The helper exports no Secret data. Its report includes configured shared-Gateway
parent paths through Gateway, ListenerSet and XListenerSet references, current
Service UID ownership, HTTP01 certificates and unresolved issuers, manager Pods,
configured controls, and address-pool capacity. Missing, cyclic and unsupported
parent references remain findings, including when another parent reaches the
shared Gateway. Parent reachability does not prove Accepted or Programmed status,
listener permission, or a serving ingress path. Candidate namespace pins require
operator review. Missing-parent findings cover ListenerSet/XListenerSet references;
other Gateway parents are outside the shared-Gateway audit target.

The report lists the tenant exposure policies and whether a legacy route
guard webhook is still registered. The GET sequence is not an atomic snapshot.
Compare resource versions and audit start/end times. The helper does not
perform admission or packet acceptance.

## Accept the installation

1. Record immutable core, chart, producer and UI/backend revisions and digests.
   Deploy the compatible core before a producer that delegates route ownership.
2. Test tenant writes: route writes are forbidden, and forged acknowledgments,
   HTTP-01 Issuers on the platform Gateway and ClusterIssuer Certificates are
   denied. Verify that
   platform producers and Service certificate issuance and renewal still work.
3. Test allow and deny from independent client networks on every offered public,
   cloud, primary, secondary and ingress path. Verify the observed client address
   and reject forged forwarding headers. Test omission, empty lists, explicit
   `/0`, malformed lists, and IPv6 or dual-stack bypasses.
4. Exercise creation, tightening, clearing and deletion under traffic. Include
   controller restart, dataplane failure, stale or missing policy evidence,
   address reuse and normal cleanup. Retain failed samples and establish their
   cause before closing a gate.
5. Qualify each managed consumer with its real protocol, TLS trust, bindings and
   private transports. Test migration and rollback with existing restrictions.
   Do not transfer acceptance from one producer revision or network topology to
   another.
6. Publish only the accepted availability flags and namespace scopes. Confirm the
   console, API, CLI, tenant documentation and agent skills agree with the
   installation's supported methods.

Check physical address-pool headroom as well as Organization quota before
allocating a test EIP. The allocator reserves the last available address; a quota
increase alone does not add routed addresses.

On rollback, preserve restrictive intent, current ownership and normal cleanup.
Disabling a console availability flag alone neither removes nor proves retention
of a dataplane policy. Inspect the actual routes, listeners, policies and packets.
