# Project Network Types

## Cloud (Recommended Default)

- Shared NAT gateway — more secure, cost-effective
- Default EIPs allocated from `ext-cloud` subnet
- Can still request public EIPs when needed (`externalNetworkType: public` on EIp resource)
- Best for: web apps, APIs, microservices, most workloads

## Public

- Dedicated public IP on the project gateway
- Default EIPs allocated from `ext-public` subnet
- Direct public IP access without NAT
- Best for: game servers, custom protocols requiring dedicated IP

## Key Difference

Both types support Gateway Routes (Envoy) and EIP+LoadBalancer exposure.
The difference is only in the **default gateway** — cloud uses shared NAT, public uses a dedicated IP.

Cloud projects can always get public EIPs on-demand by creating:
```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: my-public-eip
  namespace: {project-namespace}
spec:
  externalNetworkType: public
```
