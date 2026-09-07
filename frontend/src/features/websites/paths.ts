/**
 * Where a site's files and logs are, as the panel writes them.
 *
 * These live outside the components that use them because two pages now need
 * them — the domain panel on the websites list and the domains card on a
 * site's own page — and because a component file that also exports helpers
 * breaks fast refresh for everything in it.
 */

/** The directory a site's own tree lives under. */
export function siteDirFor(domain: string): string {
  return `/var/www/${domain}`;
}

/**
 * logsDirFor says where a site's logs are.
 *
 * From the domain, not from the document root. It used to take the document
 * root's parent, which was right only while the document root was exactly one
 * level down — and once an operator can set it to "public/dist", that
 * inference claims the logs are at <site>/public/logs, which is both wrong and
 * inside the directory being served. The Agent had the identical bug and it is
 * fixed there too.
 */
export function logsDirFor(domain: string): string {
  return `${siteDirFor(domain)}/logs`;
}

/**
 * relativeRoot turns a stored absolute path back into what a field edits.
 *
 * A path that is not under the site's own directory is shown whole rather than
 * mangled into something shorter: it means the record and the convention have
 * diverged, and hiding that would make the field lie about what is being
 * changed.
 */
export function relativeRoot(documentRoot: string, base: string): string {
  if (documentRoot === base) {
    return '';
  }
  if (documentRoot.startsWith(`${base}/`)) {
    return documentRoot.slice(base.length + 1);
  }
  return documentRoot;
}
