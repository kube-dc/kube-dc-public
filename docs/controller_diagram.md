# Controller Architecture Diagram

A high-level view of Kube-DC controller components (excluding UI) and external dependencies.

```mermaid
flowchart TB
  subgraph Installer
    CD[cluster.dev IaC]
  end

  subgraph K8sCluster["Kubernetes Cluster & CRDs"]
    CRDs[["Org, Project, OrgGroup, EIp, FIp CRDs"]]
  end

  subgraph Manager["Controller Manager"]
    OR(OrganizationReconciler)
    PR(ProjectReconciler)
    OGR(OrganizationGroupReconciler)
    EIP(EIpReconciler)
    FIP(FIpReconciler)
    SR(ServiceReconciler)
  end

  subgraph Logic["Business Logic Packages"]
    OGi[internal/organization]
    PI [internal/project]
    OGG[internal/organizationgroup]
    SLP[internal/service_lb]
    OBJ[internal/objmgr]
    UTL[internal/utils]
  end

  subgraph Ext["External Dependencies"]
    KC[Keycloak]
    KO[Kube-OVN]
    KV[KubeVirt]
    ML[Multus CNI]
    CM[Cert-Manager]
    PM[Prometheus & Loki]
  end

  CD --> CRDs
  CRDs --> OR & PR & OGR & EIP & FIP & SR

  OR --> OGi
  PR --> PI
  OGR --> OGG
  EIP --> SLP
  FIP --> SLP
  SR --> SLP

  SLP --> KO
  OGi --> KC
  PI --> KO & KV & ML
  PI --> PM & CM
  PI --> KC

  style CRDs fill:#f9f,stroke:#333,stroke-width:2px
  style Manager fill:#bbf,stroke:#333,stroke-width:2px
  style Logic fill:#bfb,stroke:#333,stroke-width:2px
  style Ext fill:#ffb,stroke:#333,stroke-width:2px
  style Installer fill:#fbb,stroke:#333,stroke-width:2px
```

## Networking Integration (Kube-OVN & Multus)

Below is a focused diagram showing how Kube-OVN and Multus CNI are installed and integrated via the Project NetworkAttachmentDefinition.

```mermaid
flowchart LR
  subgraph Installer
    KOV[Kube-OVN Helm Chart]
    MULT[Multus CNI Helm Chart]
  end

  KOV --> MULT

  subgraph CNIInfra["CNI Infrastructure"]
    OVN[ovn-daemon (kube-ovn)]
    MPods[Multus Pods]
  end

  MULT --> MPods
  KOV --> OVN
  OVN & MPods --> CNIInfra

  NewNAD[NewProjectNad Controller]
  NADCRD[NetworkAttachmentDefinition CR]
  CNIConfig["Spec.Config: {type:'kube-ovn', server_socket:'/run/openvswitch/kube-ovn-daemon.sock', provider:<proj>} "]
  PodAttach["Pod annotation 'k8s.v1.cni.cncf.io/networks' = NAD"]

  NewNAD --> NADCRD
  NADCRD --> CNIConfig
  CNIConfig --> PodAttach
  PodAttach --> CNIInfra

  style Installer fill:#fbb,stroke:#333,stroke-width:1px
  style CNIInfra fill:#ffb,stroke:#333,stroke-width:1px
  style NewNAD fill:#bfb,stroke:#333,stroke-width:1px
  style NADCRD fill:#f9f,stroke:#333,stroke-width:1px
```

Referenced code:
- Scheme registration: 【F:cmd/main.go†L57-L60】
- NAD controller: 【F:internal/project/res_nad.go†L12-L27】
  - Installer sequence: 【F:installer/kube-dc/templates/kube-dc/template.yaml†L94-L102】【F:installer/kube-dc/templates/kube-dc/template.yaml†L119-L127】

## EIP, FIP & ServiceLoadBalancer Networking Flows

```mermaid
flowchart TD
  subgraph ProjectNet["Project Networking Controllers"]
    EIPdef[NewProjectEip (Default Gateway EIP)]
    EIPcr[NewProjectEip CR]
    EIPsync[EIpReconciler]
    FIPsync[FIpReconciler]
    LBsync[ServiceReconciler]
  end

  subgraph OVNNB["OVN Northbound DB & OVS"]
    OVNNB[ovn-nb.db]
    OVSOCK[ovs-db socket]
  end

  EIPdef --> EIPcr
  EIPcr --> EIPsync
  EIPsync --> OVNNB

  FIPsync -->|Sync EIP + Floating IP| OVNNB

  LBsync -->|Ensure external IP via EIp CR| OVNNB
  LBsync -->|Configure Virtual IPs in LB| OVNNB

  OVNNB --> OVSOCK

  classDef flow fill:#eef,stroke:#666,stroke-width:1px;
  class EIPdef,EIPcr,EIPsync,FIPsync,LBsync flow;
```

### Detailed Network Stack Implementation

1. **Project VPC & Subnet provisioning** (`internal/project/res_vpc.go`)
   - Creates an OVN Virtual Private Cloud via `OvnVpc` CR and logical switch.
2. **NetworkAttachmentDefinition** (`internal/project/res_nad.go`)
   - Defines a Multus NAD with CNI config for `kube-ovn`, pointing at the OVS socket and project provider.
3. **SNAT Rule** (`internal/project/res_snat.go`)
   - Installs an `OvnSnatRule` to translate pod-source IPs to the project gateway EIP for outbound internet.
4. **Default Gateway EIP** (`internal/project/res_eip_default.go`)
   - Ensures a project-scoped `EIp` CR representing the default gateway external IP, created via `NewEipDefault`.
5. **Floating IP (FIp)** (`internal/fip/res_eip.go` & `FIpReconciler`)
   - Syncs or creates EIp owned by FIp, then updates `FIp.Status.ExternalIP` after attaching the EIp to pods via OVN.
6. **Service LoadBalancer** (`internal/service_lb/service_lb.go`, `internal/service_lb/eip_res.go`, `ServiceReconciler`)
   - `NewSvcLbEIpRes` allocates or binds an external IP for the Service.
   - `NewLoadBalancerRes` uses OVN NB client to define load balancer VIP→backend mappings and injects rules into logical router/switch.
7. **Extra External Subnets** (`internal/project/res_vpc.go`)
   - Adds `ExtraExternalSubnets` field to `Vpc.Spec` when `project.Spec.EgressNetworkType` differs from the default external subnet, enabling multi-network external connectivity.
   ```go
   if externalNetwork.Name != defaultExternalSubnet.Name {
       vpc.Spec.ExtraExternalSubnets = []string{externalNetwork.Name}
   }
   ```
   【F:internal/project/res_vpc.go†L45-L52】

-Refer to code for detailed behavior:
- Preamble and flag parsing: 【F:cmd/main.go†L117-L131】
- NAD CNI config: 【F:internal/project/res_nad.go†L14-L31】
- SNAT via OVN: 【F:internal/project/res_snat.go†L14-L45】
- Default EIP creation: 【F:internal/project/res_eip_default.go†L15-L42】
- FIp EIP sync: 【F:internal/fip/res_eip.go†L25-L50】
- Service LB orchestration: 【F:internal/service_lb/service_lb.go†L30-L58】【F:internal/service_lb/eip_res.go†L18-L40】

## Public vs Cloud External Networking

Kube-DC supports two external network types: **public** (direct public IPs) and **cloud** (cloud-provider-backed). The type influences EIP/FIP provisioning and SNAT rules:

```go
// ExternalNetworkType defines how external networks are treated:
type ExternalNetworkType string
const (
  ExternalNetworkTypePublic ExternalNetworkType = "public"
  ExternalNetworkTypeCloud  ExternalNetworkType = "cloud"
)

// MasterConfig defaults per resource if not overridden:
DefaultGwNetworkType, DefaultEipNetworkType,
DefaultFipNetworkType, DefaultSvcLbNetworkType
```
【F:api/kube-dc.com/v1/types.go†L1-L18】

### Project Egress Network Selection

The project spec may set `egressNetworkType` to choose the external subnet for VPC/SNAT/EIP.

```go
// GenerateProjectVpc picks externalSubnet based on project.Spec.EgressNetworkType:
externalNetwork, _ := utils.SelectBestExternalSubnet(ctx, cli, project.Spec.EgressNetworkType)
```【F:internal/project/res_vpc.go†L55-L61】

### SNAT Rules for Outbound Traffic

SNAT rules ensure pod egress to internet through the gateway EIP:

```go
// NewProjectSnat creates OvnSnatRule linking project namespace to gateway EIP
base.GeneratedObject = &kubeovn.OvnSnatRule{
  Spec: OvnSnatRuleSpec{
    OvnEip: DefaultOvnEipName(project, externalSubnet.Name),
    Vpc:    projectNamespace,
    VpcSubnet: SubnetName(project),
  },
}
```【F:internal/project/res_snat.go†L14-L45】

### Default Gateway EIP vs Floating IP

- **Default Gateway EIP**: A single EIp CR per project created by `NewProjectEip` when no explicit EIP exists. Used for SNAT and default outbound.
- **Floating IP (FIp)**: EIp allocated per FIp CR to attach public IPs to specific workloads.

```go
// NewProjectEip ensures default project gateway EIp exists
WithGetFunction(func(...) {
  eip, err := resourcesProcessor.GetProjectGwEip()
  if IsNotFound(err) {
    newEip, _ := NewEipDefault(...)
    base.GeneratedObject = newEip
  }
})
```【F:internal/project/res_eip_default.go†L15-L37】

```go
// SyncEip for FIp: derives EIp name from FIp and creates/gets it
// then FIpReconciler attaches exclusive ownership in OVN
```【F:internal/fip/res_eip.go†L25-L40】

### Service LoadBalancer External IP Binding

ServiceReconciler uses annotations or defaults to bind EIp to Services:

```go
// Get or create EIp for Service LB via NewSvcLbEIpRes
eipSyncer := NewSvcLbEIpRes(ctx, cli, svc, project)
eipSyncer.Sync(ctx)

// Configure OVN LB VRRP rules via NewLoadBalancerRes
lbRes := NewLoadBalancerRes(ctx, cli, svc, endpoints, eipSyncer.Found(), project)
lbRes.Sync(ctx)
```【F:internal/service_lb/eip_res.go†L18-L40】【F:internal/service_lb/service_lb.go†L75-L98】