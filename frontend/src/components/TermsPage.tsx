import { ArrowLeft } from 'lucide-react';

interface TermsPageProps {
  onBack: () => void;
}

export function TermsPage({ onBack }: TermsPageProps) {
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
          Terms of Service
        </h1>
        <p className="text-[11px] font-mono text-[var(--color-text-muted)] mb-8">
          Last updated: July 2026
        </p>

        <div className="space-y-8 text-[13px] leading-relaxed text-[var(--color-text-secondary)]">
          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              1. Service Description
            </h2>
            <p>
              Clumoove (accessible at migration.mikweb.eu) provides a cloud-to-cloud file migration
              platform that enables users to transfer files between supported cloud storage providers
              (including Google Drive, Dropbox, and HiDrive). The service acts as an intermediary to
              facilitate direct data transfers as initiated and authorized by the user.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              2. Acceptance of Terms
            </h2>
            <p>
              By creating an account and using Clumoove, you agree to be bound by these Terms of Service.
              If you do not agree with any part of these terms, you must not use the service. We reserve
              the right to update these terms at any time, and we will notify registered users of material
              changes via email.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              3. User Responsibilities
            </h2>
            <ul className="list-disc pl-5 space-y-1.5">
              <li>You must provide accurate and up-to-date account information.</li>
              <li>You are responsible for maintaining the confidentiality of your account credentials.</li>
              <li>You must only migrate files that you have the legal right to access and transfer.</li>
              <li>You agree not to use the service for any unlawful or unauthorized purpose.</li>
              <li>You must comply with the terms of service of all third-party cloud providers you connect.</li>
              <li>You are solely responsible for verifying the completeness and integrity of your migrated data.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              4. Limitation of Liability
            </h2>
            <p>
              Clumoove is provided &quot;as is&quot; without any warranty, express or implied. To the maximum
              extent permitted by applicable law:
            </p>
            <ul className="list-disc pl-5 space-y-1.5 mt-2">
              <li>We are not liable for any data loss, corruption, or incomplete transfers that may occur during migration.</li>
              <li>We are not responsible for downtime, errors, or rate limits imposed by third-party cloud providers.</li>
              <li>We are not liable for any indirect, incidental, or consequential damages arising from your use of the service.</li>
              <li>Our total liability shall not exceed the total amount paid by you for the specific migration giving rise to the claim.</li>
            </ul>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              5. Service Availability
            </h2>
            <p>
              We strive to provide reliable service, but we make no guarantee of uninterrupted or
              error-free availability. Clumoove may be temporarily unavailable for maintenance,
              updates, or due to factors beyond our control. We reserve the right to modify,
              suspend, or discontinue the service at any time without prior notice.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              6. Account Termination
            </h2>
            <p>
              We reserve the right to suspend or terminate your account at our discretion, including
              but not limited to:
            </p>
            <ul className="list-disc pl-5 space-y-1.5 mt-2">
              <li>Violation of these terms of service.</li>
              <li>Suspected fraudulent or abusive behavior.</li>
              <li>Extended periods of inactivity.</li>
              <li>At your request (account deletion).</li>
            </ul>
            <p className="mt-2">
              Upon termination, your data will be deleted in accordance with our Privacy Policy. You
              may terminate your account at any time by contacting us.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              7. Governing Law
            </h2>
            <p>
              These terms shall be governed by and construed in accordance with the laws of the
              Federal Republic of Germany and the European Union. Any disputes arising from these
              terms shall be subject to the exclusive jurisdiction of the courts in Berlin, Germany.
            </p>
          </section>

          <section>
            <h2 className="font-display font-bold text-lg text-[var(--color-portal-navy-themed)] mb-3">
              8. Contact
            </h2>
            <p>
              For any questions, concerns, or requests regarding these terms, please contact us:
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
