import type {ComponentType, SVGProps} from 'react';

import {
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  ExplainerDiagram,
} from './index';

type FlowTone = 'default' | 'accent' | 'muted' | 'storage' | 'external' | 'source';
type FlowKind = 'primary' | 'asynchronous' | 'data' | 'control';

export interface FlowStep {
  title: string;
  detail?: string | string[];
  icon?: ComponentType<SVGProps<SVGSVGElement>>;
  tone?: FlowTone;
}

export interface FlowRelationship {
  label?: string;
  labelWidth?: number;
  kind?: FlowKind;
  directed?: boolean;
  bidirectional?: boolean;
}

interface LinearFlowDiagramProps {
  diagramId: string;
  title: string;
  description: string;
  caption: string;
  sectionLabel: string;
  steps: FlowStep[];
  relationships?: FlowRelationship[];
  minWidth?: number;
}

/** Compact art-directed flow for documentation paths with two to six stages. */
export function LinearFlowDiagram({
  diagramId,
  title,
  description,
  caption,
  sectionLabel,
  steps,
  relationships = [],
  minWidth = 900,
}: LinearFlowDiagramProps): React.JSX.Element {
  const gap = steps.length >= 5 ? 18 : 28;
  const maximumWidth = steps.length >= 5 ? 160 : 230;
  const nodeWidth = Math.min(
    maximumWidth,
    (880 - gap * (steps.length - 1)) / steps.length,
  );
  const compositionWidth = nodeWidth * steps.length + gap * (steps.length - 1);
  const startX = (900 - compositionWidth) / 2;
  const y = 92;
  const height = 88;

  return (
    <ExplainerDiagram
      caption={caption}
      description={description}
      diagramId={diagramId}
      minWidth={minWidth}
      title={title}
      viewBox="0 0 900 250"
    >
      {steps.slice(0, -1).map((_step, index) => {
        const relation = relationships[index] || {};
        const x1 = startX + nodeWidth * (index + 1) + gap * index;
        const x2 = x1 + gap;
        const labelX = (x1 + x2) / 2;
        return (
          <DiagramEdge
            bidirectional={relation.bidirectional}
            d={`M${x1} ${y + height / 2} H${x2}`}
            directed={relation.directed}
            key={`${diagramId}-edge-${index}`}
            kind={relation.kind}
            label={relation.label}
            labelWidth={relation.labelWidth}
            labelX={labelX}
            labelY={y + 20}
          />
        );
      })}
      <DiagramSectionLabel label={sectionLabel} lineTo={872} x={28} y={28} />
      {steps.map((step, index) => (
        <DiagramNode
          detail={step.detail}
          height={height}
          icon={step.icon}
          key={`${diagramId}-node-${step.title}`}
          title={step.title}
          tone={step.tone}
          width={nodeWidth}
          x={startX + index * (nodeWidth + gap)}
          y={y}
        />
      ))}
    </ExplainerDiagram>
  );
}
