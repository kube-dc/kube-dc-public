import ApiIcon from '../../../../docs/diagrams/icons/api.svg';
import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import BareMetalIcon from '../../../../docs/diagrams/icons/bare-metal-server.svg';
import CloudIcon from '../../../../docs/diagrams/icons/cloud.svg';
import ComputeIcon from '../../../../docs/diagrams/icons/compute.svg';
import ControllerIcon from '../../../../docs/diagrams/icons/controller.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import GitIcon from '../../../../docs/diagrams/icons/git-repository.svg';
import KeycloakIcon from '../../../../docs/diagrams/icons/keycloak.svg';
import KubernetesIcon from '../../../../docs/diagrams/icons/kubernetes.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import PublicIcon from '../../../../docs/diagrams/icons/network-public.svg';
import SecurityIcon from '../../../../docs/diagrams/icons/security.svg';
import UsersIcon from '../../../../docs/diagrams/icons/users.svg';
import {
  DiagramBoundary,
  DiagramCallout,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export function BillingPlanControllerDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="One Organization reconciliation fans the selected billing plan into compute, default-limit, external-IP, and object-storage enforcement mechanisms."
      description="The billing-plans ConfigMap in the kube-dc namespace is watched by the Organization controller. For each assigned Organization, the controller creates or updates a HierarchicalResourceQuota enforced across child Project backing namespaces, a LimitRange propagated by HNC, EIP quota status checked during EIP creation, and a CephObjectStoreUser whose S3 quota is enforced by Ceph RGW."
      diagramId="billing-plan-controller-explainer"
      minWidth={900}
      title="Billing plan quota reconciliation"
      viewBox="0 0 900 560"
    >
      <DiagramEdge d="M270 108 H330" kind="control" />
      <DiagramEdge d="M570 108 H630" kind="control" />
      <DiagramEdge d="M750 144 C750 180 130 180 130 218 V230" />
      <DiagramEdge d="M750 144 C750 180 345 180 345 218 V230" />
      <DiagramEdge d="M750 144 C750 180 560 180 560 218 V230" />
      <DiagramEdge d="M750 144 V230" />
      <DiagramEdge d="M130 312 C130 350 345 350 345 378 V390" kind="control" />
      <DiagramEdge d="M345 312 V390" kind="control" />
      <DiagramEdge d="M750 312 C750 350 730 350 730 378 V390" kind="data" />
      <DiagramSectionLabel label="PLAN DEFINITION AND ORGANIZATION RECONCILIATION" lineTo={872} x={28} y={28} />
      <DiagramNode detail="kube-dc namespace" height={72} title="billing-plans ConfigMap" tone="source" width={240} x={30} y={72} />
      <DiagramNode detail="watches changes" height={72} icon={ControllerIcon} title="Org controller" tone="accent" width={240} x={330} y={72} />
      <DiagramNode detail="assigned plan + addons" height={72} title="Organization" width={240} x={630} y={72} />
      <DiagramNode detail="compute + memory" height={82} title="HRQ" width={190} x={35} y={230} />
      <DiagramNode detail={['default requests', 'and limits']} height={82} title="LimitRange" width={190} x={250} y={230} />
      <DiagramNode detail={['create-time', 'check']} height={82} icon={PublicIcon} title="EIP quota" width={190} x={465} y={230} />
      <DiagramNode detail="rook-ceph" height={82} title="Ceph S3 user" width={190} x={680} y={230} />
      <DiagramNode detail={['all child Project', 'backing namespaces']} height={82} icon={KubernetesIcon} title="Project enforcement" width={280} x={205} y={390} />
      <DiagramNode detail="RGW server-side quota" height={82} icon={DataIcon} title="S3 enforcement" tone="storage" width={280} x={590} y={390} />
    </ExplainerDiagram>
  );
}

export function SubscriptionLifecycleDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Cancellation at period end preserves service until deletion, while immediate deletion enters a suspended grace state; suspended and canceled Organizations can both re-subscribe."
      description="A completed checkout activates an Organization subscription. Cancel-at-period-end changes active to canceling, and subscription deletion at the end of the period changes canceling to canceled. A payment failure or manual immediate cancellation sends active to suspended. After a seven-day grace period, suspended becomes canceled. Re-subscription returns either suspended or canceled to active."
      diagramId="subscription-lifecycle-explainer"
      minWidth={840}
      title="Organization subscription lifecycle"
      viewBox="0 0 840 500"
    >
      <DiagramEdge d="M260 120 H330" label="checkout completed" labelWidth={138} labelX={295} labelY={72} />
      <DiagramEdge d="M510 120 H580" label="cancel at period end" labelWidth={150} labelX={545} labelY={72} />
      <DiagramEdge d="M420 156 C420 188 210 188 210 216 V228" label="subscription.deleted" labelWidth={146} labelX={315} labelY={188} />
      <DiagramEdge d="M670 156 C670 188 630 188 630 216 V228" label="period ends" labelWidth={88} labelX={650} labelY={188} />
      <DiagramEdge d="M300 270 H540" label="7-day grace" labelWidth={100} labelX={420} labelY={246} />
      <DiagramEdge d="M210 310 C210 350 420 350 420 378 V390" label="re-subscribe" labelWidth={104} labelX={315} labelY={350} />
      <DiagramEdge d="M630 310 C630 350 420 350 420 378 V390" label="re-subscribe" labelWidth={104} labelX={525} labelY={350} />
      <DiagramSectionLabel label="BILLING WEBHOOK STATE TRANSITIONS" lineTo={812} x={28} y={28} />
      <DiagramNode detail="event" height={72} title="Checkout" tone="external" width={200} x={60} y={84} />
      <DiagramNode detail="full plan" height={72} title="Active" tone="accent" width={180} x={330} y={84} />
      <DiagramNode detail="service continues" height={72} title="Canceling" width={180} x={580} y={84} />
      <DiagramNode detail="grace · restricted" height={82} title="Suspended" width={180} x={120} y={228} />
      <DiagramNode detail="scaled / blocked" height={82} title="Canceled" width={180} x={540} y={228} />
      <DiagramNode detail="new subscription" height={72} title="Active again" tone="accent" width={240} x={300} y={390} />
    </ExplainerDiagram>
  );
}

export function Metal3TopologyDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Three existing RKE2 servers host the management control plane; Metal3 controllers use BMC and provisioning networks to inspect, provision, join, and remediate the separate bare-metal worker pool."
      description="The Kube-DC management cluster has three RKE2 control-plane servers with OVN database replicas. Metal3 control-plane components include Bare Metal Operator managing BareMetalHost resources, Ironic providing PXE or virtual-media provisioning, CAPM3, and Metal3 IPAM. These components reach server BMCs using IPMI, Redfish, iDRAC, or iLO and provision a pool of RKE2 worker nodes that can run KubeVirt. Management, optional provider, provisioning, and BMC networks remain distinct."
      diagramId="metal3-topology-explainer"
      minWidth={900}
      textScale={1.04}
      title="Metal3 bare-metal worker topology"
      viewBox="0 0 900 750"
    >
      <DiagramEdge d="M450 168 V190" />
      <DiagramEdge d="M450 266 C450 294 250 294 250 322 V334" kind="control" />
      <DiagramEdge d="M450 266 C450 294 650 294 650 322 V334" kind="control" />
      <DiagramEdge d="M250 416 C250 444 390 444 390 472 V484" kind="control" />
      <DiagramEdge d="M650 416 C650 444 510 444 510 472 V484" kind="control" />
      <DiagramEdge d="M450 566 V630" />
      <DiagramBoundary height={540} label="KUBE-DC MANAGEMENT CLUSTER" labelWidth={310} width={860} x={20} y={40} />
      <DiagramNode detail={['master-1 · master-2', 'master-3 · RKE2 + OVN DB']} height={88} icon={KubernetesIcon} title="Control-plane servers" width={360} x={270} y={80} />
      <DiagramNode detail="Cluster API Provider Metal3 + IPAM" height={76} icon={ControllerIcon} title="CAPM3 control" tone="accent" width={420} x={240} y={190} />
      <DiagramNode detail={['BareMetalHost CRs', 'power + inspection']} height={82} icon={ControllerIcon} title="Bare Metal Operator" width={300} x={100} y={334} />
      <DiagramNode detail={['PXE / virtual media', 'image provisioning']} height={82} icon={CloudIcon} title="Ironic" width={300} x={500} y={334} />
      <DiagramNode detail={['IPMI · Redfish', 'iDRAC · iLO']} height={82} icon={NetworkIcon} title="BMC path" tone="external" width={300} x={300} y={484} />
      <DiagramBoundary height={145} kind="network" label="BARE-METAL WORKER POOL" labelWidth={270} width={860} x={20} y={585} />
      <DiagramNode detail={['worker-1 · worker-2 · worker-3 · worker-N', 'RKE2 agents · KubeVirt eligible']} height={82} icon={BareMetalIcon} title="Provisioned workers" width={700} x={100} y={630} />
    </ExplainerDiagram>
  );
}

export function MetalLbHaDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="All control-plane nodes run a speaker, but one elected speaker announces the service IP at a time; traffic arriving on that node is forwarded through Kubernetes service routing to Envoy."
      description="Internet traffic targets one floating public IP. MetalLB speakers run on all three control-plane nodes, and one speaker wins the leader election for that address and announces it on the public layer-two network using ARP. Traffic lands on the announcing node, kube-proxy forwards the LoadBalancer Service, and the request reaches an Envoy Gateway Pod. If that node fails, another speaker announces the address."
      diagramId="metallb-ha-explainer"
      minWidth={840}
      title="MetalLB layer-two failover for Envoy Gateway"
      viewBox="0 0 840 540"
    >
      <DiagramEdge d="M420 116 V160" />
      <DiagramEdge d="M420 242 V320" kind="control" />
      <DiagramEdge d="M420 392 V436" />
      <DiagramSectionLabel label="PUBLIC LOADBALANCER PATH" lineTo={812} x={28} y={28} />
      <DiagramNode detail="external clients" height={68} icon={PublicIcon} title="Internet" tone="external" width={260} x={290} y={48} />
      <DiagramNode detail="X.X.X.X" height={82} icon={NetworkIcon} title="Floating service IP" tone="accent" width={300} x={270} y={160} />
      <DiagramBoundary height={136} kind="network" label="METALLB SPEAKERS · ONE ELECTED ANNOUNCER" labelWidth={390} width={700} x={70} y={266} />
      <DiagramNode detail={['master-0 · master-1 · master-2', 'L2 ARP on public interface']} height={72} title="Control-plane nodes" width={580} x={130} y={320} />
      <DiagramNode detail={['kube-proxy Service path', 'automatic speaker failover']} height={82} icon={ApplicationIcon} title="Envoy Gateway Pod" width={340} x={250} y={436} />
    </ExplainerDiagram>
  );
}

export function InternalEndpointDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Project DNS selects the internal VIP, while a selectorless Service and manager-owned EndpointSlice keep the public hostname and TLS identity unchanged and route only to healthy control-plane backends."
      description="A Managed Cluster or Project workload resolves a platform hostname through vpc-dns to an internal MetalLB VIP on ext-cloud. The VIP fronts an annotated selectorless Service. PlatformEndpointReconciler probes eligible control-plane nodes every five seconds and writes only healthy addresses into a companion EndpointSlice. kube-proxy translates Service traffic to one of those healthy backends for the Kubernetes API or Envoy Gateway."
      diagramId="internal-endpoint-explainer"
      minWidth={900}
      title="Internal platform endpoint pattern"
      viewBox="0 0 900 650"
    >
      <DiagramEdge d="M230 116 H280" />
      <DiagramEdge d="M500 116 H550" />
      <DiagramEdge d="M650 152 C650 180 530 180 530 210 V320 H450 V340" />
      <DiagramEdge d="M700 292 C700 320 450 320 450 328 V340" kind="control" />
      <DiagramEdge d="M450 422 V470" />
      <DiagramEdge d="M450 552 V560" />
      <DiagramSectionLabel label="PROJECT-REACHABLE PLATFORM PATH" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['Managed Cluster', 'or Pod']} height={72} icon={ApplicationIcon} title="Project client" tone="external" width={200} x={30} y={80} />
      <DiagramNode detail="hostname rewrite" height={72} icon={NetworkIcon} title="vpc-dns" width={220} x={280} y={80} />
      <DiagramNode detail="ext-cloud address" height={72} icon={NetworkIcon} title="MetalLB internal VIP" tone="accent" width={300} x={550} y={80} />
      <DiagramBoundary height={232} kind="network" label="SELECTORLESS SERVICE ENDPOINT" labelWidth={300} width={820} x={40} y={190} />
      <DiagramNode detail="no Pod selector" height={82} icon={ApiIcon} title="Platform Service" width={300} x={300} y={340} />
      <DiagramNode detail={['health probes every 5s', 'manager-owned addresses']} height={82} icon={ControllerIcon} title="EndpointSlice" width={300} x={550} y={210} />
      <DiagramNode detail="healthy endpoint DNAT" height={82} icon={NetworkIcon} title="kube-proxy" width={300} x={300} y={470} />
      <DiagramNode detail={['control-plane nodes', 'API :6443 or Envoy :443']} height={82} title="Healthy backends" width={500} x={200} y={560} />
    </ExplainerDiagram>
  );
}

export function EtcdEncryptionDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The apiserver encrypts Kubernetes objects locally and sends only envelope-key operations over the KMS v2 UDS to the sidecar; etcd receives encrypted rows and OpenBao wraps keys over HTTPS."
      description="Inside each Kamaji TenantControlPlane Pod, kube-apiserver uses its EncryptionConfiguration to call the kube-dc KMS plugin sidecar over a Unix Domain Socket using Kubernetes KMS v2. The apiserver writes and reads encrypted object data from its Kamaji-managed etcd StatefulSet. The plugin authenticates to the Organization OpenBao namespace and uses the Transit engine over HTTPS to encrypt and decrypt data-encryption keys under the selected key-encryption key."
      diagramId="etcd-encryption-explainer"
      minWidth={840}
      title="Managed Cluster etcd encryption architecture"
      viewBox="0 0 840 500"
    >
      <DiagramEdge bidirectional d="M360 180 H480" kind="control" label="KMS v2 · UDS" labelWidth={104} labelX={420} labelY={154} />
      <DiagramEdge bidirectional d="M230 222 V330" kind="data" label="encrypted rows" labelWidth={112} labelX={230} labelY={280} />
      <DiagramEdge bidirectional d="M610 222 V330" kind="control" label="HTTPS Transit" labelWidth={108} labelX={610} labelY={280} />
      <DiagramBoundary height={400} label="KAMAJI TENANTCONTROLPLANE POD" labelWidth={320} width={800} x={20} y={50} />
      <DiagramNode detail="EncryptionConfiguration" height={92} icon={ApiIcon} title="kube-apiserver" tone="accent" width={300} x={60} y={130} />
      <DiagramNode detail={['native sidecar', 'KMS v2 gRPC server']} height={92} icon={SecurityIcon} title="kms-plugin" width={300} x={480} y={130} />
      <DiagramNode detail={['per-cluster StatefulSet', 'ciphertext at rest']} height={92} icon={DataIcon} title="Kamaji etcd" tone="storage" width={300} x={80} y={330} />
      <DiagramNode detail={['Organization namespace', 'Transit key / KEK']} height={92} icon={SecurityIcon} title="OpenBao" width={300} x={460} y={330} />
    </ExplainerDiagram>
  );
}

export function ExternalNetworksDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The sample uses one bonded carrier with two tagged provider segments; VLAN IDs, CIDRs, and the bond name are installation examples rather than universal requirements."
      description="A physical bonded interface named bond0 carries two example external network segments. VLAN 200 provides the ext-cloud ProviderNetwork and subnet 100.65.0.0/16. VLAN 300 provides the ext-public ProviderNetwork and subnet 192.0.2.0/28. Eligible nodes attach the configured segments to OVS; untagged and other physical layouts are also supported when configured explicitly."
      diagramId="external-networks-explainer"
      minWidth={840}
      title="Cloud and public external network example"
      viewBox="0 0 840 390"
    >
      <DiagramEdge d="M420 142 C420 178 210 178 210 206 V218" kind="data" />
      <DiagramEdge d="M420 142 C420 178 630 178 630 206 V218" kind="data" />
      <DiagramSectionLabel label="EXAMPLE NODE PROVIDER ATTACHMENT" lineTo={812} x={28} y={28} />
      <DiagramNode detail="bond0 · carrier" height={82} icon={BareMetalIcon} title="Physical interface" tone="external" width={300} x={270} y={60} />
      <DiagramNode detail={['VLAN 200', '100.65.0.0/16 · ext-cloud']} height={92} icon={CloudIcon} title="Cloud network" width={340} x={40} y={218} />
      <DiagramNode detail={['VLAN 300', '192.0.2.0/28 · ext-public']} height={92} icon={PublicIcon} title="Public network" width={340} x={460} y={218} />
    </ExplainerDiagram>
  );
}

export function ImageCatalogDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Chart configuration drives one scheduled mirror; verified image objects and manifests in Rook are summarized into the catalog ConfigMap consumed by the backend and VM-creation UI."
      description="Per-family osImages.catalog and discovery values render the images-configmap. The weekly cdi-os-mirror-refresh CronJob reads that configuration, downloads and verifies upstream images, and uploads versioned QCOW2 objects, manifests, latest aliases, and pointers to the cdi-os-images bucket in Rook RGW. After a successful run it publishes schema-v2 cdi-os-catalog data in the kube-dc namespace. kube-dc-backend reads that catalog and serves the create-VM user interface with fallback behavior documented on the page."
      diagramId="image-catalog-explainer"
      minWidth={900}
      title="Kube-DC operating-system image catalog"
      viewBox="0 0 900 680"
    >
      <DiagramEdge d="M250 116 H325" kind="control" />
      <DiagramEdge d="M575 116 H650" kind="control" />
      <DiagramEdge d="M760 157 C760 190 675 190 675 218 V230" kind="data" />
      <DiagramEdge d="M675 322 C675 356 450 356 450 378 V390" kind="control" />
      <DiagramEdge d="M450 472 V520" />
      <DiagramEdge d="M450 592 V610" />
      <DiagramSectionLabel label="CONFIGURE · MIRROR · PUBLISH · CONSUME" lineTo={872} x={28} y={28} />
      <DiagramNode detail="osImages.catalog" height={72} icon={GitIcon} title="Chart values" tone="source" width={220} x={30} y={80} />
      <DiagramNode detail="per-family inputs" height={72} title="images-configmap" width={250} x={325} y={80} />
      <DiagramNode detail={['weekly refresh', 'download + verify']} height={82} icon={ControllerIcon} title="Mirror CronJob" tone="accent" width={220} x={650} y={75} />
      <DiagramNode detail={['versioned QCOW2 + manifests', 'latest aliases + pointers']} height={92} icon={DataIcon} title="Rook image bucket" tone="storage" width={350} x={500} y={230} />
      <DiagramNode detail={['schema v2 · multi-version', 'kube-dc namespace']} height={82} title="cdi-os-catalog" width={300} x={300} y={390} />
      <DiagramNode detail="create-VM image API" height={72} icon={ApiIcon} title="kube-dc-backend" width={300} x={300} y={520} />
      <DiagramCallout detail="Catalog versions appear in the UI; mirror status stays operational." height={58} title="VM creation UI" width={620} x={140} y={610} />
    </ExplainerDiagram>
  );
}

export function OsImageOperationsDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="One verified mirror feeds three tenant image modes: containerdisks, filesystem goldens, and converted block goldens; the manager seeds per-Project clones for fast creation and migration-capable profiles."
      description="The weekly cdi-os-mirror downloads upstream cloud images into the cdi-os-images S3 bucket. From the same verified sources, it builds digest-pinned containerdisk images in the in-cluster zot depot and feeds filesystem golden DataImportCrons or one-shot DataVolumes. Filesystem goldens produce snapshots. An in-cluster converter imports, converts, and snapshots block-format goldens. The manager's periodic Project seeder copies the applicable golden snapshots into Projects, enabling instant clone and, for block goldens, live migration."
      diagramId="os-image-operations-explainer"
      minWidth={900}
      textScale={1.04}
      title="Operating-system image supply pipeline"
      viewBox="0 0 900 760"
    >
      <DiagramEdge d="M230 110 H300" kind="data" />
      <DiagramEdge d="M600 110 H670" kind="data" />
      <DiagramEdge d="M450 146 C450 188 150 188 150 216 V228" kind="data" />
      <DiagramEdge d="M450 146 V228" kind="data" />
      <DiagramEdge d="M450 146 C450 188 750 188 750 216 V228" kind="data" />
      <DiagramEdge d="M450 320 V370" kind="data" />
      <DiagramEdge d="M750 320 C750 344 540 344 540 358 V370" kind="control" />
      <DiagramEdge d="M450 452 V510" kind="control" />
      <DiagramEdge d="M360 320 C250 350 250 474 360 498 V510" kind="control" />
      <DiagramEdge d="M450 592 V650" />
      <DiagramSectionLabel label="VERIFIED IMAGE SOURCES TO PROJECT-READY ARTIFACTS" lineTo={872} x={28} y={28} />
      <DiagramNode detail="cloud images" height={72} icon={CloudIcon} title="Upstream" tone="external" width={200} x={30} y={74} />
      <DiagramNode detail="weekly verify + mirror" height={72} icon={ControllerIcon} title="cdi-os-mirror" tone="accent" width={300} x={300} y={74} />
      <DiagramNode detail="cdi-os-images" height={72} icon={DataIcon} title="S3 mirror" tone="storage" width={200} x={670} y={74} />
      <DiagramNode detail={['zot depot', 'digest-pinned images']} height={92} title="Containerdisks" width={240} x={30} y={228} />
      <DiagramNode detail={['DataImportCron / DV', 'filesystem snapshots']} height={92} icon={DataIcon} title="FS goldens" width={280} x={310} y={228} />
      <DiagramNode detail={['import · convert', 'snapshot']} height={92} icon={ControllerIcon} title="Converter" width={240} x={630} y={228} />
      <DiagramNode detail="RWX Block snapshots" height={82} icon={DataIcon} title="Block goldens" width={300} x={300} y={370} />
      <DiagramNode detail="manager · 15m resync" height={82} icon={ControllerIcon} title="Per-Project seeder" tone="accent" width={320} x={290} y={510} />
      <DiagramNode detail={['instant clone', 'live migration where eligible']} height={82} icon={ComputeIcon} title="Project VM images" width={360} x={270} y={650} />
    </ExplainerDiagram>
  );
}

export function SsoArchitectureDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="The shared sso realm owns the one Google integration and broker client; Organization realms remain the token issuers and preserve Organization-specific sessions and permissions."
      description="A central Keycloak realm named sso contains the Google identity provider with email auto-linking, the kube-dc console client, the sso-broker client, passwordless registration with email verification, and Organization membership groups under /orgs. Organization-specific realms such as shalb, acme, and foo configure the sso realm as an OIDC identity provider. Authentication is brokered through sso, but tokens for Organization access are issued by the selected Organization realm."
      diagramId="sso-architecture-explainer"
      minWidth={900}
      title="Central Google SSO with Organization realms"
      viewBox="0 0 900 580"
    >
      <DiagramEdge d="M220 150 H300" />
      <DiagramEdge d="M600 150 H680" kind="control" />
      <DiagramEdge d="M450 260 C450 298 155 298 155 326 V338" kind="control" />
      <DiagramEdge d="M450 260 V338" kind="control" />
      <DiagramEdge d="M450 260 C450 298 745 298 745 326 V338" kind="control" />
      <DiagramBoundary height={245} label="KEYCLOAK REALM · SSO" labelWidth={230} width={860} x={20} y={40} />
      <DiagramNode detail={['OAuth', 'auto-link']} height={82} icon={UsersIcon} title="Google IdP" tone="external" width={200} x={20} y={109} />
      <DiagramNode detail={['console client', 'broker client · groups']} height={92} icon={KeycloakIcon} title="Central sso realm" tone="accent" width={300} x={300} y={104} />
      <DiagramNode detail="email verification" height={82} title="Registration" width={200} x={680} y={109} />
      <DiagramSectionLabel label="OIDC BROKERING TO TOKEN-ISSUING REALMS" lineTo={872} x={28} y={310} />
      <DiagramNode detail={['local users', 'SSO IdP']} height={82} icon={KeycloakIcon} title="Realm: shalb" width={250} x={30} y={338} />
      <DiagramNode detail={['local users', 'SSO IdP']} height={82} icon={KeycloakIcon} title="Realm: acme" width={250} x={325} y={338} />
      <DiagramNode detail={['local users', 'SSO IdP']} height={82} icon={KeycloakIcon} title="Realm: foo" width={250} x={620} y={338} />
      <DiagramCallout detail="The selected Organization realm issues the console and Kubernetes token." height={64} title="Organization isolation remains" width={660} x={120} y={480} />
    </ExplainerDiagram>
  );
}

export function SsoUserJourneyDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Registration and login are distinct paths: registration verifies identity before Organization selection, while Google login brokers an existing identity into the selected Organization realm and console session."
      description="For self-service registration, a user enters email and name without a password, verifies the email, chooses to create or join an Organization, sets a password only when creating an Organization, and is redirected to the console. For Google SSO login, the user starts from the Organization console, authenticates with Google, the central sso realm auto-links by email, the identity is brokered to the Organization realm, that realm issues the token, and the user returns to the console."
      diagramId="sso-user-journey-explainer"
      minWidth={900}
      textScale={1.04}
      title="Self-service registration and Google SSO login"
      viewBox="0 0 900 650"
    >
      <DiagramEdge d="M190 166 H215" />
      <DiagramEdge d="M385 166 H410" />
      <DiagramEdge d="M590 166 H615" />
      <DiagramEdge d="M785 166 C820 166 820 256 700 256 V268" />
      <DiagramEdge d="M190 446 H215" />
      <DiagramEdge d="M385 446 H410" />
      <DiagramEdge d="M590 446 H615" />
      <DiagramEdge d="M785 446 C820 446 820 536 700 536 V548" />
      <DiagramSectionLabel label="SELF-SERVICE REGISTRATION" lineTo={872} x={28} y={28} />
      <DiagramNode detail={['email', 'name']} height={82} icon={UsersIcon} title="Sign up" width={160} x={30} y={125} />
      <DiagramNode detail="email link" height={82} title="Verify" width={170} x={215} y={125} />
      <DiagramNode detail="create or join" height={82} title="Organization" width={180} x={410} y={125} />
      <DiagramNode detail={['only on create', 'set password']} height={82} title="Complete profile" width={170} x={615} y={125} />
      <DiagramNode detail="Organization ready" height={72} icon={ApplicationIcon} title="Console" tone="accent" width={260} x={570} y={268} />
      <DiagramSectionLabel label="GOOGLE SSO LOGIN" lineTo={872} x={28} y={350} />
      <DiagramNode detail={['Org', 'login']} height={82} icon={ApplicationIcon} title="Console" width={160} x={30} y={405} />
      <DiagramNode detail="OAuth" height={82} icon={UsersIcon} title="Google" tone="external" width={170} x={215} y={405} />
      <DiagramNode detail={['email', 'auto-link']} height={82} icon={KeycloakIcon} title="sso realm" width={180} x={410} y={405} />
      <DiagramNode detail={['broker', 'token']} height={82} icon={KeycloakIcon} title="Org realm" width={170} x={615} y={405} />
      <DiagramNode detail="authenticated session" height={72} icon={ApplicationIcon} title="Console" tone="accent" width={260} x={570} y={548} />
    </ExplainerDiagram>
  );
}
