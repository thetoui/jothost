// Package version exposes build information shared by all JotHost binaries.
package version

// These values are overridden at build time via -ldflags.
var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info is the machine-readable build description reported by /healthz and the
// agent ping response.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

// Current returns the build information compiled into this binary.
func Current() Info {
	return Info{Version: Version, Commit: Commit, BuildDate: BuildDate}
}
