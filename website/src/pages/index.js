import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';
import CodeBlock from '@theme/CodeBlock';
import Layout from '@theme/Layout';
import TabItem from '@theme/TabItem';
import Tabs from '@theme/Tabs';
import styles from './index.module.css';

const install = 'go install github.com/tunahanaliozturk/derbent/cmd/derbent@latest';

const agents = [
  { value: 'claude', label: 'Claude Code', command: 'claude mcp add derbent -- derbent mcp --agent claude' },
  { value: 'codex', label: 'Codex', command: 'codex mcp add derbent -- derbent mcp --agent codex' },
];

const features = [
  {
    title: 'One shared memory',
    text: 'A note one agent writes in a repository, the others find. Full-text search, kept per project and shared by its worktrees.',
  },
  {
    title: 'Rules per agent',
    text: 'Allow, deny or ask by agent, tool and argument. The first match wins, and a tool an agent may not use is never even listed to it.',
  },
  {
    title: 'Approvals that wait',
    text: 'A held call waits for you in a terminal UI or for derbent approve from any shell. Nobody answers, and it is denied with the reason.',
  },
  {
    title: 'A receipt for every call',
    text: 'Hash-chained, with secrets masked. derbent verify names the first receipt that was edited, removed or reordered.',
  },
  {
    title: 'Built-in tools too',
    text: "Shell commands and file edits pass the same rules through each CLI's pre-tool hook, so a git push can wait for you.",
  },
  {
    title: 'One binary, no daemon',
    text: 'Go, for Windows, macOS and Linux. Every agent session runs its own gate, and one SQLite file is all they share.',
  },
];

function Window({ children, label }) {
  return (
    <div className={styles.window} aria-label={label}>
      <div className={styles.windowBar} aria-hidden="true">
        <span>–</span>
        <span>□</span>
        <span>×</span>
      </div>
      {children}
    </div>
  );
}

// An illustration of the terminal UI, not a capture of it.
function ApprovalScreen() {
  return (
    <pre className={styles.screen}>
      <span className={styles.dim}>waiting</span>
      {'\n'}
      <span className={styles.accent}>› #12</span>
      {'  codex   github__create_issue   41s   {"repo":"acme/api","title":"Fix flaky…'}
      {'\n\n'}
      <span className={styles.dim}>receipts</span>
      {'\n'}
      {'10:42:07  claude  memory_write    allowed   ok\n'}
      {'10:42:31  codex   memory_search   allowed   ok\n'}
      {'10:42:58  claude  native__Bash    approved  git push origin main\n\n'}
      <span className={styles.dim}>a approve · A session · d deny · v verify · ? keys</span>
    </pre>
  );
}

function Hero() {
  return (
    <header className={styles.hero}>
      <div className="container">
        <h1 className={styles.title}>
          <img src={useBaseUrl('derbent-mark.svg')} alt="" className={styles.mark} />
          Derbent
        </h1>
        <p className={styles.tagline}>
          One guarded pass for all your coding agents: shared memory, rules, approvals and a receipt for every
          tool call.
        </p>
        <Window label="Illustration of Derbent's terminal UI">
          <ApprovalScreen />
        </Window>
        <div className={styles.install}>
          <Tabs>
            {agents.map((agent) => (
              <TabItem key={agent.value} value={agent.value} label={agent.label}>
                <div className={styles.command}>
                  <CodeBlock language="bash">{`${install}\n${agent.command}`}</CodeBlock>
                </div>
              </TabItem>
            ))}
          </Tabs>
          <p className={styles.note}>
            Copilot CLI, Antigravity CLI, your own MCP servers and the pre-tool hooks are in the{' '}
            <Link to="/docs/connect-agents">docs</Link>.
          </p>
        </div>
        <div className={styles.buttons}>
          <Link className="button button--primary button--lg" to="/docs/installation">
            Get started →
          </Link>
          <Link className="button button--outline button--primary button--lg" href="https://github.com/tunahanaliozturk/derbent">
            GitHub →
          </Link>
        </div>
      </div>
    </header>
  );
}

export default function Home() {
  return (
    <Layout description="A local gate for coding agents: shared memory, per-agent rules, approvals and a hash-chained receipt for every tool call, over MCP.">
      <Hero />
      <main className="container">
        <section className={styles.features}>
          {features.map((feature) => (
            <div key={feature.title} className={styles.card}>
              <h3>{feature.title}</h3>
              <p>{feature.text}</p>
            </div>
          ))}
        </section>
        <p className={styles.worksWith}>Works with Claude Code · Codex · GitHub Copilot CLI · Antigravity CLI</p>
        <p className={styles.name}>
          A derbent was a guarded post on an Ottoman mountain pass. Its keepers decided who went through and kept
          a record of everyone who did.
        </p>
      </main>
    </Layout>
  );
}
