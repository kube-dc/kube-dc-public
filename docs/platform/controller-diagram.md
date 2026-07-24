# Controller Architecture

This page shows how Kube-DC controllers turn platform custom resources into
Kubernetes, network, identity, storage, and security state. It is an operator
view: resource names and component responsibilities are shown, but source-code
paths and implementation details are intentionally omitted.

## High-Level Topology

```mermaid
flowchart TB
  subgraph GitOps["Installer and GitOps"]
    CLI["kube-dc CLI"]
    Fleet["Fleet repository"]
    Flux["Flux"]
  end

  subgraph API["Kubernetes API"]
    TenantCRDs["Tenant CRDs<br/>Organization, Project, OrganizationGroup"]
    NetworkCRDs["Network CRDs<br/>EIp, FIp"]
    SecurityCRDs["Security CRDs<br/>ManagedSecret, ManagedCertificate,<br/>KMSKey, DatabaseCredentialPolicy"]
    CoreResources["Core resources<br/>Service, Secret, ConfigMap, Namespace"]
  end

  subgraph Manager["kube-dc Manager"]
    TenantControllers["Tenant controllers"]
    NetworkControllers["Network controllers"]
    SecurityControllers["Security controllers"]
    EndpointControllers["Platform endpoint controllers"]
  end

  subgraph Platform["Platform Systems"]
    Keycloak["Keycloak"]
    OpenBao["OpenBao"]
    KubeOVN["Kube-OVN and Multus"]
    KubeVirt["KubeVirt, CDI, and storage"]
    Gateway["Envoy Gateway and cert-manager"]
    Observability["Grafana, Prometheus, Loki, Mimir"]
  end

  CLI --> Fleet --> Flux --> API
  TenantCRDs --> TenantControllers
  NetworkCRDs --> NetworkControllers
  SecurityCRDs --> SecurityControllers
  CoreResources --> NetworkControllers
  CoreResources --> EndpointControllers

  TenantControllers --> Keycloak
  TenantControllers --> KubeOVN
  TenantControllers --> KubeVirt
  TenantControllers --> Observability

  NetworkControllers --> KubeOVN
  EndpointControllers --> Gateway
  EndpointControllers --> KubeOVN

  SecurityControllers --> OpenBao
  SecurityControllers --> Gateway
  SecurityControllers --> Observability

  classDef input fill:#eef5ff,stroke:#4b77be,stroke-width:1px;
  classDef manager fill:#edf8ed,stroke:#3b8c3b,stroke-width:1px;
  classDef platform fill:#fff7df,stroke:#b98500,stroke-width:1px;
  classDef api fill:#f7ecff,stroke:#7d4fa3,stroke-width:1px;

  class CLI,Fleet,Flux input;
  class TenantCRDs,NetworkCRDs,SecurityCRDs,CoreResources api;
  class TenantControllers,NetworkControllers,SecurityControllers,EndpointControllers manager;
  class Keycloak,OpenBao,KubeOVN,KubeVirt,Gateway,Observability platform;
```

## Controller Groups

| Controller group | Watches | Main responsibility |
| --- | --- | --- |
| Tenant controllers | Organizations, Projects, OrganizationGroups | Create tenant namespaces, identity mappings, RBAC, default project networking, quotas, and project lifecycle state. |
| Network controllers | EIps, FIps, LoadBalancer Services | Allocate and bind external addresses, program Kube-OVN objects, and keep service load balancers attached to the right routers and switches. |
| Platform endpoint controllers | Annotated platform Services and endpoint health | Publish platform APIs through the configured external network path and keep endpoint health discoverable. |
| Security controllers | ManagedSecrets, ManagedCertificates, KMSKeys, DatabaseCredentialPolicies | Bridge project security resources to OpenBao, cert-manager, projected Kubernetes Secrets, and status rollups. |
| Status aggregation | Project security and platform state | Roll child-resource readiness into higher-level Project and Organization status so operators and UI users see one clear state. |

## Project Lifecycle

```mermaid
sequenceDiagram
  actor Operator
  participant API as Kubernetes API
  participant Tenant as Tenant controllers
  participant Network as Network controllers
  participant Identity as Keycloak
  participant OVN as Kube-OVN and Multus
  participant Virt as KubeVirt and storage
  participant Obs as Observability

  Operator->>API: Create Organization
  API->>Tenant: Reconcile Organization
  Tenant->>Identity: Create or update realm and groups
  Tenant->>API: Publish Organization status

  Operator->>API: Create Project
  API->>Tenant: Reconcile Project
  Tenant->>API: Create project namespace and RBAC
  Tenant->>OVN: Create project VPC, subnet, NAD, and SNAT
  Tenant->>Virt: Prepare VM and storage integration
  Tenant->>Obs: Prepare project dashboards and data sources
  Tenant->>API: Publish Project status

  Operator->>API: Create OrganizationGroup
  API->>Tenant: Reconcile access mapping
  Tenant->>Identity: Sync group membership target
  Tenant->>API: Create project RoleBindings
```

## Network Flow

```mermaid
flowchart LR
  subgraph Project["Project namespace"]
    Workload["VMs and Pods"]
    Service["Service type LoadBalancer"]
    FIp["FIp resource"]
  end

  subgraph Network["Kube-DC network controllers"]
    EIp["EIp allocation"]
    SLB["Service load balancer sync"]
    FIPSync["Floating IP sync"]
  end

  subgraph OVN["Kube-OVN"]
    VPC["Project VPC and subnet"]
    SNAT["Outbound SNAT"]
    LB["OVN load balancer"]
    DNAT["Floating IP rules"]
  end

  subgraph External["External networks"]
    Public["Public or cloud external subnet"]
    Clients["External clients"]
  end

  Workload --> VPC
  VPC --> SNAT --> Public

  Service --> SLB --> EIp --> LB
  FIp --> FIPSync --> EIp --> DNAT

  Clients --> Public
  Public --> LB --> Service
  Public --> DNAT --> Workload
```

Kube-DC has two address concepts:

- **EIp** is the allocated external address object. It can back a project
  gateway, a service load balancer, or another higher-level resource.
- **FIp** attaches an external address to a specific VM interface. It is the
  tenant-facing floating-IP workflow.

The controller keeps ownership and status on the Kube-DC resources while
Kube-OVN owns the low-level routing, NAT, and load-balancer programming.

## Security Flow

```mermaid
flowchart TB
  subgraph ProjectNS["Project namespace"]
    MS["ManagedSecret"]
    MC["ManagedCertificate"]
    KMS["KMSKey"]
    DCP["DatabaseCredentialPolicy"]
    AppSecret["Projected Kubernetes Secret"]
    Certificate["cert-manager Certificate"]
  end

  subgraph Security["Security controllers"]
    SecretCtl["Secret sync"]
    CertCtl["Certificate sync"]
    KMSCtl["KMS key sync"]
    DBPolicyCtl["Database credential policy sync"]
    StatusCtl["Project status aggregation"]
  end

  subgraph Backends["Security backends"]
    Bao["OpenBao"]
    CM["cert-manager"]
    DB["Database manager"]
  end

  MS --> SecretCtl --> Bao
  SecretCtl --> AppSecret

  MC --> CertCtl --> Bao
  CertCtl --> CM --> Certificate

  KMS --> KMSCtl --> Bao
  DCP --> DBPolicyCtl --> Bao
  DCP --> DB
  DBPolicyCtl --> AppSecret

  SecretCtl --> StatusCtl
  CertCtl --> StatusCtl
  KMSCtl --> StatusCtl
  DBPolicyCtl --> StatusCtl
```

Security controllers make project-scoped security resources safe for tenants to
request while keeping privileged operations centralized in the platform. The
Ready conditions on the child resources are aggregated so the Project status can
show whether its security dependencies are usable.

## Platform Endpoint Flow

```mermaid
flowchart LR
  subgraph PlatformNS["Platform namespaces"]
    Service["Platform Service"]
    Health["Endpoint health"]
  end

  subgraph EndpointCtl["Platform endpoint controller"]
    Discovery["Discover service endpoints"]
    Publish["Publish external endpoint"]
  end

  subgraph Gateway["Gateway layer"]
    Route["Gateway route"]
    Backend["Backend target"]
    Cert["TLS certificate"]
  end

  subgraph Network["External network"]
    EIP["External IP"]
    User["Operator or user"]
  end

  Service --> Discovery
  Health --> Discovery
  Discovery --> Publish
  Publish --> Route
  Publish --> Backend
  Publish --> Cert
  Route --> EIP
  EIP --> User
```

Platform endpoints are used for management-plane services such as login, admin
interfaces, and break-glass access paths. The controller publishes only the
services that are explicitly marked for platform exposure and keeps health
visible so operators can distinguish routing issues from backend readiness.

## Reading The Diagram

- The **CLI and Fleet repository** define desired state.
- **Flux** applies that state to the management cluster.
- **Kube-DC controllers** reconcile custom resources and selected Kubernetes
  resources into real platform state.
- **External platform systems** such as Keycloak, OpenBao, Kube-OVN, KubeVirt,
  cert-manager, Envoy Gateway, and observability components do the specialized
  work.
- Status flows back to Kube-DC resources so the UI, CLI, and operators can read
  the platform state from Kubernetes.
