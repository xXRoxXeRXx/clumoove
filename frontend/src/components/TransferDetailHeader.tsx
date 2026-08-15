import type { ReactNode } from 'react';
import { ArrowLeftIcon } from './icons';
import { useTranslation } from 'react-i18next';
import { Button } from './Button';

interface TransferDetailHeaderProps {
  backLabel: string;
  onBack: () => void;
  title: string;
  id: string;
  actions: ReactNode;
}

export function TransferDetailHeader({ backLabel, onBack, title, id, actions }: TransferDetailHeaderProps) {
  const { t } = useTranslation();
  return (
    <>
      <div className="flex items-center justify-between">
        <Button onClick={onBack}>
          <ArrowLeftIcon className="h-4 w-4" aria-hidden="true" />
          {backLabel}
        </Button>
      </div>
      <div className="flex flex-col items-start justify-between gap-4 border-b border-[var(--color-border)] pb-6 md:flex-row md:items-center">
        <div className="space-y-1">
          <h1 className="font-display text-2xl font-extrabold text-[var(--color-text-primary)]">{title}</h1>
          <p className="text-xs font-mono text-[var(--color-text-muted)]">{t('common.id')}: {id}</p>
        </div>
        <div className="flex w-full flex-wrap items-center justify-start gap-2.5 md:w-auto md:justify-end">{actions}</div>
      </div>
    </>
  );
}
