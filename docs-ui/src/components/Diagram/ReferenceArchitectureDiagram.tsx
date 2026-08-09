import BareMetalIcon from '../../../../docs/diagrams/icons/bare-metal-server.svg';
import CloudIcon from '../../../../docs/diagrams/icons/cloud.svg';
import FluxIcon from '../../../../docs/diagrams/icons/flux.svg';
import KubernetesIcon from '../../../../docs/diagrams/icons/kubernetes.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import UsersIcon from '../../../../docs/diagrams/icons/users.svg';
import {
  DiagramBoundary,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export default function ReferenceArchitectureDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Illustrative production topology only. Actual node count, worker capacity, network attachment, and service placement depend on the enabled platform capabilities and workload profile."
      description="An illustrative Kubernetes management cluster contains three RKE2 server nodes forming the control-plane and etcd quorum, a management network, a separate RKE2 worker pool, and Kube-DC platform services. Operators manage the server pool while Fleet and Flux continuously reconcile platform services. Optional provider networks attach to the platform, which programs one VPC and workload subnet per Project and provisions Managed Cluster control planes with separate Kubernetes APIs, authorization, and CNI boundaries."
      diagramId="reference-architecture-explainer"
      minWidth={840}
      title="Illustrative Kube-DC reference architecture"
      viewBox="0 0 840 930"
    >
      <DiagramEdge d="M210 87 H220" kind="control" />
      <DiagramEdge d="M210 191 C225 191 225 625 350 625" kind="asynchronous" />

      <DiagramEdge d="M345 207 V240" directed={false} kind="data" />
      <DiagramEdge d="M520 207 V240" directed={false} kind="data" />
      <DiagramEdge d="M695 207 V240" directed={false} kind="data" />
      <DiagramEdge d="M345 240 H695" directed={false} kind="data" />
      <DiagramEdge d="M520 240 V285" kind="data" />

      <DiagramEdge d="M520 357 V405" directed={false} kind="data" />
      <DiagramEdge d="M345 405 H695" directed={false} kind="data" />
      <DiagramEdge d="M345 405 V435" kind="data" />
      <DiagramEdge d="M520 405 V435" kind="data" />
      <DiagramEdge d="M695 405 V435" kind="data" />
      <DiagramEdge d="M520 517 V600" kind="control" />

      <DiagramEdge bidirectional d="M210 793 C230 793 250 650 350 636" kind="data" />
      <DiagramEdge d="M520 672 C520 705 420 715 420 733 V745" kind="asynchronous" />
      <DiagramEdge d="M520 672 C520 705 680 715 680 733 V745" kind="asynchronous" />
      <DiagramEdge d="M560 786 H580" kind="control" />
      <DiagramEdge d="M420 817 V842" />

      <DiagramSectionLabel label="OPERATIONS AND DESIRED STATE" lineTo={812} x={28} y={28} />
      <DiagramBoundary
        height={660}
        label="KUBERNETES MANAGEMENT CLUSTER · ILLUSTRATIVE PRODUCTION PROFILE"
        labelWidth={550}
        width={600}
        x={220}
        y={48}
      />
      <DiagramBoundary
        height={172}
        label="RKE2 SERVER POOL · CONTROL PLANE AND ETCD QUORUM"
        labelWidth={450}
        width={540}
        x={250}
        y={82}
      />
      <DiagramBoundary
        height={170}
        label="SEPARATE WORKER POOL · CAPACITY IS INSTALLATION-SPECIFIC"
        labelWidth={480}
        width={540}
        x={250}
        y={390}
      />
      <DiagramSectionLabel label="PROJECT AND PROVIDER BOUNDARIES" lineTo={812} x={28} y={725} />

      <DiagramNode detail="automation" height={74} icon={UsersIcon} title="Operators" tone="external" width={190} x={20} y={50} />
      <DiagramNode detail={["continuous", "reconciliation"]} height={82} icon={FluxIcon} title="Fleet + Flux" tone="source" width={190} x={20} y={150} />

      <DiagramNode detail="RKE2" height={72} icon={BareMetalIcon} title="Server 1" width={150} x={270} y={135} />
      <DiagramNode detail="RKE2" height={72} icon={BareMetalIcon} title="Server 2" width={150} x={445} y={135} />
      <DiagramNode detail="RKE2" height={72} icon={BareMetalIcon} title="Server 3" width={150} x={620} y={135} />

      <DiagramNode detail={["node · API · etcd", "SSH traffic"]} height={72} icon={NetworkIcon} title="Management network" width={340} x={350} y={285} />

      <DiagramNode detail="RKE2" height={82} icon={BareMetalIcon} title="Worker 1" width={150} x={270} y={435} />
      <DiagramNode detail="RKE2" height={82} icon={BareMetalIcon} title="Worker 2" width={150} x={445} y={435} />
      <DiagramNode detail="RKE2" height={82} icon={BareMetalIcon} title="Worker N" tone="muted" width={150} x={620} y={435} />

      <DiagramNode detail={["Kube-DC · Kube-OVN", "KubeVirt · shared services"]} height={72} icon={KubernetesIcon} title="Platform services" tone="accent" width={340} x={350} y={600} />

      <DiagramNode detail={["optional", "ext-cloud", "ext-public"]} height={96} icon={CloudIcon} title="Providers" tone="external" width={190} x={20} y={745} />
      <DiagramNode detail={["one VPC + subnet", "per Project"]} height={72} icon={NetworkIcon} title="Project networks" width={260} x={300} y={745} />
      <DiagramNode detail={["separate API", "authorization · CNI"]} height={72} icon={KubernetesIcon} title="Managed Clusters" width={240} x={580} y={745} />
      <DiagramNode detail={["namespaces · Pods", "VMs · cluster workers"]} height={82} title="Project workloads" width={260} x={300} y={842} />

    </ExplainerDiagram>
  );
}
