import type {ReactNode} from 'react';
import Link from '@docusaurus/Link';
import Layout from '@theme/Layout';

const features: {title: string; body: string; to: string}[] = [
  {
    title: 'Deploy in one command',
    body: 'Installs Go, builds znnd from source, creates the systemd unit and starts syncing. Ubuntu 24.04, amd64 or arm64.',
    to: '/guide/deploy',
  },
  {
    title: 'See what the node is doing',
    body: 'nomctl status and nomctl top: sync state, momentums per second, peers, pillar production, CPU, memory, disk.',
    to: '/troubleshooting/first-look',
  },
  {
    title: 'Get told when it breaks',
    body: 'Telegram alerts for a stopped service, stalled momentums, a pillar missing slots, low disk, a node gone dark. Pair in a minute.',
    to: '/alerts/overview',
  },
  {
    title: 'Backups on a timer',
    body: 'Chain-data snapshots with checksums, retention and a systemd timer. Restore from any of them.',
    to: '/guide/backups',
  },
  {
    title: 'Support bundle',
    body: 'One archive with journals, process and host state, log tails and a node snapshot, secrets redacted, ready to share.',
    to: '/troubleshooting/support-bundle',
  },
  {
    title: 'Scriptable and interactive',
    body: 'Every menu action is a subcommand with flags and NOMCTL_* variables, so automation and the TUI share one code path.',
    to: '/reference/commands',
  },
];

export default function Home(): ReactNode {
  return (
    <Layout title="nomctl" description="Deploy, operate and watch Zenon Network nodes from one binary.">
      <header className="hero">
        <div className="container text--center">
          <span className="ledger">Zenon Network of Momentum · node control</span>
          <h1 className="hero__title">nomctl</h1>
          <p className="hero__subtitle">
            A single static binary that deploys, backs up, monitors and alerts on a Zenon node. A Go port of the
            hypercore-one deployment scripts, with a TUI for humans and subcommands for scripts.
          </p>
          <div className="install-line">
            curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh | sudo bash
          </div>
          <div>
            <Link className="button button--plasma button--lg margin-right--sm" to="/getting-started">
              Get started
            </Link>
            <Link className="button button--ghost button--lg" to="/reference/commands">
              Command reference
            </Link>
          </div>
        </div>
      </header>
      <main className="features">
        <div className="container">
          <div className="row">
            {features.map((f) => (
              <div className="col col--4 margin-bottom--lg" key={f.title}>
                <Link to={f.to} style={{textDecoration: 'none'}}>
                  <div className="feature">
                    <span className="ledger">nomctl</span>
                    <h3>{f.title}</h3>
                    <p>{f.body}</p>
                  </div>
                </Link>
              </div>
            ))}
          </div>
        </div>
      </main>
    </Layout>
  );
}
