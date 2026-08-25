import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from 'react-router-dom';
import { useState } from 'react';

import { ErrorBoundary } from '@/components/ErrorBoundary';
import { createQueryClient } from '@/app/queryClient';
import { router } from '@/app/router';

export function App() {
  // The client is created once per mount rather than at module scope so tests
  // and hot reloads never share cached server state.
  const [queryClient] = useState(createQueryClient);

  return (
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ErrorBoundary>
  );
}
