# Project Network Types

Every installation supports `cloud`. A `public` Project is available only
when the provider enables it.

## Cloud

- The Project gateway uses the configured cloud address pool.
- Outbound traffic is SNATed through the Project gateway.
- Cloud addresses are not internet-routable; they are reachable only from
  configured platform networks.
- The Project can still request a public EIP when the provider exposes that
  pool and Organization quota is available.

## Public

- The Project gateway uses the configured public address pool.
- The gateway address is internet-routable subject to firewall and provider
  policy.
- Public address quota still applies.

Both Project types can use Gateway routes, EIP-backed LoadBalancers, FIPs,
applications, and VMs. The main difference is the pool used for the default
gateway address, not the set of workload APIs available.
