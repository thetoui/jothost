import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';

import { ErrorBoundary } from '@/components/ErrorBoundary';

function Boom(): never {
  throw new Error('secret path /var/lib/jothost/db.key failed');
}

describe('ErrorBoundary', () => {
  it('renders children when nothing throws', () => {
    render(
      <ErrorBoundary>
        <p>content</p>
      </ErrorBoundary>,
    );

    expect(screen.getByText('content')).toBeInTheDocument();
  });

  it('renders a fallback and hides internal detail when a child throws', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});

    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    );

    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.getByText('Something went wrong')).toBeInTheDocument();
    // The thrown message may name internal paths and must not be displayed.
    expect(screen.queryByText(/var\/lib\/jothost/)).not.toBeInTheDocument();
  });
});
