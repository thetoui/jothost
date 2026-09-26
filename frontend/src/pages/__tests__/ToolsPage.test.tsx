import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';

import { toolGroups } from '@/components/navigation';
import { ToolsPage } from '@/pages/ToolsPage';
import { renderWithProviders } from '@/test/utils';

describe('ToolsPage', () => {
  it('shows every group with a link to each of its tools', () => {
    renderWithProviders(<ToolsPage />);

    expect(screen.getByRole('heading', { level: 1, name: 'Tools & Settings' })).toBeInTheDocument();
    for (const group of toolGroups) {
      const section = screen.getByRole('region', { name: group.title });
      for (const item of group.items) {
        // A tile's accessible name is its label followed by its description.
        const startsWithLabel = new RegExp(`^${item.label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`);
        expect(within(section).getByRole('link', { name: startsWithLabel })).toHaveAttribute(
          'href',
          item.to,
        );
      }
    }
  });
});
