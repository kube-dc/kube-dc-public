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
| `manager.webhook.protectExposureRoutes` | Install admission protection for shared Gateway claims and protected enforcement acknowledgments |
| `manager.webhook.exposureRouteTrustedProducers` | Identify the exact authenticated native route producers |
| `manager.webhook.exposureRouteProducerNamespaceUIDs` | Optionally restrict each trusted producer's native shared-Gateway attachments to current namespace UIDs |
| `manager.webhook.exposureACMEControllerUsername` | Identify cert-manager for the separately verified HTTP01 exception |
| `manager.webhook.exposureACMEPlatformNamespaceUIDs` | Pin platform Certificate namespaces eligible for that HTTP01 exception |
| `manager.fipSourcePolicy.enabled` | Enable Floating IP source enforcement after traffic and lifecycle acceptance |
| `manager.fipSourcePolicy.namespaceUIDs` | Optionally limit restrictive Floating IP qualification to exact current namespaces |
| `backend.exposureSourcePolicy` | Publish console/API availability for each qualified method; these flags do not configure OVN or Envoy |

Direct LoadBalancer and Gateway availability require the exposure guard.
Floating IP availability requires its enforcement gate. A Service with both a
Gateway route and a direct LoadBalancer needs acceptance of both paths before
publishing its client-list control. Keep unsupported consumer modes unavailable.

## Review native producers and HTTP01 ownership

Inventory existing platform and tenant routes before enabling admission. Check
the identity each running controller actually authenticates as, its intended
namespaces, and the source that owns each route. Flux labels and field-manager
names do not prove authenticated identity. Include legacy routes, redirects,
and every installed attachment kind in the compatibility review.

For a trusted producer, an omitted namespace-scope entry preserves its existing
native route trust. An explicit empty entry denies all of that producer's native
shared-Gateway attachments. A nonempty entry requires a live, non-deleting
namespace with the exact pinned UID. For example, this inactive proposal limits
one producer to one platform namespace:

```yaml
manager:
  webhook:
    protectExposureRoutes: false
    exposureRouteTrustedProducers:
      - system:serviceaccount:flux-system:helm-controller
    exposureRouteProducerNamespaceUIDs:
      system:serviceaccount:flux-system:helm-controller:
        platform-system: "00000000-0000-4000-8000-000000000001"
```

Replace the example UID with the current namespace UID obtained from the
installation. A recreated namespace needs a new review and pin. The scope map
does not reserve hostnames or coordinate native producers with Service-owned
hostname claims; test that concurrency separately.

Review HTTP01 independently. A native route namespace is not automatically
eligible for a certificate exception. Tenant HTTPS certificates require current
Service ownership and source labels. Preserve legitimate platform certificate
issuance and renewal without granting a general route-writing exception to
cert-manager.

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

For the exposure webhook, the report follows its configured Service to
EndpointSlices and records Service owner UIDs, target Pod UIDs, address matches,
ports, raw endpoint conditions and Pod-to-ReplicaSet-to-Deployment ownership.
Foreign and unresolved endpoints remain visible. The leader Lease includes its
UID, resource version, expiry observation and a Pod candidate inferred from the
holder name; this is not authenticated writer attribution. The GET sequence is
not an atomic snapshot. Compare resource versions and audit start/end times,
then verify the actual TLS handler and authenticated admission behavior. The
helper does not perform admission or packet acceptance.

## Accept the installation

1. Record immutable core, chart, producer and UI/backend revisions and digests.
   Deploy the compatible core before a producer that delegates route ownership.
   Verify every serving webhook endpoint runs the required handler and preserves
   the installation's availability requirements.
2. Test authenticated controller and tenant writes. Reject forged ownership,
   protected acknowledgments, foreign route edits and unqualified attachments.
   Verify existing platform producers and HTTP01 issuance and renewal still work.
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
