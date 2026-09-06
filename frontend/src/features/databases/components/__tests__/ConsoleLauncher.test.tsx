import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useOpenConsole } from '@/features/databases/components/ConsoleLauncher';

/**
 * What these pin down is the part that was wrong the first time.
 *
 * The original launcher posted the credentials straight at phpMyAdmin and was
 * never signed in, because phpMyAdmin will not accept a login without the CSRF
 * token and set_session it puts on its own login page. So the checks here are
 * about those two fields: that they are read, that they are sent, and that
 * their absence stops the flow instead of quietly posting a login that cannot
 * work.
 */

const session = {
  url: '/phpmyadmin/',
  database: 'shop',
  username: 'shop_rw',
  password: 'a-password',
  host: 'localhost',
};

const loginPage = `
  <form id="login_form" method="post">
    <input type="hidden" name="token" value="tok-1234">
    <input type="hidden" name="set_session" value="sess-5678">
    <input type="text" name="pma_username">
    <input type="password" name="pma_password">
  </form>
`;

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

/** The last form the launcher submitted, captured before it removes itself. */
let submitted: HTMLFormElement | null = null;

function fields(form: HTMLFormElement): Record<string, string> {
  const out: Record<string, string> = {};
  for (const input of form.querySelectorAll('input')) {
    out[input.name] = input.value;
  }
  return out;
}

beforeEach(() => {
  submitted = null;
  vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(function submitSpy(
    this: HTMLFormElement,
  ) {
    // Cloned: the launcher removes the form immediately afterwards, which is
    // the behaviour worth keeping and would otherwise make it unreadable here.
    submitted = this.cloneNode(true) as HTMLFormElement;
  });
  vi.spyOn(window, 'open').mockReturnValue({ focus: vi.fn(), close: vi.fn() } as unknown as Window);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('opening a database console', () => {
  it('sends the token and set_session phpMyAdmin will not sign in without', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        expect(String(input)).toContain('/phpmyadmin/index.php');
        return new Response(loginPage, { status: 200 });
      }),
    );
    vi.spyOn(
      await import('@/features/databases/api'),
      'consoleApi',
      'get',
    ).mockReturnValue({ consoleSession: async () => session } as never);

    const { result } = renderHook(() => useOpenConsole(), { wrapper });
    act(() => result.current.open('db-1'));

    await waitFor(() => expect(submitted).not.toBeNull());

    const sent = fields(submitted!);
    expect(sent['token']).toBe('tok-1234');
    expect(sent['set_session']).toBe('sess-5678');
    expect(sent['pma_username']).toBe('shop_rw');
    expect(sent['pma_password']).toBe('a-password');
    // Straight to the database rather than the server overview, where an
    // operator would have to find theirs in everybody else's list.
    expect(sent['target']).toContain('db=shop');
  });

  it('posts nothing when phpMyAdmin returns no login form', async () => {
    // A proxy that answers with the panel's own index.html looks like a 200
    // and would, without this, produce a form post with an empty token that
    // lands the operator on a login page with no explanation.
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html><body>nope</body></html>')));
    vi.spyOn(
      await import('@/features/databases/api'),
      'consoleApi',
      'get',
    ).mockReturnValue({ consoleSession: async () => session } as never);

    const { result } = renderHook(() => useOpenConsole(), { wrapper });
    act(() => result.current.open('db-1'));

    await waitFor(() => expect(result.current.error).toBeTruthy());
    expect(submitted).toBeNull();
    expect(result.current.error).toMatch(/login form/i);
  });

  it('never puts the password in the URL it posts to', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(loginPage, { status: 200 })));
    vi.spyOn(
      await import('@/features/databases/api'),
      'consoleApi',
      'get',
    ).mockReturnValue({ consoleSession: async () => session } as never);

    const { result } = renderHook(() => useOpenConsole(), { wrapper });
    act(() => result.current.open('db-1'));

    await waitFor(() => expect(submitted).not.toBeNull());
    expect(submitted!.method.toLowerCase()).toBe('post');
    expect(submitted!.getAttribute('action')).not.toContain(session.password);
    expect(submitted!.getAttribute('action')).not.toContain('pma_password');
  });
});
