// Package nginx generates, validates, and applies website configuration.
//
// Config is written from a Go template rather than assembled by string
// concatenation, and every value that reaches it is validated first. An nginx
// config is executed by a process running as root: a domain name that could
// carry a newline would let a caller append arbitrary directives.
package nginx

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/jothost/panel/shared/validate"
)

// SiteConfig is everything the vhost template needs.
type SiteConfig struct {
	// PrimaryDomain is the canonical name. It becomes the first server_name.
	PrimaryDomain string
	// Aliases are additional names served by the same site.
	Aliases []string
	// DocumentRoot is the directory nginx serves from.
	DocumentRoot string
	// AccessLog and ErrorLog are absolute paths inside the site's log
	// directory.
	AccessLog string
	ErrorLog  string
	// MaxBodySize caps uploads, e.g. "64m".
	MaxBodySize string
}

// Redirect is a domain that redirects elsewhere rather than serving content.
type Redirect struct {
	Domain string
	Target string
}

// siteTemplate is the vhost for a static site.
//
// PHP is not wired in here: Phase 5 owns FPM pools, and a fastcgi_pass to a
// socket that does not exist yet would produce a site that returns 502 instead
// of the plain 404 a static site should give.
var siteTemplate = template.Must(template.New("site").Parse(`# Managed by JotHost Panel. Manual edits are overwritten.
server {
    listen 80;
    listen [::]:80;

    server_name {{ .PrimaryDomain }}{{ range .Aliases }} {{ . }}{{ end }};

    root {{ .DocumentRoot }};
    index index.html index.htm;

    access_log {{ .AccessLog }};
    error_log {{ .ErrorLog }};

    client_max_body_size {{ .MaxBodySize }};

    # Do not advertise the server version to every visitor.
    server_tokens off;

    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;

    # Dotfiles are configuration and credentials, never content. Serving
    # .env or .git would hand over the site's secrets.
    location ~ /\. {
        deny all;
        access_log off;
        log_not_found off;
    }

    location / {
        try_files $uri $uri/ =404;
    }
}
`))

// redirectTemplate sends one domain to another.
var redirectTemplate = template.Must(template.New("redirect").Parse(`# Managed by JotHost Panel. Manual edits are overwritten.
server {
    listen 80;
    listen [::]:80;

    server_name {{ .Domain }};

    # 301 is permanent: a redirect configured in the panel is a decision, not
    # a temporary measure, and browsers may cache it.
    return 301 http://{{ .Target }}$request_uri;
}
`))

// defaultMaxBodySize bounds uploads unless a caller sets one.
const defaultMaxBodySize = "64m"

// Render produces the vhost for a site.
//
// Every name is re-validated here even though callers validate too: this
// function is the last point before text becomes a config nginx will execute,
// and it must not depend on a caller having remembered.
func Render(cfg SiteConfig) (string, error) {
	if err := validate.Domain(cfg.PrimaryDomain); err != nil {
		return "", err
	}
	for _, alias := range cfg.Aliases {
		if err := validate.Domain(alias); err != nil {
			return "", fmt.Errorf("alias %q: %w", alias, err)
		}
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
	if cfg.MaxBodySize == "" {
		cfg.MaxBodySize = defaultMaxBodySize
	}
	if err := validateSize(cfg.MaxBodySize); err != nil {
		return "", err
	}

	var out bytes.Buffer
	if err := siteTemplate.Execute(&out, cfg); err != nil {
		return "", fmt.Errorf("render site config: %w", err)
	}
	return out.String(), nil
}

// RenderRedirect produces the vhost for a redirecting domain.
func RenderRedirect(redirect Redirect) (string, error) {
	if err := validate.Domain(redirect.Domain); err != nil {
		return "", err
	}
	if err := validate.Domain(redirect.Target); err != nil {
		return "", fmt.Errorf("redirect target: %w", err)
	}
	if redirect.Domain == redirect.Target {
		// nginx would accept this and serve an infinite redirect loop.
		return "", fmt.Errorf("a domain cannot redirect to itself")
	}

	var out bytes.Buffer
	if err := redirectTemplate.Execute(&out, redirect); err != nil {
		return "", fmt.Errorf("render redirect config: %w", err)
	}
	return out.String(), nil
}
