import { Component, type ErrorInfo, type ReactNode, StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import './index.css';

class RootErrorBoundary extends Component<
  { children: ReactNode },
  { error: Error | null; info: string }
> {
  state = { error: null as Error | null, info: '' };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    this.setState({ error, info: info.componentStack || '' });
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="flex h-dvh w-screen items-center justify-center bg-background p-6 text-foreground">
        <div className="w-full max-w-lg space-y-3 rounded-2xl border border-border bg-card p-6 shadow-xl">
          <h2 className="text-lg font-semibold text-destructive">Render Error</h2>
          <p className="text-sm text-foreground-subtle">{this.state.error.message}</p>
          {this.state.info && (
            <pre className="max-h-48 overflow-auto rounded-lg border border-border bg-surface p-3 font-mono text-xs text-foreground-subtlest">
              {this.state.info}
            </pre>
          )}
          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={() => {
                this.setState({ error: null, info: '' });
              }}
              className="h-8 rounded-lg border border-border px-3 text-sm hover:bg-hover"
            >
              Retry
            </button>
            <button
              type="button"
              onClick={() => window.location.reload()}
              className="h-8 rounded-lg bg-foreground px-3.5 text-sm font-medium text-background hover:opacity-90"
            >
              Reload
            </button>
          </div>
        </div>
      </div>
    );
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RootErrorBoundary>
      <App />
    </RootErrorBoundary>
  </StrictMode>,
);
