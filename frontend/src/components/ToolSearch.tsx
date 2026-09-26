import { useId, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Search } from 'lucide-react';

import { locate, searchDestinations, type Destination } from '@/components/navigation';
import { useCan } from '@/features/auth/hooks';

/**
 * ToolSearch is the search box in the header: type part of a page's name, or
 * something it does, and go there.
 *
 * It searches pages rather than data. Its job is to make a short sidebar cost
 * nothing — somebody who used to click Firewall in a list of twenty-eight now
 * types "fir" and presses Enter.
 */
export function ToolSearch() {
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const navigate = useNavigate();
  const listId = useId();

  const can = useCan();
  // Only pages this person can open: search should not be a way round the menu.
  const results = searchDestinations(query, can);
  const showList = open && query.trim() !== '';
  const activeId = showList && results[active] ? `${listId}-option-${active}` : undefined;

  function go(destination: Destination) {
    navigate(destination.to);
    setQuery('');
    setOpen(false);
    inputRef.current?.blur();
  }

  function handleKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        setOpen(true);
        setActive((index) => Math.min(index + 1, Math.max(results.length - 1, 0)));
        break;
      case 'ArrowUp':
        event.preventDefault();
        setActive((index) => Math.max(index - 1, 0));
        break;
      case 'Enter': {
        const chosen = results[active];
        if (showList && chosen) {
          event.preventDefault();
          go(chosen);
        }
        break;
      }
      case 'Escape':
        // First Escape closes the list, the second clears what was typed.
        // preventDefault because Chrome clears a type="search" input on
        // Escape by itself, which would make the first press do both.
        if (showList) {
          event.preventDefault();
          setOpen(false);
        } else {
          setQuery('');
        }
        break;
    }
  }

  return (
    <div className="relative hidden md:block">
      <Search
        aria-hidden="true"
        className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-ink-dim"
      />
      <input
        ref={inputRef}
        type="search"
        role="combobox"
        aria-label="Search tools and pages"
        aria-expanded={showList}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={activeId}
        value={query}
        placeholder="Search tools…"
        onChange={(event) => {
          setQuery(event.target.value);
          setActive(0);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={handleKeyDown}
        className="h-9 w-64 rounded-md border border-surface-border bg-surface-sunken pl-8 pr-3 text-sm text-ink-strong placeholder:text-ink-dim focus:border-brand-500 focus:outline-none focus:ring-2 focus:ring-brand-500/30"
      />

      {showList && (
        <ul
          id={listId}
          role="listbox"
          aria-label="Matching pages"
          className="absolute right-0 z-40 mt-1 w-80 overflow-hidden rounded-card border border-surface-border bg-surface py-1 shadow-menu"
        >
          {results.length === 0 ? (
            <li role="presentation" className="px-3 py-2.5 text-sm text-ink-muted">
              No page matches “{query.trim()}”.
            </li>
          ) : (
            results.map((destination, index) => {
              const underTools = locate(destination.to)?.underTools ?? false;
              return (
                <li
                  key={destination.to}
                  id={`${listId}-option-${index}`}
                  role="option"
                  aria-selected={index === active}
                  // mousedown rather than click: a click lands after the
                  // input's blur has already closed the list.
                  onMouseDown={(event) => {
                    event.preventDefault();
                    go(destination);
                  }}
                  onMouseEnter={() => setActive(index)}
                  className={`flex cursor-pointer items-start gap-2.5 px-3 py-2 ${
                    index === active ? 'bg-surface-sunken' : ''
                  }`}
                >
                  <destination.icon
                    aria-hidden="true"
                    className="mt-0.5 h-4 w-4 shrink-0 text-ink-dim"
                  />
                  <span className="min-w-0">
                    <span className="block text-sm font-medium text-ink-strong">
                      {destination.label}
                      {underTools && (
                        <span className="ml-1.5 text-xs font-normal text-ink-dim">
                          in Tools &amp; Settings
                        </span>
                      )}
                    </span>
                    <span className="block truncate text-xs text-ink-muted">
                      {destination.description}
                    </span>
                  </span>
                </li>
              );
            })
          )}
        </ul>
      )}
    </div>
  );
}
