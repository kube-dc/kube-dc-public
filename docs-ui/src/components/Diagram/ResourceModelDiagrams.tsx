import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import KeycloakIcon from '../../../../docs/diagrams/icons/keycloak.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import UsersIcon from '../../../../docs/diagrams/icons/users.svg';
import {
  DiagramBoundary,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export function CloudResourceModelDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="An Organization supplies the identity, membership, billing, and shared-quota boundary; each Project is a separate working boundary containing namespaced workloads and optional Managed Clusters."
      description="An Organization contains Projects. A Project maps to a backing namespace and can contain applications and namespaced Kubernetes resources, virtual machines, networking, storage, managed services, and Managed Clusters with a separate Kubernetes API."
      diagramId="cloud-resource-model-explainer"
      minWidth={840}
      title="Kube-DC cloud resource model"
      viewBox="0 0 840 410"
    >
      <DiagramEdge d="M420 126 V174" />
      <DiagramEdge d="M420 256 C420 278 120 278 120 306 V318" />
      <DiagramEdge d="M420 256 C420 278 320 278 320 306 V318" />
      <DiagramEdge d="M420 256 C420 278 540 278 540 306 V318" />
      <DiagramEdge d="M420 256 C420 278 740 278 740 306 V318" />

      <DiagramSectionLabel label="GOVERNANCE" lineTo={812} x={28} y={28} />
      <DiagramNode detail={["identity · membership", "billing · shared quota"]} height={86} title="Organization" tone="accent" width={300} x={270} y={40} />
      <DiagramNode detail="working boundary · backing namespace" height={82} title="Project" tone="accent" width={360} x={240} y={174} />
      <DiagramSectionLabel label="PROJECT CAPABILITIES" lineTo={812} x={28} y={294} />
      <DiagramNode detail={['namespaced', 'resources']} height={72} icon={ApplicationIcon} title="Applications" width={180} x={30} y={318} />
      <DiagramNode detail="KubeVirt" height={72} title="Virtual machines" width={180} x={230} y={318} />
      <DiagramNode detail={['network · storage', 'managed services']} height={72} title="Platform services" width={200} x={430} y={318} />
      <DiagramNode detail="separate API" height={72} title="Managed Clusters" width={180} x={650} y={318} />
    </ExplainerDiagram>
  );
}

export function IdentityTenancyDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Identity claims establish API access, while Organization Groups map roles independently into each Project; workloads and Managed Clusters remain governed by their authorized Project."
      description="A user signs in through Keycloak and presents group claims to the Kubernetes API. An Organization contains an Organization Group and production and development Projects. The group maps a role into each Project, and each Project contains virtual machines, Pods, databases, and Managed Clusters."
      diagramId="identity-tenancy-explainer"
      minWidth={840}
      title="Kube-DC identity and tenancy model"
      viewBox="0 0 840 610"
    >
      <DiagramEdge d="M220 94 H330" label="signs in" labelWidth={78} labelX={275} labelY={72} />
      <DiagramEdge d="M510 94 H610" label="group claims" labelWidth={104} labelX={560} labelY={72} />

      <DiagramEdge d="M420 236 C420 258 145 258 145 286 V298" />
      <DiagramEdge d="M420 236 V298" />
      <DiagramEdge d="M420 236 C420 258 695 258 695 286 V298" />
      <DiagramEdge d="M420 370 C420 394 145 394 145 418 V430" kind="control" label="role in production" labelWidth={132} labelX={275} labelY={399} />
      <DiagramEdge d="M420 370 C420 394 695 394 695 418 V430" kind="control" label="role in development" labelWidth={142} labelX={565} labelY={399} />
      <DiagramEdge d="M145 502 V530" />
      <DiagramEdge d="M695 502 V530" />

      <DiagramSectionLabel label="IDENTITY PATH" lineTo={812} x={28} y={28} />
      <DiagramNode detail={['organization', 'member']} height={82} icon={UsersIcon} title="User" tone="external" width={190} x={30} y={60} />
      <DiagramNode detail="OIDC identity" height={68} icon={KeycloakIcon} title="Keycloak" width={180} x={330} y={60} />
      <DiagramNode detail="authorization" height={68} icon={ApiIcon} title="Kubernetes API" width={200} x={610} y={60} />

      <DiagramBoundary height={400} label="ORGANIZATION · GOVERNANCE BOUNDARY" labelWidth={330} width={800} x={20} y={170} />
      <DiagramNode detail="contains Groups + Projects" height={66} title="Organization" tone="accent" width={260} x={290} y={190} />
      <DiagramNode detail="membership mapping" height={72} title="Organization Group" width={240} x={300} y={298} />
      <DiagramNode detail="backing namespace" height={72} title="Project: production" tone="accent" width={250} x={20} y={430} />
      <DiagramNode detail="backing namespace" height={72} title="Project: development" width={250} x={570} y={430} />
      <DiagramNode detail={['VMs · Pods · databases', 'Managed Clusters']} height={72} icon={ApplicationIcon} title="Governed workloads" width={250} x={20} y={530} />
      <DiagramNode detail={['VMs · Pods · databases', 'Managed Clusters']} height={72} icon={ApplicationIcon} title="Governed workloads" width={250} x={570} y={530} />
    </ExplainerDiagram>
  );
}

export function VirtualizationResourceDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="KubeVirt reconciles the VM runtime, CDI provisions the persistent disk, and Kube-OVN attaches the VM to its Project VPC; all branches originate from the Project VirtualMachine resource."
      description="A user creates a VirtualMachine through the console, kubectl, or virtctl. KubeVirt controllers reconcile the VirtualMachine and VirtualMachineInstance, which runs in a virt-launcher Pod using QEMU and the guest operating system. The VirtualMachine also references a CDI DataVolume backed by a PersistentVolumeClaim and the Project default network connected to the Project VPC."
      diagramId="virtualization-resource-explainer"
      minWidth={840}
      title="Kube-DC virtual machine resource flow"
      viewBox="0 0 840 620"
    >
      <DiagramEdge d="M250 94 H300" />
      <DiagramEdge d="M540 94 H590" kind="control" />
      <DiagramEdge d="M700 128 C700 158 440 160 440 188 V200" kind="control" />
      <DiagramEdge d="M440 272 C440 294 140 294 140 318 V330" />
      <DiagramEdge d="M440 272 V330" />
      <DiagramEdge d="M440 272 C440 294 700 294 700 318 V330" />
      <DiagramEdge d="M140 402 V442" />
      <DiagramEdge d="M140 514 V542" />
      <DiagramEdge d="M440 402 V442" />
      <DiagramEdge d="M700 402 V442" />
      <DiagramEdge d="M260 479 H320" kind="control" />

      <DiagramSectionLabel label="RESOURCE ENTRY" lineTo={812} x={28} y={28} />
      <DiagramNode detail={['console · kubectl', 'virtctl']} height={82} icon={UsersIcon} title="User" tone="external" width={220} x={30} y={60} />
      <DiagramNode detail="runtime reconciler" height={68} icon={ControllerIcon} title="KubeVirt controllers" width={240} x={300} y={60} />
      <DiagramNode detail="disk provisioner" height={68} icon={DataIcon} title="CDI" width={220} x={590} y={60} />
      <DiagramNode detail="Project VM specification" height={72} icon={ComputeIcon} title="VirtualMachine" tone="accent" width={280} x={300} y={200} />

      <DiagramBoundary height={305} label="RUNTIME" labelWidth={110} width={240} x={20} y={305} />
      <DiagramBoundary height={305} label="STORAGE" labelWidth={110} width={240} x={320} y={305} />
      <DiagramBoundary height={305} kind="network" label="NETWORK" labelWidth={110} width={240} x={580} y={305} />
      <DiagramNode detail="KubeVirt runtime" height={72} title="VirtualMachineInstance" width={220} x={30} y={330} />
      <DiagramNode detail="VM process Pod" height={72} title="virt-launcher Pod" width={220} x={30} y={442} />
      <DiagramNode detail="QEMU + guest OS" height={58} title="Guest runtime" width={220} x={30} y={542} />
      <DiagramNode detail="CDI source" height={72} title="DataVolume" width={220} x={330} y={330} />
      <DiagramNode detail="persistent disk" height={72} title="PersistentVolumeClaim" width={220} x={330} y={442} />
      <DiagramNode detail="NAD" height={72} title="Project default network" width={220} x={590} y={330} />
      <DiagramNode detail="isolated routing" height={72} title="Project VPC" width={220} x={590} y={442} />
    </ExplainerDiagram>
  );
}

export function InstallationNetworkDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The management network carries node and platform traffic; optional provider networks are separate data paths that Kube-OVN connects to each Project VPC and its workloads."
      description="The installation management network carries node, Kubernetes API, etcd, and SSH traffic. Optional ext-cloud and ext-public provider networks attach to Kube-OVN. Kube-OVN connects the selected provider paths to an isolated Project VPC and workload subnet containing Pods, virtual machines, and Managed Cluster workers."
      diagramId="installation-network-explainer"
      minWidth={840}
      title="Kube-DC installation network model"
      viewBox="0 0 840 350"
    >
      <DiagramEdge d="M250 112 C285 112 285 175 330 175" kind="control" />
      <DiagramEdge d="M250 238 C285 238 285 175 330 175" kind="data" />
      <DiagramEdge d="M500 175 H520" />
      <DiagramEdge d="M680 175 H700" />
      <DiagramSectionLabel label="INSTALLATION NETWORK PATHS" lineTo={812} x={28} y={28} />
      <DiagramNode detail={['node · API · etcd', 'SSH traffic']} height={82} title="Management network" width={230} x={20} y={71} />
      <DiagramNode detail={['ext-cloud · ext-public', 'optional']} height={82} title="Provider networks" tone="external" width={230} x={20} y={197} />
      <DiagramNode detail="network control" height={82} title="Kube-OVN" tone="accent" width={170} x={330} y={134} />
      <DiagramNode detail="VPC + subnet" height={82} title="Project network" width={160} x={520} y={134} />
      <DiagramNode detail={['Pods · VMs', 'workers']} height={82} title="Workloads" width={120} x={700} y={134} />
    </ExplainerDiagram>
  );
}
