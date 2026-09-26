import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';

import { Sidebar } from '@/components/Sidebar';
import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import { useAuthStore } from '@/stores/authStore';
import { useUiStore } from '@/stores/uiStore';

describe('Sidebar', () => {
  beforeEach(() => {
    useUiStore.setState({ sidebarCollapsed: false });
  });

  it('exposes a labelled navigation landmark', () => {
    renderWithProviders(<Sidebar />);

    expect(screen.getByRole('navigation', { name: 'Main navigation' })).toBeInTheDocument();
  });

  it('keeps the everyday pages and one way into everything else', () => {
    renderWithProviders(<Sidebar />);
    const nav = screen.getByRole('navigation', { name: 'Main navigation' });

    expect(within(nav).getByRole('link', { name: 'Dashboard' })).toHaveAttribute('href', '/');
    expect(within(nav).getByRole('link', { name: 'Websites & Domains' })).toHaveAttribute(
      'href',
      '/websites',
    );
    expect(within(nav).getByRole('link', { name: 'Tools & Settings' })).toHaveAttribute(
      'href',
      '/tools',
    );
    // Server administration lives on Tools & Settings now, not in the sidebar.
    expect(within(nav).queryByRole('link', { name: 'Firewall' })).not.toBeInTheDocument();
    // Short enough to scan.
    expect(within(nav).getAllByRole('link').length).toBeLessThanOrEqual(10);
    expect(document.querySelectorAll('[aria-disabled="true"]')).toHaveLength(0);
  });

  it('marks the current page', () => {
    renderWithProviders(<Sidebar />, { route: '/databases' });

    expect(screen.getByRole('link', { name: 'Databases' })).toHaveAttribute(
      'aria-current',
      'page',
    );
  });

  it('lights up Tools & Settings for a page reached through it', () => {
    renderWithProviders(<Sidebar />, { route: '/firewall' });

    // Lit, but not claimed to be the page itself.
    expect(screen.getByRole('link', { name: 'Tools & Settings' })).toHaveAttribute(
      'aria-current',
      'true',
    );
    expect(screen.getByRole('link', { name: 'Dashboard' })).not.toHaveAttribute('aria-current');
  });

  it('keeps Websites & Domains lit on a website of its own', () => {
    renderWithProviders(<Sidebar />, { route: '/websites/0b6e3f0c-4f51-4f79-9d3e-8a2f6a1c2b3d' });

    expect(screen.getByRole('link', { name: 'Websites & Domains' })).toHaveAttribute(
      'aria-current',
      'true',
    );
  });

  it('collapses in response to UI state', () => {
    useUiStore.setState({ sidebarCollapsed: true });
    renderWithProviders(<Sidebar />);

    expect(screen.queryByText('JotHost Panel')).not.toBeInTheDocument();
  });

  describe('for a signed-in person', () => {
    beforeEach(() => {
      clearTokens();
      setRefreshToken('refresh-test');
      setAccessToken('access-test');
      useAuthStore.setState({ status: 'authenticated' });
    });
    afterEach(() => {
      clearTokens();
      useAuthStore.setState({ status: 'anonymous' });
      vi.restoreAllMocks();
    });

    it('shows only the pages their permissions open', async () => {
      vi.spyOn(globalThis, 'fetch').mockResolvedValue(
        envelopeResponse({
          id: 'user-1',
          username: 'customer',
          email: null,
          status: 'active',
          two_factor_enabled: false,
          recovery_codes_remaining: 0,
          roles: ['customer'],
          permissions: ['website.view', 'file.read'],
          created_at: '2026-01-01T00:00:00Z',
          last_login_at: null,
        }),
      );
      renderWithProviders(<Sidebar />);

      await waitFor(() => {
        expect(screen.queryByRole('link', { name: 'Databases' })).not.toBeInTheDocument();
      });
      expect(screen.getByRole('link', { name: 'Websites & Domains' })).toBeInTheDocument();
      expect(screen.getByRole('link', { name: 'Tools & Settings' })).toBeInTheDocument();
      expect(screen.queryByRole('link', { name: 'Dashboard' })).not.toBeInTheDocument();
      expect(screen.queryByRole('link', { name: 'Monitoring' })).not.toBeInTheDocument();
      // Server Management keeps its heading only while something is under it.
      expect(screen.getByText('Server Management')).toBeInTheDocument();
    });
  });
});
