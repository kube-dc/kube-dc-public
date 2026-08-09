import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import CloudIcon from '../../../../docs/diagrams/icons/cloud.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import PublicNetworkIcon from '../../../../docs/diagrams/icons/network-public.svg';
import {
  DiagramBoundary,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export function PhysicalNetworkDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Provider networks bind installation-specific private or public segments to eligible-node OVS bridges; Kube-OVN owns the logical attachment and routing."
      description="Optional private and public provider segments converge on a Kube-OVN ProviderNetwork. The ProviderNetwork attaches to an OVS bridge on eligible nodes, and OVS connects those segments to OVN logical routers."
      diagramId="physical-network-explainer"
      minWidth={840}
      title="External provider network attachment"
      viewBox="0 0 840 330"
    >
      <DiagramEdge d="M250 112 C270 112 270 165 300 165" kind="data" />
      <DiagramEdge d="M250 238 C270 238 270 165 300 165" kind="data" />
      <DiagramEdge d="M490 165 H510" kind="data" />
      <DiagramEdge d="M690 165 H710" kind="data" />
      <DiagramSectionLabel label="DATACENTER OR CLOUD FABRIC" lineTo={812} x={28} y={28} />
      <DiagramNode detail={["optional private", "VLAN / routed"]} height={82} icon={CloudIcon} title="Cloud segment" tone="external" width={220} x={30} y={71} />
      <DiagramNode detail={["optional public", "routed pool"]} height={82} icon={PublicNetworkIcon} title="Public segment" tone="external" width={220} x={30} y={197} />
      <DiagramNode detail="Kube-OVN" height={82} title="ProviderNetwork" tone="accent" width={190} x={300} y={124} />
      <DiagramNode detail={["eligible nodes", "bridge attachment"]} height={82} title="OVS bridge" width={180} x={510} y={124} />
      <DiagramNode detail="OVN" height={82} title="Routers" width={110} x={710} y={124} />
    </ExplainerDiagram>
  );
}

export function OvnLogicalNetworkDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Each Project has its own VPC and creator-supplied workload subnet. Its default EIP and SNAT use the selected external network; public EIPs, FIPs, and Services are optional explicit paths."
      description="Pods and virtual machines attach to the Project workload subnet and Project logical router inside the Project VPC. The router uses the configured cloud network for the default EIP and outbound SNAT. When configured, an optional public EIP, Floating IP, or LoadBalancer Service can use the public external network. Platform Pods and Services remain in the separate management VPC."
      diagramId="ovn-logical-network-explainer"
      minWidth={840}
      title="Management and Project network isolation"
      viewBox="0 0 840 510"
    >
      <DiagramEdge d="M195 258 H215" />
      <DiagramEdge d="M365 258 H385" />
      <DiagramEdge d="M575 238 C600 238 600 182 625 182" kind="data" label="default EIP + SNAT" labelWidth={136} labelX={530} labelY={190} />
      <DiagramEdge d="M575 278 C600 278 600 342 625 342" kind="asynchronous" label="optional public path" labelWidth={146} labelX={530} labelY={326} />
      <DiagramBoundary height={310} kind="network" label="PROJECT VPC · ISOLATED WORKLOAD NETWORK" labelWidth={350} width={560} x={20} y={130} />
      <DiagramBoundary height={410} label="MANAGEMENT VPC" labelWidth={180} width={220} x={610} y={50} />
      <DiagramNode detail="Pods + VMs" height={78} icon={ApplicationIcon} title="Workloads" width={160} x={35} y={219} />
      <DiagramNode detail="spec.cidrBlock" height={78} title="Project subnet" width={150} x={215} y={219} />
      <DiagramNode detail="controlled routing" height={78} title="Logical router" width={190} x={385} y={219} />
      <DiagramNode detail={["platform Pods", "Services"]} height={82} title="Platform network" width={190} x={625} y={82} />
      <DiagramNode detail={["ext-cloud", "configured"]} height={82} icon={CloudIcon} title="Cloud network" width={190} x={625} y={182} />
      <DiagramNode detail={["ext-public", "optional"]} height={82} icon={PublicNetworkIcon} title="Public network" width={190} x={625} y={302} />
    </ExplainerDiagram>
  );
}

export function EnvoyGatewayDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Envoy Gateway performs hostname-based routing only after DNS and the provider path make its listener reachable. Route, certificate, Service, and workload health remain separate checks."
      description="A client reaches the Envoy Gateway listener through DNS and a reachable provider path. Envoy matches a Gateway API hostname and route, forwards to a Kubernetes Service, and the Service sends the request to a Project workload."
      diagramId="envoy-gateway-explainer"
      minWidth={900}
      title="Envoy Gateway request flow"
      viewBox="0 0 900 280"
    >
      <DiagramEdge d="M170 132 H190" label="DNS + provider path" labelWidth={138} labelX={180} labelY={72} />
      <DiagramEdge d="M380 132 H400" label="hostname match" labelWidth={110} labelX={390} labelY={72} />
      <DiagramEdge d="M560 132 H580" />
      <DiagramEdge d="M740 132 H760" />
      <DiagramSectionLabel label="HOSTNAME-BASED APPLICATION TRAFFIC" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['external', 'request']} height={82} title="Client" tone="external" width={150} x={20} y={91} />
      <DiagramNode detail={['HTTP · HTTPS', 'gRPC']} height={82} title="Envoy listener" tone="accent" width={190} x={190} y={91} />
      <DiagramNode detail="Gateway API" height={82} icon={NetworkIcon} title="Route" width={160} x={400} y={91} />
      <DiagramNode detail={['cluster', 'service']} height={82} title="Service" width={160} x={580} y={91} />
      <DiagramNode detail="Project" height={82} title="Workload" width={120} x={760} y={91} />
    </ExplainerDiagram>
  );
}
