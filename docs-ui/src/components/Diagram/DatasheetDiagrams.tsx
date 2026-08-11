import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import KubernetesIcon from '../../../../docs/diagrams/icons/kubernetes.svg';
import ObservabilityIcon from '../../../../docs/diagrams/icons/observability.svg';
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

export function ManagedKubernetesSizingDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Usage metrics feed bounded recommendations to the control plane and datastore; the platform applies them in place where possible and preserves etcd quorum."
      description="Usage metrics feed a VerticalPodAutoscaler constrained by configured resource floors and ceilings. It recommends in-place resizing for hosted control-plane Pods where possible and quorum-aware resizing for etcd members."
      diagramId="datasheet-managed-kubernetes-sizing"
      minWidth={820}
      title="Managed Kubernetes control-plane right-sizing"
      viewBox="0 0 820 330"
    >
      <DiagramEdge d="M220 157 H270" kind="data" />
      <DiagramEdge d="M550 137 C560 137 560 98 570 98" kind="control" />
      <DiagramEdge d="M550 177 C560 177 560 238 570 238" kind="control" />
      <DiagramSectionLabel label="BOUNDED AUTOMATIC RIGHT-SIZING" lineTo={792} x={28} y={28} />
      <DiagramNode detail="observed load" height={82} icon={ObservabilityIcon} title="Usage metrics" tone="source" width={190} x={30} y={116} />
      <DiagramNode detail={['configured floors', 'targets · ceilings']} height={102} icon={ControllerIcon} title="Vertical autoscaler" tone="accent" width={280} x={270} y={106} />
      <DiagramNode detail="in-place resize" height={82} icon={KubernetesIcon} title="Control-plane Pods" width={230} x={570} y={57} />
      <DiagramNode detail="quorum-aware" height={82} icon={DataIcon} title="etcd members" tone="storage" width={230} x={570} y={197} />
    </ExplainerDiagram>
  );
}

export function ManagedKubernetesUpgradeDiagram(): React.JSX.Element {
  return (
    <LinearFlowDiagram
      caption="The tenant selects a supported target; the hosted control plane moves first, worker pools roll in bounded steps, and the tenant can validate between stages."
      description="A tenant selects a supported Kubernetes target version. Kube-DC upgrades the hosted control plane first, then rolls worker pools by joining replacement nodes, draining old nodes, and removing them. The tenant validates before continuing."
      diagramId="datasheet-managed-kubernetes-upgrade"
      sectionLabel="STAGED · TENANT-CONTROLLED UPGRADE"
      steps={[
        {title: 'Select version', detail: 'supported target', tone: 'source'},
        {title: 'Control plane', detail: 'upgrades first', icon: KubernetesIcon, tone: 'accent'},
        {title: 'Worker pools', detail: ['join · drain', 'leave in steps'], icon: ComputeIcon},
        {title: 'Validate', detail: 'pause or continue'},
      ]}
      title="Managed Kubernetes upgrade sequence"
    />
  );
}

export function ManagedDatabaseProtectionDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Applications keep one read-write endpoint while engine replication and operator promotion protect service continuity; scheduled and on-demand backups land in Project S3 for new-name or in-place restore."
      description="An application connects through a stable read-write endpoint to the current database primary. The primary replicates to an available replica, which the operator can promote after an eligible failure. Scheduled and on-demand backups are written to Project S3 and can restore a database under a new name or in place."
      diagramId="datasheet-managed-database-protection"
      minWidth={900}
      title="Managed database availability and backup path"
      viewBox="0 0 900 480"
    >
      <DiagramEdge d="M260 133 H285" />
      <DiagramEdge d="M565 133 H620" />
      <DiagramEdge d="M710 174 C710 215 530 215 530 255" kind="asynchronous" />
      <DiagramEdge d="M425 337 C425 365 300 365 300 390" kind="data" />
      <DiagramEdge d="M475 337 C475 365 650 365 650 390" kind="data" />
      <DiagramSectionLabel label="SERVE · REPLICATE · BACK UP · RESTORE" lineTo={872} x={28} y={28} />
      <DiagramNode detail="stable RW endpoint" height={82} icon={ApplicationIcon} title="Application" tone="source" width={230} x={30} y={92} />
      <DiagramNode detail="current writer" height={82} icon={DataIcon} title="Primary" tone="accent" width={280} x={285} y={92} />
      <DiagramNode detail="promotion target" height={82} icon={DataIcon} title="Replica" width={250} x={620} y={92} />
      <DiagramBoundary height={155} label="PROJECT DATA PROTECTION" labelWidth={280} width={820} x={40} y={210} />
      <DiagramNode detail={['scheduled + on demand', 'KMS envelope when configured']} height={82} icon={StorageIcon} title="Project S3 backups" tone="storage" width={360} x={270} y={255} />
      <DiagramNode detail="new name" height={72} icon={DataIcon} title="Restore copy" width={260} x={170} y={390} />
      <DiagramNode detail="controlled outage" height={72} icon={DataIcon} title="In-place restore" width={260} x={520} y={390} />
    </ExplainerDiagram>
  );
}

export function DataProtectionDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Each service protects its own data class: databases and Managed Cluster etcd use Project S3, VM and volume snapshots stay in the storage pool, and an enterprise backup integration is required for copies outside the platform's storage failure domain."
      description="Managed databases write scheduled and on-demand backups to Project S3 and restore from it. Managed Cluster etcd writes scheduled snapshots to the same Project service and restores supported datastore topologies. Virtual machine and PersistentVolume snapshots remain in the platform storage pool. Git contains desired platform configuration, not workload backups. An operator-configured enterprise integration copies protected data off site when required."
      diagramId="datasheet-data-protection"
      minWidth={900}
      title="Service-owned data protection boundaries"
      viewBox="0 0 900 680"
    >
      <DiagramEdge bidirectional d="M280 166 H340" kind="data" />
      <DiagramEdge bidirectional d="M280 306 H340" kind="data" />
      <DiagramEdge d="M620 255 H650" kind="asynchronous" />
      <DiagramEdge d="M280 521 H340" kind="data" />
      <DiagramSectionLabel label="BACKUP SCOPE FOLLOWS THE DATA OWNER" lineTo={872} x={28} y={28} />
      <DiagramBoundary height={370} label="PROJECT S3 · SERVICE BACKUPS" labelWidth={260} width={300} x={320} y={70} />
      <DiagramNode detail="service owner" height={82} icon={DataIcon} title="Managed databases" width={250} x={30} y={125} />
      <DiagramNode detail="service owner" height={82} icon={KubernetesIcon} title="Managed Clusters" width={250} x={30} y={265} />
      <DiagramNode detail={['scheduled + on demand', 'optional KMS envelope']} height={82} icon={StorageIcon} title="Database backups" tone="storage" width={250} x={340} y={125} />
      <DiagramNode detail={['scheduled snapshots', 'restore by topology']} height={82} icon={StorageIcon} title="etcd snapshots" tone="storage" width={250} x={340} y={265} />
      <DiagramNode detail={['off-site copy', 'separate domain']} height={102} icon={StorageIcon} title="Enterprise backup" tone="external" width={220} x={650} y={204} />
      <DiagramNode detail="point-in-time copies" height={82} icon={ComputeIcon} title="VMs + volumes" width={250} x={30} y={480} />
      <DiagramNode detail="snapshot storage" height={82} icon={StorageIcon} title="Storage pool" tone="storage" width={280} x={340} y={480} />
      <DiagramCallout detail="Desired state is not a backup of service or workload data." height={72} title="Git configuration" width={600} x={150} y={590} />
    </ExplainerDiagram>
  );
}

export function ObservabilityPipelineDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Organization reconciliation provisions Grafana, metrics tenancy and log routing; workloads and managed control planes feed the shared data layer, while tenant-scoped queries return only that organization's data."
      description="When an Organization is created, Kube-DC controllers provision its Grafana organization and dashboards, a Mimir metrics tenant, and Loki log routing. Project workloads and managed-cluster control planes send telemetry to the shared multi-tenant stack. Organization members authenticate with SSO and query only their tenant scope through Grafana."
      diagramId="datasheet-observability-pipeline"
      minWidth={900}
      title="Per-Organization observability provisioning and data flow"
      viewBox="0 0 900 570"
    >
      <DiagramEdge d="M220 112 H280" kind="control" />
      <DiagramEdge d="M620 112 H680" kind="control" />
      <DiagramEdge d="M230 322 H280" kind="data" />
      <DiagramEdge d="M250 442 C265 442 265 362 280 362" kind="data" />
      <DiagramEdge d="M620 342 H680" kind="data" />
      <DiagramEdge d="M770 302 V153" />
      <DiagramSectionLabel label="PROVISION · COLLECT · ISOLATE · PRESENT" lineTo={872} x={28} y={28} />
      <DiagramNode detail="tenant lifecycle" height={82} title="Organization" tone="source" width={190} x={30} y={71} />
      <DiagramNode detail="automatic reconciliation" height={82} icon={ControllerIcon} title="Kube-DC controllers" tone="accent" width={340} x={280} y={71} />
      <DiagramNode detail={['Grafana org', 'dashboards']} height={82} icon={ObservabilityIcon} title="Tenant view" width={190} x={680} y={71} />
      <DiagramNode detail={['apps · VMs', 'databases']} height={82} icon={ApplicationIcon} title="Project workloads" width={210} x={20} y={281} />
      <DiagramNode detail={['API servers', 'controllers']} height={82} icon={KubernetesIcon} title="Control planes" width={230} x={20} y={401} />
      <DiagramBoundary height={190} label="SHARED STACK · TENANT-SCOPED DATA" labelWidth={330} width={380} x={260} y={250} />
      <DiagramNode detail={['Mimir metrics', 'Loki logs']} height={92} icon={ObservabilityIcon} title="Telemetry store" tone="storage" width={340} x={280} y={302} />
      <DiagramNode detail="organization SSO" height={82} title="Tenant members" width={190} x={680} y={302} />
      <DiagramCallout detail="Retention and coverage remain operator-defined." height={64} title="Deployment policy" width={560} x={170} y={490} />
    </ExplainerDiagram>
  );
}

export function GpuServiceModelDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The accelerator catalog offers a density-oriented shared slice for containers and a whole-device VM path; model-specific quota bounds use, while a separate Organization reservation holds healthy capacity."
      description="A published accelerator catalog exposes two governed products. Shared GPU assigns a fixed memory slice to container workloads with cooperative compute sharing. Dedicated GPU attaches one whole device to a virtual machine that is not live-migratable. Per-model entitlement limits concurrent use, while an optional Organization reservation holds healthy capacity separately."
      diagramId="datasheet-gpu-service-model"
      minWidth={900}
      title="Governed shared and dedicated GPU services"
      viewBox="0 0 900 530"
    >
      <DiagramEdge d="M450 150 C450 184 230 184 230 222" />
      <DiagramEdge d="M450 150 C450 184 670 184 670 222" />
      <DiagramEdge d="M230 304 V356" />
      <DiagramEdge d="M670 304 V356" />
      <DiagramEdge d="M170 456 C100 456 50 430 50 380 V180 C50 140 120 109 180 109 H290" kind="control" />
      <DiagramEdge d="M730 456 C800 456 850 430 850 380 V180 C850 140 780 109 720 109 H610" kind="control" />
      <DiagramSectionLabel label="CATALOG · ENTITLEMENT · CAPACITY" lineTo={872} x={28} y={28} />
      <DiagramNode detail="qualified products" height={82} icon={ComputeIcon} title="Accelerator catalog" tone="accent" width={320} x={290} y={68} />
      <DiagramNode detail="fixed memory slice" height={82} icon={ComputeIcon} title="Shared GPU" width={300} x={80} y={222} />
      <DiagramNode detail="whole device" height={82} icon={ComputeIcon} title="Dedicated GPU VM" width={300} x={520} y={222} />
      <DiagramNode detail={['container workloads', 'cooperative compute']} height={82} icon={ApplicationIcon} title="Density path" width={300} x={80} y={356} />
      <DiagramNode detail={['stronger guest boundary', 'not live-migratable']} height={82} icon={ComputeIcon} title="Isolation path" width={300} x={520} y={356} />
      <DiagramCallout detail="bounds concurrent use" height={64} title="Per-model entitlement" width={300} x={20} y={456} />
      <DiagramCallout detail="holds healthy devices" height={64} title="Organization reservation" width={300} x={580} y={456} />
    </ExplainerDiagram>
  );
}

export function StorageServiceDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The operator's disk topology feeds Ceph, which publishes block, shared and S3 services to Project workloads through standard Kubernetes storage interfaces."
      description="Operator-provided SSD, NVMe, or HDD capacity forms the Ceph storage layer. Ceph publishes block volumes for Pods and virtual machines, shared read-write storage for shared applications and migration-eligible VM disks, and S3-compatible per-Project buckets used by applications, backups, and snapshots."
      diagramId="datasheet-storage-services"
      minWidth={900}
      title="Storage services from operator-provided disks"
      viewBox="0 0 900 500"
    >
      <DiagramEdge d="M450 146 V196" kind="data" />
      <DiagramEdge d="M450 288 C450 320 160 320 160 350" kind="data" />
      <DiagramEdge d="M450 288 V350" kind="data" />
      <DiagramEdge d="M450 288 C450 320 740 320 740 350" kind="data" />
      <DiagramSectionLabel label="PHYSICAL CAPACITY · STANDARD CONSUMPTION" lineTo={872} x={28} y={28} />
      <DiagramNode detail="SSD · NVMe · HDD" height={78} icon={StorageIcon} title="Your disks" tone="external" width={300} x={300} y={68} />
      <DiagramNode detail="topology + replication" height={92} icon={StorageIcon} title="Ceph storage layer" tone="accent" width={380} x={260} y={196} />
      <DiagramNode detail="PVCs · VM disks" height={92} icon={DataIcon} title="Block volumes" width={250} x={35} y={350} />
      <DiagramNode detail={['shared filesystems', 'migratable VM disks']} height={92} icon={StorageIcon} title="RWX storage" width={250} x={325} y={350} />
      <DiagramNode detail={['Project buckets', 'apps · backups']} height={92} icon={StorageIcon} title="S3 object storage" tone="storage" width={250} x={615} y={350} />
    </ExplainerDiagram>
  );
}
