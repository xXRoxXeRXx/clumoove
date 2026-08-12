import { CloudArrowDownIcon, CloudArrowUpIcon } from '@heroicons/react/24/outline';
import { SelectedPathsViewer } from './SelectedPathsViewer';
import { useTranslation } from 'react-i18next';

interface TransferEndpointsProps {
  sourceLabel: string;
  targetLabel: string;
  oauthLabel: string;
  sourceProvider: string;
  sourceUrl?: string;
  selectedPaths?: string[];
  targetProvider: string;
  targetUrl?: string;
  targetDir?: string;
}

export function TransferEndpoints({ sourceLabel, targetLabel, oauthLabel, sourceProvider, sourceUrl, selectedPaths, targetProvider, targetUrl, targetDir }: TransferEndpointsProps) {
  const { t } = useTranslation();
  return (
    <section className="grid grid-cols-1 gap-6 md:grid-cols-2" aria-label={`${sourceLabel} / ${targetLabel}`}>
      <article className="ui-card space-y-4 p-5">
        <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
          <CloudArrowDownIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
          <h3 className="font-display text-xs font-bold uppercase tracking-wider text-[var(--color-text-primary)]">{sourceLabel}</h3>
        </div>
        <div className="space-y-2">
          <div className="text-sm font-extrabold capitalize text-[var(--color-text-primary)]">{sourceProvider || t('common.unspecified')}</div>
          <div className="break-all font-mono text-xs leading-normal text-[var(--color-text-muted)]">{sourceUrl || oauthLabel}</div>
          <SelectedPathsViewer paths={selectedPaths} />
        </div>
      </article>
      <article className="ui-card space-y-4 p-5">
        <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
          <CloudArrowUpIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
          <h3 className="font-display text-xs font-bold uppercase tracking-wider text-[var(--color-text-primary)]">{targetLabel}</h3>
        </div>
        <div className="space-y-2">
          <div className="text-sm font-extrabold capitalize text-[var(--color-text-primary)]">{targetProvider || t('common.unspecified')}</div>
          <div className="break-all font-mono text-xs leading-normal text-[var(--color-text-muted)]">{targetUrl || oauthLabel}</div>
          <span className="ui-badge ui-badge-muted inline-flex px-2.5 py-1 font-mono text-xs">{targetDir || '/'}</span>
        </div>
      </article>
    </section>
  );
}
