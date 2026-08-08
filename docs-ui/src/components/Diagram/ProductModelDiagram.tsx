import React from 'react';

import {
  DiagramBoundary,
  DiagramCallout,
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

/**
 * Art-directed equivalent of the product hierarchy in
 * docs/platform/architecture-overview.md. Keep the relationships synchronized
 * with the legacy Mermaid diagram while the comparison is under review.
 */
export default function ProductModelDiagram(): React.JSX.Element {
  return (
    <ExplainerDiagram
      diagramId="product-model-explainer"
      title="Kube-DC product hierarchy"
      description="A management cluster contains the acme and example Organizations. The acme Organization contains production and development Projects. The production Project contains virtual machines, container workloads, databases and object storage, and a Managed Cluster."
      caption={
        <>
          Kube-DC product hierarchy. Organizations are tenant boundaries;
          Projects are governed workload boundaries inside an Organization.
        </>
      }
      viewBox="0 0 840 702"
      minWidth={840}
    >
      {/* Relationship layer: keep every connector behind labels and cards. */}
      <DiagramEdge d="M 420 104 V 116" directed={false} />
      <DiagramEdge d="M 420 116 C 380 116, 205 126, 205 140 V 150" />
      <DiagramEdge d="M 420 116 C 460 116, 635 126, 635 140 V 150" />
      <DiagramEdge d="M 205 216 V 234" directed={false} />
      <DiagramEdge d="M 205 234 C 205 250, 205 260, 205 272 V 282" />
      <DiagramEdge d="M 205 234 C 260 234, 635 250, 635 272 V 282" />
      <DiagramEdge
        d="M 205 364 V 442"
        directed={false}
        label="contains"
        labelX={205}
        labelY={389}
        labelWidth={76}
      />
      <DiagramEdge d="M 205 442 C 180 442, 126 460, 126 480 V 492" />
      <DiagramEdge d="M 205 442 C 230 442, 324 460, 324 480 V 492" />
      <DiagramEdge d="M 205 442 C 280 442, 522 460, 522 480 V 492" />
      <DiagramEdge d="M 205 442 C 330 442, 720 460, 720 480 V 492" />

      {/* Annotation layer: text masks relationships that pass behind it. */}
      <DiagramBoundary
        x={20}
        y={404}
        width={800}
        height={200}
        label="PROJECT: PRODUCTION · WORKLOAD CAPABILITIES"
        labelWidth={430}
      />
      <DiagramSectionLabel x={28} y={24} label="PLATFORM" lineTo={812} />
      <DiagramSectionLabel x={28} y={134} label="TENANCY" lineTo={812} />
      <DiagramSectionLabel x={28} y={266} label="GOVERNED WORKLOAD BOUNDARIES" lineTo={812} />

      {/* Card layer. */}
      <DiagramNode
        x={300}
        y={38}
        width={240}
        title="Management cluster"
        detail="contains Organizations"
        tone="accent"
      />

      <DiagramNode
        x={60}
        y={150}
        width={290}
        title="Organization: acme"
        detail="identity · membership · quota"
        tone="accent"
      />
      <DiagramNode
        x={490}
        y={150}
        width={290}
        title="Organization: example"
        detail="independent tenant boundary"
        tone="muted"
      />

      <DiagramNode
        x={60}
        y={282}
        width={290}
        height={82}
        title="Project: production"
        detail={['backing namespace', 'dedicated VPC']}
        tone="accent"
      />
      <DiagramNode
        x={490}
        y={282}
        width={290}
        height={82}
        title="Project: development"
        detail={['backing namespace', 'dedicated VPC']}
      />

      <DiagramNode
        x={34}
        y={492}
        width={184}
        height={88}
        title="Virtual machines"
        detail="Project workload"
      />
      <DiagramNode
        x={232}
        y={492}
        width={184}
        height={88}
        title="Containers"
        detail="Project workload"
      />
      <DiagramNode
        x={430}
        y={492}
        width={184}
        height={88}
        title="Data services"
        detail={['databases', 'object storage']}
        tone="storage"
      />
      <DiagramNode
        x={628}
        y={492}
        width={184}
        height={88}
        title="Managed Cluster"
        detail={['separate', 'Kubernetes API']}
      />

      <DiagramCallout
        x={40}
        y={616}
        width={760}
        height={68}
        title="Organization governance and Project isolation are different boundaries."
        detail="A Managed Cluster retains its own API, authorization, and CNI boundaries."
      />
    </ExplainerDiagram>
  );
}
