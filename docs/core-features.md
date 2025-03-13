# Core Features

Kube-DC extends Kubernetes with a robust set of features designed for enterprise data center operations. This page provides detailed technical specifications and use cases for each of Kube-DC's core capabilities.

> Looking for a high-level overview? Visit our [introduction page](index.md).

## Table of Contents

- [Organization Management](#organization-management)
- [Namespace as a Service](#namespace-as-a-service)
- [Network Management](#network-management)
- [Virtualization](#virtualization)
- [Infrastructure as Code](#infrastructure-as-code)
- [Integrated Flexible Billing](#integrated-flexible-billing)
- [Management Services](#management-services)

## Organization Management

!!! info "Foundation for Multi-Tenancy"
    Organization Management provides the foundation for Kube-DC's multi-tenant capabilities, enabling complete isolation between different users and groups.

Kube-DC's multi-tenant architecture allows service providers to host multiple organizations with complete isolation and customization.

**Capabilities:**

- **Multi-Organization Support**: Host multiple organizations on a single Kube-DC installation with complete logical separation
- **Custom SSO Integration**: Each organization can configure its own identity provider:
    - Google Workspace / Gmail
    - Microsoft Active Directory / Azure AD
    - GitHub
    - GitLab
    - LDAP
    - SAML 2.0 providers
    - OpenID Connect providers
- **Hierarchical Group Management**: Create and manage groups within organizations with inheritance of permissions
- **Flexible RBAC**: Assign fine-grained permissions to groups for specific projects or resources
- **Organizational Quotas**: Set resource limits at the organization level to ensure fair resource allocation

!!! example "Real-World Applications"
    - **Managed Service Providers**: Host multiple client organizations with separate authentication systems
    - **Enterprise IT**: Separate departments with different authentication requirements
    - **Educational Institutions**: Provide isolated environments for different departments or research groups

## Namespace as a Service

!!! info "Projects and Workloads"
    Namespaces in Kube-DC function as projects, providing isolated environments for deploying and managing diverse workloads.

Every project in Kube-DC is allocated its own Kubernetes namespace with extended capabilities for running both containers and virtual machines.

**Capabilities:**

- **Unified Management**: Deploy and manage both VMs and containers from a single interface
- **Project Isolation**: Complete network and resource isolation between projects
- **Resource Quotas**: Set limits on CPU, memory, storage, and other resources per project
- **Integrated Dashboard**: View and manage all workloads through a unified web interface
- **Custom Templates**: Create and use templates for quick deployment of common workloads

!!! example "Real-World Applications"
    - **Application Modernization**: Run legacy VMs alongside containerized microservices
    - **Development Environments**: Provide isolated environments for development, testing, and staging
    - **Mixed Workloads**: Support teams that require both traditional and cloud-native infrastructure

## Network Management

!!! info "Advanced Connectivity"
    Kube-DC's network capabilities enable sophisticated connectivity options while maintaining isolation between projects.

Kube-DC provides advanced networking capabilities that bridge traditional data center networking with cloud-native concepts.

**Capabilities:**

- **Dedicated VPC per Project**: Each project gets its own virtual network environment
- **VLAN Integration**: Connect to physical network infrastructure using VLANs
- **Software-Defined Networking**: Create overlay networks with software-defined control
- **Network Peering**: Connect project networks with each other or with external networks
- **NAT and Internet Gateway**: Control outbound and inbound internet access per project
- **External IP Assignment**: Assign public IPs directly to VMs or Kubernetes services
- **Load Balancer Integration**: Create and manage load balancers for services and VMs
- **Network Policies**: Define granular rules for network traffic filtering
- **DNS Management**: Automatic DNS for services and VMs with custom domain support

!!! example "Real-World Applications"
    - **Hybrid Cloud Deployments**: Extend on-premises networks to containerized workloads
    - **Multi-Tier Applications**: Create complex network topologies for enterprise applications
    - **Secure Isolation**: Create zero-trust network environments with fine-grained control

## Virtualization

!!! info "KubeVirt Integration"
    Built on KubeVirt, Kube-DC provides enterprise-grade virtualization capabilities fully integrated with Kubernetes.

Built on KubeVirt, Kube-DC provides enterprise-grade virtualization capabilities integrated with Kubernetes.

**Capabilities:**

- **Hardware Vendor Support**: Compatible with major hardware vendors' servers and components
- **GPU Passthrough**: Support for Nvidia GPU passthrough to virtual machines
- **ARM Support**: Run VMs on ARM-based infrastructure
- **Web Console**: Access VM consoles directly through the web UI
- **SSH Integration**: SSH access management with key authentication
- **Live Migration**: Move running VMs between nodes without downtime
- **Snapshots**: Create point-in-time snapshots of VM volumes
- **VM Templates**: Create and use templates for rapid VM provisioning
- **Custom Boot Options**: Configure boot order, firmware settings, and UEFI support
- **VM Import/Export**: Import existing VMs from other platforms

!!! example "Real-World Applications"
    - **Legacy Application Support**: Run applications that require traditional VMs
    - **Windows Workloads**: Host Windows servers alongside Linux containers
    - **GPU-Accelerated Computing**: Provide GPU resources for AI/ML or rendering workloads
    - **Specialized Operating Systems**: Run operating systems not supported in containers

## Infrastructure as Code

!!! info "API-Driven Architecture"
    Kube-DC's API-driven approach enables automation and integration with popular infrastructure tools.

Kube-DC leverages and extends the Kubernetes API to enable comprehensive infrastructure automation.

**Capabilities:**

- **Native Kubernetes API**: Manage all Kube-DC resources using standard Kubernetes tools
- **Custom Resource Definitions (CRDs)**: Extended Kubernetes objects for managing organizations, projects, VMs, and more
- **GitOps Compatible**: Deploy and manage infrastructure using GitOps workflows
- **Terraform Provider**: Official Terraform provider for Kube-DC resources
- **Ansible Integration**: Ansible modules for managing Kube-DC resources
- **Crossplane Support**: Use Crossplane to provision and manage Kube-DC resources
- **Pulumi Provider**: Programmatically manage Kube-DC using multiple languages

!!! example "Real-World Applications"
    - **Automated Infrastructure**: Create fully automated infrastructure provisioning workflows
    - **Self-Service Portals**: Build custom self-service interfaces using the Kube-DC API
    - **CI/CD Integration**: Include infrastructure provisioning in CI/CD pipelines
    - **Multi-Cloud Management**: Manage Kube-DC resources alongside other cloud resources

## Integrated Flexible Billing

!!! info "Cost Management"
    Track, allocate, and manage costs across all resources with Kube-DC's comprehensive billing capabilities.

Kube-DC includes comprehensive resource tracking and billing capabilities suitable for both service providers and internal IT organizations.

**Capabilities:**

- **Resource Metering**: Track usage of CPU, memory, storage, GPU, and network resources
- **Custom Pricing Models**: Define pricing tiers for different resource types and customers
- **Project-Based Billing**: Track and bill resource usage at the project level
- **Cost Allocation**: Assign costs to organizational units, projects, or individual resources
- **Quota Enforcement**: Automatically enforce resource limits based on billing status
- **Usage Reporting**: Generate detailed usage reports for analysis and billing
- **Billing API**: Integrate with external billing systems through a comprehensive API
- **Chargeback Models**: Support for various internal chargeback models for enterprise use

!!! example "Real-World Applications"
    - **Managed Service Providers**: Bill customers for exact resource usage
    - **Enterprise IT**: Implement internal chargeback or showback for departmental resource usage
    - **Resource Optimization**: Identify resource usage patterns and optimize costs

## Management Services

!!! info "Value-Added Services"
    Extend Kube-DC's capabilities by offering managed services on top of the core platform.

Kube-DC provides a platform for delivering managed services on top of its infrastructure.

**Capabilities:**

- **Database as a Service**: Deploy and manage databases with automated operations
  - PostgreSQL
  - MySQL/MariaDB
  - Microsoft SQL Server
  - And more
- **Object Storage**: S3-compatible storage with multi-tenancy support
- **NoSQL Databases**: Managed NoSQL database offerings
  - Redis
  - MongoDB
  - Elasticsearch/OpenSearch
- **AI/ML Platform**: Infrastructure for deploying and serving AI/ML models
  - LLM serving
  - Model training infrastructure
  - GPU resource allocation
- **Backup Services**: Automated backup solutions for VMs and containers
- **Monitoring as a Service**: Multi-tenant monitoring solutions
- **Service Catalog**: Self-service provisioning of common services

!!! example "Real-World Applications"
    - **Internal Platform Team**: Provide managed services to development teams
    - **Managed Service Providers**: Offer value-added services beyond basic infrastructure
    - **AI/ML Operations**: Provide specialized infrastructure for data science teams
