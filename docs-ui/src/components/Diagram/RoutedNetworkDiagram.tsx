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
      description="Workloads in Project production use VPC 10.0.0.0/24. The Project VPC router sends approved remote destinations through two managed active and standby routing gateways. Those gateways exchange the Project CIDR and approved prefix 198.51.100.0/24 over eBGP with an external router or firewall. Other traffic continues through the Project's existing default gateway to the Internet. If no managed gateway has a healthy approved route, remote-destination traffic fails closed instead of falling through to the Internet path."
      diagramId="routed-network-explainer"
      minWidth={900}
      title="Project VPC connected to an approved remote network with BGP"
      viewBox="0 0 900 520"
    >
      <DiagramEdge d="M210 164 H250" kind="data" />
      <DiagramEdge d="M420 164 H480" kind="data" />
      <DiagramEdge
        bidirectional
        d="M660 140 H710"
        kind="control"
        label="eBGP"
        labelWidth={54}
        labelX={685}
        labelY={108}
      />
      <DiagramEdge d="M660 184 H710" kind="data" />
      <DiagramEdge
        d="M795 208 V280"
        kind="data"
        label="approved prefix"
        labelWidth={116}
        labelX={795}
        labelY={252}
      />
      <DiagramEdge d="M335 208 V280" kind="asynchronous" />
      <DiagramEdge d="M420 324 H480" kind="data" />
      <DiagramSectionLabel label="PROJECT VPC TO AN APPROVED REMOTE NETWORK" lineTo={872} x={28} y={28} />
      <DiagramBoundary height={190} kind="network" label="PROJECT PRODUCTION · VPC 10.0.0.0/24" labelWidth={350} width={420} x={20} y={60} />
      <DiagramBoundary height={190} label="KUBE-DC ROUTING" labelWidth={150} width={220} x={460} y={60} />
      <DiagramBoundary height={350} kind="network" label="ROUTED DOMAIN" labelWidth={110} width={190} x={690} y={60} />
      <DiagramNode detail="Pods + VMs" height={88} icon={ApplicationIcon} title="Workloads" width={170} x={40} y={120} />
      <DiagramNode detail={['approved routes', 'default separate']} height={88} icon={NetworkIcon} title="VPC router" tone="accent" width={170} x={250} y={120} />
      <DiagramNode detail={['two replicas', 'active / standby']} height={88} icon={NetworkIcon} title="BGP gateways" width={180} x={480} y={120} />
      <DiagramNode detail={['router / firewall', 'managed peer']} height={88} icon={NetworkIcon} title="Edge peer" tone="external" width={170} x={710} y={120} />
      <DiagramNode detail="198.51.100.0/24" height={88} icon={NetworkIcon} title="Remote network" tone="external" width={170} x={710} y={280} />
      <DiagramNode detail="existing path" height={88} icon={PublicIcon} title="Default gateway" width={170} x={250} y={280} />
      <DiagramNode detail="SNAT unchanged" height={88} icon={PublicIcon} title="Internet" tone="external" width={180} x={480} y={280} />
      <DiagramCallout
        detail="Approved remote destinations fail closed; other traffic keeps the default Internet path."
        height={64}
        title="Separate paths by destination"
        width={820}
        x={40}
        y={430}
      />
    </ExplainerDiagram>
  );
}
