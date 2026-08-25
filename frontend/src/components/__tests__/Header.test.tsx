import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { Header } from '@/components/Header';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import { useUiStore } from '@/stores/uiStore';

describe('Header', () => {
  beforeEach(() => {
    useUiStore.setState({ sidebarCollapsed: false });
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      envelopeResponse({ status: 'ok', service: 'api', version: '0.1.0-dev' }),
    );
  });

  it('toggles the sidebar', async () => {
    const user = userEvent.setup();
    renderWithProviders(<Header />);

    await user.click(screen.getByRole('button', { name: 'Collapse sidebar' }));

    expect(useUiStore.getState().sidebarCollapsed).toBe(true);
    expect(screen.getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument();
  });

  it('shows API status once health resolves', async () => {
    renderWithProviders(<Header />);

    expect(await screen.findByText('API online')).toBeInTheDocument();
  });
});
