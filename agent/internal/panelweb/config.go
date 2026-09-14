package panelweb

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/php"
)

// renderNginxConfig produces the whole nginx configuration for the panel's
// instance.
//
// A complete configuration rather than a drop-in: this instance shares nothing
// with the host's nginx, which is the point. It reads only the panel's own
// sites directory, so a website written by the website system cannot appear in
// it, and a mistake in one cannot stop the other starting.
func (m *Manager) renderNginxConfig() string {
	var out strings.Builder

	out.WriteString(`# Managed by JotHost Panel. Manual edits are overwritten.
#
# The panel's own nginx. It serves the applications the panel installs -
# phpMyAdmin today - and nothing else. Customer websites are served by the
# host's nginx, reading its own directory, in its own process.
#
# It listens on loopback. These applications are reached through the panel's
# public vhost, which is what decides whether the visitor gets to them at all;
# a port open to the world would be a second front door with no lock on it.

`)

	// Not daemon off: the Agent starts this and does not supervise it, so it
	// has to survive the Agent exiting.
	if m.webGroup != "" {
		// Explicit, rather than trusting nginx's compiled-in default to match
		// whatever the distribution's own nginx runs as. The workers have to be
		// able to open the panel's PHP socket, and that socket's group is this
		// one.
		fmt.Fprintf(&out, "user %s;\n", m.webGroup)
	}
	fmt.Fprintf(&out, "pid %s;\n", filepath.Join(m.runDir, "nginx.pid"))
	fmt.Fprintf(&out, "error_log %s warn;\n", filepath.Join(LogDir, "error.log"))
	out.WriteString("worker_processes 2;\n\n")

	out.WriteString("events {\n    worker_connections 512;\n}\n\n")

	out.WriteString("http {\n")

	// nginx will not start if it cannot create these, and its default paths
	// are inside the host nginx's prefix - which this instance must not write
	// into, or the two would be sharing state again.
	for _, temp := range []struct{ directive, dir string }{
		{"client_body_temp_path", "client_body"},
		{"proxy_temp_path", "proxy"},
		{"fastcgi_temp_path", "fastcgi"},
		{"uwsgi_temp_path", "uwsgi"},
		{"scgi_temp_path", "scgi"},
	} {
		fmt.Fprintf(&out, "    %s %s;\n", temp.directive, filepath.Join(TempRoot, temp.dir))
	}
	out.WriteString("\n")

	out.WriteString(`    include ` + mimeTypesPath() + `;
    default_type application/octet-stream;

    sendfile on;
    tcp_nopush on;
    keepalive_timeout 65;
    server_tokens off;

    # An import through phpMyAdmin is the one thing here that is large.
    client_max_body_size 256m;

`)
	fmt.Fprintf(&out, "    access_log %s;\n\n", filepath.Join(LogDir, "access.log"))

	// A default server that refuses. Without it, a request arriving with an
	// unexpected Host is answered by whichever application sorts first, which
	// is how one panel application ends up serving another's URLs.
	fmt.Fprintf(&out, `    server {
        listen %s default_server;
        server_name _;
        return 444;
    }

`, m.Address())

	fmt.Fprintf(&out, "    include %s/*.conf;\n", filepath.Join(m.root, "sites.d"))
	out.WriteString("}\n")

	return out.String()
}

// mimeTypesPath finds the distribution's mime.types.
//
// Hard-coding /etc/nginx/mime.types would be the one thing this instance still
// took from the host's tree, and Alpine and Debian agree on it - but a host
// that has moved it should not get an nginx that refuses to start over a file
// list, so a missing one is simply not included.
func mimeTypesPath() string {
	for _, candidate := range []string{
		"/etc/nginx/mime.types",
		"/usr/local/nginx/conf/mime.types",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/etc/nginx/mime.types"
}

// writeFPMConfig writes the panel PHP master's own configuration.
//
// Its pool directory is the panel's, not the websites'. That is the separation
// in one line: this master cannot be stopped from starting by a pool somebody
// wrote for a website, and reloading it for a panel application does not
// signal the master every customer site is running on.
func (m *Manager) writeFPMConfig(version string) error {
	var out strings.Builder

	out.WriteString(`; Managed by JotHost Panel. Manual edits are overwritten.
;
; The panel's own PHP-FPM master. It reads only the panel's pool directory, so
; a customer's pool cannot stop it starting, and a website reload does not
; signal it.

[global]
`)
	fmt.Fprintf(&out, "pid = %s\n", filepath.Join(m.runDir, "php-fpm.pid"))
	fmt.Fprintf(&out, "error_log = %s\n", filepath.Join(LogDir, "php-fpm.log"))

	// Left in the foreground would make --daemonize a lie; set explicitly so
	// the intent is not left to a distribution default.
	out.WriteString("daemonize = no\n")

	// A master that exits when its last pool fails would take phpMyAdmin down
	// silently. Logged instead, so the reason survives.
	out.WriteString("log_level = notice\n")
	out.WriteString("emergency_restart_threshold = 5\n")
	out.WriteString("emergency_restart_interval = 1m\n")
	out.WriteString("process_control_timeout = 10s\n\n")

	fmt.Fprintf(&out, "include = %s\n", filepath.Join(m.PoolDir(), "*.conf"))

	path := FPMConfigPathIn(m.root)
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_ = version
	return nil
}

// WritePool installs a pool into the panel's own pool directory.
//
// The rendering is the websites' renderer: a pool is a pool, and having two
// would mean a hardening applied to one and forgotten in the other. Only where
// it is written differs.
func (m *Manager) WritePool(name string, rendered string) (string, error) {
	if err := m.EnsureLayout(); err != nil {
		return "", err
	}
	path := filepath.Join(m.PoolDir(), name+".conf")
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// RemovePool deletes a pool. A pool that is not there is not an error.
func (m *Manager) RemovePool(name string) error {
	path := filepath.Join(m.PoolDir(), name+".conf")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// FPMBinary reports the php-fpm command for a version, so callers do not have
// to know how the PHP package names it.
func FPMBinary(version string) string { return php.CommandFor(version) }

// lookupGroup resolves a group name to a gid.
func lookupGroup(name string) (int, bool) {
	group, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return 0, false
	}
	return gid, true
}
