# Scaling & Performance Guide

This guide helps you estimate how much performance you can achieve with Kube-DC Cloud resources. These estimates assume an optimized deployment using Helm charts, Envoy load balancing, and managed database services.

## Core Technical Stack

* **Ingress:** Envoy-based Gateway (optimized for high-throughput SSL offloading)
* **Compute:** High-performance vCPU nodes on a low-latency network
* **Storage:** NVMe-backed Persistent Volumes + S3-compatible Object Storage
* **Database:** Decoupled Managed Database (PostgreSQL/MariaDB) to free up compute resources

---

## Plan Overview & Resource Pools

| Feature | **Starter Pool** | **Pro Pool** | **Scale Pool** |
| :--- | :--- | :--- | :--- |
| **vCPU** | 4 Cores | 8 Cores | 16 Cores |
| **RAM** | 8 GB | 24 GB | 56 GB |
| **NVMe Storage** | 60 GB | 160 GB | 320 GB |
| **Object Storage** | 20 GB | 100 GB | 500 GB |
| **Dedicated IPv4** | 1 (Shared Ingress) | 1 (Shared Ingress) | **3 Dedicated IPs** |

---

## WordPress Performance Estimates

WordPress performance on Kubernetes is driven by **PHP-FPM worker density** and **Object Caching (Redis)**. By offloading the database and images (S3), the vCPU is dedicated entirely to page rendering.

| Metric | **Starter (4/8)** | **Pro (8/24)** | **Scale (16/56)** |
| :--- | :--- | :--- | :--- |
| **Concurrent Users (Peak)** | 80 – 120 | 250 – 400 | 800 – 1,200 |
| **Daily Active Users (DAU)** | 15,000 | 45,000 | 120,000+ |
| **Monthly Active Users (MAU)** | 450,000 | 1.3 Million | 4 Million+ |
| **Best For** | High-traffic blogs, SMEs | Large communities, WooCommerce | Enterprise news, Viral portals |

:::tip Optimization Tip
Use the **Scale Pool's** 56GB RAM to allocate a large Redis cache. This allows WordPress to serve "Hot" data from memory, reducing database latency to <10ms.
:::

---

## SaaS Startup Performance Estimates

SaaS workloads (Node.js, Go, Python) are typically "Logic-Heavy." These pools allow for horizontal scaling where the application is split into **API Pods** and **Background Workers**.

| Metric | **Starter (4/8)** | **Pro (8/24)** | **Scale (16/56)** |
| :--- | :--- | :--- | :--- |
| **Concurrent API Req/sec** | 150 – 300 | 600 – 1,000 | 2,500 – 4,000+ |
| **Daily Active Users (DAU)** | 1,200 | 5,500 | 20,000+ |
| **Monthly Active Users (MAU)** | 8,000 | 35,000 | 150,000+ |
| **Architecture** | Single-service CRUD | Microservices (HA) | High Availability + AI/Data Jobs |

### Workload Split Recommendation (Scale Pool)

In the **Scale Pool (16 vCPU / 56 GB)**, we recommend the following resource distribution:

* **Web/API Pods (8 vCPU):** Handles user-facing traffic via Envoy
* **Background Workers (6 vCPU):** Processes tasks, emails, and data exports
* **System/Caching (2 vCPU):** Local Redis/Memcached for session management

---

## Scaling Mechanics on Kube-DC

### Horizontal Pod Autoscaling (HPA)

Your Helm chart is pre-configured to monitor CPU/Memory utilization. When a threshold (e.g., 70% CPU) is met:

1. Kubernetes spins up additional Pods within your resource pool
2. Envoy automatically detects new Pods and begins routing traffic to them
3. The **Managed Database** scales independently, ensuring your app logic never waits for a query

### The Scale Pool Advantage

The **Scale Pool** includes **3 Dedicated IPv4 addresses**. This is critical for:

* **Outbound Reputation:** Dedicated IPs for mail relays or 3rd party API integrations
* **Traffic Isolation:** One IP for the Main App, one for the Admin/Backoffice, and one for Webhook listeners to prevent "Head-of-Line" blocking
* **B2B Whitelisting:** Providing your enterprise clients with a static IP for their firewall rules

---

## Comparison Summary for Decision Making

| Need | Recommendation |
| :--- | :--- |
| **"I'm launching a new product or blog."** | **Starter Pool.** Most cost-effective way to get high-performance NVMe and Managed DB. |
| **"I have a growing user base and need 99.9% uptime."** | **Pro Pool.** Extra RAM allows for multi-replica "High Availability" deployments. |
| **"I have millions of visitors or heavy data processing."** | **Scale Pool.** The 16 vCPU capacity and Dedicated IPs provide enterprise-grade stability. |

---

## Next Steps

- Learn how to [deploy your first app](deploy-first-app.md) with automatic scaling
- Explore [managed databases](managed-databases.md) for decoupled data storage
- Check [billing and usage](billing-usage.md) to monitor your resource consumption
