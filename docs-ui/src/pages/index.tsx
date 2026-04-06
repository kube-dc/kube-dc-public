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
        <Heading as="h1" className="hero__title">
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
    <div className={clsx('col col--4')}>
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
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout
      title="Home"
      description="Kube-DC — Kubernetes Data Center Platform documentation">
      <HomepageHeader />
      <main className="container margin-vert--xl">
        <div className="row">
          <DocCard
            title="Cloud Guide"
            description="User documentation for Kube-DC Cloud. Deploy VMs, Kubernetes clusters, and manage your infrastructure."
            link="/cloud"
            buttonLabel="Cloud Guide"
            items={[
              'Virtual Machines & Containers',
              'Networking & Public IPs',
              'Managed Kubernetes',
              'Storage & Backups',
              'Account & Billing',
            ]}
          />
          <DocCard
            title="Platform Docs"
            description="Operator documentation for deploying and managing the Kube-DC platform on your own infrastructure."
            link="/platform"
            buttonLabel="Platform Docs"
            items={[
              'Installation & Setup',
              'Architecture Deep Dive',
              'Operations & Configuration',
              'Infrastructure Add-ons',
              'Billing & Quota Management',
            ]}
          />
          <DocCard
            title="AI IDE Integration"
            description="Use AI assistants to manage your Kube-DC infrastructure with natural language — directly from your IDE."
            link="/cloud/ai-ide-integration"
            buttonLabel="Setup AI Skills"
            items={[
              'Claude Code, Cursor, Windsurf, Codex',
              'Agent Skills with YAML Templates',
              'Kubernetes MCP Server Setup',
              'Natural Language Workflows',
            ]}
          />
        </div>
      </main>
    </Layout>
  );
}
