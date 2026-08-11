import React from 'react';
import styles from './styles.module.css';

type DatasheetFigureProps = {
  alt: string;
  caption: string;
  src: string;
  narrow?: boolean;
};

export default function DatasheetFigure({
  alt,
  caption,
  src,
  narrow = false,
}: DatasheetFigureProps): React.JSX.Element {
  return (
    <figure className={`${styles.figure} ${narrow ? styles.narrow : ''}`}>
      <img alt={alt} className={styles.image} loading="lazy" src={src} />
      <figcaption className={styles.caption}>{caption}</figcaption>
    </figure>
  );
}
