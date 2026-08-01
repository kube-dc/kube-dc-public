---
name: expose-service
description: Expose a Service from a Kube-DC Project with a hostname-based HTTP/HTTPS/TLS Gateway route or a dedicated EIP-backed LoadBalancer for selected TCP/UDP ports.
---

## Prerequisites

- The workload and Service selector exist in a Ready Project.
- Know the Project's backing namespace: `{organization}-{project}`.
- Check public IPv4 quota before requesting a public EIP.
- For `expose-route: "https"`, create the Project's cert-manager `Issuer`
  once before exposing the Service.

## Choose an Exposure Method

| Need | Method |
|---|---|
| HTTP hostname | Gateway route with `expose-route: "http"` |
| HTTPS with Gateway TLS termination | Gateway route with `expose-route: "https"` |
| SNI-based TLS passthrough | Gateway route with `expose-route: "tls-passthrough"` |
| Selected TCP or UDP ports | EIP-backed `LoadBalancer` Service |
| Direct access to one VM interface | `FIp`; use the `manage-networking` skill |

The Gateway controller creates HTTPRoute or TLSRoute resources. It does not
create a GRPCRoute. Validate the application's HTTP/2 behavior before putting
gRPC behind an HTTPS HTTPRoute; use a dedicated LoadBalancer when that
compatibility is unknown.

## Gateway Route

### Create the HTTPS Issuer Once Per Project

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: "{backing-namespace}"
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: "{valid-email}"
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
    - http01:
        gatewayHTTPRoute:
          parentRefs:
          - group: gateway.networking.k8s.io
            kind: Gateway
            name: eg
            namespace: envoy-gateway-system
```

This Issuer is a prerequisite, not something the Service creates.

### Annotate a LoadBalancer Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: "{service-name}"
  namespace: "{backing-namespace}"
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"
    # Set this for a multi-port Service:
    service.nlb.kube-dc.com/route-port: "{service-port}"
    # Optional:
    # service.nlb.kube-dc.com/route-hostname: "app.example.com"
    # service.nlb.kube-dc.com/tls-issuer: "letsencrypt"
spec:
  type: LoadBalancer
  selector:
    app: "{application-label}"
  ports:
  - name: http
    port: 8080
    targetPort: 8080
```

The controller assigns a hostname under the installation's configured domain
unless `route-hostname` is set. Read it from
`service.nlb.kube-dc.com/route-hostname-status`; do not construct a provider
domain in automation.

For `https`, the Gateway terminates TLS and forwards HTTP to the selected
Service port. For `tls-passthrough`, the backend terminates TLS and must
present a certificate valid for the public SNI hostname.

To use an existing TLS Secret with an HTTPS route, set
`service.nlb.kube-dc.com/tls-secret`. The Secret must contain a certificate
valid for the route hostname.

See [envoy-gateway-examples.yaml](envoy-gateway-examples.yaml).

## Dedicated EIP and LoadBalancer

Create an address, then bind the Service:

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: "{eip-name}"
  namespace: "{backing-namespace}"
spec:
  externalNetworkType: public
---
apiVersion: v1
kind: Service
metadata:
  name: "{service-name}"
  namespace: "{backing-namespace}"
  annotations:
    service.nlb.kube-dc.com/bind-on-eip: "{eip-name}"
spec:
  type: LoadBalancer
  selector:
    app: "{application-label}"
  ports:
  - name: protocol
    port: 8080
    targetPort: 8080
    protocol: TCP # or UDP
```

A `public` address is internet-routable subject to firewall policy. A
`cloud` address is reachable only from networks configured by the provider.
The availability of either pool is installation-specific.

See [eip-loadbalancer-examples.yaml](eip-loadbalancer-examples.yaml).

## Annotation Reference

All route/LB annotations below use the
`service.nlb.kube-dc.com/` prefix.

| Suffix | Meaning |
|---|---|
| `expose-route` | `http`, `https`, or `tls-passthrough` |
| `route-hostname` | Optional explicit FQDN |
| `route-port` | One selected Service port; set it on multi-port Services |
| `tls-issuer` | Issuer name; default `letsencrypt` |
| `tls-secret` | User-provided TLS Secret |
| `bind-on-eip` | Bind a LoadBalancer to a named EIP |
| `bind-on-default-gw-eip` | Bind to the Project gateway EIP |
| `autodelete` | Advanced recovery: delete a Service that remains without endpoints |

`autodelete` does not manage EIP cleanup. Leave it unset for ordinary
workload lifecycle.

Set `network.kube-dc.com/external-network-type: public|cloud` when creating a
LoadBalancer that should allocate its own EIP. The network type is immutable
after allocation.

## Verification

Gateway route:

```bash
kubectl get service {service-name} -n {backing-namespace} \
  -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}{"\n"}'
kubectl get httproute,tlsroute,certificate -n {backing-namespace}
```

Dedicated address:

```bash
kubectl get eip {eip-name} -n {backing-namespace} \
  -o jsonpath='{.status.ready}{"\t"}{.status.ipAddress}{"\n"}'
kubectl get service {service-name} -n {backing-namespace} \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}{"\n"}'
```

If no address or hostname appears, inspect the Service, EIP, route, Certificate,
Issuer, and endpoints. A LoadBalancer cannot route to pods that do not match its
selector.

## Safety

- Do not promise a fixed hostname, public IP, price, or provisioning time.
- Use Gateway routes for compatible hostname-based web traffic.
- Use an EIP-backed LoadBalancer for arbitrary TCP/UDP or uncertain protocol
  compatibility.
- A VM/pod cannot simultaneously be a public FIP target and a cloud
  LoadBalancer backend.
- TLS passthrough provides no certificate; validate the backend certificate.
