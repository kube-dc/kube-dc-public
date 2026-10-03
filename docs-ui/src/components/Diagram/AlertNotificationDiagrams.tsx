import {DiagramEdge, DiagramNode, DiagramSectionLabel, ExplainerDiagram} from './index';
import {LinearFlowDiagram} from './FlowDiagram';

export function AlertNotificationArchitecture(): React.JSX.Element {
  return (
    <ExplainerDiagram
      diagramId="alert-notification-architecture"
      title="Notification policy and delivery in one installation"
      description="Flux applies Fleet policy to a baseline Secret. The backend combines it with Admin Console destination settings and writes a runtime Secret. Local Alertmanager reads the runtime Secret and receives classified alerts from local Prometheus. It notifies Slack and optionally Opsgenie for classified P1 and P2 incidents."
      caption="Repeat this configuration per installation. Fleet owns policy; console destination settings persist in the separate runtime Secret."
      viewBox="0 0 900 650"
    >
      <DiagramSectionLabel label="POLICY AND DESTINATION CONFIGURATION" lineTo={872} x={28} y={28} />
      <DiagramEdge d="M260 100 H330" kind="control" />
      <DiagramEdge d="M570 100 H640" kind="control" />
      <DiagramEdge d="M760 140 V245 H575" kind="control" />
      <DiagramEdge d="M260 245 H325" kind="control" />
      <DiagramEdge d="M450 290 V325 H760 V360" kind="control" />
      <DiagramNode title="Fleet repository" detail="rules and routing policy" tone="source" x={20} y={60} width={240} height={80} />
      <DiagramNode title="Flux" detail="local reconciliation" x={330} y={60} width={240} height={80} />
      <DiagramNode title="Baseline Secret" detail="Fleet managed" tone="storage" x={640} y={60} width={240} height={80} />
      <DiagramNode title="Admin Console" detail={["destination credentials", "enable or disable delivery"]} x={20} y={200} width={240} height={90} />
      <DiagramNode title="Backend reconciler" detail={["merge and validate", "preserve last valid config"]} tone="accent" x={325} y={200} width={250} height={90} />
      <DiagramSectionLabel label="LOCAL ALERT EVALUATION AND DELIVERY" lineTo={620} x={28} y={340} />
      <DiagramEdge d="M260 400 H330" />
      <DiagramEdge d="M640 400 H570" kind="control" />
      <DiagramEdge d="M450 440 V475 H295 V510" />
      <DiagramEdge d="M450 475 H605 V510" />
      <DiagramNode title="Prometheus" detail="classified alerts" x={20} y={360} width={240} height={80} />
      <DiagramNode title="Alertmanager" detail="group and route" tone="accent" x={330} y={360} width={240} height={80} />
      <DiagramNode title="Runtime Secret" detail="backend managed" tone="storage" x={640} y={360} width={240} height={80} />
      <DiagramNode title="Slack" detail={["warnings and paging", "when enabled"]} tone="external" x={175} y={510} width={240} height={90} />
      <DiagramNode title="Opsgenie" detail={["classified P1/P2 only", "when enabled"]} tone="external" x={485} y={510} width={240} height={90} />
    </ExplainerDiagram>
  );
}

export function AlertNotificationAdoption(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      diagramId="alert-notification-adoption"
      title="Adopt console management in two phases"
      description="Inspect the existing policy. First deploy the baseline and backend reconciler. Verify the runtime Secret and successful synchronization before a separate Fleet change selects the runtime Secret. Then configure destinations and test delivery."
      caption="The runtime Secret must exist and validate before Alertmanager selects it. Keep the two Fleet reconciliations separate."
      sectionLabel="INSPECT · SEED · VERIFY · SWITCH · TEST"
      steps={[
        {title: 'Inspect', detail: ['existing policy', 'and destinations']},
        {title: 'Seed', detail: ['baseline and', 'reconciler'], tone: 'source'},
        {title: 'Verify', detail: ['runtime Secret', 'sync succeeds'], tone: 'accent'},
        {title: 'Switch', detail: ['select runtime', 'in Alertmanager']},
        {title: 'Test', detail: ['destinations', 'and recovery'], tone: 'external'},
      ]}
    />
  );
}
