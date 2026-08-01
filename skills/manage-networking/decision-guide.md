# Networking Decision Guide

| Requirement | Recommended path |
|---|---|
| HTTP or HTTPS hostname | Gateway route with the `expose-service` skill |
| TLS passthrough with a valid backend certificate | Gateway TLSRoute |
| Arbitrary TCP or UDP ports | EIP-backed LoadBalancer Service |
| SSH or direct access to one VM interface | FIP |
| Managed database from a workstation | `KdcDatabase.spec.expose.type: loadbalancer` |
| Internal application-to-database traffic | Engine Service on the Project VPC |

## EIP or FIP?

| Aspect | EIP with LoadBalancer | FIP |
|---|---|---|
| Target | Service selector | One internal IP or VM interface |
| Exposed traffic | Declared Service ports | Direct one-to-one NAT |
| Load balancing | Yes | No |
| Typical use | Applications, TCP/UDP services, databases | VM administration or all-port direct access |

Standard Project roles do not grant pod port-forward, so port-forward is not a
tenant database or VM access method.

## Public or Cloud Address?

- `public` is internet-routable subject to firewall and provider policy.
- `cloud` is reachable only from networks configured by the provider.

Pool availability is installation-specific. A cloud Project can request a
public EIP only when the provider exposes that pool and Organization quota is
available.
