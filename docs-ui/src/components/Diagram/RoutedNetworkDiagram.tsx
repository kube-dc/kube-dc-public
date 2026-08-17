import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import PublicIcon from '../../../../docs/diagrams/icons/network-public.svg';
import {
  DiagramBoundary,
  DiagramCallout,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export default function RoutedNetworkDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="A whole Project VPC—not a second workload interface—reaches only operator-approved remote prefixes through redundant managed gateways. BGP remains platform-managed, while the Project's default Internet path stays unchanged."
      description="Workloads in Project production use VPC 10.0.0.0/24. The Project VPC router sends approved remote destinations through two managed routing gateway replicas in active and standby mode. Those gateways exchange the Project CIDR and approved prefix 198.51.100.0/24 over eBGP with an external router or firewall. Other traffic continues through the Project's existing default gateway to the Internet. If no managed gateway has a healthy approved route, remote-destination traffic fails closed instead of falling through to the Internet path."
      diagramId="routed-network-explainer"
      minWidth={900}
      textScale={0.92}
      title="Project VPC connected to an approved remote network with BGP"
      viewBox="0 0 920 520"
    >
      <DiagramEdge d="M200 164 H230" kind="data" />
      <DiagramEdge d="M410 164 H465" kind="data" />
      <DiagramEdge
        bidirectional
        d="M645 140 H715"
        kind="control"
        label="eBGP"
        labelWidth={54}
        labelX={680}
        labelY={112}
      />
      <DiagramEdge d="M645 184 H715" kind="data" />
      <DiagramEdge
        d="M800 208 V280"
        kind="data"
        label="approved route"
        labelWidth={108}
        labelX={800}
        labelY={252}
      />
      <DiagramEdge d="M320 208 V280" kind="asynchronous" />
      <DiagramEdge d="M410 324 H465" kind="data" />
      <DiagramSectionLabel label="PROJECT VPC TO AN APPROVED REMOTE NETWORK" lineTo={892} x={28} y={28} />
      <DiagramBoundary height={190} kind="network" label="PROJECT PRODUCTION · VPC 10.0.0.0/24" labelWidth={340} width={410} x={20} y={60} />
      <DiagramBoundary height={190} label="KUBE-DC ROUTING" labelWidth={145} width={215} x={450} y={60} />
      <DiagramBoundary height={350} kind="network" label="ROUTED DOMAIN" labelWidth={105} width={200} x={700} y={60} />
      <DiagramNode detail="Pods + VMs" height={88} icon={ApplicationIcon} title="Workloads" width={160} x={40} y={120} />
      <DiagramNode detail={['approved routes', 'default separate']} height={88} icon={NetworkIcon} title="VPC router" tone="accent" width={180} x={230} y={120} />
      <DiagramNode detail={['two replicas', 'active · standby']} height={88} icon={NetworkIcon} title="BGP pair" width={180} x={465} y={120} />
      <DiagramNode detail={['router / FW', 'managed policy']} height={88} icon={NetworkIcon} title="Edge peer" tone="external" width={170} x={715} y={120} />
      <DiagramNode detail="198.51.100.0/24" height={88} icon={NetworkIcon} title="Remote CIDR" tone="external" width={170} x={715} y={280} />
      <DiagramNode detail="existing gateway" height={88} icon={PublicIcon} title="Default path" width={180} x={230} y={280} />
      <DiagramNode detail="SNAT unchanged" height={88} icon={PublicIcon} title="Internet" tone="external" width={180} x={465} y={280} />
      <DiagramCallout
        detail="Approved remote destinations fail closed; other traffic keeps the default Internet path."
        height={64}
        title="Separate paths by destination"
        width={840}
        x={40}
        y={430}
      />
    </ExplainerDiagram>
  );
}
