import { ArrowLeft } from 'lucide-react';

interface PrivacyPageProps {
  onBack: () => void;
}

export function PrivacyPage({ onBack }: PrivacyPageProps) {
  return (
    <div className="max-w-3xl mx-auto animate-fade-in">
      <button
        onClick={onBack}
        className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] transition-colors mb-6 cursor-pointer"
      >
        <ArrowLeft className="w-3.5 h-3.5" />
        Back
      </button>

      <div className="glass-panel rounded-2xl shadow-portal border border-[var(--color-glass-border)] p-8 md:p-10">
        <h1 className="font-display font-extrabold text-2xl md:text-3xl text-[var(--color-portal-navy-themed)] mb-1">
          Privacy Policy
        </h1>
        <p className="text-[11px] font-mono text-[var(--color-text-muted)] mb-8">
          Last updated: July 2026
        </p>

        <div className="space-y-8 text-[13px] leading-relaxed text-[var(--color-text-secondary)]">
          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              1. Who We Are
            </h2>
            <p>
              Clumoove (migration.mikweb.eu) is a cloud-to-cloud file migration service operated by MikWeb.
              This privacy policy explains how we collect, use, store, and protect your personal data when
              you use our service.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              2. Data We Collect
            </h2>
            <div className="space-y-3">
              <p>We collect the following categories of data:</p>
              <ul className="list-disc pl-5 space-y-1.5">
                <li><strong className="text-[var(--color-text-primary)]">Account Information:</strong> Email address, display name, and authentication credentials (password hash, OAuth tokens) necessary to create and maintain your account.</li>
                <li><strong className="text-[var(--color-text-primary)]">OAuth Tokens:</strong> Temporary access tokens from third-party providers (Google, Dropbox, HiDrive) that you authorize to enable file transfers. These tokens are stored in encrypted form and only used during active migrations.</li>
                <li><strong className="text-[var(--color-text-primary)]">File Metadata:</strong> File names, sizes, paths, and directory structures from your connected cloud accounts. File contents are processed in transit but are not persistently stored on our servers.</li>
                <li><strong className="text-[var(--color-text-primary)]">Usage Data:</strong> Basic interaction data such as migration status, error logs, and timestamps required to operate and troubleshoot the service.</li>
              </ul>
            </div>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              3. How We Use Your Data
            </h2>
            <ul className="list-disc pl-5 space-y-1.5">
              <li>To authenticate you and manage your account.</li>
              <li>To facilitate cloud-to-cloud file migrations you initiate.</li>
              <li>To communicate with you about your migrations (status updates, error notifications).</li>
              <li>To improve and troubleshoot the service.</li>
              <li>We do <strong className="text-[var(--color-text-primary)]">not</strong> use your data for marketing, advertising, or profiling purposes.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              4. Data Storage &amp; Encryption
            </h2>
            <ul className="list-disc pl-5 space-y-1.5">
              <li>All data in transit is encrypted using TLS 1.3.</li>
              <li>OAuth tokens and credentials are encrypted at rest using AES-256.</li>
              <li>File data is streamed directly between source and destination providers and is not persistently stored on Clumoove infrastructure.</li>
              <li>Database backups are encrypted and retained for a maximum of 30 days.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              5. Data Retention &amp; Deletion
            </h2>
            <p>
              Your data is stored only as long as necessary to complete your migrations. Migration metadata,
              logs, and OAuth tokens are automatically deleted <strong className="text-[var(--color-text-primary)]">within 30 days</strong> after a
              migration is completed or cancelled. Account information (email, name) is retained until you
              delete your account. You can request immediate deletion of all your data at any time by
              contacting us.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              6. Third-Party Access
            </h2>
            <p>
              Clumoove integrates with the following third-party services solely for the purpose of
              executing file migrations you explicitly authorize:
            </p>
            <ul className="list-disc pl-5 space-y-1.5 mt-2">
              <li><strong className="text-[var(--color-text-primary)]">Google Drive / Google APIs</strong> &mdash; to read files from or write files to your Google Drive.</li>
              <li><strong className="text-[var(--color-text-primary)]">Dropbox API</strong> &mdash; to read files from or write files to your Dropbox.</li>
              <li><strong className="text-[var(--color-text-primary)]">HiDrive (Strato)</strong> &mdash; to read files from or write files to your HiDrive storage.</li>
            </ul>
            <p className="mt-2">
              We do not sell, rent, or share your personal data with any third party for their own purposes.
              Each OAuth integration is governed by the respective provider&apos;s privacy policy.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              7. Your Rights (GDPR)
            </h2>
            <p>
              If you are located in the European Union, you have the following rights under the General
              Data Protection Regulation (GDPR):
            </p>
            <ul className="list-disc pl-5 space-y-1.5 mt-2">
              <li><strong className="text-[var(--color-text-primary)]">Right to Access:</strong> Request a copy of the personal data we hold about you.</li>
              <li><strong className="text-[var(--color-text-primary)]">Right to Rectification:</strong> Request correction of inaccurate data.</li>
              <li><strong className="text-[var(--color-text-primary)]">Right to Erasure (&quot;Right to be Forgotten&quot;):</strong> Request deletion of your data.</li>
              <li><strong className="text-[var(--color-text-primary)]">Right to Restrict Processing:</strong> Request restriction of how we use your data.</li>
              <li><strong className="text-[var(--color-text-primary)]">Right to Data Portability:</strong> Request transfer of your data to another service.</li>
              <li><strong className="text-[var(--color-text-primary)]">Right to Object:</strong> Object to processing of your personal data.</li>
            </ul>
            <p className="mt-2">
              To exercise any of these rights, please contact us at the email address below. We will
              respond within 30 days.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              8. Contact
            </h2>
            <p>
              If you have any questions about this privacy policy or wish to exercise your data protection
              rights, please contact us:
            </p>
            <p className="mt-2 font-mono text-sm text-[var(--color-portal-navy-themed)] font-semibold">
              mikwebbb@gmail.com
            </p>
          </section>
        </div>
      </div>
    </div>
  );
}
