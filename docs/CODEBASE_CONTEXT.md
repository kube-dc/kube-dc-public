# Codebase Context for Kube-DC

This document summarizes the codebase structure of the Kube-DC project for future reference. It serves as a single source of truth to avoid repeating exploratory analysis.

## Top-level Organization

```
.github/             # GitHub workflows and issue templates
.vscode/             # Editor settings
api/                 # Kubernetes CRD API definitions for kube-dc.com
charts/              # Helm charts for deploying Kube-DC
cmd/                 # Controller manager entry point (main.go)
docs/                # User-facing documentation and architecture guides
examples/            # Sample manifests and usage scenarios
hack/                # Development and automation scripts
internal/            # Core libraries, controllers, and utilities
installer/           # Installation manifests and scripts
services/            # Auxiliary Kubernetes services (DB, storage, etc.)
ui/                  # Web UI (frontend and backend)

Dockerfile           # Container image for controller manager
Dockerfile_manager   # Alternate Dockerfile for the manager image
.dockerignore        # Files to ignore in Docker builds
.gitignore           # Git ignore rules
.golangci.yml        # GolangCI-Lint configuration
Makefile             # Build and automation targets
PROJECT              # Project metadata
README.md            # Project overview and key features
go.mod, go.sum       # Go module definitions
package-lock.json    # Node/NPM dependency lock file for UI backend
mkdocs.yml           # MkDocs configuration for documentation site
```

## Detailed Directory Breakdown

### cmd/
Contains the `main.go` entry point for the Kube-DC controller manager that initializes and runs Kubernetes controllers.

### internal/
Modular Go packages implementing business logic and controller patterns:
- service_lb/: Load balancer and external IP management
- organization/: Organization CRD and Keycloak integration
- eip/, fip/: External/ floating IP resource controllers
- project/, organizationgroup/: CRD controllers for multi-tenancy
- client/, objmgr/, controller/, utils/: Core abstractions for resource management

### ui/ (Web UI)
The `ui` directory contains the Kube‑DC user interface, split into two subprojects:

#### frontend/
React/TypeScript single‑page application scaffolded from PatternFly Seed:
- **Entry**: `src/index.tsx` and `src/app/` for layout, routing, and components
- **Build**: Webpack configs (`webpack.common.js`, `webpack.dev.js`, `webpack.prod.js`) and scripts in `package.json`
- **Assets & manifests**: `kubernetes/` holds deployment, service, and ingress YAML for UI
- **Dev tools**: Jest tests, Storybook (`stories/`), ESLint/Prettier, bundle analyzer, and Surge deployment (`dr-surge.js`)

```text
ui/frontend/
├── src/
│   ├── index.tsx
│   └── app/
├── kubernetes/
├── package.json
├── webpack.common.js
└── README.md
```
【F:ui/frontend/README.md†L1-L6】【F:ui/frontend/package.json†L9-L16】【F:ui/frontend/webpack.common.js†L1-L7】

#### backend/
Node.js/Express API server that provides UI endpoints and in‑cluster proxies:
- **Server**: `app.js` sets up routes, CORS, body parsing, and WebSocket proxy for VNC
- **Controllers**: `controllers/` contains modules for cloud-shell, VMs, volumes, network, projects, metrics, system functions, etc.
- **Proxy**: HTTP and WebSocket proxy middleware to route VNC and other traffic via Kubernetes services
- **Kubernetes manifests**: `kubernetes/` and `kubernetes_service/` directories for deployment YAML

```text
ui/backend/
├── app.js
├── controllers/
│   ├── cloudShellModule.js
│   └── volumeModule.js
├── kubernetes/
├── package.json
└── README.md
```
【F:ui/backend/app.js†L1-L20】【F:ui/backend/controllers/cloudShellModule.js†L1-L10】

### hack/
Utility scripts for:
- cluster setup and bootstrap
- UI code updates and build automation
- integration tests and version management

### charts/
Helm chart definitions to deploy Kube-DC components onto a Kubernetes cluster.

### docs/
Markdown files for:
- Tutorials (quickstart, kubeconfig, IP & LB, VMs, user groups)
- Architecture (networking, virtualization, multi-tenancy, overview)
- Community and support guidelines

### examples/
Sample manifests demonstrating cluster API integration, VM workloads, and organization/user configurations.

### installer/
Installation scripts and YAML manifests for bootstrapping the control plane and CRDs.

### services/
Predefined Kubernetes objects for ancillary services such as database and storage provisioning.

## Go Controller Manager Architecture

### cmd/main.go
- Registers schemes for Kubernetes core, kube-dc CRDs, OVN, and CNI types.
- Parses flags (metrics address, leader election, Keycloak debug, HTTP/2, config secret).
- Initializes controller-runtime Manager with metrics server, health/readiness probes, and webhook server.
- Sets global configuration (ConfigSecretName, KubeDcNamespace).
- Registers Reconcilers: OrganizationReconciler, ProjectReconciler, OrganizationGroupReconciler, EIpReconciler, FIpReconciler, ServiceReconciler.

```go
// Add CRD schemes and plugins; initialize Manager and register controllers
utilruntime.Must(clientgoscheme.AddToScheme(scheme))
utilruntime.Must(kubedccomv1.AddToScheme(scheme))
// ...
mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{...})
// ...
(&controller.OrganizationReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Debug: debug}).SetupWithManager(mgr)
(&controller.ProjectReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Debug: debug}).SetupWithManager(mgr)
// ...
(&corecontroller.ServiceReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr)
```
【F:cmd/main.go†L54-L60】【F:cmd/main.go†L169-L212】

### CRD Types and Schemas (api/kube-dc.com/v1)
- Defines custom resources: Organization, Project, OrganizationGroup, EIp, FIp.
- `*_types.go` files describe Spec and Status fields; `*_extend.go` adds loader and helper methods.

```text
api/kube-dc.com/v1/
├── organization_types.go
├── project_types.go
├── organizationgroup_types.go
├── eip_types.go
├── fip_types.go
├── organization_extend.go
└── project_extend.go
```
【F:api/kube-dc.com/v1/organization_types.go†L1-L80】【F:api/kube-dc.com/v1/project_types.go†L1-L80】

### Controllers (internal/controller)
- **OrganizationReconciler**: Manages Organization CR, delegates to internal/organization for Keycloak realm, auth config, roles, and secrets.
- **ProjectReconciler**: Manages Project CR, orchestrates namespace, VPC, subnet, SNAT, EIP, keypairs, roles, and DNS via internal/project.
- **OrganizationGroupReconciler**: Syncs OrganizationGroup CR, handling Keycloak groups and Kubernetes rolebindings.
- **EIpReconciler** / **FIpReconciler**: Reconcile external/Floating IP CRs.
- **ServiceReconciler**: Reconciles `ServiceTypeLoadBalancer` Services and their Endpoints; loads Project context; manages external IPs via `NewSvcLbEIpRes` and OVN-based load balancers via `NewLoadBalancerRes` in `service_controller.go`.
  【F:internal/controller/core/service_controller.go†L52-L83】【F:internal/controller/core/service_controller.go†L116-L140】

```text
internal/controller/
├── kube-dc.com/
│   ├── organization_controller.go
│   ├── project_controller.go
│   ├── organizatongroup_controller.go
│   ├── eip_controller.go
│   └── fip_controller.go
└── core/
    └── service_controller.go
```
【F:internal/controller/kube-dc.com/organization_controller.go†L1-L30】【F:internal/controller/core/service_controller.go†L1-L20】

### Business Logic (internal packages)
- **internal/organization**: Orchestrates Organization CR synchronization by invoking resource controllers:
  - `organization.go`: Sync/Delete pipeline calling NewKeycloakRealm, NewKubeAuthConfig, NewRealmRole, NewRealmAccessSeret to manage Keycloak realms, Kubernetes auth secrets, realm roles, and access secrets.
    【F:internal/organization/organization.go†L12-L58】【F:internal/organization/organization.go†L61-L102】
- **internal/project**: Orchestrates Project CR lifecycle, provisioning namespaces, networking, and identities:
  - `project.go`: Sync/Delete pipeline calling NewProjectNamespace, NewProjectVpc, NewProjectEip, NewProjectSubnet, NewProjectNad, NewProjectSnat, NewProjectKeyPairSeret, NewProjectAuthKeySecret, NewProjectKeycloakRole, NewProjectRole, NewProjectRoleBinding, NewProjectVpcDns.
    【F:internal/project/project.go†L13-L58】【F:internal/project/project.go†L59-L137】
- **internal/organizationgroup**: Manages Keycloak group and Kubernetes bindings per project.
- **internal/service_lb**: Implements Service LoadBalancer logic using OVN and EIp CRD:
  - `service_lb.go`: Defines `LBResource` which configures OVN logical router/switch load balancers (VIPs→backends) via `NewLoadBalancerRes`, with `Sync`/`Delete` methods to mutate OVN NB DB.
  - `eip_res.go`: Defines `NewSvcLbEIpRes` to reconcile external IP addresses (EIp CRD) for services, based on annotations or project gateway, with functions to Get/Create/Delete and update status.
  【F:internal/service_lb/service_lb.go†L30-L41】【F:internal/service_lb/eip_res.go†L18-L27】
- **internal/objmgr**: Generic resource manager abstractions for creating/updating Kubernetes objects.
- **internal/utils**: Common utilities (random names, JSON copy, resource processor).

### External Integrations
- **Keycloak** via gocloak for identity management.
- **OVN** via kube-ovn client for software‑defined networking.
- **NetworkAttachmentDefinitions** via CNI client for custom network attachments.

### Metrics, Healthchecks & Leader Election
- Exposes secure metrics endpoint with authentication filters.
- Readiness and liveness probes via `/healthz` and `/readyz`.
- Optional leader election for HA controller managers.

## Installation via cluster.dev Infrastructure as Code

Kube-DC leverages the [cluster.dev](https://github.com/shalb/cluster.dev) IaC framework (v0.9.7) to provision and deploy its control plane and dependencies.

Under `installer/kube-dc`:
- **stack.yaml**: Defines a `Stack` using the `templates/kube-dc/` StackTemplate to orchestrate installation units.
  ```yaml
  name: cluster
  template: "./templates/kube-dc/"
  kind: Stack
  ```
  【F:installer/kube-dc/stack.yaml†L1-L4】【F:go.mod†L16】
- **project.yaml**: Defines a `Project` for cluster.dev, setting owner organization and project defaults.
  ```yaml
  name: dev
  kind: Project
  ```
  【F:installer/kube-dc/project.yaml†L1-L3】
- **templates/kube-dc/template.yaml**: StackTemplate with sequential units: Terraform install, password generators, CRDs, Helm charts (kube-ovn, multus-cni, kubevirt, Keycloak, cert-manager, ingress-nginx, monitoring stack, kube-dc core), and custom shell hooks.
  【F:installer/kube-dc/templates/kube-dc/template.yaml†L17-L23】

The installer docs demonstrate bootstrapping cluster.dev CLI and deploying the stack:
- Bootstrapping cluster.dev: `curl -fsSL https://raw.githubusercontent.com/shalb/cluster.dev/master/scripts/get_cdev.sh | sh`
  【F:docs/quickstart-hetzner.md†L175-L176】
- High-level install step: “**Kube-DC Installation**: Use cluster.dev to deploy Kube-DC components”
  【F:docs/quickstart-overview.md†L104-L105】

## CI/CD & Testing

### GitHub Actions workflows
- **release.yaml**: on tag pushes, builds and pushes Helm charts via `hack/build.sh` inside Alpine/helm container.
  【F:.github/workflows/release.yaml†L1-L20】
- **sync_to_public_repo.yaml**: on `main` changes to charts/examples/docs/installer/hack, syncs to the public kube-dc-public repo.
  【F:.github/workflows/sync_to_public_repo.yaml†L1-L55】

### Local CI via Makefile (Go code)
- `make test`: run unit tests with envtest and coverage.
- `make test-e2e`: run end-to-end tests via Kind.
- `make lint`, `make fmt`, `make vet`: lint, format, and vet Go code.
- `make build`: compile the controller manager binary.
  【F:Makefile†L14-L23】【F:Makefile†L27-L38】【F:Makefile†L95-L114】

### Frontend CI (React/TypeScript)
- `npm run ci-checks`: type-check, ESLint lint, and Jest coverage tests.
- Additional scripts: `start:dev`, `build`, `test`, `storybook`, `bundle-profile:analyze`.
  【F:ui/frontend/package.json†L21-L26】

### Backend CI (Node.js/Express)
- `npm run lint`: run ESLint for backend controllers.
  【F:ui/backend/package.json†L28-L33】

## Usage
Refer to this file for project structure insights to avoid redundant codebase exploration.