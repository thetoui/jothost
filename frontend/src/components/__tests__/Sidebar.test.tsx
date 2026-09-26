import { beforeEach, describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';

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
});
