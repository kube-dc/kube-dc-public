import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ApplicationWebIcon from '../../../../docs/diagrams/icons/application-web.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataServiceIcon from '../../../../docs/diagrams/icons/data-service.svg';
import FluxIcon from '../../../../docs/diagrams/icons/flux.svg';
import GitRepositoryIcon from '../../../../docs/diagrams/icons/git-repository.svg';
import KeycloakIcon from '../../../../docs/diagrams/icons/keycloak.svg';
import NetworkServiceIcon from '../../../../docs/diagrams/icons/network-service.svg';
import ObservabilityIcon from '../../../../docs/diagrams/icons/observability.svg';
import SecurityIcon from '../../../../docs/diagrams/icons/security.svg';
import UsersIcon from '../../../../docs/diagrams/icons/users.svg';
import {
  DiagramBoundary,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export default function ManagementPlaneDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      diagramId="management-plane-explainer"
      caption="The installation loop and runtime reconciliation loop are separate: Fleet and Flux install and configure the management cluster; Kube-DC controllers reconcile product resources through the management APIs."
      description="Users and operators access Kube-DC through the console, admin console, or CLI. These access surfaces call Kubernetes and backend APIs. The APIs feed Kube-DC controllers, which reconcile Keycloak; Kube-OVN, Multus, and Envoy Gateway; KubeVirt, Kamaji, and Cluster API; CSI and Rook Ceph; OpenBao, External Secrets, and cert-manager; and Grafana, Mimir, and Loki. Separately, the Fleet repository supplies desired installation state to Flux, which installs and configures the management cluster."
      minWidth={840}
      title="Kube-DC management plane"
      viewBox="0 0 840 730"
    >
      <DiagramEdge d="M250 96 H300" />
      <DiagramEdge d="M455 132 C455 164 395 178 395 218 V230" />
      <DiagramEdge d="M530 266 H560" />

      <DiagramEdge
        d="M680 302 C680 324 540 324 540 342 V560"
        directed={false}
        kind="asynchronous"
        label="reconciles"
        labelWidth={86}
        labelX={604}
        labelY={324}
      />
      <DiagramEdge d="M540 342 C500 342 375 346 375 353 V365" kind="asynchronous" />
      <DiagramEdge d="M540 342 C580 342 695 346 695 353 V365" kind="asynchronous" />
      <DiagramEdge d="M540 452 C500 452 375 456 375 463 V475" kind="asynchronous" />
      <DiagramEdge d="M540 452 C580 452 695 456 695 463 V475" kind="asynchronous" />
      <DiagramEdge d="M540 560 C500 560 375 566 375 573 V585" kind="asynchronous" />
      <DiagramEdge d="M540 560 C580 560 695 566 695 573 V585" kind="asynchronous" />

      <DiagramEdge
        d="M115 432 V505"
        kind="control"
        label="desired state"
        labelWidth={108}
        labelX={115}
        labelY={466}
      />
      <DiagramEdge d="M210 546 H240" kind="control" />

      <DiagramSectionLabel label="ACCESS PATH" lineTo={820} x={20} y={30} />
      <DiagramSectionLabel label="INSTALLATION CONTROL" x={20} y={330} />
      <DiagramBoundary
        height={520}
        label="MANAGEMENT CLUSTER · CONTROL AND PLATFORM SERVICES"
        labelWidth={500}
        width={580}
        x={240}
        y={180}
      />

      <DiagramNode
        detail="platform consumers"
        height={72}
        icon={UsersIcon}
        title="Users and operators"
        tone="external"
        width={230}
        x={20}
        y={60}
      />
      <DiagramNode
        detail="access surfaces"
        height={72}
        icon={ApplicationWebIcon}
        title="Console, admin console, CLI"
        width={310}
        x={300}
        y={60}
      />
      <DiagramNode
        detail="Kubernetes + backend"
        height={72}
        icon={ApiIcon}
        title="Platform APIs"
        width={270}
        x={260}
        y={230}
      />
      <DiagramNode
        detail="reconciliation"
        height={72}
        icon={ControllerIcon}
        title="Kube-DC controllers"
        tone="accent"
        width={240}
        x={560}
        y={230}
      />

      <DiagramNode
        detail="Keycloak"
        height={82}
        icon={KeycloakIcon}
        title="Identity"
        width={230}
        x={260}
        y={365}
      />
      <DiagramNode
        detail={["Kube-OVN · Multus", "Envoy Gateway"]}
        height={82}
        icon={NetworkServiceIcon}
        title="Networking"
        width={210}
        x={590}
        y={365}
      />
      <DiagramNode
        detail={["KubeVirt · Kamaji", "Cluster API"]}
        height={82}
        icon={ComputeIcon}
        title="Compute"
        width={230}
        x={260}
        y={475}
      />
      <DiagramNode
        detail={["CSI", "Rook Ceph"]}
        height={82}
        icon={DataServiceIcon}
        title="Storage"
        tone="storage"
        width={210}
        x={590}
        y={475}
      />
      <DiagramNode
        detail={["OpenBao", "External Secrets", "cert-manager"]}
        height={96}
        icon={SecurityIcon}
        title="Security"
        width={230}
        x={260}
        y={585}
      />
      <DiagramNode
        detail={["Grafana · Mimir", "Loki"]}
        height={82}
        icon={ObservabilityIcon}
        title="Observability"
        width={210}
        x={590}
        y={585}
      />

      <DiagramNode
        detail="desired state"
        height={72}
        icon={GitRepositoryIcon}
        title="Fleet repo"
        tone="source"
        width={190}
        x={20}
        y={360}
      />
      <DiagramNode
        detail={["installs +", "configures"]}
        height={82}
        icon={FluxIcon}
        title="Flux"
        tone="external"
        width={190}
        x={20}
        y={505}
      />
    </ExplainerDiagram>
  );
}
