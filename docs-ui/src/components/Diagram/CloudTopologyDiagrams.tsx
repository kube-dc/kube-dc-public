import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import GitIcon from '../../../../docs/diagrams/icons/git-repository.svg';
import KubernetesIcon from '../../../../docs/diagrams/icons/kubernetes.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import PublicIcon from '../../../../docs/diagrams/icons/network-public.svg';
import UsersIcon from '../../../../docs/diagrams/icons/users.svg';
import {
  DiagramBoundary,
  DiagramCallout,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export function GitOpsProjectMappingDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The external delivery platform holds an independently scoped credential for each target Project; each credential is constrained to that Project's generated backing namespace."
      description="One external CI or GitOps delivery platform targets three Kube-DC Projects. The development credential maps to acme-development, the staging credential maps to acme-staging, and the production credential maps to acme-production. No target is granted cluster-admin or cross-namespace access."
      diagramId="gitops-project-mapping-explainer"
      minWidth={840}
      title="External GitOps controller Project mapping"
      viewBox="0 0 840 380"
    >
      <DiagramEdge d="M420 130 C420 166 145 166 145 202 V214" kind="control" />
      <DiagramEdge d="M420 130 V214" kind="control" />
      <DiagramEdge d="M420 130 C420 166 695 166 695 202 V214" kind="control" />
      <DiagramSectionLabel label="EXPLICIT PROJECT-SCOPED TARGETS" lineTo={812} x={28} y={28} />
      <DiagramNode detail="external CI / GitOps" height={78} icon={GitIcon} title="Delivery platform" tone="source" width={300} x={270} y={52} />
      <DiagramNode detail="acme-development" height={82} title="Project: development" width={250} x={20} y={214} />
      <DiagramNode detail="acme-staging" height={82} title="Project: staging" width={250} x={295} y={214} />
      <DiagramNode detail="acme-production" height={82} title="Project: production" tone="accent" width={250} x={570} y={214} />
      <DiagramCallout detail="One scoped credential per target; reject cluster-scoped objects." height={64} title="Automation boundary" width={620} x={110} y={306} />
    </ExplainerDiagram>
  );
}

export function VlanWorkloadTopologyDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The Project VPC interface retains the default route; the secondary net1 interface carries only traffic for the attached physical VLAN and its datacenter devices."
      description="A Pod or virtual machine in the acme-production backing namespace has two network interfaces. eth0 uses address 10.0.0.15/24 on the Project VPC and remains the default route. net1 uses address 192.0.2.30/24 on datacenter VLAN 4014 and has no default route. The tagged VLAN reaches a storage array at 192.0.2.10 and an appliance at 192.0.2.11."
      diagramId="vlan-workload-topology-explainer"
      minWidth={840}
      title="Project workload with a datacenter VLAN"
      viewBox="0 0 840 470"
    >
      <DiagramEdge d="M420 146 C420 176 210 176 210 204 V216" />
      <DiagramEdge d="M420 146 C420 176 630 176 630 204 V216" kind="data" />
      <DiagramEdge d="M630 298 V340" kind="data" />
      <DiagramBoundary height={265} label="PROJECT · ACME-PRODUCTION" labelWidth={260} width={800} x={20} y={50} />
      <DiagramNode detail="Pod or VM" height={78} icon={ApplicationIcon} title="Workload" tone="accent" width={280} x={280} y={68} />
      <DiagramNode detail={['eth0 · 10.0.0.15/24', 'DEFAULT ROUTE']} height={82} icon={NetworkIcon} title="Project VPC" width={300} x={60} y={216} />
      <DiagramNode detail={['net1 · 192.0.2.30/24', 'no default route']} height={82} icon={NetworkIcon} title="VLAN interface" width={300} x={480} y={216} />
      <DiagramNode detail={['802.1Q VLAN 4014', 'storage · appliance']} height={92} title="Physical segment" tone="external" width={420} x={420} y={340} />
    </ExplainerDiagram>
  );
}

export function ProjectPrivateNetworkDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="All Project workloads draw private addresses from the Project subnet; the VPC router applies Project routing and SNAT before traffic reaches the selected cloud or public gateway EIP."
      description="Project production maps to backing namespace acme-production and contains subnet 10.0.0.0/24. Virtual machines ubuntu and debian and Pod nginx receive private addresses from that subnet. The subnet attaches to the Project VPC router. The router applies source NAT to the default gateway EIP on the configured cloud or public external network."
      diagramId="project-private-network-explainer"
      minWidth={840}
      title="Project VPC and default gateway"
      viewBox="0 0 840 500"
    >
      <DiagramEdge d="M200 230 C230 230 230 330 280 330" />
      <DiagramEdge d="M420 230 V294" />
      <DiagramEdge d="M640 230 C610 230 610 330 560 330" />
      <DiagramEdge d="M420 396 V418" kind="data" />
      <DiagramBoundary height={400} kind="network" label="PROJECT PRODUCTION · VPC 10.0.0.0/24" labelWidth={390} width={800} x={20} y={50} />
      <DiagramNode detail="10.0.0.10" height={72} icon={ComputeIcon} title="VM: ubuntu" width={180} x={20} y={194} />
      <DiagramNode detail="10.0.0.11" height={72} icon={ComputeIcon} title="VM: debian" width={180} x={330} y={194} />
      <DiagramNode detail="10.0.0.20" height={72} icon={ApplicationIcon} title="Pod: nginx" width={180} x={640} y={194} />
      <DiagramNode detail="SNAT · Project routing" height={102} icon={NetworkIcon} title="VPC router" tone="accent" width={280} x={280} y={294} />
      <DiagramNode detail="cloud or public" height={72} icon={PublicIcon} title="Default gateway EIP" width={280} x={280} y={418} />
    </ExplainerDiagram>
  );
}

export function ManagedClusterTopologyDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="KdcCluster owns a Kamaji control plane, datastore, worker MachineDeployment, and CCM inside the Project infrastructure namespace; users reach a separate tenant Kubernetes API and run workloads on its workers."
      description="In the acme-production Project infrastructure namespace, KdcCluster dev owns a LoadBalancer Service for the API, a Kamaji TenantControlPlane with API server, scheduler, and controller-manager Pods, a KdcClusterDatastore backed by an etcd StatefulSet, a worker MachineDeployment backed by KubeVirt virtual machines, and a CCM Deployment. Together these resources create Managed Cluster dev with three workers and a separate Kubernetes API for tenant Deployments, Services, and persistent volume claims."
      diagramId="managed-cluster-topology-explainer"
      minWidth={900}
      textScale={1.04}
      title="KubeVirt-backed Managed Cluster topology"
      viewBox="0 0 900 720"
    >
      <DiagramEdge d="M450 162 C450 190 140 190 140 218 V230" />
      <DiagramEdge d="M450 162 C450 190 350 190 350 218 V230" />
      <DiagramEdge d="M450 162 C450 190 560 190 560 218 V230" />
      <DiagramEdge d="M450 162 C450 190 760 190 760 218 V230" />
      <DiagramEdge d="M350 312 V350" />
      <DiagramEdge d="M560 312 V350" />
      <DiagramEdge d="M450 442 V500" />
      <DiagramBoundary height={450} label="MANAGEMENT CLUSTER · ACME-PRODUCTION" labelWidth={410} width={860} x={20} y={50} />
      <DiagramNode detail="dev" height={72} icon={KubernetesIcon} title="KdcCluster" tone="accent" width={260} x={320} y={90} />
      <DiagramNode detail={['dev-cp', 'LoadBalancer API']} height={82} title="Control-plane Service" width={220} x={30} y={230} />
      <DiagramNode detail="Kamaji" height={82} title="TenantControlPlane" width={200} x={250} y={230} />
      <DiagramNode detail="dev-etcd" height={82} icon={DataIcon} title="Datastore" width={200} x={460} y={230} />
      <DiagramNode detail="kccm-dev" height={82} title="CCM Deployment" width={200} x={680} y={230} />
      <DiagramNode detail={['API server · scheduler', 'controller-manager Pods']} height={92} icon={ControllerIcon} title="Control-plane Pods" width={300} x={200} y={350} />
      <DiagramNode detail={['etcd StatefulSet', 'persistent storage']} height={92} icon={DataIcon} title="etcd cluster" tone="storage" width={300} x={510} y={350} />
      <DiagramBoundary height={170} kind="network" label="MANAGED CLUSTER DEV · SEPARATE API" labelWidth={350} width={860} x={20} y={520} />
      <DiagramNode detail={['worker-1 · worker-2', 'worker-3 · KubeVirt VMs']} height={92} icon={ComputeIcon} title="Worker MachineDeployment" width={360} x={60} y={560} />
      <DiagramNode detail={['Deployments · Services', 'PVCs']} height={92} icon={ApplicationIcon} title="Tenant workloads" width={360} x={480} y={560} />
    </ExplainerDiagram>
  );
}

export function ExposureConflictDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="This is an unsupported mixed-gateway combination: a public FIP forces the workload's return path toward the public gateway while the cloud LoadBalancer expects the cloud gateway."
      description="One Pod or virtual machine at internal address 10.0.0.30 is simultaneously selected by a public Floating IP and a cloud-network LoadBalancer Service. The public FIP creates source-based routing toward public gateway 198.51.100.1, while the cloud LoadBalancer expects return traffic through cloud gateway 100.65.0.1. The conflicting asymmetric paths break the service."
      diagramId="exposure-conflict-explainer"
      minWidth={840}
      title="Public FIP and cloud LoadBalancer conflict"
      viewBox="0 0 840 390"
    >
      <DiagramEdge d="M420 152 C420 184 210 184 210 212 V224" kind="data" />
      <DiagramEdge d="M420 152 C420 184 630 184 630 212 V224" kind="control" />
      <DiagramSectionLabel label="UNSUPPORTED ASYMMETRIC RETURN PATHS" lineTo={812} x={28} y={28} />
      <DiagramNode detail="10.0.0.30" height={82} icon={ApplicationIcon} title="Pod or VM" tone="accent" width={280} x={280} y={70} />
      <DiagramNode detail={['public FIP', 'gateway 198.51.100.1']} height={92} icon={PublicIcon} title="Public path" width={340} x={40} y={224} />
      <DiagramNode detail={['cloud LoadBalancer', 'gateway 100.65.0.1']} height={92} icon={NetworkIcon} title="Cloud path" width={340} x={460} y={224} />
      <DiagramCallout detail="Choose one exposure method or keep both paths on the same network type." height={58} title="BROKEN: conflicting source routing" tone="danger" width={720} x={60} y={326} />
    </ExplainerDiagram>
  );
}

export function ExposureDecisionDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Choose by traffic shape: hostname-based web traffic uses a Gateway Route, direct VM access uses a Floating IP, and arbitrary declared TCP or UDP ports use an EIP-backed LoadBalancer."
      description="The exposure decision starts with the workload and protocol. A web application or API uses an HTTPS Gateway Route with automatic hostname and TLS after Issuer setup. Direct SSH or RDP access to a virtual machine uses a dedicated EIP and Floating IP mapping. A custom TCP, UDP, or gRPC service uses a dedicated EIP and LoadBalancer Service with explicit ports. Multiple Services that intentionally share one external address can use the default gateway EIP with distinct ports where the platform supports that pattern."
      diagramId="exposure-decision-explainer"
      minWidth={900}
      title="Choose a Kube-DC exposure method"
      viewBox="0 0 900 470"
    >
      <DiagramEdge d="M450 138 C450 174 130 174 130 212 V224" />
      <DiagramEdge d="M450 138 C450 174 345 174 345 212 V224" />
      <DiagramEdge d="M450 138 C450 174 565 174 565 212 V224" />
      <DiagramEdge d="M450 138 C450 174 780 174 780 212 V224" />
      <DiagramSectionLabel label="WHAT ARE YOU EXPOSING?" lineTo={872} x={28} y={28} />
      <DiagramNode detail="workload + protocol" height={78} icon={UsersIcon} title="Exposure need" tone="accent" width={300} x={300} y={60} />
      <DiagramNode detail={['web app / API', 'HTTPS + TLS']} height={92} icon={ApplicationIcon} title="Gateway Route" width={200} x={30} y={224} />
      <DiagramNode detail={['VM direct access', 'EIP + FIP']} height={92} icon={ComputeIcon} title="Floating IP" width={200} x={245} y={224} />
      <DiagramNode detail={['TCP / UDP', 'EIP + LB']} height={92} icon={NetworkIcon} title="LoadBalancer" width={210} x={460} y={224} />
      <DiagramNode detail={['multiple Services', 'shared EIP · ports']} height={92} title="Shared gateway" width={200} x={690} y={224} />
      <DiagramCallout detail="Do not combine public-FIP and cloud-LoadBalancer return paths on one endpoint." height={64} title="Keep network type and return path consistent" tone="warning" width={720} x={90} y={356} />
    </ExplainerDiagram>
  );
}

export function OrganizationGroupLifecycleDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The OrganizationGroup controller keeps the Organization-realm group and all selected Project RoleBindings aligned across create, update, and delete reconciliation."
      description="Creating an OrganizationGroup creates a Keycloak group in the Organization realm and RoleBindings for every Project and role in spec.permissions; new tokens that carry the group claim receive that Project access. Updating the resource reconciles RoleBindings across affected Project backing namespaces. Deleting the resource removes the Keycloak group and all associated RoleBindings."
      diagramId="organization-group-lifecycle-explainer"
      minWidth={840}
      title="OrganizationGroup controller lifecycle"
      viewBox="0 0 840 560"
    >
      <DiagramEdge d="M210 170 H315" kind="control" />
      <DiagramEdge d="M525 150 H630" />
      <DiagramEdge d="M525 190 H630" />
      <DiagramEdge d="M210 300 H315" kind="control" />
      <DiagramEdge d="M525 300 H630" />
      <DiagramEdge d="M210 430 H315" kind="control" />
      <DiagramEdge d="M525 410 H630" />
      <DiagramEdge d="M525 450 H630" />
      <DiagramSectionLabel label="CREATE · UPDATE · DELETE RECONCILIATION" lineTo={812} x={28} y={28} />
      <DiagramNode detail="OrganizationGroup" height={82} title="Create" tone="source" width={180} x={30} y={129} />
      <DiagramNode detail="controller" height={82} icon={ControllerIcon} title="Reconcile" tone="accent" width={210} x={315} y={129} />
      <DiagramNode detail={['Keycloak group', 'Project bindings']} height={102} title="Create outputs" width={180} x={630} y={119} />
      <DiagramNode detail={['permissions', 'changed']} height={82} title="Update" tone="source" width={180} x={30} y={259} />
      <DiagramNode detail="controller" height={82} icon={ControllerIcon} title="Reconcile" tone="accent" width={210} x={315} y={259} />
      <DiagramNode detail={['affected', 'RoleBindings']} height={82} title="Update outputs" width={180} x={630} y={259} />
      <DiagramNode detail="OrganizationGroup" height={82} title="Delete" tone="source" width={180} x={30} y={389} />
      <DiagramNode detail={['final', 'reconciliation']} height={82} icon={ControllerIcon} title="Reconcile" tone="accent" width={210} x={315} y={389} />
      <DiagramNode detail={['delete Keycloak', 'delete bindings']} height={102} title="Delete outputs" width={180} x={630} y={379} />
    </ExplainerDiagram>
  );
}
