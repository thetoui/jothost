package backup

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
	"github.com/jothost/panel/shared/version"
)

// defaultHTTPTimeout bounds one S3 request.
//
// It is generous because it covers uploading a whole archive over somebody's
// home connection, and it exists at all because a destination that accepts a
// connection and then never answers would otherwise pin a job until the
// Agent's own operation timeout — with no explanation.
const defaultHTTPTimeout = 6 * time.Hour

// NewProvider builds a Provider.
//
// The working directory is created here rather than on first use, so a host
// that cannot stage an archive says so at startup instead of at 3am when the
// first scheduled backup runs.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	hostname := opts.Hostname
	if hostname == "" {
		if name, err := os.Hostname(); err == nil {
			hostname = name
		}
	}

	provider := &Provider{
		runner:        opts.Runner,
		databases:     opts.Databases,
		log:           log,
		workDir:       opts.WorkDir,
		siteRoot:      opts.SiteRoot,
		localRoots:    append([]string(nil), opts.LocalRoots...),
		panelDatabase: opts.PanelDatabase,
		http:          client,
		hostname:      hostname,
		now:           now,
	}

	if provider.workDir != "" {
		// 0700: a staged archive is every file of every site on the host, and
		// it sits here for the length of one backup. Anything wider would make
		// that readable by any account on the machine for that window.
		if err := os.MkdirAll(provider.workDir, 0o700); err != nil {
			log.Error("backups are unavailable: the working directory could not be created",
				"dir", provider.workDir, "error", err.Error())
			provider.workDir = ""
		} else if err := os.Chmod(provider.workDir, 0o700); err != nil {
			log.Warn("could not tighten the backup working directory",
				"dir", provider.workDir, "error", err.Error())
		}
	}

	return provider
}

// Available reports whether this Agent can take backups.
func (p *Provider) Available() bool {
	return p != nil && p.workDir != ""
}

// Capabilities describes what this host can actually do, which is not the same
// as what the panel offers.
//
// It exists so the panel can grey out a destination kind rather than accept one
// and fail at the first scheduled run. A destination that cannot be reached is
// the most dangerous object in this phase: it looks like protection.
type Capabilities struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Local is always true when backups are available at all.
	Local bool `json:"local"`
	S3    bool `json:"s3"`
	SFTP  bool `json:"sftp"`
	// MySQL and Postgres report whether the dump tools are installed, which is
	// separate from whether the servers are running: a host can have a working
	// database and no mysqldump, and only one of those can be backed up.
	MySQLDump    bool     `json:"mysql_dump"`
	PostgresDump bool     `json:"postgres_dump"`
	Engines      []string `json:"engines"`
	WorkDir      string   `json:"work_dir,omitempty"`
	// Panel says whether this host can back up the panel's own database, and
	// PanelReason why not when it cannot.
	Panel       bool   `json:"panel"`
	PanelReason string `json:"panel_reason,omitempty"`
}

// Capabilities reports what this host can do.
func (p *Provider) Capabilities() Capabilities {
	caps := Capabilities{}
	if !p.Available() {
		caps.Reason = ErrUnavailable.Error()
		return caps
	}
	caps.Available = true
	caps.Local = true
	// S3 needs nothing installed: it is HTTP, and the Agent speaks it.
	caps.S3 = true
	caps.SFTP = p.runner != nil && p.runner.Available(CommandSFTP)
	caps.WorkDir = p.workDir

	if p.databases != nil {
		for _, engine := range []string{validate.EngineMySQL, validate.EngineMariaDB, validate.EnginePostgres} {
			dumper, err := p.databases.DumperFor(engine)
			if err != nil || dumper == nil {
				continue
			}
			caps.Engines = append(caps.Engines, engine)
			if engine == validate.EnginePostgres {
				caps.PostgresDump = true
			} else {
				caps.MySQLDump = true
			}
		}
	}

	switch {
	case p.panelDatabase == "":
		caps.PanelReason = ErrPanelUnavailable.Error()
	case !caps.PostgresDump:
		// Either pg_dump is missing or PostgreSQL did not answer the Agent. The
		// second is what an install without the Agent's database credentials
		// looks like, and the reason says both rather than guessing.
		caps.PanelReason = "the Agent cannot dump PostgreSQL on this host: pg_dump is missing, or PostgreSQL did not accept the Agent's connection"
	default:
		caps.Panel = true
	}
	return caps
}

// resolveDocumentRoot checks a document root against the Agent's site root.
//
// A request names a document root, and this is the only place that value is
// turned into a path anything walks. Without it, "back up the site at /etc"
// would archive the host's configuration — including every credential in it —
// and send it to a destination the same request chose.
func (p *Provider) resolveDocumentRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: a document root is required", ErrNothingToBackUp)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: %q is not an absolute path", ErrNothingToBackUp, root)
	}

	cleaned := filepath.Clean(root)
	if p.siteRoot == "" {
		return "", fmt.Errorf("%w: this agent has no site root configured", ErrUnavailable)
	}
	siteRoot := filepath.Clean(p.siteRoot)

	// EvalSymlinks so a document root that is a symlink out of the site root
	// is caught. A path check that only looked at the text would be satisfied
	// by /var/www/site -> /etc.
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s does not exist", ErrNothingToBackUp, cleaned)
		}
		return "", fmt.Errorf("%w: %s", ErrNothingToBackUp, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(siteRoot)
	if err != nil {
		// The site root itself not resolving is a configuration problem, not a
		// request problem, and is reported as one.
		return "", fmt.Errorf("%w: the site root %s could not be resolved: %s",
			ErrUnavailable, siteRoot, err)
	}

	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %s is not inside %s",
			ErrNothingToBackUp, cleaned, resolvedRoot)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNothingToBackUp, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrNothingToBackUp, cleaned)
	}
	return resolved, nil
}

// staging returns a fresh path in the working directory.
//
// The name is the Agent's own, built from a random suffix rather than anything
// in the request: a staging path derived from a key would be a second place a
// key could escape from, and one is enough.
func (p *Provider) staging(prefix string) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}
	handle, err := os.CreateTemp(p.workDir, prefix+"-*.tmp")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	path := handle.Name()
	if err := handle.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	// CreateTemp already makes it 0600; removing it now means the callers that
	// need O_EXCL get a free path rather than an existing empty file.
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	return path, nil
}

// stagingDir returns a fresh private directory in the working directory.
func (p *Provider) stagingDir(prefix string) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}
	dir, err := os.MkdirTemp(p.workDir, prefix+"-*")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	return dir, nil
}

// agentVersion records what produced an archive.
func agentVersion() string { return version.Version }

// contextDone reports the context's error as one of this package's, so a
// cancelled backup is not reported as a corrupt one.
func contextDone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the operation was cancelled: %w", err)
	}
	return nil
}
