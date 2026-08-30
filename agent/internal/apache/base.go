package apache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/template"

	"github.com/jothost/panel/shared/validate"
)

// baseTemplate is everything that is true of the whole Apache server.
//
// It is a file of ours in Apache's include directory rather than an edit to
// httpd.conf, because a distribution owns httpd.conf and will replace it on
// upgrade. The few things that genuinely cannot live here — the MPM, and the
// Listen on port 80 that belongs to nginx — are handled by the marked edits in
// prepareMainConfig below.
var baseTemplate = template.Must(template.New("apache-base").Parse(
	`# Managed by JotHost Panel. Manual edits are overwritten.
#
# Apache runs here as a backend behind nginx. It never holds a public port: the
# only sockets it listens on are the loopback ports in the site files beside
# this one.

# The group Apache reads site content through.
#
# A site's files are owned by the site's own account and group-owned by the web
# server's group, which is how nginx reads them without being able to write
# them. Apache has to be in that same group or every request is a 403 on a site
# that looks perfectly configured.
Group {{ .WebGroup }}

# The real client address.
#
# Every request arrives from 127.0.0.1, so without this the access log, any
# IP-based rule in a .htaccess, and everything an application reads from
# REMOTE_ADDR all see the proxy instead of the visitor.
#
# The trusted-proxy list is what makes it safe: X-Forwarded-For is a header a
# client can send, and believing it from anywhere would let anyone claim any
# address they liked — including one a .htaccess grants access to.
LoadModule remoteip_module modules/mod_remoteip.so
RemoteIPHeader X-Forwarded-For
{{- range .TrustedProxies }}
RemoteIPInternalProxy {{ . }}
{{- end }}

# Do not advertise the version, or the module list, to every visitor.
ServerTokens Prod
ServerSignature Off

# A name for the server itself, so Apache does not spend startup trying to
# resolve the host's name and does not warn about it on every start.
ServerName jothost-backend

# Apache is behind a proxy that is the only thing allowed to reach it, but a
# request that arrives for a name no vhost claims still has to land somewhere.
# It lands on nothing.
UseCanonicalName Off

# Never act as a forward proxy. mod_proxy is loaded here for FastCGI, and a
# forward proxy left open is how a host ends up relaying somebody else's
# traffic.
ProxyRequests Off
`))

// BaseConfig is the content of the panel's server-wide Apache configuration.
type BaseConfig struct {
	// WebGroup is the group Apache reads site files through.
	WebGroup string
	// TrustedProxies are the addresses whose X-Forwarded-For is believed.
	TrustedProxies []string
}

// DefaultTrustedProxies is the loopback, which is the only place nginx reaches
// Apache from in this arrangement.
var DefaultTrustedProxies = []string{"127.0.0.1", "::1"}

// RenderBase produces the server-wide configuration.
func RenderBase(cfg BaseConfig) (string, error) {
	if err := validate.GroupName(cfg.WebGroup); err != nil {
		return "", fmt.Errorf("web group: %w", err)
	}
	if len(cfg.TrustedProxies) == 0 {
		cfg.TrustedProxies = DefaultTrustedProxies
	}
	for _, proxy := range cfg.TrustedProxies {
		if err := validateProxy(proxy); err != nil {
			return "", err
		}
	}

	var out bytes.Buffer
	if err := baseTemplate.Execute(&out, cfg); err != nil {
		return "", fmt.Errorf("render apache base config: %w", err)
	}
	return out.String(), nil
}

// proxyPattern accepts an address or CIDR made only of the characters an
// address is made of. Anything else could close the directive.
var proxyPattern = regexp.MustCompile(`^[0-9a-fA-F.:]+(/[0-9]{1,3})?$`)

func validateProxy(value string) error {
	if !proxyPattern.MatchString(value) {
		return fmt.Errorf("%w: %q is not an address or CIDR",
			ErrInvalidConfig, value)
	}
	return nil
}

// EnsureBase makes the host ready to run Apache as a backend.
//
// It is idempotent and cheap enough to run before every site write, which is
// deliberate: the alternative is a separate "set up Apache" step that can be
// missed, and a first site that fails for a reason unrelated to itself.
func (p *Provider) EnsureBase(ctx context.Context) error {
	if !p.Available() {
		return ErrUnavailable
	}
	if p.webGroup == "" {
		return ErrNoWebGroup
	}

	rendered, err := RenderBase(BaseConfig{WebGroup: p.webGroup})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(p.sitesDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", p.sitesDir, err)
	}

	path := basePath(p.sitesDir)
	if current, ok := readIfExists(path); !ok || !bytes.Equal(current, []byte(rendered)) {
		if err := os.WriteFile(path, []byte(rendered), configMode); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}

	return p.prepareMainConfig()
}

// basePath is where the server-wide configuration lives.
func basePath(sitesDir string) string {
	return sitesDir + "/" + BaseConfigName
}

// The two edits the panel makes to the distribution's httpd.conf.
//
// Neither can be made from an included file. A second Listen on port 80 is
// still a Listen on port 80, and only one MPM may be loaded — an included
// LoadModule for a second one is a fatal error, not an override.
//
// The edits are line-scoped, marked, and idempotent: each matching line is
// commented out with a marker saying who did it and why, and the replacement
// is added beside it. An operator reading httpd.conf can see exactly what was
// changed and undo it with a text editor.
const (
	editMarker = "# Changed by JotHost Panel: "

	reasonListen = "nginx holds port 80 in hybrid mode; " +
		"Apache listens on the loopback ports in conf.d instead."
	reasonMPM = "PHP runs through FPM, not mod_php, so the event MPM is " +
		"usable — and it serves the same traffic in far fewer processes."
)

var (
	// A bare "Listen 80" or "Listen 0.0.0.0:80". A Listen already scoped to
	// the loopback is one of ours and is left alone.
	listenPattern = regexp.MustCompile(`^\s*Listen\s+(?:[0-9.]+:)?\d+\s*$`)
	// The MPM currently loaded, whichever it is.
	mpmPattern = regexp.MustCompile(`^\s*LoadModule\s+mpm_(\w+)_module\s+(\S+)\s*$`)
)

// prepareMainConfig applies the two edits, verifies the result, and puts the
// original back if Apache rejects it.
func (p *Provider) prepareMainConfig() error {
	original, ok := readIfExists(p.mainConfig)
	if !ok {
		// No httpd.conf means no Apache to configure. The caller finds out
		// from Validate, with Apache's own message, rather than from a guess
		// made here.
		return nil
	}

	updated, changed := applyMainConfigEdits(string(original))
	if !changed {
		return nil
	}

	// The first time the panel touches this file, the original is kept beside
	// it. A distribution's httpd.conf is not ours to lose.
	backup := p.mainConfig + ".jothost-original"
	if _, exists := readIfExists(backup); !exists {
		if err := os.WriteFile(backup, original, configMode); err != nil {
			return fmt.Errorf("back up %s: %w", p.mainConfig, err)
		}
	}

	if err := os.WriteFile(p.mainConfig, []byte(updated), configMode); err != nil {
		return fmt.Errorf("write %s: %w", p.mainConfig, err)
	}
	return nil
}

// applyMainConfigEdits comments out the public Listen and swaps the MPM.
//
// It reports whether anything changed, so a host already prepared is not
// rewritten on every site change.
func applyMainConfigEdits(config string) (string, bool) {
	lines := strings.Split(config, "\n")
	out := make([]string, 0, len(lines)+4)
	changed := false
	mpmDone := false

	for _, line := range lines {
		switch {
		case listenPattern.MatchString(line) && !isLoopbackListen(line):
			out = append(out, editMarker+reasonListen, "#"+line)
			changed = true

		case mpmPattern.MatchString(line):
			groups := mpmPattern.FindStringSubmatch(line)
			name, path := groups[1], groups[2]
			if name == "event" {
				// Already what we want. Left exactly as it is.
				out = append(out, line)
				mpmDone = true
				continue
			}
			out = append(out, editMarker+reasonMPM, "#"+line)
			if !mpmDone {
				// The event module lives beside the one being replaced, so the
				// path is derived from a path the distribution itself wrote
				// rather than guessed at.
				out = append(out, "LoadModule mpm_event_module "+
					strings.Replace(path, "mod_mpm_"+name, "mod_mpm_event", 1))
				mpmDone = true
			}
			changed = true

		default:
			out = append(out, line)
		}
	}

	return strings.Join(out, "\n"), changed
}

// isLoopbackListen reports whether a Listen line is already scoped to the
// loopback, which means it is one of the panel's own site files.
func isLoopbackListen(line string) bool {
	return strings.Contains(line, "127.0.0.1:") || strings.Contains(line, "[::1]:")
}

// validatePath checks a path before it becomes part of a directive.
//
// The same rules as the nginx package applies, and for the same reason: a
// value that could close a directive is a value that becomes configuration.
// Apache adds one of its own — a double quote would close the quoted paths in
// the template.
func validatePath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("%w: path is required", validate.ErrInvalidPath)
	case !strings.HasPrefix(path, "/"):
		return fmt.Errorf("%w: must be absolute", validate.ErrInvalidPath)
	case strings.Contains(path, ".."):
		return fmt.Errorf("%w: must not contain '..'", validate.ErrInvalidPath)
	case strings.ContainsAny(path, "\x00\n\r\"<>"):
		return fmt.Errorf("%w: contains an illegal character", validate.ErrInvalidPath)
	default:
		return nil
	}
}
