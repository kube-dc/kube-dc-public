# Kube-DC

<p align="center">
  <img src="docs/images/logo-readme.png" alt="Kube-DC Logo" width="300">
</p>

<p align="center">
  <strong>An enterprise-grade platform that transforms Kubernetes into a comprehensive Data Center solution.</strong>
</p>

<p align="center">
  <a href="https://docs.kube-dc.com">Documentation</a> •
  <a href="#key-features">Key Features</a> •
  <a href="#use-cases">Use Cases</a> •
  <a href="#community">Community</a>
</p>

## Overview

Kube-DC bridges the gap between traditional virtualization and modern container orchestration, allowing you to run both legacy workloads and cloud-native applications on the same platform. By leveraging Kubernetes as the foundation, Kube-DC inherits its robust ecosystem while extending functionality to support enterprise requirements.

Kube-DC provides organizations with a unified management interface for all their infrastructure needs, from multi-tenancy and virtualization to networking and billing.

![Kube-DC Architecture Overview](docs/images/arch-overview.png)

## Key Features

### Multi-Tenancy
- **Multiple Organizations**: Host multiple organizations with complete isolation
- **Custom SSO Integration**: Configure various identity providers (Google, Microsoft, GitHub, etc.)
- **Hierarchical Group Management**: Create and manage groups with permission inheritance
- **Flexible RBAC**: Assign fine-grained permissions to specific resources

### Unified Workload Management
- **VMs and Containers**: Deploy and manage both from a single interface
- **Project Isolation**: Complete network and resource isolation between projects
- **Resource Quotas**: Set limits on CPU, memory, storage per project
- **Custom Templates**: Create and use templates for quick deployment

### Advanced Networking
- **VPC per Project**: Each project gets its own virtual network
- **VLAN Support**: Connect to physical infrastructure
- **Software-Defined Networking**: Create overlay networks
- **Network Peering**: Connect project networks
- **External IP Assignment**: Assign public IPs to services
- **Load Balancer Integration**: Manage load balancers for services

### Enterprise Virtualization
- **KubeVirt Integration**: Enterprise-grade VM capabilities
- **Live Migration**: Move VMs between nodes without downtime
- **GPU Passthrough**: Full GPU access for VMs
- **Storage Integration**: Connect to enterprise storage systems
- **VM Import/Export**: Import existing VMs from other platforms

### Infrastructure as Code
- **Kubernetes-Native APIs**: Use familiar Kubernetes patterns
- **GitOps Compatible**: Works with Flux, ArgoCD
- **Terraform Provider**: Infrastructure as code with Terraform
- **Custom Resources**: Define and manage resources with CRDs
- **Programmatic Control**: RESTful APIs for automation

### Integrated Billing
- **Resource Tracking**: Monitor resource usage across projects
- **Cost Allocation**: Assign costs to organizations and projects
- **Flexible Pricing Models**: Implement custom pricing
- **Billing Reports**: Generate detailed billing reports
- **Budget Controls**: Set spending limits and alerts

### Managed Services Platform
- **Database as a Service**: Deploy and manage databases
- **Storage Services**: Provide managed storage options
- **AI/ML Infrastructure**: Deploy specialized infrastructure
- **Service Catalog**: Deploy from pre-configured templates
- **Lifecycle Management**: Automated backups and updates
## Use Cases

### For Enterprise IT
- Run legacy VMs alongside modern containers
- Implement chargeback models for departmental resource usage
- Provide self-service infrastructure while maintaining governance
- Reduce operational costs by consolidating platforms

### For Service Providers
- Offer multi-tenant infrastructure with complete isolation
- Provide value-added services beyond basic IaaS
- Implement flexible billing based on actual resource usage
- Support diverse customer workloads on a single platform

### For DevOps Teams
- Unify VM and container management workflows
- Implement infrastructure as code for all resources
- Integrate with existing CI/CD pipelines
- Enable developer self-service while maintaining control

## Documentation

Comprehensive documentation is available at [docs.kube-dc.com](https://docs.kube-dc.com).

Key documentation sections:
- [Core Features](https://docs.kube-dc.com/core-features/)
- [Architecture & Concepts](https://docs.kube-dc.com/architecture-overview/)
- [Installation Guides](https://docs.kube-dc.com/quickstart-overview/)
- [Multi-Tenancy Management](https://docs.kube-dc.com/creating-and-managing-entities/)
- [API Reference](https://docs.kube-dc.com/reference-api/)

## Community

Kube-DC is built with a focus on community collaboration. We welcome contributions, bug reports, and feature requests.

- [GitHub Issues](https://github.com/kube-dc/kube-dc-public/issues): Report bugs or request features
- [GitHub Discussions](https://github.com/kube-dc/kube-dc-public/discussions): Ask questions and discuss ideas
- [Slack Channel](https://join.slack.com/t/kube-dc/shared_invite/zt-31mr5c6ci-W3kYQ7qGDULlGQ5QJjsxmA): Join our community chat
