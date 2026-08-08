import FloatingIpIcon from '../../../../docs/diagrams/icons/floating-ip.svg';
import PublicNetworkIcon from '../../../../docs/diagrams/icons/network-public.svg';
import VirtualMachineIcon from '../../../../docs/diagrams/icons/virtual-machine.svg';
import {
  DiagramBoundary,
  DiagramCallout,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export default function FloatingIpToVmDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The Floating IP remains a platform resource. OVN maps it bidirectionally to the VM's existing private interface; the public address is not configured inside the guest."
      description="External traffic reaches Floating IP ubuntu-fip with example public address 203.0.113.10 in Organization acme and Project production. Bidirectional one-to-one NAT across all ports maps that address to private address 10.0.0.153 on VM ubuntu."
      diagramId="fip-to-vm-explainer"
      minWidth={840}
      title="Floating IP to virtual machine relationship"
      viewBox="0 0 840 410"
    >
      <DiagramEdge bidirectional d="M190 190 H260" kind="data" />
      <DiagramEdge
        bidirectional
        d="M480 190 H550"
        kind="data"
        label="1:1 NAT · all ports"
        labelWidth={146}
        labelX={515}
        labelY={165}
      />

      <DiagramSectionLabel label="ROUTED EXTERNAL TRAFFIC" lineTo={812} x={28} y={28} />
      <DiagramBoundary
        height={260}
        label="ORGANIZATION: ACME"
        labelWidth={190}
        width={620}
        x={200}
        y={62}
      />
      <DiagramBoundary
        height={150}
        kind="network"
        label="PROJECT: PRODUCTION · BACKING NAMESPACE ACME-PRODUCTION"
        labelWidth={450}
        width={560}
        x={230}
        y={112}
      />

      <DiagramNode
        detail="routed"
        height={82}
        icon={PublicNetworkIcon}
        title="External"
        tone="external"
        width={170}
        x={20}
        y={149}
      />
      <DiagramNode
        badge="PUBLIC"
        detail="203.0.113.10"
        height={82}
        icon={FloatingIpIcon}
        title="FIP ubuntu-fip"
        tone="accent"
        width={220}
        x={260}
        y={149}
      />
      <DiagramNode
        detail={["10.0.0.153", "vpc_net_0"]}
        height={82}
        icon={VirtualMachineIcon}
        title="VM ubuntu"
        width={220}
        x={550}
        y={149}
      />

      <DiagramCallout
        detail="The VM keeps its private address; firewall controls still apply."
        height={68}
        title="The public address is mapped at the platform edge, not configured in the guest."
        width={760}
        x={40}
        y={330}
      />
    </ExplainerDiagram>
  );
}
