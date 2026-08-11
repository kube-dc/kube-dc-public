import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

function HomepageHeader() {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={clsx('hero hero--primary', styles.heroBanner)}>
      <div className="container">
        <Heading as="h1" className={clsx('hero__title', styles.heroTitle)}>
          {siteConfig.title}
        </Heading>
        <p className="hero__subtitle">{siteConfig.tagline}</p>
      </div>
    </header>
  );
}

type CardProps = {
  title: string;
  description: string;
  link: string;
  buttonLabel: string;
  items: string[];
};

function DocCard({title, description, link, buttonLabel, items}: CardProps) {
  return (
    <div className={clsx('col col--4', styles.cardColumn)}>
      <div className={styles.card}>
        <Heading as="h2">{title}</Heading>
        <p>{description}</p>
        <ul className={styles.cardList}>
          {items.map((item, idx) => (
            <li key={idx}>{item}</li>
          ))}
        </ul>
        <Link className="button button--primary button--lg" to={link}>
          {buttonLabel}
        </Link>
      </div>
    </div>
  );
}

export default function Home(): ReactNode {
  return (
    <Layout
      title="Home"
      description="Kube-DC — Kubernetes Data Center Platform documentation">
      <HomepageHeader />
      <main className="container margin-vert--xl">
        <div className="row">
          <DocCard
            title="Cloud Guide"
            description="Create Projects, deploy workloads, and manage cloud resources in your Organization."
            link="/cloud"
            buttonLabel="Open Cloud Guide"
            items={[
              'Organizations and Projects',
              'Applications and Virtual Machines',
              'Managed Clusters',
              'Networking, Storage, and Data Services',
              'Access, Usage, and Billing',
            ]}
          />
          <DocCard
            title="Platform Docs"
            description="Install, secure, and operate the Kube-DC platform on your infrastructure."
            link="/platform"
            buttonLabel="Open Platform Docs"
            items={[
              'Installation and Upgrades',
              'Architecture and Security',
              'Networking and Storage',
              'Observability and Day-2 Operations',
              'Operator CLI and Reference',
            ]}
          />
          <DocCard
            title="Platform Datasheet"
            description="Evaluate Kube-DC capabilities, architecture, service boundaries, and operating responsibilities."
            link="/datasheet"
            buttonLabel="Open Datasheet"
            items={[
              'Platform and Tenancy Model',
              'Managed Clusters and Virtual Machines',
              'Databases, Networking, and Storage',
              'Security and Observability',
              'GPU Service Models',
            ]}
          />
        </div>
        <p className={styles.secondaryLink}>
          Automating Kube-DC? <Link to="/cloud/ai-ide-integration">Set up AI agent skills</Link>.
        </p>
      </main>
    </Layout>
  );
}
