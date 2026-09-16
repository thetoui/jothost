import { Component, type ErrorInfo, type ReactNode } from 'react';

import { Button } from '@/components/ui/Button';

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

interface State {
  error: Error | null;
}

/**
 * ErrorBoundary keeps a render failure in one subtree from blanking the whole
 * panel. Error details are logged to the console for developers but are not
 * rendered, so internal paths never reach the screen.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error('Unhandled UI error', error, info.componentStack);
  }

  handleReset = (): void => {
    this.setState({ error: null });
  };

  render(): ReactNode {
    if (!this.state.error) {
      return this.props.children;
    }
    if (this.props.fallback) {
      return this.props.fallback;
    }

    return (
      <div
        role="alert"
        className="flex h-full flex-col items-center justify-center gap-4 p-8 text-center"
      >
        <h1 className="text-xl font-semibold text-ink-strong">Something went wrong</h1>
        <p className="max-w-md text-sm text-ink">
          The page failed to render. Reloading usually resolves it. If the problem persists,
          check the API logs.
        </p>
        <Button variant="primary" onClick={this.handleReset}>
          Try again
        </Button>
      </div>
    );
  }
}
