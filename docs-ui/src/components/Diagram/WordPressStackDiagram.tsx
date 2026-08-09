import ApplicationIcon from '../../../../docs/diagrams/icons/application-web.svg';
import DataIcon from '../../../../docs/diagrams/icons/data-service.svg';
import NetworkIcon from '../../../../docs/diagrams/icons/network-service.svg';
import PublicIcon from '../../../../docs/diagrams/icons/network-public.svg';
import {
  DiagramBoundary,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

export default function WordPressStackDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      caption="Gateway traffic fans out through the WordPress Service, while both Pods share persistent storage and database credentials; database and file backups land in separate Project buckets."
      description="A browser sends HTTPS traffic with a certificate to the platform Gateway, which forwards to the wordpress Service in the Project. The Service targets two WordPress Pods. Both Pods use a shared rbd-vm volume and managed MariaDB credentials. The database writes daily backups to the Project S3 bucket, while a backup Job reads the shared volume and archives wp-content to a separate wordpress-files bucket."
      diagramId="wordpress-stack-explainer"
      minWidth={840}
      title="WordPress services in a Kube-DC Project"
      viewBox="0 0 840 610"
    >
      <DiagramEdge d="M190 96 H350" label="HTTPS + certificate" labelWidth={142} labelX={270} labelY={101} />
      <DiagramEdge d="M580 96 H610" />
      <DiagramEdge d="M710 132 C710 164 320 164 320 192 V204" />
      <DiagramEdge d="M710 132 C710 164 570 164 570 192 V204" />
      <DiagramEdge d="M320 286 C320 320 420 320 420 348 V360" directed={false} kind="data" />
      <DiagramEdge d="M570 286 C570 320 420 320 420 348 V360" directed={false} kind="data" />
      <DiagramEdge d="M320 286 C320 344 680 344 680 378 V390" label="credentials" labelWidth={92} labelX={570} labelY={341} />
      <DiagramEdge d="M570 286 C570 344 680 344 680 378 V390" />
      <DiagramEdge d="M170 432 H310" directed={false} kind="data" label="reads volume" labelWidth={98} labelX={240} labelY={410} />
      <DiagramEdge d="M680 472 C680 514 565 514 565 538 V550" kind="data" label="daily backups" labelWidth={108} labelX={635} labelY={520} />
      <DiagramEdge d="M170 474 C170 514 285 514 285 538 V550" kind="data" label="wp-content archive" labelWidth={134} labelX={235} labelY={520} />

      <DiagramSectionLabel label="PUBLIC REQUEST PATH" lineTo={812} x={28} y={28} />
      <DiagramNode detail={['external', 'client']} height={72} icon={PublicIcon} title="Browser" tone="external" width={160} x={30} y={60} />
      <DiagramNode detail="TLS entry point" height={72} icon={NetworkIcon} title="Platform Gateway" tone="accent" width={230} x={350} y={60} />
      <DiagramNode detail="wordpress" height={72} title="Service" width={200} x={610} y={60} />

      <DiagramBoundary height={350} label="YOUR PROJECT · APPLICATION AND DATA" labelWidth={340} width={800} x={20} y={170} />
      <DiagramNode detail="replica 1" height={82} icon={ApplicationIcon} title="WordPress Pod" width={220} x={210} y={204} />
      <DiagramNode detail="replica 2" height={82} icon={ApplicationIcon} title="WordPress Pod" width={220} x={460} y={204} />
      <DiagramNode detail="rbd-vm" height={82} icon={DataIcon} title="Shared volume" tone="storage" width={220} x={310} y={360} />
      <DiagramNode detail={['wp-content', 'backup']} height={82} title="Backup Job" width={140} x={30} y={390} />
      <DiagramNode detail="managed credentials" height={82} icon={DataIcon} title="Managed MariaDB" width={230} x={565} y={390} />
      <DiagramNode detail="wordpress-files" height={60} icon={DataIcon} title="File bucket" tone="storage" width={230} x={170} y={550} />
      <DiagramNode detail="database backups" height={60} icon={DataIcon} title="Project S3 bucket" tone="storage" width={230} x={450} y={550} />
    </ExplainerDiagram>
  );
}
