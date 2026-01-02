# Current OVN Network Architecture

## Overview

This document provides a complete view of the current OVN-based network architecture in the kube-dc management cluster, including VPCs, subnets, EIPs, service exposure, and Envoy Gateway integration.

## Physical Network Layer

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         PHYSICAL NETWORK                                     │
│                                                                              │
│  ┌─────────────────────────┐              ┌─────────────────────────┐       │
│  │      VLAN 4011          │              │      VLAN 4013          │       │
│  │      ext-public         │              │      ext-cloud          │       │
│  │   168.119.17.48/28      │              │   100.65.0.0/16         │       │
│  │                         │              │                         │       │
│  │   Gateway: 168.119.17.49│              │   Gateway: 100.65.0.1   │       │
│  │   Internet-routable ✅  │              │   Internal-only ❌      │       │
│  └───────────┬─────────────┘              └───────────┬─────────────┘       │
│              │                                        │                      │
│              └────────────────┬───────────────────────┘                      │
│                               │                                              │
│                     ┌─────────┴─────────┐                                    │
│                     │   Provider Bridge  │                                    │
│                     │   br-ext-cloud     │                                    │
│                     │   (on each node)   │                                    │
│                     └─────────┬─────────┘                                    │
└───────────────────────────────┼──────────────────────────────────────────────┘
                                │
                                ▼
┌───────────────────────────────────────────────────────────────────────────────┐
│                              OVN NETWORK                                       │
└───────────────────────────────────────────────────────────────────────────────┘
```

## OVN Logical Network Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                    OVN LOGICAL NETWORK                                           │
│                                                                                                  │
│  ┌────────────────────────────────────────────────────────────────────────────────────────────┐ │
│  │                              ovn-cluster VPC (Management)                                   │ │
│  │                                                                                             │ │
│  │   ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐    ┌───────────────┐  │ │
│  │   │   ovn-default   │    │    ext-cloud    │    │   ext-public    │    │     join      │  │ │
│  │   │  10.100.0.0/16  │    │  100.65.0.0/16  │    │168.119.17.48/28 │    │ 172.30.0.0/22 │  │ │
│  │   │                 │    │                 │    │                 │    │               │  │ │
│  │   │ • kube-system   │    │ • LB VIPs       │    │ • Public LB VIPs│    │ • Node IPs    │  │ │
│  │   │ • kamaji-system │    │ • Cloud EIPs    │    │ • Public EIPs   │    │ • kube-proxy  │  │ │
│  │   │ • envoy-gateway │    │                 │    │                 │    │   SNAT        │  │ │
│  │   │ • ingress-nginx │    │                 │    │                 │    │               │  │ │
│  │   └────────┬────────┘    └────────┬────────┘    └────────┬────────┘    └───────┬───────┘  │ │
│  │            │                      │                      │                     │          │ │
│  │            └──────────────────────┼──────────────────────┼─────────────────────┘          │ │
│  │                                   │                      │                                │ │
│  │                         ┌─────────┴──────────────────────┴─────────┐                      │ │
│  │                         │         ovn-cluster Router               │                      │ │
│  │                         │                                          │                      │ │
│  │                         │  Ports:                                  │                      │ │
│  │                         │  • ovn-cluster-ovn-default: 10.100.0.1   │                      │ │
│  │                         │  • ovn-cluster-ext-cloud: 100.65.0.101   │                      │ │
│  │                         │  • ovn-cluster-join: 172.30.0.1          │                      │ │
│  │                         │                                          │                      │ │
│  │                         │  SNAT: 10.100.0.0/16 → 100.65.0.101      │                      │ │
│  │                         └──────────────────────────────────────────┘                      │ │
│  └────────────────────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                                  │
│  ┌─────────────────────────────────┐  ┌─────────────────────────────────┐                       │
│  │     shalb-demo VPC (Project)    │  │     shalb-dev VPC (Project)     │                       │
│  │                                 │  │                                 │                       │
│  │   ┌─────────────────────────┐   │  │   ┌─────────────────────────┐   │                       │
│  │   │  shalb-demo-default     │   │  │   │  shalb-dev-default      │   │                       │
│  │   │     10.0.10.0/24        │   │  │   │     10.1.0.0/16         │   │                       │
│  │   │                         │   │  │   │                         │   │                       │
│  │   │  • Customer pods        │   │  │   │  • Customer pods        │   │                       │
│  │   │  • test-http-app        │   │  │   │  • Development workloads│   │                       │
│  │   └───────────┬─────────────┘   │  │   └───────────┬─────────────┘   │                       │
│  │               │                 │  │               │                 │                       │
│  │   ┌───────────┴─────────────┐   │  │   ┌───────────┴─────────────┐   │                       │
│  │   │  shalb-demo Router      │   │  │   │  shalb-dev Router       │   │                       │
│  │   │                         │   │  │   │                         │   │                       │
│  │   │  ext-cloud: 100.65.0.102│   │  │   │  ext-public:168.119.17.51│  │                       │
│  │   │  SNAT: 10.0.10.0/24     │   │  │   │  ext-cloud: (via extra) │   │                       │
│  │   │       → 100.65.0.102    │   │  │   │  SNAT: 10.1.0.0/16      │   │                       │
│  │   └─────────────────────────┘   │  │   │       → 168.119.17.51   │   │                       │
│  │                                 │  │   └─────────────────────────┘   │                       │
│  │   extraExternalSubnets: []      │  │   extraExternalSubnets:         │                       │
│  │   Default GW: ext-cloud         │  │   [ext-cloud, ext-public]       │                       │
│  └─────────────────────────────────┘  └─────────────────────────────────┘                       │
│                                                                                                  │
│  ┌─────────────────────────────────┐                                                            │
│  │    shalb-envoy VPC (Project)    │                                                            │
│  │                                 │                                                            │
│  │   ┌─────────────────────────┐   │                                                            │
│  │   │  shalb-envoy-default    │   │                                                            │
│  │   │     10.0.40.0/24        │   │                                                            │
│  │   └───────────┬─────────────┘   │                                                            │
│  │               │                 │                                                            │
│  │   ┌───────────┴─────────────┐   │                                                            │
│  │   │  shalb-envoy Router     │   │                                                            │
│  │   │                         │   │                                                            │
│  │   │  ext-public:168.119.17.52│  │                                                            │
│  │   │  SNAT: 10.0.40.0/24     │   │                                                            │
│  │   │       → 168.119.17.52   │   │                                                            │
│  │   └─────────────────────────┘   │                                                            │
│  │                                 │                                                            │
│  │   extraExternalSubnets:         │                                                            │
│  │   [ext-public]                  │                                                            │
│  └─────────────────────────────────┘                                                            │
└──────────────────────────────────────────────────────────────────────────────────────────────────┘
```

## VPC and Subnet Summary

### VPCs

| VPC | Purpose | Namespaces | External Subnets | Default GW |
|-----|---------|------------|------------------|------------|
| `ovn-cluster` | Management cluster | kube-system, kamaji-system, envoy-gateway-system | ext-cloud, ext-public | N/A |
| `shalb-demo` | Customer project | shalb-demo | ext-cloud (default) | 100.65.0.1 |
| `shalb-dev` | Customer project | shalb-dev | ext-cloud, ext-public | 168.119.17.49 |
| `shalb-envoy` | Customer project | shalb-envoy | ext-public | 168.119.17.49 |

### Subnets

| Subnet | VPC | CIDR | VLAN | Purpose |
|--------|-----|------|------|---------|
| `ovn-default` | ovn-cluster | 10.100.0.0/16 | - | Management pods |
| `ext-cloud` | ovn-cluster | 100.65.0.0/16 | 4013 | Cloud LB VIPs |
| `ext-public` | ovn-cluster | 168.119.17.48/28 | 4011 | Public LB VIPs |
| `join` | ovn-cluster | 172.30.0.0/22 | - | Node-to-OVN connectivity |
| `shalb-demo-default` | shalb-demo | 10.0.10.0/24 | - | Customer pods |
| `shalb-dev-default` | shalb-dev | 10.1.0.0/16 | - | Customer pods |
| `shalb-envoy-default` | shalb-envoy | 10.0.40.0/24 | - | Customer pods |

## Join Subnet and Node Connectivity

The `join` subnet (172.30.0.0/22) connects Kubernetes nodes to the OVN network:

```
┌──────────────────────────────────────────────────────────────────────────┐
│                           NODE NETWORKING                                 │
│                                                                           │
│  ┌─────────────────────────────────────────────────────────────────────┐ │
│  │                    kube-dc-worker-1                                  │ │
│  │                                                                      │ │
│  │   Physical Interfaces:                                               │ │
│  │   • br-ext-cloud: 138.201.132.165/26 (provider bridge)              │ │
│  │   • enp0s31f6.4012: 192.168.1.4 (internal VLAN)                     │ │
│  │                                                                      │ │
│  │   OVN Interfaces:                                                    │ │
│  │   • ovn0: 172.30.0.2/22 (join subnet - node IP)                     │ │
│  │                                                                      │ │
│  │   Routes:                                                            │ │
│  │   • 10.100.0.0/16 via 172.30.0.1 dev ovn0 (pod network)             │ │
│  │   • default via 138.201.132.129 dev br-ext-cloud (internet)         │ │
│  │                                                                      │ │
│  └─────────────────────────────────────────────────────────────────────┘ │
│                                                                           │
│              │                                                            │
│              │ ovn0 (172.30.0.2)                                          │
│              │                                                            │
│              ▼                                                            │
│  ┌─────────────────────────────────────────────────────────────────────┐ │
│  │                      OVN Join Switch                                 │ │
│  │                      172.30.0.0/22                                   │ │
│  │                                                                      │ │
│  │   Connected to:                                                      │ │
│  │   • ovn-cluster router (172.30.0.1) - gateway to OVN network        │ │
│  │   • All nodes (172.30.0.x)                                          │ │
│  └─────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────┘
```

### Join Subnet Role

1. **Node Registration**: Each node gets an IP on the join subnet (172.30.0.x)
2. **Pod Network Access**: Nodes route pod traffic (10.100.x.x) via join subnet gateway (172.30.0.1)
3. **kube-proxy SNAT**: When external traffic arrives via externalIPs, kube-proxy SNATs to 172.30.0.x

## EIP (External IP) Resources

### EIP Types

| Type | Network | Purpose | Internet Routable |
|------|---------|---------|-------------------|
| `cloud` | ext-cloud (100.65.x.x) | Internal services, Kamaji, etcd | ❌ No |
| `public` | ext-public (168.119.x.x) | Public-facing services | ✅ Yes |

### Current EIPs

| Namespace | EIP Name | IP | Type | Usage |
|-----------|----------|-----|------|-------|
| shalb-demo | default-gw | 100.65.0.102 | cloud | SNAT for project |
| shalb-demo | slb-test-http-app-bc6y7 | 100.65.0.112 | cloud | LoadBalancer service |
| shalb-demo | demo-cluster-api-eip | 100.65.0.105 | cloud | Kamaji control plane |
| shalb-demo | mt-api-eip | 100.65.0.108 | cloud | MT control plane |
| shalb-dev | default-gw | 168.119.17.51 | public | SNAT for project |
| shalb-dev | bohdan-prod-eip | 168.119.17.57 | public | FIP |
| shalb-envoy | default-gw | 168.119.17.52 | public | SNAT for project |

## Service LoadBalancer Architecture

### LoadBalancer Service Flow

```
┌──────────────────────────────────────────────────────────────────────────────────────┐
│                     SERVICE LOADBALANCER ARCHITECTURE                                 │
│                                                                                       │
│   ┌───────────────────────────────────────────────────────────────────────────────┐  │
│   │                    LoadBalancer Service Creation                               │  │
│   │                                                                                │  │
│   │   apiVersion: v1                                                               │  │
│   │   kind: Service                                                                │  │
│   │   metadata:                                                                    │  │
│   │     name: test-http-app                                                        │  │
│   │     namespace: shalb-demo                                                      │  │
│   │     annotations:                                                               │  │
│   │       service.nlb.kube-dc.com/bind-on-eip: default-gw                         │  │
│   │       gateway.kube-dc.com/create-backend: "true"  # Optional: creates Backend │  │
│   │   spec:                                                                        │  │
│   │     type: LoadBalancer                                                         │  │
│   │     ports:                                                                     │  │
│   │       - port: 80                                                               │  │
│   │     selector:                                                                  │  │
│   │       app: test-http-app                                                       │  │
│   └───────────────────────────────────────────────────────────────────────────────┘  │
│                                        │                                              │
│                                        ▼                                              │
│   ┌───────────────────────────────────────────────────────────────────────────────┐  │
│   │                    Service Controller (kube-dc)                                │  │
│   │                                                                                │  │
│   │   1. Allocate EIP (if not exists)                                             │  │
│   │      → Creates EIp resource                                                    │  │
│   │      → Creates OvnEip resource                                                 │  │
│   │                                                                                │  │
│   │   2. Create OVN LoadBalancer                                                   │  │
│   │      → VIP: 100.65.0.112:80                                                    │  │
│   │      → Backends: 10.0.10.28:80 (pod IPs)                                       │  │
│   │      → Attach to VPC router                                                    │  │
│   │                                                                                │  │
│   │   3. Create External Service + Endpoints                                       │  │
│   │      → test-http-app-ext (headless)                                            │  │
│   │      → Endpoints: 100.65.0.112:80                                              │  │
│   │                                                                                │  │
│   │   4. Create Gateway Backend (if annotated)                                     │  │
│   │      → test-http-app-backend                                                   │  │
│   │      → Endpoint: 100.65.0.112:80                                               │  │
│   └───────────────────────────────────────────────────────────────────────────────┘  │
│                                        │                                              │
│                                        ▼                                              │
│   ┌───────────────────────────────────────────────────────────────────────────────┐  │
│   │                        Created Resources                                       │  │
│   │                                                                                │  │
│   │   ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐               │  │
│   │   │      EIp        │  │    OvnEip       │  │  OVN LB         │               │  │
│   │   │                 │  │                 │  │                 │               │  │
│   │   │ slb-test-http-  │  │ eip-slb-test-   │  │ VIP:            │               │  │
│   │   │ app-bc6y7       │  │ http-app-bc6y7  │  │ 100.65.0.112:80 │               │  │
│   │   │                 │  │                 │  │                 │               │  │
│   │   │ IP: 100.65.0.112│  │ V4IP:           │  │ Backends:       │               │  │
│   │   │ Type: cloud     │  │ 100.65.0.112    │  │ 10.0.10.28:80   │               │  │
│   │   └─────────────────┘  └─────────────────┘  └─────────────────┘               │  │
│   │                                                                                │  │
│   │   ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐               │  │
│   │   │ Service -ext    │  │  Endpoints -ext │  │ Backend (GW API)│               │  │
│   │   │                 │  │                 │  │                 │               │  │
│   │   │ test-http-app-  │  │ test-http-app-  │  │ test-http-app-  │               │  │
│   │   │ ext             │  │ ext             │  │ backend         │               │  │
│   │   │                 │  │                 │  │                 │               │  │
│   │   │ ClusterIP: None │  │ IP:             │  │ Endpoint:       │               │  │
│   │   │ (headless)      │  │ 100.65.0.112    │  │ 100.65.0.112:80 │               │  │
│   │   └─────────────────┘  └─────────────────┘  └─────────────────┘               │  │
│   └───────────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

### -ext Service Purpose

The `-ext` suffix services provide a way for other components to discover the external LoadBalancer IP:

```yaml
# Original Service
apiVersion: v1
kind: Service
metadata:
  name: test-http-app
  namespace: shalb-demo
spec:
  type: LoadBalancer
  clusterIP: 10.101.61.188
  ports:
    - port: 80
  selector:
    app: test-http-app
status:
  loadBalancer:
    ingress:
      - ip: 100.65.0.112

---
# Auto-created -ext Service (headless)
apiVersion: v1
kind: Service
metadata:
  name: test-http-app-ext
  namespace: shalb-demo
  labels:
    kube-dc.com/endpoint-type: external
    kube-dc.com/managed-by: service-lb-controller
    kube-dc.com/source-service: test-http-app
spec:
  clusterIP: None  # Headless
  ports:
    - name: http
      port: 80

---
# Auto-created Endpoints
apiVersion: v1
kind: Endpoints
metadata:
  name: test-http-app-ext
  namespace: shalb-demo
subsets:
  - addresses:
      - ip: 100.65.0.112  # External LB IP
    ports:
      - name: http
        port: 80
```

## Envoy Gateway Architecture

### Current Deployment

```
┌──────────────────────────────────────────────────────────────────────────────────────┐
│                         ENVOY GATEWAY ARCHITECTURE                                    │
│                                                                                       │
│   ┌───────────────────────────────────────────────────────────────────────────────┐  │
│   │                    envoy-gateway-system namespace                              │  │
│   │                    (in ovn-default subnet: 10.100.0.0/16)                      │  │
│   │                                                                                │  │
│   │   ┌────────────────────────┐     ┌────────────────────────────────────────┐   │  │
│   │   │  Envoy Gateway         │     │  Envoy Proxy Pod                       │   │  │
│   │   │  Controller            │     │                                        │   │  │
│   │   │                        │     │  Pod IP: 10.100.0.250                  │   │  │
│   │   │  • Watches Gateway,    │     │                                        │   │  │
│   │   │    HTTPRoute, TLSRoute │     │  Listens on:                           │   │  │
│   │   │  • Configures Envoy    │     │  • 7443 (TLS passthrough)              │   │  │
│   │   │    proxy               │     │  • 8080 (HTTP)                         │   │  │
│   │   └────────────────────────┘     │                                        │   │  │
│   │                                  └────────────────────────────────────────┘   │  │
│   │                                                   │                           │  │
│   │   ┌───────────────────────────────────────────────┴───────────────────────┐   │  │
│   │   │  Service: envoy-envoy-gateway-system-eg-tls-passthrough-24835ac8      │   │  │
│   │   │                                                                        │   │  │
│   │   │  Type: ClusterIP                                                       │   │  │
│   │   │  ClusterIP: 10.101.148.184                                             │   │  │
│   │   │  externalIPs: [88.99.29.250]  ◄── Shared with nginx-ingress            │   │  │
│   │   │                                                                        │   │  │
│   │   │  Ports:                                                                │   │  │
│   │   │  • 7443 → 7443 (TLS passthrough)                                       │   │  │
│   │   │  • 8080 → 8080 (HTTP)                                                  │   │  │
│   │   └────────────────────────────────────────────────────────────────────────┘   │  │
│   └───────────────────────────────────────────────────────────────────────────────┘  │
│                                                                                       │
│                                        │                                              │
│                                        │ externalIPs: 88.99.29.250                    │
│                                        │                                              │
│                                        ▼                                              │
│   ┌───────────────────────────────────────────────────────────────────────────────┐  │
│   │                         EXTERNAL TRAFFIC FLOW                                  │  │
│   │                                                                                │  │
│   │   Internet Client (88.99.218.47)                                               │  │
│   │         │                                                                      │  │
│   │         ▼                                                                      │  │
│   │   Node External IP (88.99.29.250:7443)                                         │  │
│   │         │                                                                      │  │
│   │         ▼ kube-proxy intercepts                                                │  │
│   │         │ SNAT: src 88.99.218.47 → 172.30.0.2 (join IP) ◄── CLIENT IP LOST!   │  │
│   │         │                                                                      │  │
│   │         ▼                                                                      │  │
│   │   Envoy Pod (10.100.0.250:7443)                                                │  │
│   │         │ Sees client IP: 172.30.0.2 ❌                                        │  │
│   │         │                                                                      │  │
│   │         ▼ Routes via HTTPRoute/TLSRoute                                        │  │
│   │   Backend (via Backend resource or Service)                                    │  │
│   └───────────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

### Gateway API Resources

```
┌──────────────────────────────────────────────────────────────────────────────────────┐
│                         GATEWAY API RESOURCE HIERARCHY                                │
│                                                                                       │
│   ┌───────────────────────────────────────────────────────────────────────────────┐  │
│   │                    GatewayClass (cluster-scoped)                               │  │
│   │                                                                                │  │
│   │   name: eg                                                                     │  │
│   │   controller: gateway.envoyproxy.io/gatewayclass-controller                   │  │
│   └───────────────────────────────────────────────────────────────────────────────┘  │
│                                        │                                              │
│                                        ▼                                              │
│   ┌───────────────────────────────────────────────────────────────────────────────┐  │
│   │                    Gateway (envoy-gateway-system/eg-tls-passthrough)           │  │
│   │                                                                                │  │
│   │   spec:                                                                        │  │
│   │     gatewayClassName: eg                                                       │  │
│   │     listeners:                                                                 │  │
│   │       - name: tls                                                              │  │
│   │         port: 7443                                                             │  │
│   │         protocol: TLS                                                          │  │
│   │         tls:                                                                   │  │
│   │           mode: Passthrough                                                    │  │
│   │       - name: http                                                             │  │
│   │         port: 8080                                                             │  │
│   │         protocol: HTTP                                                         │  │
│   │   status:                                                                      │  │
│   │     addresses:                                                                 │  │
│   │       - value: 10.101.148.184                                                  │  │
│   └───────────────────────────────────────────────────────────────────────────────┘  │
│                                        │                                              │
│                    ┌───────────────────┴───────────────────┐                         │
│                    │                                       │                         │
│                    ▼                                       ▼                         │
│   ┌────────────────────────────────┐   ┌────────────────────────────────┐            │
│   │  TLSRoute (shalb-demo/demo-    │   │  HTTPRoute (shalb-demo/test-   │            │
│   │  cluster)                      │   │  http-app)                     │            │
│   │                                │   │                                │            │
│   │  hostnames:                    │   │  hostnames:                    │            │
│   │  - demo-cluster.stage.kube-   │   │  - test-http-app.stage.kube-  │            │
│   │    dc.com                      │   │    dc.com                      │            │
│   │                                │   │                                │            │
│   │  backendRefs:                  │   │  backendRefs:                  │            │
│   │  - name: demo-cluster-backend  │   │  - name: test-http-app-backend │            │
│   │    kind: Backend               │   │    kind: Backend               │            │
│   │    group: gateway.envoyproxy.io│   │    group: gateway.envoyproxy.io│            │
│   └────────────────────────────────┘   └────────────────────────────────┘            │
│                    │                                       │                         │
│                    ▼                                       ▼                         │
│   ┌────────────────────────────────┐   ┌────────────────────────────────┐            │
│   │  Backend (shalb-demo/demo-     │   │  Backend (shalb-demo/test-     │            │
│   │  cluster-backend)              │   │  http-app-backend)             │            │
│   │                                │   │                                │            │
│   │  spec:                         │   │  spec:                         │            │
│   │    endpoints:                  │   │    endpoints:                  │            │
│   │    - ip:                       │   │    - ip:                       │            │
│   │        address: 100.65.0.105   │   │        address: 100.65.0.112   │            │
│   │        port: 6443              │   │        port: 80                │            │
│   └────────────────────────────────┘   └────────────────────────────────┘            │
│                    │                                       │                         │
│                    ▼                                       ▼                         │
│   ┌────────────────────────────────┐   ┌────────────────────────────────┐            │
│   │  OVN LoadBalancer (cloud)      │   │  OVN LoadBalancer (cloud)      │            │
│   │                                │   │                                │            │
│   │  VIP: 100.65.0.105:6443        │   │  VIP: 100.65.0.112:80          │            │
│   │  Backends: 10.0.10.x:6443      │   │  Backends: 10.0.10.28:80       │            │
│   │  (Kamaji control plane pods)   │   │  (test-http-app pod)           │            │
│   └────────────────────────────────┘   └────────────────────────────────┘            │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

## Complete Traffic Flow Examples

### Example 1: TLS Passthrough to Kamaji Control Plane

```
Internet Client
      │
      │ https://demo-cluster.stage.kube-dc.com:7443
      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  Node (88.99.29.250:7443)                                                │
│      │                                                                   │
│      ▼ kube-proxy                                                        │
│      │ SNAT: client IP → 172.30.0.x                                      │
│      ▼                                                                   │
│  Envoy Pod (10.100.0.250:7443)                                           │
│      │                                                                   │
│      │ TLSRoute matches SNI: demo-cluster.stage.kube-dc.com              │
│      │ Backend: demo-cluster-backend                                     │
│      ▼                                                                   │
│  ┌─────────────────────────────────────────────────────────────────────┐│
│  │ OVN Network (ovn-default → ext-cloud)                                ││
│  │                                                                      ││
│  │ Envoy → 100.65.0.105:6443 (Backend IP endpoint)                     ││
│  │           │                                                          ││
│  │           ▼ OVN LoadBalancer DNAT                                    ││
│  │       10.0.10.x:6443 (Kamaji pod in shalb-demo VPC)                  ││
│  └─────────────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────────────┘
```

### Example 2: HTTP Route to Test App

```
Internet Client
      │
      │ http://test-http-app.stage.kube-dc.com:8080
      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  Node (88.99.29.250:8080)                                                │
│      │                                                                   │
│      ▼ kube-proxy                                                        │
│      │ SNAT: client IP → 172.30.0.x                                      │
│      ▼                                                                   │
│  Envoy Pod (10.100.0.250:8080)                                           │
│      │                                                                   │
│      │ HTTPRoute matches Host: test-http-app.stage.kube-dc.com           │
│      │ Backend: test-http-app-backend                                    │
│      ▼                                                                   │
│  ┌─────────────────────────────────────────────────────────────────────┐│
│  │ OVN Network (ovn-default → ext-cloud)                                ││
│  │                                                                      ││
│  │ Envoy → 100.65.0.112:80 (Backend IP endpoint)                       ││
│  │           │                                                          ││
│  │           ▼ OVN LoadBalancer DNAT                                    ││
│  │       10.0.10.28:80 (test-http-app pod in shalb-demo VPC)            ││
│  └─────────────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────────────┘
```

## Nginx Ingress (Comparison)

```
┌──────────────────────────────────────────────────────────────────────────┐
│                     NGINX INGRESS CONFIGURATION                           │
│                                                                           │
│   Namespace: ingress-nginx                                                │
│                                                                           │
│   Service: ingress-nginx-controller                                       │
│   Type: LoadBalancer                                                      │
│   ClusterIP: 10.101.151.121                                               │
│   externalIPs: [88.99.29.250]  ◄── Same IP as Envoy!                     │
│                                                                           │
│   Ports:                                                                  │
│   • 80:31928 (HTTP)                                                       │
│   • 443:31891 (HTTPS)                                                     │
│   • 6443:30504 (TCP - for Kamaji?)                                        │
│                                                                           │
│   Note: nginx-ingress uses pod networking (10.100.0.153)                  │
│         NOT hostNetwork                                                   │
│         Same SNAT issue as Envoy - client IP lost                         │
└──────────────────────────────────────────────────────────────────────────┘
```

## Known Issues

### 1. Client IP Preservation

**Problem**: kube-proxy SNATs incoming traffic through the join subnet (172.30.0.x), losing the real client IP.

**Impact**: SecurityPolicy IP-based filtering doesn't work correctly.

**Current Workaround**: None implemented.

**Proposed Solutions**:
- Cloud Bridge Router (see `cloud_bridge_network_routing_ingress.md`)
- `externalTrafficPolicy: Local`
- Proxy Protocol

### 2. Cross-VPC Communication

**Problem**: Pods in different VPCs (e.g., ovn-default vs shalb-demo) cannot communicate directly.

**Solution**: Use OVN LoadBalancer VIPs on ext-cloud network as intermediary. The Backend resource points to the LB VIP (100.65.x.x), which DNATs to the actual pod IPs.

### 3. Shared externalIP

**Problem**: Both nginx-ingress and Envoy Gateway share 88.99.29.250. Port conflicts are managed by using different ports:
- nginx: 80, 443, 6443
- Envoy: 7443, 8080

## Resource Summary

| Resource Type | Count | Purpose |
|---------------|-------|---------|
| VPCs | 5 | Network isolation per project |
| Subnets | 8 | IP allocation for pods and LBs |
| EIps | 14 | External IP management |
| OvnEips | 14 | OVN external IP binding |
| OvnSnatRules | 5 | Outbound NAT for VPCs |
| Backends | 3 | Gateway API backend endpoints |
| HTTPRoutes | 1 | HTTP routing rules |
| TLSRoutes | 2 | TLS passthrough routing |
| Gateways | 1 | Envoy Gateway listener |

## References

- [Kube-OVN VPC Documentation](https://kubeovn.github.io/docs/stable/en/vpc/vpc/)
- [Envoy Gateway Documentation](https://gateway.envoyproxy.io/)
- [Gateway API Specification](https://gateway-api.sigs.k8s.io/)
- Cloud Bridge PRD: `cloud_bridge_network_routing_ingress.md`
- Cloud Network Enable: `cloud_network_enable_cluster.md`
- Gateway Primitives: `gateway_primitives.md`
