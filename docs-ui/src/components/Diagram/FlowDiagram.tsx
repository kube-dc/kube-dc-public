import type {ComponentType, SVGProps} from 'react';

import {
  DiagramEdge,
  DiagramNode,
  DiagramSectionLabel,
  type EdgeTone,
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
  tone?: EdgeTone;
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
  const baseGap = steps.length >= 5 ? 18 : 28;
  const labelledGaps = steps.slice(0, -1).map((_step, index) => {
    const relationship = relationships[index];
    return relationship?.label
      ? Math.max(baseGap, (relationship.labelWidth || 90) + 24)
      : baseGap;
  });
  const maximumWidth = steps.length >= 5 ? 160 : 230;
  const availableWidth = 872;
  const inlineNodeWidth = Math.min(
    maximumWidth,
    (availableWidth - labelledGaps.reduce((total, gap) => total + gap, 0))
      / steps.length,
  );
  const labelsAbove = steps.length >= 4 && inlineNodeWidth < 190;
  const gaps = labelsAbove
    ? steps.slice(0, -1).map(() => baseGap)
    : labelledGaps;
  const nodeWidth = Math.min(
    maximumWidth,
    (availableWidth - gaps.reduce((total, gap) => total + gap, 0)) / steps.length,
  );
  const compositionWidth = nodeWidth * steps.length
    + gaps.reduce((total, gap) => total + gap, 0);
  const startX = (900 - compositionWidth) / 2;
  const nodeX = (index: number) => startX + index * nodeWidth
    + gaps.slice(0, index).reduce((total, gap) => total + gap, 0);
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
        const x1 = nodeX(index) + nodeWidth;
        const x2 = nodeX(index + 1);
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
            labelY={labelsAbove ? 72 : y + height / 2 + 5}
            tone={relation.tone}
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
          x={nodeX(index)}
          y={y}
        />
      ))}
    </ExplainerDiagram>
  );
}
