interface TransferProgressProps {
  progress: number;
  rate: string;
  transferred: string;
  remaining: string;
  labels: {
    progress: string;
    transferRate: string;
    transferred: string;
    remaining: string;
  };
}

export function TransferProgress({ progress, rate, transferred, remaining, labels }: TransferProgressProps) {
  return (
    <section className="ui-card flex flex-col p-6" aria-label={labels.progress}>
      <div className="mb-6 flex items-end justify-between border-b border-[var(--color-border-light)] pb-4">
        <div>
          <span className="ui-label">{labels.progress}</span>
          <h3 className="mt-1.5 font-display text-5xl font-extrabold leading-none text-[var(--color-text-primary)]">{progress}%</h3>
        </div>
        <div className="flex flex-col items-end text-right">
          <span className="ui-label">{labels.transferRate}</span>
          <p className="mt-1.5 font-mono text-base font-extrabold text-[var(--color-success-text)]">{rate}</p>
        </div>
      </div>
       <ProgressBar value={progress} label={labels.progress} valueText={`${Math.round(progress)}%`} className="mb-6 h-5 border border-[var(--color-border)] p-0.5" />
      <div className="grid grid-cols-2 gap-4 text-xs font-mono font-bold uppercase tracking-wider text-[var(--color-text-muted)]">
        <span>{labels.transferred}: <strong className="text-[var(--color-text-primary)]">{transferred}</strong></span>
        <span className="text-right">{labels.remaining}: <strong className="text-[var(--color-text-primary)]">{remaining}</strong></span>
      </div>
    </section>
  );
}
import { ProgressBar } from './ProgressBar';
