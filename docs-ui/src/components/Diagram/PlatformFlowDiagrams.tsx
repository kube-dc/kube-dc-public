import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import PublicIcon from '../../../../docs/diagrams/icons/network-public.svg';
import {LinearFlowDiagram} from './FlowDiagram';

export function RookObjectStorageFlowDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="External S3 traffic terminates TLS at the Gateway, reaches the Rook Ceph object gateway over its Service, and is persisted by Ceph on storage devices managed as OSDs."
      description="An S3 client connects over HTTPS to s3.example.com. The platform Gateway terminates TLS and forwards the request to the rook-ceph-rgw Service on port 80. The RADOS Gateway handles the S3 operation, and Ceph persists object data through its configured pools and OSDs on worker block devices."
      diagramId="rook-object-storage-flow-explainer"
      sectionLabel="S3 REQUEST AND STORAGE PATH"
      steps={[
        {title: 'S3 client', detail: 'HTTPS', icon: PublicIcon, tone: 'external'},
        {title: 'Gateway', detail: 'TLS termination', icon: NetworkIcon, tone: 'accent'},
        {title: 'Ceph RGW', detail: ['rook-ceph-rgw', 'Service :80'], icon: ApplicationIcon},
        {title: 'Ceph storage', detail: ['pools + OSDs', 'worker devices'], icon: DataIcon, tone: 'storage'},
      ]}
      relationships={[{}, {}, {kind: 'data'}]}
      title="Rook Ceph object-storage request flow"
    />
  );
}

export function Metal3RemediationDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="Remediation is a bounded recovery attempt: health detection creates a Metal3 request, CAPM3 power-cycles and isolates the host, and operators inspect terminal failure before any reviewed reprovision or replacement."
      description="MachineHealthCheck detects a worker whose Ready condition remains Unknown for five minutes and creates a Metal3Remediation request. The CAPM3 remediation controller powers the server off through its BMC, applies the out-of-service taint, then powers it on. The RKE2 agent reconnects and the Node becomes Ready. If retryLimit is exhausted, remediation reports failure and requires operator review rather than assuming automatic replacement."
      diagramId="metal3-remediation-explainer"
      sectionLabel="BARE-METAL HEALTH RECOVERY"
      steps={[
        {title: 'Health check', detail: ['Ready = ?', '5 minutes']},
        {title: 'Remediation CR', detail: 'Metal3 request', tone: 'source'},
        {title: 'CAPM3', detail: ['off + taint', 'power on'], icon: ControllerIcon, tone: 'accent'},
        {title: 'Worker reboot', detail: ['RKE2', 'reconnects']},
        {title: 'Outcome', detail: ['Ready / failed', 'retryLimit']},
      ]}
      relationships={[{kind: 'control'}, {kind: 'control'}, {kind: 'control'}, {}]}
      title="Metal3 worker remediation sequence"
    />
  );
}

export function TenantVlanLifecycleDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="The platform owns the physical wire and Organization allocation; only the final reversible ProjectNetwork attachment is delegated to the Organization administrator."
      description="A platform administrator defines one FabricSegment for a physical ProviderNetwork and VLAN ID. A platform administrator creates a FabricSegmentAllocation that delegates that segment to Organization acme. An Organization administrator then creates, removes, or rebinds a ProjectNetwork attachment for Project production."
      diagramId="tenant-vlan-lifecycle-explainer"
      sectionLabel="WIRE · ALLOCATION · PROJECT ATTACHMENT"
      steps={[
        {title: 'FabricSegment', detail: ['physical wire', 'pn-ext-4014'], icon: NetworkIcon},
        {title: 'Allocation', detail: ['Organization acme', 'platform owned'], icon: ControllerIcon, tone: 'accent'},
        {title: 'ProjectNetwork', detail: ['production', 'Org reversible'], icon: ApplicationIcon},
      ]}
      relationships={[{label: 'delegate', labelWidth: 72, kind: 'control'}, {label: 'assign', labelWidth: 62, kind: 'control'}]}
      title="Tenant attachment to a physical VLAN"
    />
  );
}
