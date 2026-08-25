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

export function renderWithProviders(ui: ReactElement, options?: Omit<RenderOptions, 'wrapper'>) {
  const queryClient = createTestQueryClient();

  function Wrapper({ children }: WrapperProps) {
    return (
      <QueryClientProvider client={queryClient}>
        <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
          {children}
        </MemoryRouter>
      </QueryClientProvider>
    );
  }

  return { queryClient, ...render(ui, { wrapper: Wrapper, ...options }) };
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
