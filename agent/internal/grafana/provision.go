package grafana

import (
	"fmt"
	"strings"
)

// header marks every file this package writes.
const header = "# Managed by JotHost Panel. Manual edits are overwritten.\n"

// renderDatasource writes the PostgreSQL connection Grafana reads metrics
// through.
//
// The role is read-only and separate from the panel's own. Grafana lets anyone
// with editor rights write SQL, and that SQL runs as whoever the datasource
// says: pointing it at the panel's own role would make a Grafana editor a
// database administrator on the panel's data.
func renderDatasource(cfg DatasourceConfig) []byte {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}

	var out strings.Builder
	out.WriteString(header)
	out.WriteString("#\n")
	out.WriteString("# The panel's own metrics database, read-only.\n")
	out.WriteString("apiVersion: 1\n\n")
	out.WriteString("datasources:\n")
	out.WriteString("  - name: JotHost\n")
	out.WriteString("    uid: jothost-metrics\n")
	out.WriteString("    type: postgres\n")
	fmt.Fprintf(&out, "    url: %s\n", yamlString(fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)))
	fmt.Fprintf(&out, "    user: %s\n", yamlString(cfg.User))
	fmt.Fprintf(&out, "    database: %s\n", yamlString(cfg.Database))
	out.WriteString("    isDefault: true\n")
	// Grafana keeps a provisioned datasource in step with this file on every
	// start. Without it, a change here would be ignored because the datasource
	// already exists.
	out.WriteString("    editable: false\n")
	out.WriteString("    secureJsonData:\n")
	fmt.Fprintf(&out, "      password: %s\n", yamlString(cfg.Password))
	out.WriteString("    jsonData:\n")
	fmt.Fprintf(&out, "      sslmode: %s\n", yamlString(sslMode))
	out.WriteString("      postgresVersion: 1600\n")
	// Grafana will otherwise open as many connections as it feels like against
	// the panel's database, which is the same database the panel needs to
	// answer requests from.
	out.WriteString("      maxOpenConns: 4\n")
	out.WriteString("      maxIdleConns: 2\n")
	return []byte(out.String())
}

// renderDashboardProvider tells Grafana where to find the dashboard JSON.
func renderDashboardProvider(dashboardDir string) []byte {
	var out strings.Builder
	out.WriteString(header)
	out.WriteString("apiVersion: 1\n\n")
	out.WriteString("providers:\n")
	out.WriteString("  - name: JotHost\n")
	out.WriteString("    type: file\n")
	// Not editable in the UI. A provisioned dashboard somebody edits in
	// Grafana is a dashboard that differs from the file the panel maintains,
	// with no sign of it anywhere the panel can see.
	out.WriteString("    allowUiUpdates: false\n")
	out.WriteString("    options:\n")
	fmt.Fprintf(&out, "      path: %s\n", yamlString(dashboardDir))
	return []byte(out.String())
}

// Markers around the block the panel owns inside grafana.ini.
//
// The first version of this package wrote its settings to conf/jothost.ini and
// Grafana read none of them: it takes one config file, and a second one beside
// it is a file nothing opens. So the settings go into the ini Grafana actually
// reads, fenced, and everything outside the fence is left exactly as the
// operator and the distribution left it.
const (
	blockStart = "; ---- JotHost Panel: managed settings. Do not edit inside this block. ----"
	blockEnd   = "; ---- End of JotHost Panel settings ----"
)

// mergeSettings puts the panel's block into an existing ini.
//
// Replaces the block if it is there and appends it if it is not, so running
// this twice produces the same file and an operator's own settings above it
// are never touched. Grafana takes the last value for a key, which is why the
// block goes at the end: it has to win over the defaults it is overriding.
func mergeSettings(existing []byte, block string) []byte {
	text := string(existing)

	if start := strings.Index(text, blockStart); start >= 0 {
		if end := strings.Index(text[start:], blockEnd); end >= 0 {
			finish := start + end + len(blockEnd)
			return []byte(text[:start] + block + text[finish:])
		}
		// A start marker with no end is a file somebody edited halfway
		// through. Everything from the marker on is the panel's, because
		// there is no honest way to tell where its block stopped.
		return []byte(text[:start] + block)
	}

	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + "\n" + block + "\n")
}

// renderSettings is the block itself.
func renderSettings(rootURL string) string {
	var out strings.Builder
	out.WriteString(blockStart)
	out.WriteString("\n[server]\n")
	// Loopback only. Grafana is reached through the vhost the panel writes,
	// which is where the operator decides who may see it; a Grafana listening
	// on every interface is one exposed on a port nothing in the panel guards.
	out.WriteString("http_addr = 127.0.0.1\n")
	fmt.Fprintf(&out, "http_port = %d\n", DefaultPort)
	if rootURL != "" {
		fmt.Fprintf(&out, "root_url = %s\n", rootURL)
		// Grafana builds its own asset URLs from root_url when it is served
		// under a path rather than at a domain root.
		out.WriteString("serve_from_sub_path = false\n")
	}

	out.WriteString("\n[security]\n")
	// The setting that makes an iframe work at all. Without it Grafana sends
	// X-Frame-Options: deny and every embedded panel is a blank box.
	out.WriteString("allow_embedding = true\n")
	// A session cookie that a browser will send from inside an iframe on the
	// panel's own origin. Lax is not enough for a cross-origin frame; None
	// requires Secure, which is why this is only right behind TLS and is said
	// so in the panel.
	out.WriteString("cookie_samesite = none\n")
	out.WriteString("cookie_secure = true\n")

	out.WriteString("\n[auth.anonymous]\n")
	// Deliberately off, and the reason is the whole security model. phpMyAdmin
	// is served the same way and for the same reason: reaching the URL proves
	// nothing, and the tool decides who its visitor is. Turning this on would
	// publish every metric on the host to anybody who found the address.
	out.WriteString("enabled = false\n")

	out.WriteString("\n[users]\n")
	// A viewer cannot write SQL through the datasource. Somebody who should be
	// able to is given the role in Grafana deliberately, rather than getting it
	// by being the first person to sign in.
	out.WriteString("auto_assign_org_role = Viewer\n")
	// No trailing newline: the block ends exactly at its end marker, so a
	// replacement is byte-for-byte and the file does not gain a blank line on
	// every provision. The append path adds the newline instead.
	out.WriteString(blockEnd)
	return out.String()
}

// yamlString quotes a value so nothing in it is read as YAML.
//
// A database password containing a colon, a hash or a leading brace would
// otherwise change the shape of the document rather than the value in it.
//
// The newline is the one that matters and the one a first attempt misses.
// Escaping backslash and quote keeps the value inside its quotes; a raw
// newline ends the line regardless of them, and whatever follows becomes a
// sibling field — written by whoever chose the password, immediately beneath
// the credentials. There is a test that puts "is_admin: true" in a password,
// and it failed against the first version of this function.
func yamlString(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				// Everything else below printable ASCII, written as a YAML
				// escape rather than as itself.
				fmt.Fprintf(&out, `\x%02x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// renderDashboard is the panel's host dashboard.
//
// Written out rather than exported from a running Grafana: a dashboard the
// panel provisions has to be readable in the repository, and a blob of
// generated JSON with an id and a version in it is not.
//
// The queries read system_metrics directly, which is why the panel does not
// need Prometheus. $__timeFilter and $__timeGroupAlias are Grafana's macros
// for the dashboard's time range and its bucket size; both expand to SQL
// against the timestamp column.
func renderDashboard() []byte {
	return []byte(`{
  "__comment": "Managed by JotHost Panel. Manual edits are overwritten.",
  "uid": "jothost-host",
  "title": "JotHost — host",
  "tags": ["jothost"],
  "timezone": "browser",
  "editable": false,
  "schemaVersion": 39,
  "refresh": "1m",
  "time": { "from": "now-6h", "to": "now" },
  "templating": {
    "list": [
      {
        "name": "server",
        "label": "Server",
        "type": "query",
        "datasource": { "type": "postgres", "uid": "jothost-metrics" },
        "query": "SELECT hostname AS __text, id::text AS __value FROM servers ORDER BY hostname",
        "refresh": 1
      }
    ]
  },
  "panels": [
    {
      "id": 1,
      "title": "CPU",
      "type": "timeseries",
      "datasource": { "type": "postgres", "uid": "jothost-metrics" },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 0 },
      "fieldConfig": {
        "defaults": { "unit": "percent", "min": 0, "max": 100 },
        "overrides": []
      },
      "targets": [
        {
          "format": "time_series",
          "rawQuery": true,
          "rawSql": "SELECT $__timeGroupAlias(timestamp, $__interval), avg(cpu_percent) AS \"CPU\" FROM system_metrics WHERE $__timeFilter(timestamp) AND server_id = $server::uuid GROUP BY 1 ORDER BY 1"
        }
      ]
    },
    {
      "id": 2,
      "title": "Memory",
      "type": "timeseries",
      "datasource": { "type": "postgres", "uid": "jothost-metrics" },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 0 },
      "fieldConfig": {
        "defaults": { "unit": "percent", "min": 0, "max": 100 },
        "overrides": []
      },
      "targets": [
        {
          "format": "time_series",
          "rawQuery": true,
          "rawSql": "SELECT $__timeGroupAlias(timestamp, $__interval), avg(memory_percent) AS \"Memory\" FROM system_metrics WHERE $__timeFilter(timestamp) AND server_id = $server::uuid GROUP BY 1 ORDER BY 1"
        }
      ]
    },
    {
      "id": 3,
      "title": "Disk",
      "type": "timeseries",
      "datasource": { "type": "postgres", "uid": "jothost-metrics" },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 8 },
      "fieldConfig": {
        "defaults": { "unit": "percent", "min": 0, "max": 100 },
        "overrides": []
      },
      "targets": [
        {
          "format": "time_series",
          "rawQuery": true,
          "rawSql": "SELECT $__timeGroupAlias(timestamp, $__interval), avg(disk_percent) AS \"Disk\" FROM system_metrics WHERE $__timeFilter(timestamp) AND server_id = $server::uuid GROUP BY 1 ORDER BY 1"
        }
      ]
    },
    {
      "id": 4,
      "title": "Load average",
      "type": "timeseries",
      "datasource": { "type": "postgres", "uid": "jothost-metrics" },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 8 },
      "fieldConfig": { "defaults": { "unit": "short", "min": 0 }, "overrides": [] },
      "targets": [
        {
          "format": "time_series",
          "rawQuery": true,
          "rawSql": "SELECT $__timeGroupAlias(timestamp, $__interval), avg(load_1) AS \"1 min\", avg(load_5) AS \"5 min\", avg(load_15) AS \"15 min\" FROM system_metrics WHERE $__timeFilter(timestamp) AND server_id = $server::uuid GROUP BY 1 ORDER BY 1"
        }
      ]
    },
    {
      "id": 5,
      "title": "Network",
      "type": "timeseries",
      "datasource": { "type": "postgres", "uid": "jothost-metrics" },
      "gridPos": { "h": 8, "w": 24, "x": 0, "y": 16 },
      "fieldConfig": { "defaults": { "unit": "Bps" }, "overrides": [] },
      "targets": [
        {
          "__comment": "The counters are cumulative, so a rate needs the difference between samples. Migration 0004 stores them raw for exactly this reason: a rate can be recomputed over any window, a stored rate cannot.",
          "format": "time_series",
          "rawQuery": true,
          "rawSql": "SELECT $__timeGroupAlias(timestamp, $__interval), avg(rx_rate) AS \"In\", avg(tx_rate) AS \"Out\" FROM (SELECT timestamp, server_id, GREATEST(network_rx - lag(network_rx) OVER w, 0) / NULLIF(EXTRACT(epoch FROM timestamp - lag(timestamp) OVER w), 0) AS rx_rate, GREATEST(network_tx - lag(network_tx) OVER w, 0) / NULLIF(EXTRACT(epoch FROM timestamp - lag(timestamp) OVER w), 0) AS tx_rate FROM system_metrics WHERE $__timeFilter(timestamp) AND server_id = $server::uuid WINDOW w AS (PARTITION BY server_id ORDER BY timestamp)) rates GROUP BY 1 ORDER BY 1"
        }
      ]
    }
  ]
}
`)
}
