import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import KubernetesIcon from '../../../../docs/diagrams/icons/kubernetes.svg';
import KeycloakIcon from '../../../../docs/diagrams/icons/keycloak.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import PublicIcon from '../../../../docs/diagrams/icons/network-public.svg';
import UsersIcon from '../../../../docs/diagrams/icons/users.svg';
import {LinearFlowDiagram} from './FlowDiagram';

export function AiMcpFlowDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="The MCP server is a permission-preserving bridge: the assistant can perform only the Kubernetes operations authorized by the Project kubeconfig supplied to that server."
      description="Claude Code, Cursor, Devin Desktop, or another compatible AI IDE sends Model Context Protocol requests to a Kubernetes MCP server. The server uses the Kubernetes API and the supplied Project-scoped kubeconfig to access the Kube-DC management cluster in that Project context."
      diagramId="ai-mcp-flow-explainer"
      sectionLabel="AI TOOL ACCESS PATH"
      steps={[
        {title: 'AI IDE', detail: ['Claude · Cursor', 'Devin Desktop'], icon: ApplicationIcon, tone: 'external'},
        {title: 'Kubernetes MCP', detail: 'tool bridge', icon: ControllerIcon, tone: 'accent'},
        {title: 'Kube-DC API', detail: ['Project context', 'via kubeconfig'], icon: ApiIcon},
      ]}
      relationships={[
        {label: 'MCP', labelWidth: 52, kind: 'control'},
        {label: 'Kubernetes API', labelWidth: 112},
      ]}
      title="AI IDE access through MCP"
    />
  );
}

export function ManagedClusterLoadBalancerDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="The per-cluster CCM watches the tenant Service, creates its platform-side counterpart in the Project backing namespace, and reports the allocated external address back to the Managed Cluster."
      description="A LoadBalancer Service named my-app on port 3000 exists in Managed Cluster dev. The per-cluster Cloud Controller Manager watches it and creates a corresponding LoadBalancer Service in the acme-production backing namespace on the platform management cluster. The platform Service receives an external IP, which is reported back to the Managed Cluster Service."
      diagramId="managed-cluster-loadbalancer-explainer"
      sectionLabel="MANAGED CLUSTER SERVICE EXPOSURE"
      steps={[
        {title: 'Managed Service', detail: ['dev · my-app:3000', 'type LoadBalancer'], icon: KubernetesIcon},
        {title: 'Per-cluster CCM', detail: ['watches +', 'reconciles'], icon: ControllerIcon, tone: 'accent'},
        {title: 'Platform Service', detail: ['acme-production', 'external IP'], icon: NetworkIcon},
      ]}
      relationships={[{label: 'watches', labelWidth: 70, kind: 'control'}, {label: 'creates', labelWidth: 66}]}
      title="Managed Cluster LoadBalancer reconciliation"
    />
  );
}

export function ManagedClusterStorageDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="KubeVirt CSI translates the tenant PVC into platform storage in the owning Project and hot-plugs the resulting volume to the worker VM where the Pod is scheduled."
      description="A five GiB PersistentVolumeClaim using the kubevirt StorageClass is created in a Managed Cluster. The infrastructure-side KubeVirt CSI controller creates a DataVolume and PersistentVolumeClaim using platform storage in the production Project backing namespace. The volume is hot-plugged to the selected Managed Cluster worker virtual machine and mounted into the Pod."
      diagramId="managed-cluster-storage-explainer"
      sectionLabel="KUBEVIRT CSI STORAGE PATH"
      steps={[
        {title: 'Tenant PVC', detail: ['my-data · 5 GiB', 'class kubevirt'], icon: DataIcon},
        {title: 'KubeVirt CSI', detail: ['infrastructure', 'side'], icon: ControllerIcon, tone: 'accent'},
        {title: 'Project volume', detail: ['DV + PVC', 'Project storage'], icon: DataIcon, tone: 'storage'},
        {title: 'Worker VM', detail: ['hot-plugged', 'mounted in Pod'], icon: ComputeIcon},
      ]}
      relationships={[{kind: 'control'}, {label: 'creates', labelWidth: 66}, {label: 'hot-plug', labelWidth: 76, kind: 'data'}]}
      title="Managed Cluster persistent storage"
    />
  );
}

export function OutboundTrafficDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="Private workload traffic leaves through the Project router, which applies source NAT using the Project gateway EIP before forwarding to the permitted upstream network."
      description="A virtual machine or Pod with a private Project address sends outbound traffic to the Project VPC router. The router applies source NAT using the default gateway EIP. The translated traffic then reaches the Internet when platform egress policy and upstream networking allow it."
      diagramId="outbound-traffic-explainer"
      sectionLabel="DEFAULT PROJECT EGRESS"
      steps={[
        {title: 'VM or Pod', detail: '10.0.0.x', icon: ComputeIcon},
        {title: 'Project router', detail: 'VPC routing', icon: NetworkIcon},
        {title: 'Gateway EIP', detail: 'SNAT source', icon: PublicIcon, tone: 'accent'},
        {title: 'Upstream', detail: ['Internet when', 'policy permits'], tone: 'external'},
      ]}
      relationships={[{}, {label: 'SNAT', labelWidth: 58, kind: 'data'}, {}]}
      title="Project outbound traffic through SNAT"
    />
  );
}

export function GatewayIngressDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="DNS brings the client to the shared Envoy listener, where TLS terminates and an HTTPRoute selects the Project Service and Pod by hostname."
      description="A client resolves the configured application hostname in DNS and connects to the shared Envoy Gateway on port 443. Envoy terminates TLS using the Project certificate, a Gateway API HTTPRoute matches the hostname, and the selected backend Service forwards to the Pod."
      diagramId="gateway-ingress-explainer"
      sectionLabel="HOSTNAME-BASED HTTPS INGRESS"
      steps={[
        {title: 'Client', detail: 'HTTPS', icon: UsersIcon, tone: 'external'},
        {title: 'DNS', detail: 'app hostname'},
        {title: 'Envoy', detail: [':443', 'TLS'], icon: NetworkIcon, tone: 'accent'},
        {title: 'HTTPRoute', detail: ['hostname', 'match']},
        {title: 'Service', detail: 'backend'},
        {title: 'Pod', detail: 'application'},
      ]}
      relationships={[{}, {}, {}, {}, {}]}
      title="Gateway Route request path"
    />
  );
}

export function LoadBalancerIngressDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="A dedicated EIP receives declared TCP or UDP ports and the OVN load balancer forwards them to the selected Pod or virtual-machine endpoint."
      description="An external client connects to a dedicated external IP on a declared TCP or UDP port. Kube-OVN load-balancer rules associated with the LoadBalancer Service translate and forward that traffic to the selected Pod or virtual-machine endpoint."
      diagramId="loadbalancer-ingress-explainer"
      sectionLabel="DEDICATED-IP SERVICE INGRESS"
      steps={[
        {title: 'Client', detail: 'TCP or UDP', icon: UsersIcon, tone: 'external'},
        {title: 'Dedicated EIP', detail: 'declared ports', icon: PublicIcon, tone: 'accent'},
        {title: 'OVN load balancer', detail: 'Service rules'},
        {title: 'Pod or VM', detail: ['selected', 'endpoint'], icon: ComputeIcon},
      ]}
      relationships={[{}, {kind: 'data'}, {}]}
      title="EIP and LoadBalancer ingress"
    />
  );
}

export function TeamAuthenticationDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="The Organization realm authenticates the user and emits group claims; the API validates that token, then Project RoleBindings authorize individual Kubernetes requests."
      description="A user authenticates against the Organization-specific Keycloak realm using OIDC. The realm issues a JSON Web Token containing Organization group claims. The Kubernetes API validates the token, and RoleBindings in each authorized Project backing namespace map those groups to the applicable Project roles."
      diagramId="team-authentication-explainer"
      sectionLabel="AUTHENTICATION THEN AUTHORIZATION"
      steps={[
        {title: 'User', detail: 'sign in', icon: UsersIcon, tone: 'external'},
        {title: 'Org realm', detail: ['Keycloak', 'OIDC token'], icon: KeycloakIcon, tone: 'accent'},
        {title: 'Kubernetes API', detail: 'validates JWT', icon: ApiIcon},
        {title: 'RoleBindings', detail: ['group claims', 'Project access']},
      ]}
      relationships={[{label: 'OIDC', labelWidth: 54}, {label: 'JWT', labelWidth: 48, kind: 'control'}, {kind: 'control'}]}
      title="Organization authentication and Project authorization"
    />
  );
}

export function VlanAllocationDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="Platform administrators allocate a physical segment to an Organization; Organization administrators then attach, detach, and later reattach that allocation to one of their Projects."
      description="A platform administrator allocates VLAN 4014 to Organization acme. An Organization administrator assigns the allocated segment to Project production. The Organization administrator can later unassign that Project attachment and assign the same Organization-owned segment to Project staging."
      diagramId="vlan-allocation-explainer"
      sectionLabel="PHYSICAL SEGMENT DELEGATION"
      steps={[
        {title: 'Platform admin', detail: 'allocates', icon: UsersIcon, tone: 'external'},
        {title: 'VLAN 4014', detail: ['physical', 'segment'], icon: NetworkIcon},
        {title: 'Organization', detail: ['acme owns', 'allocation'], tone: 'accent'},
        {title: 'Project attachment', detail: ['production', 'reversible']},
      ]}
      relationships={[{kind: 'control'}, {}, {label: 'assign', labelWidth: 62, kind: 'control'}]}
      title="Datacenter VLAN allocation and assignment"
    />
  );
}
