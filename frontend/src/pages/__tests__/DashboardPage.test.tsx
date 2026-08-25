import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';

import { DashboardPage } from '@/pages/DashboardPage';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';

describe('DashboardPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
  });

  it('renders API health once loaded', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(
      envelopeResponse({ status: 'ok', service: 'api', version: '0.1.0-dev' }),
    );

    renderWithProviders(<DashboardPage />);

    expect(await screen.findByText('Online')).toBeInTheDocument();
    expect(screen.getByText('0.1.0-dev')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Dashboard' })).toBeInTheDocument();
  });

  it('surfaces an unreachable API without crashing', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('SERVICE_UNAVAILABLE', 'One or more dependencies are unavailable', 503),
    );

    renderWithProviders(<DashboardPage />);

    await waitFor(() => {
      expect(screen.getByText('Unreachable')).toBeInTheDocument();
    });
    expect(screen.getByRole('alert')).toBeInTheDocument();
  });
});
