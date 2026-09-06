package grafana

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Layout is where this host's Grafana actually keeps its files.
//
// Discovered rather than assumed, and that distinction is the whole reason this
// file exists. The first version of this package wrote a datasource into
// /etc/grafana/provisioning and a settings file into /etc/grafana/conf, both of
// which looked right, neither of which Grafana reads on Alpine: its init script
// passes cfg:paths.provisioning=/var/lib/grafana/provisioning and its config is
// /etc/grafana.ini, not /etc/grafana/grafana.ini.
//
// Every unit test passed. Grafana started. The datasource list was empty. A
// test that asserts a file's contents proves the file, not that anything reads
// it — so the paths are now taken from the host's own configuration, and the
// integration check asks Grafana what it loaded.
type Layout struct {
	// ConfigFile is the ini Grafana reads.
	ConfigFile string
	// ProvisioningDir is where it looks for datasources and dashboards.
	ProvisioningDir string
	// DashboardDir is where the panel puts its dashboard JSON. It sits under
	// the provisioning directory so one path discovery covers both.
	DashboardDir string
	// Source names where the paths came from, for a status message that can
	// explain itself.
	Source string
}

// configCandidates are the ini files, most specific first.
var configCandidates = []string{
	"/etc/grafana/grafana.ini",
	"/etc/grafana.ini",
	"/usr/local/etc/grafana/grafana.ini",
}

// optionFiles are where distributions put the arguments the service starts
// with. Alpine's carries cfg:paths.provisioning; Debian's carries the same
// settings as environment variables.
var optionFiles = []string{
	"/etc/conf.d/grafana",
	"/etc/default/grafana-server",
	"/etc/sysconfig/grafana-server",
}

// provisioningCandidates are the fallbacks, used only when nothing on the host
// says where provisioning lives.
var provisioningCandidates = []string{
	"/var/lib/grafana/provisioning",
	"/etc/grafana/provisioning",
	"/usr/share/grafana/conf/provisioning",
}

// DiscoverLayout works out where this host's Grafana reads from.
func DiscoverLayout() (Layout, error) {
	layout := Layout{}

	for _, candidate := range configCandidates {
		if fileExists(candidate) {
			layout.ConfigFile = candidate
			break
		}
	}
	if layout.ConfigFile == "" {
		return layout, fmt.Errorf("%w: no grafana.ini was found in %s",
			ErrNotInstalled, strings.Join(configCandidates, ", "))
	}

	// The service's own arguments win over anything else: they are what the
	// running process was told, and they override the ini.
	if dir, source := provisioningFromOptions(); dir != "" {
		layout.ProvisioningDir = dir
		layout.Source = source
	} else if dir := provisioningFromConfig(layout.ConfigFile); dir != "" {
		layout.ProvisioningDir = dir
		layout.Source = layout.ConfigFile
	} else {
		for _, candidate := range provisioningCandidates {
			if dirExists(candidate) {
				layout.ProvisioningDir = candidate
				layout.Source = "the first directory that exists"
				break
			}
		}
	}
	if layout.ProvisioningDir == "" {
		return layout, fmt.Errorf(
			"this host's Grafana does not say where it reads provisioning from, "+
				"and none of %s exists", strings.Join(provisioningCandidates, ", "))
	}

	// Grafana's dashboard provider is given a path; putting the JSON beside
	// the provisioning files keeps everything the panel writes in one place.
	layout.DashboardDir = filepath.Join(layout.ProvisioningDir, "jothost-dashboards")
	return layout, nil
}

// provisioningFromOptions reads cfg:paths.provisioning or GF_PATHS_PROVISIONING
// out of the files a distribution starts the service with.
func provisioningFromOptions() (string, string) {
	for _, path := range optionFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "#") {
				continue
			}
			// Alpine: cfg:paths.provisioning=$GRAFANA_HOME/provisioning
			if value, ok := valueAfter(line, "cfg:paths.provisioning="); ok {
				if expanded := expandGrafanaHome(value, content); expanded != "" {
					return expanded, path
				}
			}
			// Debian and RHEL: GF_PATHS_PROVISIONING=/etc/grafana/provisioning
			if value, ok := valueAfter(line, "GF_PATHS_PROVISIONING="); ok {
				if expanded := expandGrafanaHome(value, content); expanded != "" {
					return expanded, path
				}
			}
			if value, ok := valueAfter(line, "PROVISIONING_CFG_DIR="); ok {
				if expanded := expandGrafanaHome(value, content); expanded != "" {
					return expanded, path
				}
			}
		}
	}
	return "", ""
}

// provisioningFromConfig reads an uncommented `provisioning =` from the ini.
func provisioningFromConfig(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// A commented default — ";provisioning = conf/provisioning" — is not a
		// setting. Reading it as one is how the wrong directory gets chosen on
		// a host that never configured this.
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "provisioning") {
			continue
		}
		_, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			// Relative to the home path, which the ini does not state. Left
			// alone rather than guessed at; the candidates below cover it.
			return ""
		}
		return value
	}
	return ""
}

// expandGrafanaHome resolves $GRAFANA_HOME and ${GRAFANA_HOME} against the
// same file that used it.
func expandGrafanaHome(value string, content []byte) string {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "GRAFANA_HOME") {
		if filepath.IsAbs(value) {
			return value
		}
		return ""
	}

	home := ""
	for _, line := range strings.Split(string(content), "\n") {
		if candidate, ok := valueAfter(strings.TrimSpace(line), "GRAFANA_HOME="); ok {
			home = strings.Trim(strings.TrimSpace(candidate), `"'`)
			break
		}
	}
	if home == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "${GRAFANA_HOME}", home)
	value = strings.ReplaceAll(value, "$GRAFANA_HOME", home)
	if !filepath.IsAbs(value) {
		return ""
	}
	return value
}

// valueAfter returns what follows a prefix on a line, and whether it was there.
// It tolerates the leading `export ` Debian's files use.
func valueAfter(line, prefix string) (string, bool) {
	line = strings.TrimPrefix(line, "export ")
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	return strings.TrimPrefix(line, prefix), true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
