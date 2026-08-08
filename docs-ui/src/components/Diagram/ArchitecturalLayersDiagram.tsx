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
      viewBox="0 0 840 870"
    >
      <DiagramEdge d="M210 91 H270" />
      <DiagramEdge d="M520 91 H580" />
      <DiagramEdge d="M690 132 C690 158 420 164 420 183 V195" kind="control" />

      <DiagramEdge d="M310 231 C280 231 280 231 280 231" kind="asynchronous" />
      <DiagramEdge d="M530 231 H550" kind="asynchronous" />
      <DiagramEdge d="M420 267 C420 278 170 278 170 283 V295" kind="asynchronous" />
      <DiagramEdge d="M420 267 V295" kind="asynchronous" />
      <DiagramEdge d="M420 267 C420 278 670 278 670 283 V295" kind="asynchronous" />

      <DiagramEdge d="M420 267 C420 372 170 408 170 438 V450" kind="asynchronous" />
      <DiagramEdge d="M420 267 C420 372 420 408 420 438 V450" kind="asynchronous" />
      <DiagramEdge d="M420 267 C420 372 670 408 670 438 V450" kind="asynchronous" />
      <DiagramEdge d="M660 267 C660 430 290 500 290 528 V540" kind="control" />
      <DiagramEdge d="M660 267 C660 430 600 500 600 528 V540" kind="control" />
      <DiagramEdge d="M420 267 C420 430 600 500 600 528 V540" kind="asynchronous" />

      <DiagramEdge d="M420 622 V670" />
      <DiagramEdge d="M420 742 V770" />
      <DiagramEdge d="M210 706 H270" kind="asynchronous" />

      <DiagramSectionLabel label="ACCESS AND MANAGEMENT" lineTo={812} x={28} y={28} />
      <DiagramBoundary
        height={220}
        label="PLATFORM CONTROL · GOVERNANCE AND INTEGRATIONS"
        labelWidth={430}
        width={800}
        x={20}
        y={155}
      />
      <DiagramBoundary
        height={232}
        label="WORKLOAD AND DATA CAPABILITIES"
        labelWidth={310}
        width={800}
        x={20}
        y={390}
      />
      <DiagramSectionLabel label="FOUNDATION" lineTo={812} x={28} y={655} />

      <DiagramNode detail="operators" height={82} icon={UsersIcon} title="Users" tone="external" width={180} x={30} y={50} />
      <DiagramNode detail="console · admin · CLI" height={82} icon={ApplicationWebIcon} title="Access surfaces" width={250} x={270} y={50} />
      <DiagramNode detail={["Kubernetes", "backend APIs"]} height={82} icon={ApiIcon} title="Platform APIs" width={220} x={580} y={50} />

      <DiagramNode detail="reconciliation" height={72} title="Kube-DC controllers" tone="accent" width={220} x={310} y={195} />
      <DiagramNode detail={["Organizations", "Projects · RBAC · VPC"]} height={82} title="Governance" width={220} x={60} y={190} />
      <DiagramNode detail={["identity · security", "observability"]} height={82} title="Platform integrations" width={220} x={550} y={190} />

      <DiagramNode detail="Keycloak · RBAC" height={68} title="Identity" width={220} x={60} y={295} />
      <DiagramNode detail="OpenBao · certificates" height={68} title="Security" width={220} x={310} y={295} />
      <DiagramNode detail="Grafana · Mimir · Loki" height={68} title="Observability" width={220} x={560} y={295} />

      <DiagramNode detail={["Kube-OVN · Multus", "Envoy Gateway"]} height={78} icon={NetworkIcon} title="Networking" width={220} x={60} y={450} />
      <DiagramNode detail="KubeVirt · CDI" height={78} icon={ComputeIcon} title="Compute" width={220} x={310} y={450} />
      <DiagramNode detail={["CSI · Rook Ceph", "managed databases"]} height={78} icon={DataServiceIcon} title="Data services" width={220} x={560} y={450} />
      <DiagramNode detail={["backing namespace", "Organization + Project"]} height={82} title="Project workloads" width={220} x={180} y={540} />
      <DiagramNode detail={["separate API", "authorization · CNI"]} height={82} icon={KubernetesIcon} title="Managed Clusters" width={300} x={450} y={540} />

      <DiagramNode detail={["desired state", "installation"]} height={82} icon={FluxIcon} title="Fleet + Flux" tone="source" width={190} x={20} y={670} />
      <DiagramNode detail="platform execution plane" height={72} icon={KubernetesIcon} title="Management cluster" tone="accent" width={300} x={270} y={670} />
      <DiagramNode detail={["physical or cloud", "compute · networks · storage"]} height={82} icon={CloudIcon} title="Infrastructure" tone="external" width={300} x={270} y={770} />
    </ExplainerDiagram>
  );
}
