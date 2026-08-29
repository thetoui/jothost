package nginx

// TLS defaults applied to every HTTPS site.
//
// These are the Mozilla "intermediate" recommendations, which is the right
// trade-off for shared hosting: modern crypto, and no client from the last
// decade locked out. They are constants rather than options because a per-site
// cipher list is a per-site opportunity to get cryptography wrong.
const (
	// TLS 1.0 and 1.1 are deprecated and prohibited for anything handling card
	// data; both are omitted rather than offered and discouraged.
	tlsProtocols = "TLSv1.2 TLSv1.3"

	// Forward secrecy only, AEAD only. Anything without both is absent from
	// this list rather than ordered below the good options.
	tlsCiphers = "ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:" +
		"ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:" +
		"ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305"

	// HSTS for a year. Long enough to be meaningful; the panel does not
	// preload or include subdomains, because both are commitments a site owner
	// has to make deliberately rather than have a control panel make for them.
	hstsMaxAge = "31536000"
)

// sslServerBlock is the HTTPS half of a site's configuration.
//
// It is a separate template rather than more conditionals inside the HTTP one:
// the two blocks differ in almost every directive, and interleaving them with
// {{ if }} produced something no one could read and check.
const sslServerBlock = `
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;

    server_name {{ .PrimaryDomain }}{{ range .Aliases }} {{ . }}{{ end }};

    root {{ .DocumentRoot }};
    index {{ if .PHPSocket }}index.php {{ end }}index.html index.htm;

    access_log {{ .AccessLog }};
    error_log {{ .ErrorLog }};

    client_max_body_size {{ .MaxBodySize }};

    ssl_certificate {{ .SSL.CertificatePath }};
    ssl_certificate_key {{ .SSL.PrivateKeyPath }};

    ssl_protocols ` + tlsProtocols + `;
    ssl_ciphers ` + tlsCiphers + `;
    # The client's cipher preference is honoured for TLS 1.3, where every
    # option is sound and the client knows best what its hardware accelerates.
    ssl_prefer_server_ciphers off;

    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 1d;
    # Tickets off: without key rotation they undermine forward secrecy, which
    # is the property the cipher list above was chosen for.
    ssl_session_tickets off;

    server_tokens off;

    # Tells browsers to refuse plain HTTP for this host in future, which closes
    # the window where a first request can be intercepted before the redirect.
    add_header Strict-Transport-Security "max-age=` + hstsMaxAge + `" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;

    # Dotfiles are configuration and credentials, never content.
    location ~ /\. {
        deny all;
        access_log off;
        log_not_found off;
    }

    location / {
        try_files $uri $uri/{{ if .PHPSocket }} /index.php?$query_string{{ end }} =404;
    }
{{ if .PHPSocket }}
    location ~ \.php$ {
        # A request only reaches PHP if the script it names exists. Removing
        # this line turns any uploaded file into executable code.
        try_files $uri =404;

        include fastcgi_params;
        fastcgi_pass unix:{{ .PHPSocket }};
        fastcgi_index index.php;

        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param DOCUMENT_ROOT $document_root;
        # Tells the application it is being reached over HTTPS, which is what
        # frameworks use to build absolute URLs and set secure cookies.
        fastcgi_param HTTPS on;

        fastcgi_read_timeout 60s;
        fastcgi_buffers 16 16k;
        fastcgi_buffer_size 32k;
    }
{{ end }}}
`

// acmeChallengeBlock exposes the HTTP-01 challenge path.
//
// This is the single most easily broken piece of an automated HTTPS setup. Two
// things would break renewal silently, sixty days after anyone last looked:
//
//   - Redirecting everything to HTTPS. The CA fetches the challenge over plain
//     HTTP and does not follow a redirect to a certificate it has not issued
//     yet. So this block comes before the redirect and is excluded from it.
//   - The dotfile deny rule. The challenge lives under /.well-known/, which
//     matches `location ~ /\.` — so this uses the `^~` prefix form, which in
//     nginx takes precedence over any regex location and stops the deny rule
//     from ever being consulted for this path.
//
// Neither failure shows up at issue time. Both show up as an expired
// certificate two months later.
const acmeChallengeBlock = `
    location ^~ /.well-known/acme-challenge/ {
        root {{ .SSL.ChallengeRoot }};
        default_type "text/plain";
        access_log off;
    }
`
