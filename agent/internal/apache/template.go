package apache

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/jothost/panel/shared/validate"
)

// SiteConfig is everything the Apache vhost template needs.
type SiteConfig struct {
	// PrimaryDomain is the canonical name, and the vhost's ServerName. nginx
	// passes the client's Host through, so this is what selects the vhost.
	PrimaryDomain string
	// Aliases are the other names this site answers to. They must be listed
	// here as well as in nginx: nginx decides what reaches Apache, and Apache
	// decides which vhost serves it. A name missing here lands on whichever
	// vhost happens to be first on the port.
	Aliases []string
	// DocumentRoot is the directory Apache serves from.
	DocumentRoot string
	// BackendPort is the loopback port this vhost listens on.
	BackendPort int
	// AccessLog and ErrorLog are absolute paths inside the site's log
	// directory. They are Apache's own: a request served here never reaches
	// nginx's handler, so nginx's access log would show the proxy hop and
	// nothing about what Apache did with it.
	AccessLog string
	ErrorLog  string
	// PHPSocket is the FPM pool this site's .php requests go to. Empty means a
	// static site, and the handler is omitted rather than pointing at a socket
	// that does not exist.
	PHPSocket string
	// MaxBodySize caps uploads in bytes. Zero means Apache's own default.
	MaxBodySize int64
	// AllowOverride enables .htaccess. It is the reason most people turn
	// hybrid mode on at all, and it is per-site because reading a .htaccess on
	// every directory of every request is not free.
	AllowOverride bool
	// TrustedProxies are the addresses whose X-Forwarded-For may be believed.
	TrustedProxies []string
}

// siteTemplate is one site's Apache virtual host.
//
// Two things in here are load-bearing and easy to get wrong:
//
//   - The PHP handler is a proxy to FPM over a Unix socket, not mod_php. mod_php
//     would run every site's code inside the Apache process, as one shared
//     account, which is the arrangement per-site pools exist to avoid. It also
//     rules out mpm_event, since mod_php is not thread-safe.
//   - The proxy handler is inside a <FilesMatch>, and the enclosing <Directory>
//     is the document root only. A SetHandler at server scope would hand
//     *every* path ending in .php to FPM, including ones that do not exist,
//     and FPM would then be the thing deciding what to execute.
var siteTemplate = template.Must(template.New("apache-site").Parse(
	`# Managed by JotHost Panel. Manual edits are overwritten.
#
# This site is served by Apache behind nginx. nginx holds the public port and
# the certificate; this listens on the loopback only, so nothing outside the
# host can reach it directly.
Listen 127.0.0.1:{{ .BackendPort }}

<VirtualHost 127.0.0.1:{{ .BackendPort }}>
    ServerName {{ .PrimaryDomain }}
{{- range .Aliases }}
    ServerAlias {{ . }}
{{- end }}

    DocumentRoot "{{ .DocumentRoot }}"

    ErrorLog "{{ .ErrorLog }}"
    # %a rather than %h: with mod_remoteip below, %a is the real client and %h
    # would be 127.0.0.1 for every request ever logged.
    CustomLog "{{ .AccessLog }}" "%a %l %u %t \"%r\" %>s %b \"%{Referer}i\" \"%{User-Agent}i\""

{{- if .MaxBodySize }}

    LimitRequestBody {{ .MaxBodySize }}
{{- end }}

    <Directory "{{ .DocumentRoot }}">
        # FollowSymLinks is needed by mod_rewrite, which is most of what a
        # .htaccess does. SymLinksIfOwnerMatch would be safer still, but it
        # stats every path element on every request and breaks deployments
        # that symlink a shared directory in.
        Options FollowSymLinks
        AllowOverride {{ if .AllowOverride }}All{{ else }}None{{ end }}
        Require all granted
        DirectoryIndex {{ if .PHPSocket }}index.php {{ end }}index.html index.htm
    </Directory>

    # Nothing above the document root is servable, whatever a symlink or a
    # rewrite says. Apache's default is to allow the whole filesystem unless
    # told otherwise, and a rewrite ending in an absolute path is enough to
    # reach it.
    <Directory />
        AllowOverride None
        Require all denied
    </Directory>

    # Dotfiles are configuration and credentials, never content. .htaccess and
    # .htpasswd would otherwise be readable over HTTP — the file that protects
    # a directory handed out by the server that reads it — and .env and .git
    # with them.
    <FilesMatch "^\.">
        Require all denied
    </FilesMatch>
    <DirectoryMatch "/\.[^/]+">
        Require all denied
    </DirectoryMatch>
{{ if .PHPSocket }}
    # PHP goes to this site's own FPM pool, which runs as this site's own
    # account. The handler is attached to files that exist — the enclosing
    # Directory is the document root, and Apache resolves the file before the
    # handler runs — so an uploaded image named to end in .php is served, not
    # executed.
    <FilesMatch "\.php$">
        <If "-f %{REQUEST_FILENAME}">
            SetHandler "proxy:unix:{{ .PHPSocket }}|fcgi://localhost"
        </If>
        <Else>
            Require all denied
        </Else>
    </FilesMatch>

    # A slow script must not hold a backend connection open indefinitely.
    <Proxy "unix:{{ .PHPSocket }}|fcgi://localhost">
        ProxySet timeout=60 connectiontimeout=5
    </Proxy>
{{- else }}
    # PHP is off for this site, so a .php file is not content: serving it would
    # hand out the source, and a site that had PHP switched off still has its
    # config.php with the database password in it.
    <FilesMatch "\.php$">
        Require all denied
    </FilesMatch>
{{- end }}
</VirtualHost>
`))

// Render produces the Apache configuration for one site.
//
// Every value is re-validated here even though callers validate too: this is
// the last point before text becomes a configuration a root process executes,
// and it must not depend on a caller having remembered.
func Render(cfg SiteConfig) (string, error) {
	if err := validate.ServerName(cfg.PrimaryDomain); err != nil {
		return "", err
	}
	for _, alias := range cfg.Aliases {
		if err := validate.ServerName(alias); err != nil {
			return "", fmt.Errorf("alias %q: %w", alias, err)
		}
	}
	if err := validate.BackendPort(cfg.BackendPort); err != nil {
		return "", err
	}
	if err := validatePath(cfg.DocumentRoot); err != nil {
		return "", fmt.Errorf("document root: %w", err)
	}
	if err := validatePath(cfg.AccessLog); err != nil {
		return "", fmt.Errorf("access log: %w", err)
	}
	if err := validatePath(cfg.ErrorLog); err != nil {
		return "", fmt.Errorf("error log: %w", err)
	}
	if cfg.PHPSocket != "" {
		if err := validatePath(cfg.PHPSocket); err != nil {
			return "", fmt.Errorf("php socket: %w", err)
		}
	}
	if cfg.MaxBodySize < 0 {
		return "", fmt.Errorf("%w: a negative body limit", ErrInvalidConfig)
	}

	var out bytes.Buffer
	if err := siteTemplate.Execute(&out, cfg); err != nil {
		return "", fmt.Errorf("render apache config: %w", err)
	}
	return out.String(), nil
}
