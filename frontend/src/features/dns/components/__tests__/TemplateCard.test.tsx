import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { TemplateCard } from '@/features/dns/components/TemplateCard';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { DNSTemplate } from '@/types/api';

function template(overrides: Partial<DNSTemplate> = {}): DNSTemplate {
  return {
    id: 'template-1',
    server_id: 'server-1',
    name: 'Default',
    description: 'The apex and www.',
    is_default: true,
    builtin: true,
    records: [
      {
        name: '@',
        type: 'A',
        ttl: 0,
        value: '{ip}',
        priority: 0,
        weight: 0,
        port: 0,
      },
    ],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function mockProfile() {
  return envelopeResponse({
    id: 'user-1',
    username: 'admin',
    email: null,
    status: 'active',
    two_factor_enabled: false,
    recovery_codes_remaining: 0,
    roles: ['admin'],
    permissions: ['server.view', 'dns.manage'],
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('TemplateCard', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    templates?: DNSTemplate[];
    placeholders?: { token: string; means: string }[];
    onWrite?: (url: string, init?: RequestInit) => void;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile();
      }
      if (init?.method && init.method !== 'GET') {
        options.onWrite?.(url, init);
        return envelopeResponse({ ok: true });
      }
      return envelopeResponse({
        templates: options.templates ?? [template()],
        placeholders: options.placeholders ?? [
          { token: '{domain}', means: 'the domain the zone is for' },
          { token: '{ip}', means: "this host's own address" },
        ],
      });
    });
  }

  it('offers no way to delete the built-in template', async () => {
    // Editing it is fine — an operator wanting different defaults should not
    // have to make a second template. Deleting it is not: it is the only thing
    // deciding what a new zone starts with, and without one every domain
    // created afterwards gets an empty zone.
    mockApi({ templates: [template({ name: 'Default', builtin: true })] });
    renderWithProviders(<TemplateCard />);

    expect(await screen.findByRole('button', { name: 'Edit' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Delete Default/ })).not.toBeInTheDocument();
  });

  it('offers a delete for a template somebody made', async () => {
    // The counterpart, so the check above is a statement about the built-in
    // and not about the card never rendering a delete at all.
    mockApi({
      templates: [template({ id: 'template-2', name: 'With mail', builtin: false })],
    });
    renderWithProviders(<TemplateCard />);

    expect(await screen.findByRole('button', { name: 'Delete With mail' })).toBeInTheDocument();
  });

  it('names the placeholders the server reported rather than its own', async () => {
    // A form offering a placeholder the panel cannot fill writes that literal
    // text into every zone made from the template, so the list is the API's.
    mockApi({
      templates: [template()],
      placeholders: [{ token: '{zonename}', means: 'a token only this server knows' }],
    });
    renderWithProviders(<TemplateCard />);

    await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
    expect(await screen.findByText('{zonename}')).toBeInTheDocument();
  });

  it('sends the whole record list, with the placeholders left unexpanded', async () => {
    // The API replaces a template's records wholesale and substitutes at zone
    // creation time. A card that resolved "{ip}" here would bake this host's
    // address into the template and every zone made from it thereafter.
    const writes: { url: string; body: unknown }[] = [];
    mockApi({
      templates: [template({ id: 'template-9', name: 'Mail', builtin: false })],
      onWrite: (url, init) => {
        writes.push({ url, body: JSON.parse(String(init?.body ?? '{}')) });
      },
    });
    renderWithProviders(<TemplateCard />);

    await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Add record' }));
    await userEvent.click(screen.getByRole('button', { name: 'Save template' }));

    await waitFor(() => expect(writes).toHaveLength(1));
    const write = writes[0]!;
    // PUT, not POST: editing an existing template must not make a second one.
    expect(write.url).toContain('/dns/templates/template-9');
    const body = write.body as { records: { value: string }[] };
    expect(body.records).toHaveLength(2);
    expect(body.records.map((r) => r.value)).toEqual(['{ip}', '{ip}']);
  });
});
