import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ApplicationWebIcon from '../../../../docs/diagrams/icons/application-web.svg';
import CloudIcon from '../../../../docs/diagrams/icons/cloud.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import DataServiceIcon from '../../../../docs/diagrams/icons/data-service.svg';
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

export default function ArchitecturalLayersDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Kube-DC architectural layers. Organizations and Projects govern resources in the management cluster; Managed Clusters retain separate API, authorization, and CNI boundaries."
      description="Users access the console, admin console, and CLI, which call Kubernetes and backend APIs. Kube-DC controllers reconcile Organization and Project governance, platform integrations, networking, compute, data services, Project workloads, and Managed Clusters. These capabilities run on the Kubernetes management cluster and physical or cloud infrastructure. Fleet and Flux separately install and configure the management cluster."
      diagramId="architectural-layers-explainer"
      minWidth={840}
      title="Kube-DC architectural layers"
      viewBox="0 0 840 950"
    >
      <DiagramEdge d="M210 91 H270" />
      <DiagramEdge d="M520 91 H580" />
      <DiagramEdge d="M690 132 C690 160 420 160 420 185" kind="control" />

      <DiagramEdge d="M330 257 C330 278 125 278 125 295" kind="asynchronous" />
      <DiagramEdge d="M390 257 C390 278 328 278 328 295" kind="asynchronous" />
      <DiagramEdge d="M450 257 C450 278 513 278 513 295" kind="asynchronous" />
      <DiagramEdge d="M510 257 C510 278 715 278 715 295" kind="asynchronous" />

      <DiagramEdge allowDetachedEnd d="M420 257 V455" directed={false} kind="control" />
      <DiagramEdge d="M170 455 H670" directed={false} kind="control" />
      <DiagramEdge d="M170 455 V480" kind="control" />
      <DiagramEdge d="M420 455 V480" kind="control" />
      <DiagramEdge d="M670 455 V480" kind="control" />

      <DiagramEdge d="M170 558 V575" directed={false} />
      <DiagramEdge allowDetachedEnd d="M420 558 V575" directed={false} />
      <DiagramEdge d="M670 558 V575" directed={false} />
      <DiagramEdge d="M170 575 H670" directed={false} />
      <DiagramEdge allowDetachedStart d="M290 575 V600" />
      <DiagramEdge allowDetachedStart d="M600 575 V600" />

      <DiagramEdge d="M420 827 V850" />
      <DiagramEdge d="M210 791 H270" kind="asynchronous" />

      <DiagramSectionLabel label="ACCESS AND MANAGEMENT" lineTo={812} x={28} y={28} />
      <DiagramBoundary
        height={240}
        label="PLATFORM CONTROL · GOVERNANCE AND INTEGRATIONS"
        labelWidth={430}
        width={800}
        x={20}
        y={155}
      />
      <DiagramBoundary
        height={300}
        label="WORKLOAD AND DATA CAPABILITIES"
        labelWidth={310}
        width={800}
        x={20}
        y={415}
      />
      <DiagramSectionLabel label="FOUNDATION" lineTo={812} x={28} y={740} />

      <DiagramNode detail="operators" height={82} icon={UsersIcon} title="Users" tone="external" width={180} x={30} y={50} />
      <DiagramNode detail="console · admin · CLI" height={82} icon={ApplicationWebIcon} title="Access surfaces" width={250} x={270} y={50} />
      <DiagramNode detail={["Kubernetes", "backend APIs"]} height={82} icon={ApiIcon} title="Platform APIs" width={220} x={580} y={50} />

      <DiagramNode detail="reconciliation" height={72} title="Kube-DC controllers" tone="accent" width={240} x={300} y={185} />
      <DiagramNode detail={["Organizations", "Projects · RBAC · VPC"]} height={82} title="Governance" width={210} x={20} y={295} />
      <DiagramNode detail="Keycloak · RBAC" height={82} title="Identity" width={165} x={245} y={295} />
      <DiagramNode detail={["OpenBao", "certificates"]} height={82} title="Security" width={165} x={430} y={295} />
      <DiagramNode detail={["Grafana · Mimir", "Loki"]} height={82} title="Observability" width={210} x={610} y={295} />

      <DiagramNode detail={["Kube-OVN · Multus", "Envoy Gateway"]} height={78} icon={NetworkIcon} title="Networking" width={220} x={60} y={480} />
      <DiagramNode detail="KubeVirt · CDI" height={78} icon={ComputeIcon} title="Compute" width={220} x={310} y={480} />
      <DiagramNode detail={["CSI · Rook Ceph", "managed databases"]} height={78} icon={DataServiceIcon} title="Data services" width={220} x={560} y={480} />
      <DiagramNode detail={["backing namespace", "Organization + Project"]} height={82} title="Project workloads" width={220} x={180} y={600} />
      <DiagramNode detail={["separate API", "authorization · CNI"]} height={82} icon={KubernetesIcon} title="Managed Clusters" width={300} x={450} y={600} />

      <DiagramNode detail={["desired state", "installation"]} height={82} icon={FluxIcon} title="Fleet + Flux" tone="source" width={190} x={20} y={755} />
      <DiagramNode detail="platform execution plane" height={72} icon={KubernetesIcon} title="Management cluster" tone="accent" width={300} x={270} y={755} />
      <DiagramNode detail={["physical or cloud", "compute · networks · storage"]} height={82} icon={CloudIcon} title="Infrastructure" tone="external" width={300} x={270} y={850} />
    </ExplainerDiagram>
  );
}
