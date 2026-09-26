import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useLocation } from 'react-router-dom';

import { ToolSearch } from '@/components/ToolSearch';
import { searchDestinations } from '@/components/navigation';
import { renderWithProviders } from '@/test/utils';

function WhereAmI() {
  return <p data-testid="location">{useLocation().pathname}</p>;
}

function renderSearch() {
  return renderWithProviders(
    <>
      <ToolSearch />
      <WhereAmI />
    </>,
  );
}

describe('searchDestinations', () => {
  it('ranks a label that starts with the term first', () => {
    expect(searchDestinations('fire')[0]?.to).toBe('/firewall');
  });

  it('finds a page by what it does, not only by its name', () => {
    // Plesk calls it IP Address Banning and the daemon is fail2ban; the page
    // is "Intrusion prevention". Any of the three should find it.
    expect(searchDestinations('fail2ban').map((d) => d.to)).toContain('/fail2ban');
    expect(searchDestinations('banning').map((d) => d.to)).toContain('/fail2ban');
  });

  it('returns nothing for an empty query', () => {
    expect(searchDestinations('   ')).toEqual([]);
  });
});

describe('ToolSearch', () => {
  it('goes to the first match on Enter', async () => {
    const user = userEvent.setup();
    renderSearch();

    await user.type(screen.getByRole('combobox', { name: 'Search tools and pages' }), 'fire');
    expect(screen.getByRole('option', { name: /Firewall/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await user.keyboard('{Enter}');

    expect(screen.getByTestId('location')).toHaveTextContent('/firewall');
    // And it tidies up after itself.
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(screen.getByRole('combobox')).toHaveValue('');
  });

  it('moves through the results with the arrow keys', async () => {
    const user = userEvent.setup();
    renderSearch();

    const input = screen.getByRole('combobox');
    await user.type(input, 'log');
    const options = screen.getAllByRole('option');
    expect(options.length).toBeGreaterThan(1);

    await user.keyboard('{ArrowDown}');
    expect(options[1]).toHaveAttribute('aria-selected', 'true');
    expect(input).toHaveAttribute('aria-activedescendant', options[1]?.id);
  });

  it('says where a Tools & Settings page lives', async () => {
    const user = userEvent.setup();
    renderSearch();

    await user.type(screen.getByRole('combobox'), 'firewall');

    expect(screen.getByRole('option', { name: /Firewall/ })).toHaveTextContent(
      'in Tools & Settings',
    );
  });

  it('says so when nothing matches', async () => {
    const user = userEvent.setup();
    renderSearch();

    await user.type(screen.getByRole('combobox'), 'zzzz');

    expect(screen.queryAllByRole('option')).toHaveLength(0);
    expect(screen.getByText(/No page matches/)).toBeInTheDocument();
  });

  it('closes on Escape, then clears on a second Escape', async () => {
    const user = userEvent.setup();
    renderSearch();

    const input = screen.getByRole('combobox');
    await user.type(input, 'dns');
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(input).toHaveValue('dns');

    await user.keyboard('{Escape}');
    expect(input).toHaveValue('');
  });
});
