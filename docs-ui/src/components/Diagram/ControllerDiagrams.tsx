import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import FluxIcon from '../../../../docs/diagrams/icons/flux.svg';
import GitIcon from '../../../../docs/diagrams/icons/git-repository.svg';
import KeycloakIcon from '../../../../docs/diagrams/icons/keycloak.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import ObservabilityIcon from '../../../../docs/diagrams/icons/observability.svg';
import SecurityIcon from '../../../../docs/diagrams/icons/security.svg';
import UsersIcon from '../../../../docs/diagrams/icons/users.svg';
import {
  DiagramBoundary,
  DiagramEdge,
  DiagramNode,
  ExplainerDiagram,
} from './index';

export function ControllerTopologyDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Fleet and Flux deliver desired state to the Kubernetes API; resource-specific Kube-DC controllers then reconcile identity, networking, virtualization, security, endpoints, and observability integrations."
      description="The kube-dc CLI updates the Fleet repository, Flux reconciles it through the Kubernetes API, and Kubernetes resources are watched by Organization and Project, network, security, and platform endpoint controllers. Product controllers integrate with Keycloak, Kube-OVN, KubeVirt, storage, and observability. Network controllers integrate with Kube-OVN. Security controllers integrate with OpenBao, Envoy Gateway, cert-manager, and observability. Endpoint controllers maintain core Kubernetes resources."
      diagramId="controller-topology-explainer"
      minWidth={1000}
      textScale={1.12}
      title="Kube-DC controller topology"
      viewBox="0 0 1000 780"
    >
      <DiagramEdge d="M210 92 H250" kind="control" />
      <DiagramEdge d="M480 92 H530" kind="control" />
      <DiagramEdge d="M730 92 H780" kind="control" />

      <DiagramEdge d="M280 235 H370" />
      <DiagramEdge d="M280 365 H370" />
      <DiagramEdge d="M280 495 H370" />
      <DiagramEdge d="M280 625 C330 625 330 365 370 365" />
      <DiagramEdge d="M280 625 H370" />

      <DiagramEdge d="M620 225 C660 225 660 225 700 225" kind="control" />
      <DiagramEdge d="M620 245 C660 245 660 405 700 405" kind="control" />
      <DiagramEdge d="M620 255 C650 255 650 495 700 495" kind="control" />
      <DiagramEdge d="M620 265 C640 265 640 675 700 675" kind="control" />
      <DiagramEdge d="M620 365 C660 365 660 405 700 405" kind="control" />
      <DiagramEdge d="M620 495 C650 495 650 315 700 315" kind="control" />
      <DiagramEdge d="M620 505 C660 505 660 585 700 585" kind="control" />
      <DiagramEdge d="M620 515 C640 515 640 675 700 675" kind="control" />
      <DiagramEdge d="M370 625 C330 625 330 655 280 655" kind="control" />

      <DiagramNode detail={['installation', 'command']} height={82} icon={UsersIcon} title="kube-dc CLI" tone="external" width={180} x={30} y={51} />
      <DiagramNode detail="desired state" height={64} icon={GitIcon} title="Fleet repository" tone="source" width={230} x={250} y={60} />
      <DiagramNode detail={['GitOps', 'reconciliation']} height={82} icon={FluxIcon} title="Flux" width={200} x={530} y={51} />
      <DiagramNode detail="resource API" height={72} title="Kubernetes API" tone="accent" width={190} x={780} y={56} />

      <DiagramBoundary height={600} label="KUBERNETES RESOURCES" labelWidth={230} width={280} x={20} y={150} />
      <DiagramBoundary height={600} label="KUBE-DC MANAGER" labelWidth={205} width={290} x={350} y={150} />
      <DiagramBoundary height={600} label="PLATFORM SYSTEMS" labelWidth={205} width={300} x={680} y={150} />

      <DiagramNode detail={['Organization · Project', 'OrganizationGroup']} height={82} title="Product CRDs" width={250} x={30} y={194} />
      <DiagramNode detail="EIp · FIp" height={72} icon={NetworkIcon} title="Network CRDs" width={250} x={30} y={329} />
      <DiagramNode detail={['ManagedSecret · KMSKey', 'certificate · DB policy']} height={88} title="Security CRDs" width={250} x={30} y={451} />
      <DiagramNode detail={['Service · Secret', 'ConfigMap · Namespace']} height={82} title="Core resources" width={250} x={30} y={590} />

      <DiagramNode detail={['Organization +', 'Project']} height={82} icon={ControllerIcon} title="Product controllers" tone="accent" width={250} x={370} y={194} />
      <DiagramNode detail={['external IP', 'lifecycle']} height={82} icon={ControllerIcon} title="Network controllers" tone="accent" width={250} x={370} y={324} />
      <DiagramNode detail={['secrets +', 'certificates']} height={82} icon={ControllerIcon} title="Security controllers" tone="accent" width={250} x={370} y={454} />
      <DiagramNode detail={['selectorless', 'endpoints']} height={82} icon={ControllerIcon} title="Endpoint controllers" tone="accent" width={250} x={370} y={584} />

      <DiagramNode detail="identity" height={62} icon={KeycloakIcon} title="Keycloak" width={270} x={700} y={194} />
      <DiagramNode detail="secret backend" height={62} icon={SecurityIcon} title="OpenBao" width={270} x={700} y={284} />
      <DiagramNode detail="network + Multus" height={62} icon={NetworkIcon} title="Kube-OVN" width={270} x={700} y={374} />
      <DiagramNode detail="CDI + storage" height={62} icon={ComputeIcon} title="KubeVirt" width={270} x={700} y={464} />
      <DiagramNode detail="Envoy + cert-manager" height={62} icon={NetworkIcon} title="Gateway services" width={270} x={700} y={554} />
      <DiagramNode detail={['Grafana · metrics', 'logs']} height={82} icon={ObservabilityIcon} title="Observability" width={270} x={700} y={634} />
    </ExplainerDiagram>
  );
}

export function ProjectLifecycleDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Reconciliation proceeds in three operator-visible phases: establish the Organization and identity state, create all Project boundaries and integrations, then materialize group-based Project RoleBindings."
      description="A platform administrator creates an Organization through the Kubernetes API. Product controllers reconcile its Keycloak realm and groups and publish Organization status. An Organization administrator then creates a Project. Product controllers create its backing namespace, hierarchy, RBAC, trust resources, VPC, subnet, network attachment, SNAT, router policies, optional backup and image resources, observability scope, and Project status. Finally, an OrganizationGroup is reconciled into a Keycloak membership target and Project RoleBindings."
      diagramId="project-lifecycle-explainer"
      minWidth={1000}
      textScale={1.12}
      title="Project lifecycle reconciliation"
      viewBox="0 0 1000 970"
    >
      <DiagramEdge d="M210 136 H250" label="create Organization" labelWidth={142} labelX={230} labelY={82} />
      <DiagramEdge d="M440 136 H470" label="reconcile" labelWidth={80} labelX={455} labelY={82} />
      <DiagramEdge d="M700 136 H750" label="realm + groups" labelWidth={112} labelX={725} labelY={82} />
      <DiagramEdge d="M585 177 C585 220 345 220 345 184 V172" kind="control" label="publish status" labelWidth={108} labelX={465} labelY={216} />

      <DiagramEdge d="M210 406 H250" label="create Project" labelWidth={108} labelX={230} labelY={350} />
      <DiagramEdge d="M440 406 H470" label="reconcile" labelWidth={80} labelX={455} labelY={350} />
      <DiagramEdge d="M585 447 C585 475 115 475 115 508 V520" label="namespace · RBAC · trust" labelWidth={168} labelX={350} labelY={475} />
      <DiagramEdge d="M585 447 C585 475 300 475 300 508 V520" label="VPC · subnet · policies" labelWidth={158} labelX={445} labelY={502} />
      <DiagramEdge d="M585 447 C585 475 485 475 485 508 V520" label="backup + images" labelWidth={124} labelX={535} labelY={475} />
      <DiagramEdge d="M585 447 C585 475 670 475 670 508 V520" label="datasource scope" labelWidth={128} labelX={630} labelY={502} />
      <DiagramEdge d="M585 447 C585 475 855 475 855 508 V520" kind="control" label="publish status" labelWidth={108} labelX={720} labelY={475} />

      <DiagramEdge d="M210 756 H250" label="create OrgGroup" labelWidth={128} labelX={230} labelY={700} />
      <DiagramEdge d="M440 756 H470" label="reconcile" labelWidth={80} labelX={455} labelY={700} />
      <DiagramEdge d="M585 797 C585 825 730 825 730 863 V875" label="sync identity target" labelWidth={142} labelX={655} labelY={825} />
      <DiagramEdge d="M585 797 C585 845 900 845 900 863 V875" kind="control" label="create RoleBindings" labelWidth={142} labelX={740} labelY={852} />

      <DiagramBoundary height={250} label="1 · ORGANIZATION SETUP" labelWidth={230} width={960} x={20} y={30} />
      <DiagramNode detail="actor" height={72} title="Platform admin" tone="external" width={180} x={30} y={100} />
      <DiagramNode detail="Organization API" height={72} title="Kubernetes API" width={190} x={250} y={100} />
      <DiagramNode detail={['Organization', 'reconcile']} height={82} title="Product controllers" tone="accent" width={230} x={470} y={95} />
      <DiagramNode detail="realm + groups" height={72} icon={KeycloakIcon} title="Keycloak" width={200} x={750} y={100} />

      <DiagramBoundary height={330} label="2 · PROJECT SETUP" labelWidth={190} width={960} x={20} y={300} />
      <DiagramNode detail="actor" height={72} title="Org admin" tone="external" width={180} x={30} y={370} />
      <DiagramNode detail="Project API" height={72} title="Kubernetes API" width={190} x={250} y={370} />
      <DiagramNode detail={['Project', 'reconcile']} height={82} title="Product controllers" tone="accent" width={230} x={470} y={365} />
      <DiagramNode detail={['namespace +', 'access']} height={82} title="API resources" width={170} x={30} y={520} />
      <DiagramNode detail={['Kube-OVN +', 'Multus']} height={82} title="Networking" width={170} x={215} y={520} />
      <DiagramNode detail={['KubeVirt +', 'storage']} height={82} title="Virtualization" width={170} x={400} y={520} />
      <DiagramNode detail={['datasource', 'scope']} height={82} title="Observability" width={170} x={585} y={520} />
      <DiagramNode detail={['Project', 'conditions']} height={82} title="Project status" width={170} x={770} y={520} />

      <DiagramBoundary height={310} label="3 · ACCESS MAPPING" labelWidth={205} width={960} x={20} y={650} />
      <DiagramNode detail="actor" height={72} title="Org admin" tone="external" width={180} x={30} y={720} />
      <DiagramNode detail={['OrganizationGroup', 'API']} height={82} title="Kubernetes API" width={190} x={250} y={715} />
      <DiagramNode detail={['access', 'reconcile']} height={82} title="Product controllers" tone="accent" width={230} x={470} y={715} />
      <DiagramNode detail={['membership', 'target']} height={82} title="Keycloak" width={160} x={650} y={875} />
      <DiagramNode detail="per Project" height={82} title="RoleBindings" width={160} x={820} y={875} />
    </ExplainerDiagram>
  );
}

export function ControllerNetworkFlowDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Three paths share the Project VPC but remain distinct: default outbound SNAT, Service LoadBalancer publication, and one-to-one Floating IP translation."
      description="Project virtual machines and Pods connect to the Project VPC and subnet, which uses outbound SNAT to the public or cloud external subnet. A LoadBalancer Service is synchronized by the service load-balancer controller through an EIp allocation and OVN load balancer. A FIp resource is synchronized through an EIp allocation and OVN floating-IP rules. External clients reach the external subnet, which forwards through the appropriate OVN rule to the Service or workload."
      diagramId="controller-network-flow-explainer"
      minWidth={1000}
      textScale={1.23}
      title="Project workload network flow"
      viewBox="0 0 1100 720"
    >
      <DiagramEdge d="M240 218 H300" />
      <DiagramEdge d="M560 218 H630" />
      <DiagramEdge d="M850 218 C880 218 990 260 990 290 V302" />

      <DiagramEdge d="M240 378 H300" />
      <DiagramEdge d="M560 378 C590 378 590 518 630 518" />
      <DiagramEdge d="M740 482 V414" />
      <DiagramEdge d="M990 214 V302" />
      <DiagramEdge d="M990 374 C990 430 870 430 870 320 H740 V342" />
      <DiagramEdge d="M740 414 V450 H270 C250 450 250 320 230 320 H135 V342" />

      <DiagramEdge d="M240 578 H300" />
      <DiagramEdge d="M560 578 C590 578 590 538 630 538" />
      <DiagramEdge d="M740 554 V562" />
      <DiagramEdge d="M990 374 V540 Q990 558 972 558 H740 V562" />
      <DiagramEdge d="M740 634 V670 H270 V160 H135 V182" />

      <DiagramBoundary height={580} label="PROJECT" labelWidth={105} width={240} x={20} y={110} />
      <DiagramBoundary height={580} label="NETWORK CONTROLLERS" labelWidth={225} width={300} x={280} y={110} />
      <DiagramBoundary height={580} kind="network" label="KUBE-OVN" labelWidth={120} width={260} x={610} y={110} />
      <DiagramBoundary height={580} kind="network" label="EXTERNAL" labelWidth={120} width={210} x={880} y={110} />

      <DiagramNode detail="VMs + Pods" height={72} icon={ComputeIcon} title="Workloads" width={210} x={30} y={182} />
      <DiagramNode detail="VPC + subnet" height={72} title="Project network" width={260} x={300} y={182} />
      <DiagramNode detail="default egress" height={72} title="Outbound SNAT" width={220} x={630} y={182} />
      <DiagramNode detail="cloud / public" height={72} title="External subnet" width={200} x={890} y={302} />

      <DiagramNode detail="type LoadBalancer" height={72} title="Service" width={210} x={30} y={342} />
      <DiagramNode detail="Service sync" height={72} icon={ControllerIcon} title="LB controller" width={260} x={300} y={342} />
      <DiagramNode detail="allocation" height={72} title="EIp" width={220} x={630} y={482} />
      <DiagramNode detail="OVN load balancer" height={72} title="Load-balancer rule" width={220} x={630} y={342} />

      <DiagramNode detail="one-to-one NAT" height={72} title="FIp resource" width={210} x={30} y={542} />
      <DiagramNode detail="Floating IP sync" height={72} icon={ControllerIcon} title="FIp controller" width={260} x={300} y={542} />
      <DiagramNode detail="DNAT + SNAT" height={72} title="Floating-IP rules" width={220} x={630} y={562} />
      <DiagramNode detail="ingress source" height={72} title="Clients" width={200} x={890} y={142} />
    </ExplainerDiagram>
  );
}

export function ControllerSecurityFlowDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Each Project-scoped security resource has a dedicated controller and backend path; controller conditions converge separately in Project status without exposing platform credentials."
      description="ManagedSecret, ManagedCertificate, KMSKey, and DatabaseCredentialPolicy resources in a Project backing namespace are handled by dedicated security controllers. Secret sync uses OpenBao and writes a projected Kubernetes Secret. Certificate sync uses OpenBao and cert-manager to create a Certificate. KMS key sync uses OpenBao. Database credential policy sync uses OpenBao and the database manager and writes a projected Secret. All four controllers report to Project status aggregation."
      diagramId="controller-security-flow-explainer"
      minWidth={1000}
      textScale={1.12}
      title="Project secret and certificate flow"
      viewBox="0 0 1000 900"
    >
      <DiagramEdge d="M310 200 H350" />
      <DiagramEdge d="M630 200 C655 200 675 182 700 182" kind="control" />
      <DiagramEdge d="M390 236 C360 250 335 250 335 270 V670 C335 685 100 685 100 700" />

      <DiagramEdge d="M310 330 H350" />
      <DiagramEdge d="M630 330 C665 330 665 200 700 200" kind="control" />
      <DiagramEdge d="M630 350 H700" kind="control" />
      <DiagramEdge d="M930 386 C960 386 975 400 975 430 V780 H240 V800" />

      <DiagramEdge d="M310 460 H350" />
      <DiagramEdge d="M630 460 C675 460 675 218 700 218" kind="control" />

      <DiagramEdge d="M310 590 H350" />
      <DiagramEdge d="M630 580 C680 580 680 232 700 232" kind="control" />
      <DiagramEdge d="M260 626 V650 H680 V610 H700" kind="control" />
      <DiagramEdge d="M490 631 C490 676 170 676 170 688 V700" />

      <DiagramEdge d="M630 200 C650 200 650 830 700 830" kind="asynchronous" />
      <DiagramEdge d="M630 330 C660 330 660 830 700 830" kind="asynchronous" />
      <DiagramEdge d="M630 460 C670 460 670 830 700 830" kind="asynchronous" />
      <DiagramEdge d="M630 590 C680 590 680 830 700 830" kind="asynchronous" />

      <DiagramBoundary height={800} label="PROJECT BACKING NAMESPACE" labelWidth={285} width={310} x={20} y={70} />
      <DiagramBoundary height={800} label="SECURITY CONTROLLERS" labelWidth={225} width={300} x={340} y={70} />
      <DiagramBoundary height={800} label="PLATFORM BACKENDS" labelWidth={205} width={300} x={680} y={70} />

      <DiagramNode detail="source resource" height={72} title="ManagedSecret" width={280} x={30} y={164} />
      <DiagramNode detail="source resource" height={72} title="ManagedCertificate" width={280} x={30} y={294} />
      <DiagramNode detail="source resource" height={72} title="KMSKey" width={280} x={30} y={424} />
      <DiagramNode detail="source resource" height={72} title="DatabaseCredentialPolicy" width={280} x={30} y={554} />
      <DiagramNode detail="ManagedSecret + DB policy" height={72} title="Projected Secret" width={280} x={30} y={700} />
      <DiagramNode detail="cert-manager output" height={72} title="Certificate" width={280} x={30} y={800} />

      <DiagramNode detail="secret reconciliation" height={72} icon={ControllerIcon} title="Secret sync" tone="accent" width={280} x={350} y={164} />
      <DiagramNode detail={['certificate', 'reconciliation']} height={82} icon={ControllerIcon} title="Certificate sync" tone="accent" width={280} x={350} y={289} />
      <DiagramNode detail="key reconciliation" height={72} icon={ControllerIcon} title="KMS key sync" tone="accent" width={280} x={350} y={424} />
      <DiagramNode detail={['credential', 'reconciliation']} height={82} icon={ControllerIcon} title="DB policy sync" tone="accent" width={280} x={350} y={549} />

      <DiagramNode detail="secret + key backend" height={72} icon={SecurityIcon} title="OpenBao" width={270} x={700} y={164} />
      <DiagramNode detail="certificate issuer" height={72} icon={SecurityIcon} title="cert-manager" width={270} x={700} y={314} />
      <DiagramNode detail="credential source" height={72} icon={DataIcon} title="Database manager" width={270} x={700} y={554} />
      <DiagramNode detail="controller conditions" height={72} title="Project status" width={270} x={700} y={794} />
    </ExplainerDiagram>
  );
}

export function PlatformEndpointFlowDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Fleet owns the address pool, listener, and selectorless Service; the endpoint controller probes eligible nodes and maintains its EndpointSlice, while Project clients resolve a stable internal VIP."
      description="The chart and Fleet desired state defines the MetalLB IPAddressPool and advertisement, annotated selectorless LoadBalancer Service, and Envoy or Kubernetes API listener. The Service triggers PlatformEndpointReconciler, which watches the Service and Nodes, probes each eligible node backend, and maintains a companion EndpointSlice attached to the Service. A Managed Cluster or Project workload resolves a vpc-dns hostname rewrite to a stable internal VIP and reaches the same Service."
      diagramId="platform-endpoint-flow-explainer"
      minWidth={1000}
      textScale={1.12}
      title="Platform endpoint reconciliation"
      viewBox="0 0 1000 590"
    >
      <DiagramEdge d="M140 236 V275" kind="control" />
      <DiagramEdge d="M140 395 V357" kind="control" />
      <DiagramEdge d="M250 316 C290 316 290 200 340 200" />
      <DiagramEdge d="M475 236 V280" />
      <DiagramEdge d="M475 352 V400" />
      <DiagramEdge d="M340 436 C300 436 300 336 250 336" kind="control" />
      <DiagramEdge d="M825 236 V280" />
      <DiagramEdge d="M825 352 V400" />
      <DiagramEdge d="M690 436 H630 V376 H270 V336 H250" />

      <DiagramBoundary height={440} label="FLEET DESIRED STATE" labelWidth={215} width={250} x={20} y={90} />
      <DiagramBoundary height={440} label="PLATFORM ENDPOINT RECONCILER" labelWidth={300} width={300} x={320} y={90} />
      <DiagramBoundary height={440} label="PROJECT VPC PATH" labelWidth={190} width={330} x={650} y={90} />

      <DiagramNode detail="pool + advertisement" height={72} title="MetalLB addresses" tone="source" width={220} x={30} y={164} />
      <DiagramNode detail={['selectorless', 'LoadBalancer']} height={82} title="Annotated Service" tone="source" width={220} x={30} y={275} />
      <DiagramNode detail={['Envoy or', 'Kubernetes API']} height={82} title="Gateway listener" tone="source" width={220} x={30} y={395} />
      <DiagramNode detail="Service + Nodes" height={72} icon={ControllerIcon} title="Watch" tone="accent" width={270} x={340} y={164} />
      <DiagramNode detail="eligible node backends" height={72} title="Probe" width={270} x={340} y={280} />
      <DiagramNode detail="companion resource" height={72} title="EndpointSlice" width={270} x={340} y={400} />
      <DiagramNode detail="Managed Cluster / workload" height={72} title="Project client" width={270} x={690} y={164} />
      <DiagramNode detail="hostname rewrite" height={72} icon={NetworkIcon} title="vpc-dns" width={270} x={690} y={280} />
      <DiagramNode detail="stable internal address" height={72} title="Internal VIP" width={270} x={690} y={400} />
    </ExplainerDiagram>
  );
}
