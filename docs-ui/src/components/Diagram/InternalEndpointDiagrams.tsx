import React from 'react';

import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import BareMetalIcon from '../../../../docs/diagrams/icons/bare-metal-server.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import GitIcon from '../../../../docs/diagrams/icons/git-repository.svg';
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

export function PlatformHairpinDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Without the feature, a Project Pod sends platform traffic to the public address and an upstream 1:1 NAT router drops it. With the feature, Project DNS returns an internal VIP on ext-cloud and the traffic stays inside the cluster."
      description="Two request paths from a Project Pod to the platform front door. In the first path, the Pod resolves login or kube-api to the public IP. The packet reaches an upstream 1:1 NAT router, which does not send it back into the cluster, so the connection times out. In the second path, the Project vpc-dns resolver returns an internal VIP on the ext-cloud network. The packet reaches the VIP, and kube-proxy translates it to Envoy on port 443 or the Kubernetes API server on port 6443. Public DNS and clients outside the cluster continue to use the public address."
      diagramId="internal-endpoint-hairpin"
      minWidth={900}
      title="Why Project Pods need an internal path"
      viewBox="0 0 900 430"
    >
      <DiagramEdge d="M240 101 H360" kind="data" label="public IP" labelWidth={96} labelX={300} labelY={106} />
      <DiagramEdge d="M600 101 H720" kind="data" label="dropped" labelWidth={80} labelX={660} labelY={106} tone="danger" />
      <DiagramEdge d="M240 263 H360" kind="data" label="VIP" labelWidth={52} labelX={300} labelY={268} />
      <DiagramEdge d="M600 263 H720" kind="data" label="DNAT" labelWidth={60} labelX={660} labelY={268} />
      <DiagramSectionLabel label="WITHOUT THE FEATURE · PUBLIC ADDRESS" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['login.<domain>', 'public answer']} height={82} icon={ApplicationIcon} title="Project Pod" width={210} x={30} y={60} />
      <DiagramNode detail={['public IP', 'no hairpin turn']} height={82} icon={PublicIcon} title="Upstream 1:1 NAT" tone="external" width={240} x={360} y={60} />
      <DiagramNode detail={['Envoy :443', 'API :6443']} height={82} title="Front door" width={150} x={720} y={60} />
      <DiagramSectionLabel label="WITH THE FEATURE · INTERNAL VIP" lineTo={872} x={28} y={190} />
      <DiagramNode detail={['login.<domain>', 'VIP answer']} height={82} icon={ApplicationIcon} title="Project Pod" width={210} x={30} y={222} />
      <DiagramNode detail={['ext-cloud /32', 'MetalLB']} height={82} icon={NetworkIcon} title="Internal VIP" tone="accent" width={240} x={360} y={222} />
      <DiagramNode detail={['Envoy :443', 'API :6443']} height={82} title="Front door" width={150} x={720} y={222} />
      <DiagramCallout detail="Public DNS and clients outside the cluster still use the public address." height={64} title="Only Project DNS changes" width={840} x={30} y={340} />
    </ExplainerDiagram>
  );
}

export function EndpointDecisionDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Run the classifier first. Act on its verdict when the confidence is high and infra attachment is off; otherwise the smoke test from a Project Pod decides."
      description="The operator runs kube-dc bootstrap doctor topology. A Class A result with high confidence means the feature is required. A Class B or Class C result with high confidence, on an installation without infra attachment, means the feature is not needed. A medium-confidence or ambiguous result, or any installation with infra attachment on, means the operator runs the smoke test from an ordinary Pod in a Project namespace, because the classifier cannot detect Class D. A timeout after six seconds means Class A or Class D, so the feature is required. Any HTTP answer, from 2xx to 4xx, means Class B or C, so the feature stays off."
      diagramId="internal-endpoint-decision"
      minWidth={900}
      title="Decide whether a cluster needs internal platform endpoints"
      viewBox="0 0 900 560"
    >
      <DiagramEdge d="M450 142 C450 176 165 176 165 198 V210" kind="control" />
      <DiagramEdge d="M450 142 V210" kind="control" />
      <DiagramEdge d="M450 142 C450 176 735 176 735 198 V210" kind="control" />
      <DiagramEdge d="M450 292 C450 326 295 326 295 348 V360" kind="control" />
      <DiagramEdge d="M450 292 C450 326 605 326 605 348 V360" kind="control" />
      <DiagramEdge d="M170 401 H140 V292" kind="control" />
      <DiagramEdge d="M730 401 H760 V292" kind="control" />
      <DiagramSectionLabel label="CLASSIFY, THEN DECIDE" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['kube-dc bootstrap', 'doctor topology']} height={82} icon={ControllerIcon} title="Classifier" tone="accent" width={260} x={320} y={60} />
      <DiagramNode detail={['Class A or D', 'verdict: required']} height={82} title="Enable the feature" width={250} x={40} y={210} />
      <DiagramNode detail={['medium, ambiguous,', 'or infra attachment']} height={82} title="Run the smoke test" width={250} x={325} y={210} />
      <DiagramNode detail={['Class B or C, high', 'no infra attachment']} height={82} title="Leave it off" width={250} x={610} y={210} />
      <DiagramNode detail={['no answer in 6 s', 'Class A or D']} height={82} title="Timeout" tone="external" width={250} x={170} y={360} />
      <DiagramNode detail={['any 2xx to 4xx', 'Class B or C']} height={82} title="HTTP answer" width={250} x={480} y={360} />
      <DiagramCallout detail="Run it from an ordinary Project Pod. The classifier cannot detect Class D." height={64} title="The smoke test is authoritative" width={840} x={30} y={470} />
    </ExplainerDiagram>
  );
}

export function DualHomedPodDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The longest matching prefix chooses the interface. An address inside the infra routes goes out through eth0, where the infra-lock SecurityGroup blocks ordinary Pods. Any other address goes out through net1 and the Project router."
      description="A Project Pod with infra attachment has two interfaces. eth0 is on the shared infra-net subnet and carries only the infra routes: the node subnet, the join subnet, and the Pod CIDR. Its per-Project infra-lock SecurityGroup allows replies and exact grants only, and ordinary workloads have no grant to the front door, so a connection to a front-door VIP on the node network is blocked. That is Class D. net1 is on the Project VPC and carries the default route. A connection to the internal VIP on the ext-cloud network goes through the Project router and reaches Envoy on port 443."
      diagramId="internal-endpoint-dual-homed"
      minWidth={900}
      title="Which interface a Project Pod uses for the front door"
      viewBox="0 0 900 580"
    >
      <DiagramEdge d="M450 142 C450 176 190 176 190 198 V210" kind="data" label="infra routes" labelWidth={112} labelX={300} labelY={182} />
      <DiagramEdge d="M450 142 C450 176 710 176 710 198 V210" kind="data" label="default route" labelWidth={120} labelX={600} labelY={182} />
      <DiagramEdge d="M190 292 V340" kind="data" />
      <DiagramEdge d="M190 422 V470" kind="data" label="blocked" labelWidth={80} labelX={190} labelY={451} tone="danger" />
      <DiagramEdge d="M710 292 V340" kind="data" />
      <DiagramEdge d="M710 422 V470" kind="data" label="DNAT" labelWidth={60} labelX={710} labelY={451} />
      <DiagramSectionLabel label="LONGEST PREFIX CHOOSES THE INTERFACE" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['eth0: infra routes', 'net1: default route']} height={82} icon={ApplicationIcon} title="Project Pod" width={260} x={320} y={60} />
      <DiagramNode detail={['infra-net subnet', 'node, join, Pod CIDR']} height={82} icon={NetworkIcon} title="eth0" width={320} x={30} y={210} />
      <DiagramNode detail={['reply-only + grants', 'no front-door grant']} height={82} title="infra-lock SecurityGroup" tone="muted" width={320} x={30} y={340} />
      <DiagramNode detail={['on the node network', 'Class D: timeout']} height={82} icon={PublicIcon} title="Front-door VIP" tone="external" width={320} x={30} y={470} />
      <DiagramNode detail={['Project VPC subnet', 'router, SNAT, allowlists']} height={82} icon={NetworkIcon} title="net1" width={320} x={550} y={210} />
      <DiagramNode detail={['envoyGateway endpoint', 'on ext-cloud']} height={82} icon={NetworkIcon} title="Internal VIP" tone="accent" width={320} x={550} y={340} />
      <DiagramNode detail={['Envoy :443', 'reached']} height={82} title="Front door" width={320} x={550} y={470} />
    </ExplainerDiagram>
  );
}

export function InternalEndpointDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="vpc-dns sends the Project Pod to the internal VIP. The Project router passes the VIP because it is on both global allowlists. kube-proxy on the announcing node forwards only to the backend on that same node."
      description="A Project Pod asks its per-Project vpc-dns resolver for a platform hostname and receives the internal VIP. The Pod sends the connection through its Project router, which permits the VIP through the ingress and egress global allowlists and source-translates the traffic onto the ext-cloud network. The VIP lives on one gateway node, where the elected MetalLB speaker announces it from the node's anchor IP on br-ext-cloud. Because the Service uses externalTrafficPolicy Local, kube-proxy on that node translates the connection only to the local Envoy on port 443 or the local Kubernetes API server on port 6443. The traffic never crosses to another node."
      diagramId="internal-endpoint-explainer"
      minWidth={900}
      title="Request path through an internal platform endpoint"
      viewBox="0 0 900 760"
    >
      <DiagramEdge bidirectional d="M580 133 H660" kind="data" label="DNS" labelWidth={44} labelX={620} labelY={138} />
      <DiagramEdge d="M450 174 V206" kind="data" />
      <DiagramEdge d="M450 288 V380" kind="data" label="to VIP" labelWidth={70} labelX={450} labelY={320} />
      <DiagramEdge d="M290 421 H320" kind="control" />
      <DiagramEdge d="M450 462 V500" kind="data" />
      <DiagramEdge d="M450 582 V620" kind="data" />
      <DiagramBoundary height={250} kind="network" label="PROJECT VPC" labelWidth={150} width={860} x={20} y={48} />
      <DiagramNode detail={['login.<domain>', 'kube-api.<domain>']} height={82} icon={ApplicationIcon} title="Project Pod" width={260} x={320} y={92} />
      <DiagramNode detail={['hosts block', 'name to VIP']} height={82} title="vpc-dns" width={200} x={660} y={92} />
      <DiagramNode detail={['allowlist 32000/29500', 'SNAT to ext-cloud']} height={82} icon={NetworkIcon} title="Project router" width={260} x={320} y={206} />
      <DiagramBoundary height={400} kind="network" label="ANNOUNCING GATEWAY NODE" labelWidth={260} width={860} x={20} y={330} />
      <DiagramNode detail={['host IP on bridge', 'GARP source']} height={82} title="Anchor IP" tone="muted" width={250} x={40} y={380} />
      <DiagramNode detail={['MetalLB speaker', 'on br-ext-cloud']} height={82} icon={NetworkIcon} title="Internal VIP" tone="accent" width={260} x={320} y={380} />
      <DiagramNode detail={['policy: Local', 'DNAT on this node']} height={82} icon={NetworkIcon} title="kube-proxy" width={260} x={320} y={500} />
      <DiagramCallout detail="Traffic stays on this node." height={64} title="externalTrafficPolicy: Local" width={270} x={600} y={509} />
      <DiagramNode detail={['Envoy :443', 'or apiserver :6443']} height={82} icon={ApiIcon} title="Local backend" width={260} x={320} y={620} />
    </ExplainerDiagram>
  );
}

export function EndpointMembershipDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The manager is the only writer of the EndpointSlice. kube-proxy and MetalLB both read it, so a backend that fails its probes stops receiving traffic and stops attracting the VIP."
      description="kube-dc-manager lists the Nodes that match the Service's backend node selector and probes each node's InternalIP every five seconds with a three-second timeout. It writes the healthy addresses, each with its node name and marked ready, into the EndpointSlice named after the Service with the suffix -mgr. kube-proxy reads the slice and translates traffic to the endpoint on its own node. MetalLB reads the same slice and announces the VIP only from a node that has a ready local endpoint. Membership rules: one successful probe adds a backend; two consecutive failures remove it; when every backend fails, the manager keeps the last good endpoints that are still candidates; when no Node matches the selector, the manager empties the slice."
      diagramId="internal-endpoint-membership"
      minWidth={900}
      title="How the manager keeps the backend set healthy"
      viewBox="0 0 900 600"
    >
      <DiagramEdge d="M340 101 H250" kind="control" label="probe" labelWidth={60} labelX={295} labelY={106} />
      <DiagramEdge d="M560 101 H650" kind="control" label="writes" labelWidth={66} labelX={605} labelY={106} />
      <DiagramEdge d="M760 142 C760 186 510 186 510 218 V230" kind="asynchronous" />
      <DiagramEdge d="M760 142 V230" kind="asynchronous" />
      <DiagramSectionLabel label="PROBE · WRITE · CONSUME" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['nodeSelector match', 'InternalIP']} height={82} icon={BareMetalIcon} title="Candidate nodes" width={220} x={30} y={60} />
      <DiagramNode detail={['probe every 5 s', 'timeout 3 s']} height={82} icon={ControllerIcon} title="kube-dc-manager" tone="accent" width={220} x={340} y={60} />
      <DiagramNode detail={['<name>-mgr', 'ready + nodeName']} height={82} title="EndpointSlice" width={220} x={650} y={60} />
      <DiagramNode detail={['DNAT to the', 'local endpoint']} height={82} icon={NetworkIcon} title="kube-proxy" width={220} x={400} y={230} />
      <DiagramNode detail={['announces only', 'where one is ready']} height={82} title="MetalLB speaker" width={220} x={650} y={230} />
      <DiagramSectionLabel label="MEMBERSHIP RULES" lineTo={872} x={28} y={360} />
      <DiagramNode detail={['1 success:', 'added to the slice']} height={82} title="Healthy" width={270} x={30} y={390} />
      <DiagramNode detail={['2 failures in a row:', 'removed']} height={82} title="Unhealthy" width={270} x={315} y={390} />
      <DiagramNode detail={['keep the last good', 'candidate endpoints']} height={82} title="All unhealthy" width={270} x={600} y={390} />
      <DiagramCallout detail="The manager empties the slice, so a selector that matches no Node is visible at once." height={64} title="No candidate nodes" tone="warning" width={840} x={30} y={500} />
    </ExplainerDiagram>
  );
}

export function EndpointOwnershipDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="One set of cluster-config.env values feeds four independent mechanisms. Project Pods reach the VIP only when all four agree on the same addresses."
      description="The Fleet cluster-config.env file holds the VIPs, the feature switches, the global allowlists, and the anchor IPs. The kube-dc Helm chart renders the MetalLB IPAddressPool, the L2Advertisement or BGPAdvertisement, and the selectorless Service; kube-dc-manager then probes the backend nodes and writes the EndpointSlice. A Fleet patch on the vpc-dns-corefile ConfigMap feeds every per-Project vpc-dns resolver. The global allowlists are written by the manager into every Project router at priorities 32000 and 29500. The kube-dc CLI command bootstrap anchors apply installs a systemd unit on each gateway node that binds the anchor IP on br-ext-cloud."
      diagramId="internal-endpoint-ownership"
      minWidth={900}
      title="Who configures each part of the feature"
      viewBox="0 0 900 590"
    >
      <DiagramEdge d="M450 142 C450 176 120 176 120 198 V210" kind="control" />
      <DiagramEdge d="M450 142 C450 176 340 176 340 198 V210" kind="control" />
      <DiagramEdge d="M450 142 C450 176 560 176 560 198 V210" kind="control" />
      <DiagramEdge d="M450 142 C450 176 780 176 780 198 V210" kind="control" />
      <DiagramEdge d="M120 292 V370" kind="asynchronous" />
      <DiagramEdge d="M340 292 V370" kind="asynchronous" />
      <DiagramEdge d="M560 292 V370" kind="asynchronous" />
      <DiagramEdge d="M780 292 V370" kind="asynchronous" />
      <DiagramSectionLabel label="DESIRED STATE · RENDERED BY · RUNS AS" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['VIPs, switches,', 'allowlists, anchors']} height={82} icon={GitIcon} title="cluster-config.env" tone="source" width={300} x={300} y={60} />
      <DiagramNode detail={['pool, advertisement', 'selectorless Service']} height={82} title="kube-dc chart" width={200} x={20} y={210} />
      <DiagramNode detail={['Fleet patch on', 'vpc-dns-corefile']} height={82} title="vpc-dns Corefile" width={200} x={240} y={210} />
      <DiagramNode detail={['INGRESS + EGRESS', 'global allowlist']} height={82} title="Allowlists" width={200} x={460} y={210} />
      <DiagramNode detail={['anchors apply', 'systemd on gw nodes']} height={82} title="kube-dc CLI" width={200} x={680} y={210} />
      <DiagramNode detail={['probes nodes,', 'writes <name>-mgr']} height={82} title="kube-dc-manager" tone="accent" width={200} x={20} y={370} />
      <DiagramNode detail={['vpc-dns-<project>', 'per Project']} height={82} title="Resolvers" width={200} x={240} y={370} />
      <DiagramNode detail={['manager writes', '32000 + 29500']} height={82} title="Project routers" width={200} x={460} y={370} />
      <DiagramNode detail={['host IP on', 'br-ext-cloud']} height={82} title="Anchor IP" width={200} x={680} y={370} />
      <DiagramCallout detail="Use the same VIP values in all four columns. A mismatch in one column breaks the path." height={64} title="Keep the four columns in step" width={840} x={30} y={490} />
    </ExplainerDiagram>
  );
}
