# Kube-DC Platform

Kube-DC is an open-source platform that transforms Kubernetes into a comprehensive Data Center solution. This documentation covers installation, architecture, and operations for platform administrators.

## Overview

Kube-DC extends Kubernetes with multi-tenancy, virtualization (KubeVirt), advanced networking (Kube-OVN), and integrated billing. It can be deployed on bare-metal servers or existing Kubernetes clusters.

## Quick Links

- **Install Kube-DC** — Start with the [Installation Overview](installation-overview.md)
- **Step-by-Step Deployment** — Follow the [Installation Guide](installation-guide.md)
- **Architecture** — Understand the [Architecture Overview](architecture-overview.md)
- **Networking** — Deep dive into [Networking Architecture](architecture-networking.md)

## Key Components

- **Multi-Tenancy** — Organizations, Projects, and RBAC via Keycloak
- **Virtualization** — KubeVirt-based VM management with cloud-init
- **Networking** — Kube-OVN with VPC-per-project, EIPs, FIPs, and LoadBalancers
- **Billing** — Configurable billing plans with resource quotas
- **Storage** — Block storage (PVC) and S3-compatible object storage (Rook Ceph)

## Community

- [GitHub](https://github.com/kube-dc/kube-dc-public) — Source code and issues
- [Slack](https://join.slack.com/t/kube-dc/shared_invite/zt-31mr5c6ci-W3kYQ7qGDULlGQ5QJjsxmA) — Community chat

---

Looking for the cloud user guide? See the [Cloud Guide](/).
