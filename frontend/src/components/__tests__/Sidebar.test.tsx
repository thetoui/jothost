import { beforeEach, describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';

import { Sidebar } from '@/components/Sidebar';
import { renderWithProviders } from '@/test/utils';
import { useUiStore } from '@/stores/uiStore';

describe('Sidebar', () => {
  beforeEach(() => {
    useUiStore.setState({ sidebarCollapsed: false });
  });

  it('exposes a labelled navigation landmark', () => {
    renderWithProviders(<Sidebar />);

    expect(screen.getByRole('navigation', { name: 'Main navigation' })).toBeInTheDocument();
  });

  it('links only to destinations implemented in this phase', () => {
    renderWithProviders(<Sidebar />);

    expect(screen.getByRole('link', { name: 'Dashboard' })).toHaveAttribute('href', '/');
    // Websites became a real destination in Phase 4, PHP in Phase 5.
    expect(screen.getByRole('link', { name: 'Websites' })).toHaveAttribute(
      'href',
      '/websites',
    );
    expect(screen.getByRole('link', { name: 'PHP' })).toHaveAttribute('href', '/php');
    // Future-phase modules are visible but must not be clickable links.
    expect(screen.queryByRole('link', { name: 'Databases' })).not.toBeInTheDocument();
    expect(screen.getByTitle('Databases — not yet implemented')).toBeInTheDocument();
  });

  it('collapses in response to UI state', () => {
    useUiStore.setState({ sidebarCollapsed: true });
    renderWithProviders(<Sidebar />);

    expect(screen.queryByText('JotHost Panel')).not.toBeInTheDocument();
  });
});
