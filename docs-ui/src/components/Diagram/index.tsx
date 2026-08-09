import React, {
  createContext,
  useContext,
  useId,
  type ComponentType,
  type CSSProperties,
  type ReactNode,
  type SVGProps,
} from 'react';
import clsx from 'clsx';

import styles from './styles.module.css';

type RelationshipKind = 'primary' | 'asynchronous' | 'data' | 'control';
export type EdgeTone = 'default' | 'muted' | 'data' | 'success' | 'warning' | 'danger';
type NodeTone = 'default' | 'accent' | 'muted' | 'storage' | 'external' | 'source';
type CalloutTone = 'info' | 'warning' | 'danger';

interface DiagramContextValue {
  markerIds: Record<EdgeTone, string>;
  textScale: number;
}

const DiagramContext = createContext<DiagramContextValue | null>(null);

function useDiagram(): DiagramContextValue {
  const value = useContext(DiagramContext);
  if (!value) {
    throw new Error('Diagram primitives must be rendered inside ExplainerDiagram.');
  }
  return value;
}

export interface ExplainerDiagramProps {
  diagramId: string;
  title: string;
  description: string;
  caption: ReactNode;
  viewBox: string;
  children: ReactNode;
  className?: string;
  minWidth?: number;
  textScale?: number;
}

/**
 * Responsive, print-safe frame for art-directed documentation diagrams.
 *
 * Geometry remains explicit in the child primitives. This is intentional:
 * explainer diagrams use composition to communicate hierarchy and emphasis,
 * while D2 remains the default when automatic topology layout is preferable.
 */
export function ExplainerDiagram({
  diagramId,
  title,
  description,
  caption,
  viewBox,
  children,
  className,
  minWidth = 900,
  textScale = 1,
}: ExplainerDiagramProps): React.JSX.Element {
  const reactId = useId().replace(/:/g, '');
  const titleId = `${reactId}-title`;
  const descriptionId = `${reactId}-description`;
  const markerIds: DiagramContextValue['markerIds'] = {
    default: `${reactId}-arrow-default`,
    muted: `${reactId}-arrow-muted`,
    data: `${reactId}-arrow-data`,
    success: `${reactId}-arrow-success`,
    warning: `${reactId}-arrow-warning`,
    danger: `${reactId}-arrow-danger`,
  };
  const viewportStyle = {
    '--diagram-min-width': `${minWidth}px`,
    '--diagram-title-size': `${17 * textScale}px`,
    '--diagram-detail-size': `${13.5 * textScale}px`,
    '--diagram-callout-title-size': `${14 * textScale}px`,
  } as CSSProperties;

  return (
    <figure
      className={clsx(styles.figure, className)}
      data-diagram-explainer="true"
      data-diagram-id={diagramId}
    >
      <div
        className={styles.viewport}
        data-diagram-scroll="true"
        style={viewportStyle}
        tabIndex={0}
        aria-label={`Scrollable diagram: ${title}`}
      >
        <svg
          className={styles.canvas}
          viewBox={viewBox}
          role="img"
          aria-labelledby={`${titleId} ${descriptionId}`}
          preserveAspectRatio="xMidYMid meet"
        >
          <title id={titleId}>{title}</title>
          <desc id={descriptionId}>{description}</desc>
          <defs>
            {Object.entries(markerIds).map(([tone, id]) => (
              <marker
                id={id}
                key={id}
                className={styles[`marker${tone[0].toUpperCase()}${tone.slice(1)}`]}
                viewBox="0 0 10 10"
                refX="10"
                refY="5"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
                markerUnits="strokeWidth"
              >
                <path d="M 0 0 L 10 5 L 0 10 z" />
              </marker>
            ))}
          </defs>
          <DiagramContext.Provider value={{markerIds, textScale}}>
            {children}
          </DiagramContext.Provider>
        </svg>
      </div>
      <figcaption className={styles.caption}>{caption}</figcaption>
    </figure>
  );
}

export interface DiagramNodeProps {
  x: number;
  y: number;
  width: number;
  height?: number;
  title: string;
  detail?: string | string[];
  icon?: ComponentType<SVGProps<SVGSVGElement>>;
  tone?: NodeTone;
  badge?: string;
}

export function DiagramNode({
  x,
  y,
  width,
  height = 66,
  title,
  detail,
  icon: Icon,
  tone = 'default',
  badge,
}: DiagramNodeProps): React.JSX.Element {
  const {textScale} = useDiagram();
  const detailLines = detail
    ? Array.isArray(detail) ? detail : [detail]
    : [];
  const textX = x + (Icon ? 58 : 18);

  return (
    <g
      className={clsx(styles.node, styles[`node${tone[0].toUpperCase()}${tone.slice(1)}`])}
      data-diagram-node="true"
    >
      <rect x={x} y={y} width={width} height={height} rx="5" />
      {Icon && (
        <Icon
          aria-hidden="true"
          className={styles.nodeIcon}
          focusable="false"
          height="28"
          width="28"
          x={x + 16}
          y={y + (height - 28) / 2}
        />
      )}
      <text className={styles.nodeTitle} x={textX} y={y + (detailLines.length ? 27 : height / 2 + 6)}>
        {title}
      </text>
      {detailLines.length > 0 && (
        <text className={styles.nodeDetail} x={textX} y={y + 50}>
          {detailLines.map((line, index) => (
            <tspan key={line} x={textX} dy={index === 0 ? 0 : 17 * textScale}>
              {line}
            </tspan>
          ))}
        </text>
      )}
      {badge && (
        <text className={styles.nodeBadge} x={x + width - 16} y={y + 27} textAnchor="end">
          {badge}
        </text>
      )}
    </g>
  );
}

export interface DiagramEdgeProps {
  d: string;
  kind?: RelationshipKind;
  directed?: boolean;
  bidirectional?: boolean;
  label?: string;
  labelX?: number;
  labelY?: number;
  labelWidth?: number;
  labelAnchor?: 'start' | 'middle' | 'end';
  tone?: EdgeTone;
  allowDetachedStart?: boolean;
  allowDetachedEnd?: boolean;
}

export function DiagramEdge({
  d,
  kind = 'primary',
  directed = true,
  bidirectional = false,
  label,
  labelX = 0,
  labelY = 0,
  labelWidth = 90,
  labelAnchor = 'middle',
  tone = 'default',
  allowDetachedStart = false,
  allowDetachedEnd = false,
}: DiagramEdgeProps): React.JSX.Element {
  const {markerIds, textScale} = useDiagram();
  const labelLeft = labelAnchor === 'middle'
    ? labelX - labelWidth / 2
    : labelAnchor === 'end'
      ? labelX - labelWidth
      : labelX;
  const labelHeight = 22 * textScale;

  return (
    <g
      className={clsx(
        styles.edge,
        styles[`edge${kind[0].toUpperCase()}${kind.slice(1)}`],
        styles[`edgeTone${tone[0].toUpperCase()}${tone.slice(1)}`],
      )}
      data-diagram-edge="true"
      data-edge-allow-detached-end={allowDetachedEnd ? 'true' : undefined}
      data-edge-allow-detached-start={allowDetachedStart ? 'true' : undefined}
    >
      <path
        data-diagram-edge-path="true"
        d={d}
        markerStart={bidirectional ? `url(#${markerIds[tone]})` : undefined}
        markerEnd={directed ? `url(#${markerIds[tone]})` : undefined}
      />
      {label && (
        <g className={styles.edgeLabel}>
          <rect x={labelLeft} y={labelY - 15 * textScale} width={labelWidth} height={labelHeight} rx="3" />
          <text x={labelX} y={labelY} textAnchor={labelAnchor}>
            {label}
          </text>
        </g>
      )}
    </g>
  );
}

export interface DiagramBoundaryProps {
  x: number;
  y: number;
  width: number;
  height: number;
  label: string;
  labelWidth?: number;
  kind?: 'logical' | 'network';
}

export function DiagramBoundary({
  x,
  y,
  width,
  height,
  label,
  labelWidth = 360,
  kind = 'logical',
}: DiagramBoundaryProps): React.JSX.Element {
  return (
    <g
      className={clsx(styles.boundary, kind === 'network' && styles.boundaryNetwork)}
      data-diagram-boundary="true"
    >
      <rect x={x} y={y} width={width} height={height} rx="6" />
      <rect
        className={styles.boundaryLabelBackground}
        x={x + 12}
        y={y + 9}
        width={labelWidth}
        height="26"
        rx="3"
      />
      <text data-diagram-boundary-label="true" x={x + 18} y={y + 27}>{label}</text>
    </g>
  );
}

export interface DiagramSectionLabelProps {
  x: number;
  y: number;
  label: string;
  lineTo?: number;
}

export function DiagramSectionLabel({
  x,
  y,
  label,
  lineTo,
}: DiagramSectionLabelProps): React.JSX.Element {
  return (
    <g className={styles.sectionLabel} data-diagram-section-label="true">
      <text x={x} y={y}>{label}</text>
      {lineTo && <line x1={x} y1={y + 12} x2={lineTo} y2={y + 12} />}
    </g>
  );
}

export interface DiagramCalloutProps {
  x: number;
  y: number;
  width: number;
  height: number;
  title: string;
  detail?: string;
  tone?: CalloutTone;
}

export function DiagramCallout({
  x,
  y,
  width,
  height,
  title,
  detail,
  tone = 'info',
}: DiagramCalloutProps): React.JSX.Element {
  return (
    <g
      className={clsx(styles.callout, styles[`callout${tone[0].toUpperCase()}${tone.slice(1)}`])}
      data-diagram-callout="true"
    >
      <rect x={x} y={y} width={width} height={height} rx="5" />
      <text className={styles.calloutTitle} x={x + 18} y={y + 27}>{title}</text>
      {detail && <text className={styles.calloutDetail} x={x + 18} y={y + 50}>{detail}</text>}
    </g>
  );
}
