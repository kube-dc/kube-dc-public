import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import StorageIcon from '../../../../docs/diagrams/icons/storage.svg';
import {
  DiagramBoundary,
  DiagramCallout,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';
import {LinearFlowDiagram} from './FlowDiagram';

export function ManagedServicesModelDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The catalog defines the service contract. The platform runs the service. Bindings deliver credentials to applications in the Project."
      description="The provider publishes classes and plans. A tenant selects a plan through the console or API to create a ManagedService. The platform applies the family integration and reports status. A ServiceBinding delivers a credential Secret that the application uses to connect. Operations and backups depend on the selected plan."
      diagramId="managed-services-model"
      minWidth={900}
      title="One control model for an extensible catalog"
      viewBox="0 0 900 630"
    >
      <DiagramEdge d="M440 111 H530" kind="control" />
      <DiagramEdge d="M695 152 V212" kind="control" />
      <DiagramEdge d="M540 253 H360" kind="control" />
      <DiagramEdge d="M205 294 V365" kind="control" />
      <DiagramEdge d="M360 406 H540" kind="data" />
      <DiagramEdge d="M695 365 V294" kind="data" />
      <DiagramSectionLabel label="SELECT · PROVISION · CONNECT" lineTo={872} x={28} y={28} />
      <DiagramBoundary height={410} label="SERVICE WORKFLOW · PROVISION AND CONNECT" labelWidth={390} width={860} x={20} y={160} />
      <DiagramNode detail="classes · plans · capabilities" height={82} icon={DataIcon} title="Provider catalog" tone="source" width={360} x={80} y={70} />
      <DiagramNode detail="console · manifests · GitOps" height={82} icon={ApiIcon} title="Tenant request" width={330} x={530} y={70} />
      <DiagramNode detail="selected class and plan" height={82} title="ManagedService" tone="accent" width={310} x={540} y={212} />
      <DiagramNode detail="family integration · status" height={82} icon={ControllerIcon} title="Platform execution" width={310} x={50} y={212} />
      <DiagramNode detail="credential role → Secret" height={82} title="ServiceBinding" width={310} x={50} y={365} />
      <DiagramNode detail="service endpoint · TLS" height={82} icon={ApplicationIcon} title="Application" width={310} x={540} y={365} />
      <DiagramCallout detail="Operations, topology, and recovery depend on the family and plan." height={72} title="Published capabilities" width={740} x={80} y={477} />
    </ExplainerDiagram>
  );
}

export function ManagedServicesOperationDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="An accepted request can still fail execution checks. Confirm the operation result and the service state after each change."
      description="A tenant submits an immutable ServiceOperation with a service UID and idempotency key. The hub checks the service identity and plan. Requests wait for approval and a maintenance window when required. The runner executes the operation and reports progress, results, and evidence."
      diagramId="managed-services-operation"
      sectionLabel="REQUEST · CHECK · APPROVE · EXECUTE · VERIFY"
      steps={[
        {title: 'Request', detail: ['service UID', 'idempotency key'], tone: 'source'},
        {title: 'Check', detail: ['plan · capacity', 'service state']},
        {title: 'Wait if required', detail: ['approval', 'maintenance'], tone: 'accent'},
        {title: 'Execute', detail: ['family adapter', 'progress']},
        {title: 'Verify', detail: ['result · status', 'evidence']},
      ]}
      title="Managed service operation lifecycle"
    />
  );
}

export function ManagedServicesArchitectureDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The hub checks provider policy. Data-plane runners execute signed instructions through family adapters and report observed results."
      description="Fleet publishes a versioned service catalog to the management cluster. The console and Kubernetes API submit tenant resources to the hub. The hub signs revisions and operations for a data-plane runner. Family adapters manage engines directly or through native operators. Runners return status, usage, and evidence. Backup-enabled families write to the configured object store."
      diagramId="managed-services-architecture"
      minWidth={900}
      title="Managed services control and execution"
      viewBox="0 0 900 750"
    >
      <DiagramEdge d="M305 161 H530" kind="control" />
      <DiagramEdge d="M670 202 V247" kind="control" />
      <DiagramEdge d="M330 288 H530" kind="control" />
      <DiagramEdge d="M670 329 V455" kind="control" />
      <DiagramEdge d="M530 496 H340" kind="control" />
      <DiagramEdge d="M205 537 V565" kind="control" />
      <DiagramEdge d="M340 606 H530" kind="data" />
      <DiagramSectionLabel label="PROVIDER POLICY · FAMILY IMPLEMENTATION" lineTo={872} x={28} y={28} />
      <DiagramBoundary height={280} label="MANAGEMENT CLUSTER" labelWidth={210} width={860} x={20} y={65} />
      <DiagramBoundary height={270} label="DATA PLANE · SHARED OR SEPARATE CLUSTER" labelWidth={400} width={860} x={20} y={400} />
      <DiagramNode detail="reviewed revisions" height={82} icon={DataIcon} title="Fleet catalog" tone="source" width={250} x={55} y={120} />
      <DiagramNode detail={["policy · placement", "signed instructions"]} height={82} icon={ControllerIcon} title="Hub" tone="accent" width={290} x={530} y={247} />
      <DiagramNode detail="class · plan · connectivity" height={82} title="Provider contract" width={290} x={530} y={120} />
      <DiagramNode detail="console · Kubernetes API" height={82} icon={ApiIcon} title="Tenant resources" width={280} x={50} y={247} />
      <DiagramNode detail={["signed instructions", "observed status"]} height={82} icon={ControllerIcon} title="Runner" tone="accent" width={290} x={530} y={455} />
      <DiagramNode detail="service-specific actions" height={82} icon={ControllerIcon} title="Family adapter" width={270} x={70} y={455} />
      <DiagramNode detail={["native operator", "where required"]} height={82} icon={DataIcon} title="Service engine" width={270} x={70} y={565} />
      <DiagramNode detail="backup-enabled plans" height={82} icon={StorageIcon} title="Backup store" tone="storage" width={290} x={530} y={565} />
      <DiagramCallout detail="Status, usage, and evidence return to the hub. Credentials use controlled delivery." height={64} title="Observed results" width={800} x={50} y={680} />
    </ExplainerDiagram>
  );
}
