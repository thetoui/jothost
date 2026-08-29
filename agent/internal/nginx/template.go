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

// SSLConfig is the certificate a site serves HTTPS with.
type SSLConfig struct {
	// CertificatePath and PrivateKeyPath are absolute paths nginx reads as
	// root, before it drops privileges to its worker user.
	CertificatePath string
	PrivateKeyPath  string
	// RedirectToHTTPS sends plain HTTP to the secure site. The ACME challenge
	// path is always excluded from it; see acmeChallengeBlock.
	RedirectToHTTPS bool
	// ChallengeRoot is the webroot the ACME challenge is served from. Empty
	// omits the challenge block, which is correct for a self-signed site that
	// will never speak to a certificate authority.
	ChallengeRoot string
}

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
	// PHPSocket is the FPM pool this site passes .php requests to. Empty means
	// a static site, and the PHP block is omitted entirely rather than pointing
	// at a socket that does not exist.
	PHPSocket string
	// SSL is the site's certificate. Nil means HTTP only, and the HTTPS server
	// block is omitted rather than pointing at a certificate that is not there
	// — which nginx refuses to start with, taking every other site down too.
	SSL *SSLConfig
}

// Redirect is a domain that redirects elsewhere rather than serving content.
type Redirect struct {
	Domain string
	Target string
}

// siteTemplate is the vhost for a site.
//
// The PHP block is the most dangerous configuration this panel writes, and the
// two guards in it are not optional:
//
//   - `try_files $uri =404` runs before fastcgi_pass, so a request only reaches
//     PHP if the .php file it names actually exists. Without it, nginx passes
//     /uploads/avatar.jpg/x.php to FPM, FPM walks back to the real file, and
//     an uploaded image is executed as code. That is the classic path-info
//     remote code execution, and it is the default behaviour without this line.
//   - SCRIPT_FILENAME is built from $document_root and $fastcgi_script_name.
//     Deriving it from $request_filename or from unvalidated PATH_INFO is the
//     other half of the same hole.
//
// fastcgi_split_path_info is deliberately absent: nothing served here needs
// PATH_INFO, and enabling it reintroduces exactly the parsing that makes the
// attack possible.
var siteTemplate = template.Must(template.New("site").Parse(`# Managed by JotHost Panel. Manual edits are overwritten.
server {
    listen 80;
    listen [::]:80;

    server_name {{ .PrimaryDomain }}{{ range .Aliases }} {{ . }}{{ end }};
{{ if and .SSL .SSL.ChallengeRoot }}` + acmeChallengeBlock + `{{ end }}
{{- if and .SSL .SSL.RedirectToHTTPS }}
    # Everything except the challenge path above goes to HTTPS. 301 rather
    # than 302: enabling HTTPS is a decision, not a temporary measure.
    location / {
        return 301 https://$host$request_uri;
    }
}
{{- else }}
    root {{ .DocumentRoot }};
    index {{ if .PHPSocket }}index.php {{ end }}index.html index.htm;

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
        try_files $uri $uri/{{ if .PHPSocket }} /index.php?$query_string{{ end }} =404;
    }
{{ if not .PHPSocket }}
    # PHP is off for this site, so a .php file is not content: serving it
    # would hand out the source, and a site that had PHP switched off still
    # has its config.php with the database password in it.
    location ~ \.php$ {
        deny all;
        access_log off;
        log_not_found off;
    }
{{ else }}
    location ~ \.php$ {
        # A request only reaches PHP if the script it names exists. Removing
        # this line turns any uploaded file into executable code.
        try_files $uri =404;

        include fastcgi_params;
        fastcgi_pass unix:{{ .PHPSocket }};
        fastcgi_index index.php;

        # Built from the resolved root and the matched script name, never from
        # client-controlled path info.
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param DOCUMENT_ROOT $document_root;

        # A slow script must not hold a connection open indefinitely; the pool
        # has a bounded number of workers and they are shared with every other
        # request to this site.
        fastcgi_read_timeout 60s;
        fastcgi_buffers 16 16k;
        fastcgi_buffer_size 32k;
    }
{{ end }}}
{{- end }}
{{- if .SSL }}` + sslServerBlock + `{{ end }}`))

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
	if cfg.PHPSocket != "" {
		if err := validatePath(cfg.PHPSocket); err != nil {
			return "", fmt.Errorf("php socket: %w", err)
		}
	}
	if cfg.SSL != nil {
		if err := validateSSL(cfg.SSL); err != nil {
			return "", err
		}
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

// validateSSL checks the certificate paths before they reach the template.
//
// A path that could close a directive is refused here rather than trusted: an
// ssl_certificate line is read by a root process, and the file it names is the
// site's identity.
func validateSSL(cfg *SSLConfig) error {
	if err := validatePath(cfg.CertificatePath); err != nil {
		return fmt.Errorf("certificate path: %w", err)
	}
	if err := validatePath(cfg.PrivateKeyPath); err != nil {
		return fmt.Errorf("private key path: %w", err)
	}
	if cfg.ChallengeRoot != "" {
		if err := validatePath(cfg.ChallengeRoot); err != nil {
			return fmt.Errorf("challenge root: %w", err)
		}
	}
	return nil
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
