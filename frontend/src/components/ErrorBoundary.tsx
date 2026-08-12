import { Component, type ErrorInfo, type ReactNode } from 'react';
import i18n from '../i18n';
import { markBoundaryError } from '../utils/boundaryErrors';
import { logger } from '../utils/logger';

type ErrorBoundaryProps = {
  children: ReactNode;
  /** Logical area the boundary guards, e.g. "dashboard" or "transfer". */
  scope?: string;
  /** Optional inline fallback render. Defaults to a full-page recovery view. */
  fallback?: () => ReactNode;
};

type ErrorBoundaryState = { failed: boolean };

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: false };

  private reset = (): void => {
    this.setState({ failed: false });
  };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    markBoundaryError(error);
    logger.error('application render failed', error, {
      route: window.location.pathname,
      scope: this.props.scope,
      componentStack: info.componentStack,
    });
  }

  render(): ReactNode {
    if (!this.state.failed) return this.props.children;
    if (this.props.fallback) return this.props.fallback();

    return (
      <main className="flex min-h-screen items-center justify-center bg-[var(--color-bg-primary)] p-6 text-[var(--color-text-primary)]">
        <section className="ui-card max-w-md space-y-4 p-6 text-center" role="alert" aria-live="assertive">
          <h1 className="text-xl font-semibold">{i18n.t('errorBoundary.title')}</h1>
          <p className="text-[var(--color-text-secondary)]">{i18n.t('errorBoundary.description')}</p>
          <button className="ui-button-secondary px-4 py-2" type="button" onClick={this.reset}>
            {i18n.t('common.retry')}
          </button>
          <button className="ui-button-primary px-4 py-2" type="button" onClick={() => window.location.reload()}>
            {i18n.t('errorBoundary.reload')}
          </button>
        </section>
      </main>
    );
  }
}
