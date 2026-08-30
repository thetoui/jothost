import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, type RenderOptions } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { ReactElement, ReactNode } from 'react';

/** A query client with retries disabled so failures surface immediately. */
export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  });
}

interface WrapperProps {
  children: ReactNode;
}

interface ProviderOptions extends Omit<RenderOptions, 'wrapper'> {
  /**
   * The URL the router starts at.
   *
   * Pages that read the query string — the file manager's ?path=, the editor's
   * — cannot be tested at all without it.
   */
  route?: string;
}

export function renderWithProviders(ui: ReactElement, options?: ProviderOptions) {
  const queryClient = createTestQueryClient();
  const { route = '/', ...renderOptions } = options ?? {};

  function Wrapper({ children }: WrapperProps) {
    return (
      <QueryClientProvider client={queryClient}>
        <MemoryRouter
          initialEntries={[route]}
          future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
        >
          {children}
        </MemoryRouter>
      </QueryClientProvider>
    );
  }

  return { queryClient, ...render(ui, { wrapper: Wrapper, ...renderOptions }) };
}

/** Builds a successful API envelope Response for fetch mocking. */
export function envelopeResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ success: status < 400, data, request_id: 'req_test' }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** Builds an error API envelope Response for fetch mocking. */
export function errorResponse(code: string, message: string, status = 500): Response {
  return new Response(
    JSON.stringify({ success: false, error: { code, message }, request_id: 'req_test' }),
    { status, headers: { 'Content-Type': 'application/json' } },
  );
}
