import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useOpenConsole } from '@/features/databases/components/ConsoleLauncher';

/**
 * What these pin down are the two things that were wrong, in order.
 *
 * The first launcher posted credentials straight at phpMyAdmin and was never
 * signed in: phpMyAdmin will not accept a login without a CSRF token bound to
 * the session cookie the page was served with.
 *
 * The second read that token only out of #login_form. phpMyAdmin keeps one
 * signed-in account per browser, so after the first database the entry page is
 * the signed-in interface and has no login form in it — and every database
 * after the first refused to open. The token is on that page too, and posting
 * credentials with it switches the account outright.
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

/**
 * What phpMyAdmin answers with once this browser already holds a session: the
 * interface, with a CSRF token but no login form anywhere in it.
 */
const signedInPage = `
  <div id="page_content">
    <form id="menu-bar" method="post">
      <input type="hidden" name="token" value="tok-signed-in">
    </form>
  </div>
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

  it('posts nothing when the answer is not a phpMyAdmin page at all', async () => {
    // A proxy answering with the panel's own index.html looks like a 200 and
    // would, without this, produce a form post with an empty token that lands
    // the operator on a login page with no explanation.
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
    expect(result.current.error).toMatch(/sign in from/i);
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

describe('opening a second database', () => {
  it('signs in with the token from the page it is already signed in on', async () => {
    // The reported bug. phpMyAdmin keeps one signed-in account per browser, so
    // the entry page answers the second click with the interface rather than a
    // login form. The token is there; only the login form is not.
    vi.stubGlobal('fetch', vi.fn(async () => new Response(signedInPage, { status: 200 })));
    vi.spyOn(
      await import('@/features/databases/api'),
      'consoleApi',
      'get',
    ).mockReturnValue({ consoleSession: async () => session } as never);

    const { result } = renderHook(() => useOpenConsole(), { wrapper });
    act(() => result.current.open('db-2'));

    await waitFor(() => expect(submitted).not.toBeNull());

    const sent = fields(submitted!);
    expect(sent['token']).toBe('tok-signed-in');
    expect(sent['pma_username']).toBe('shop_rw');
    expect(result.current.error).toBeNull();
  });

  it('never signs out to do it', async () => {
    // A sign-out would work and was the first fix written. It is not needed —
    // posting with the page token switches the account — and it would leave a
    // window in which the browser holds no session at all.
    const requests: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        requests.push(String(input));
        return new Response(signedInPage, { status: 200 });
      }),
    );
    vi.spyOn(
      await import('@/features/databases/api'),
      'consoleApi',
      'get',
    ).mockReturnValue({ consoleSession: async () => session } as never);

    const { result } = renderHook(() => useOpenConsole(), { wrapper });
    act(() => result.current.open('db-2'));

    await waitFor(() => expect(submitted).not.toBeNull());
    expect(requests.filter((url) => url.includes('logout'))).toHaveLength(0);
    expect(requests).toHaveLength(1);
  });

  it('omits set_session when the page has none', async () => {
    // The signed-in page carries no set_session. phpMyAdmin does not require
    // it, and posting it empty is not the same as leaving it out.
    vi.stubGlobal('fetch', vi.fn(async () => new Response(signedInPage, { status: 200 })));
    vi.spyOn(
      await import('@/features/databases/api'),
      'consoleApi',
      'get',
    ).mockReturnValue({ consoleSession: async () => session } as never);

    const { result } = renderHook(() => useOpenConsole(), { wrapper });
    act(() => result.current.open('db-2'));

    await waitFor(() => expect(submitted).not.toBeNull());
    expect(fields(submitted!)).not.toHaveProperty('set_session');
  });

  it('still sends set_session when the login form offers one', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(loginPage, { status: 200 })));
    vi.spyOn(
      await import('@/features/databases/api'),
      'consoleApi',
      'get',
    ).mockReturnValue({ consoleSession: async () => session } as never);

    const { result } = renderHook(() => useOpenConsole(), { wrapper });
    act(() => result.current.open('db-1'));

    await waitFor(() => expect(submitted).not.toBeNull());
    expect(fields(submitted!)['set_session']).toBe('sess-5678');
  });
});
