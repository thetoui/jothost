// Package sites provisions a website's filesystem and system account.
//
// This is where the Agent's root privilege is actually spent, so every path is
// validated against an allowed root before anything is created, and ownership
// is set with syscalls rather than by shelling out to chown.
package sites

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/fsperm"
	"github.com/jothost/panel/agent/internal/pathsec"
)

// Directory layout under a site's root.
//
// Content and logs are separate directories so the site's own user can be
// given write access to what it serves without also being able to rewrite the
// record of who visited it.
const (
	ContentDir = "public"
	LogsDir    = "logs"
)

// Permissions for a provisioned site.
//
// The layout is owner = the site's own account, group = the web server's
// group, nothing for anyone else. That is what gives a site both isolation and
// a working server: the site user owns its content, the web server reads it
// through the group, and another site's user gets nothing.
//
// Group-owning by the site's own group instead looks tidier and produces a
// site that refuses every visitor, because the web server has no way through
// the directory. That mistake is invisible until the first request.
const (
	// siteRootMode lets the owner work and the web server traverse, but keeps
	// the world out of a directory that may hold application configuration.
	siteRootMode os.FileMode = 0o750
	// contentMode is the same: the web server must read what it serves.
	contentMode os.FileMode = 0o750
	// logsMode allows the same access; logs record visitor addresses.
	logsMode os.FileMode = 0o750
	// indexMode is readable by the owner and the web server's group.
	indexMode os.FileMode = 0o640
)

// Errors returned by the provisioner.
var (
	ErrOutsideRoot = errors.New("site path is outside the allowed root")
	ErrExists      = errors.New("site directory already exists")
	// ErrNoWebGroup means the web server's group could not be resolved, so a
	// provisioned site would not be readable by the server meant to serve it.
	ErrNoWebGroup = errors.New("the web server group could not be resolved")
	// ErrOccupied means a site directory already exists, holds files, and does
	// not belong to the account being provisioned — so provisioning would hand
	// one customer's files to another.
	ErrOccupied = errors.New("site directory already holds files owned by another account")
)

// orphanUID and orphanGID are what a kept-but-abandoned site tree is given.
//
// Root, because root is the one identifier the system will never hand out
// again. Every other uid on the host is a number in an allocation pool: free
// it while files still carry it and the next account created inherits them.
const (
	orphanUID = 0
	orphanGID = 0
	// orphanMode closes a retained tree to everyone but root. The site is
	// gone, nothing serves it, and the web server no longer needs a way in.
	orphanMode os.FileMode = 0o700
)

// Layout is the resolved set of paths for one site.
type Layout struct {
	Root      string `json:"root"`
	Content   string `json:"content"`
	Logs      string `json:"logs"`
	AccessLog string `json:"access_log"`
	ErrorLog  string `json:"error_log"`
}

// ApacheAccessLog and ApacheErrorLog are where the backend writes in hybrid
// mode.
//
// Separate files rather than the ones above, because both servers are in the
// path and each records a different half of the truth: nginx sees the request
// arrive and the proxy hop, Apache sees what was actually served, which
// .htaccess rewrote it, and what PHP did. Interleaving them into one file
// would produce two lines per request that look like two requests.
func (l Layout) ApacheAccessLog() string {
	return filepath.Join(l.Logs, "apache-access.log")
}

func (l Layout) ApacheErrorLog() string {
	return filepath.Join(l.Logs, "apache-error.log")
}

// Provisioner creates and removes site directories.
type Provisioner struct {
	validator *pathsec.Validator
	// root is where sites live, e.g. /var/www.
	root string
	// webGID is the web server's group. Site directories are group-owned by it
	// so the server can read what it serves; -1 means it was not found.
	webGID   int
	webGroup string
}

// DefaultRoot is where website directories are created.
const DefaultRoot = "/var/www"

// DefaultWebGroups are tried in order when no group is configured.
//
// Distributions disagree: Alpine and RHEL run nginx as "nginx", Debian and
// Ubuntu as "www-data".
var DefaultWebGroups = []string{"nginx", "www-data"}

// NewProvisioner builds a Provisioner confined to root.
//
// webGroup names the web server's group; empty probes the conventional names.
// A group that cannot be resolved is not fatal here — the Agent still starts
// and every other operation works — but provisioning refuses rather than
// creating a site the server cannot read.
func NewProvisioner(root, webGroup string) (*Provisioner, error) {
	if root == "" {
		root = DefaultRoot
	}
	clean := filepath.Clean(root)

	// The directory must exist before the validator resolves it, or a symlink
	// check later has nothing to resolve against.
	// fsperm, so a host without the directory gets one nginx can walk into.
	// Under the Agent's umask a plain MkdirAll made it 0700, and every site
	// beneath it would have been unreachable. An existing /var/www is left as
	// the operator has it.
	if err := fsperm.MkdirAll(clean, 0o755); err != nil {
		return nil, fmt.Errorf("create site root %s: %w", clean, err)
	}

	validator, err := pathsec.NewValidator(clean)
	if err != nil {
		return nil, err
	}

	gid, resolved := resolveWebGroup(webGroup)
	return &Provisioner{
		validator: validator,
		root:      clean,
		webGID:    gid,
		webGroup:  resolved,
	}, nil
}

// resolveWebGroup finds the web server's group id.
func resolveWebGroup(name string) (int, string) {
	candidates := DefaultWebGroups
	if name != "" {
		candidates = []string{name}
	}

	for _, candidate := range candidates {
		group, err := user.LookupGroup(candidate)
		if err != nil {
			continue
		}
		if gid, err := strconv.Atoi(group.Gid); err == nil {
			return gid, candidate
		}
	}
	return -1, ""
}

// WebGroup returns the resolved web server group, or "" when none was found.
func (p *Provisioner) WebGroup() string { return p.webGroup }

// HasWebGroup reports whether a site can be made readable by the web server.
func (p *Provisioner) HasWebGroup() bool { return p.webGID >= 0 }

// Root returns the configured site root.
func (p *Provisioner) Root() string { return p.root }

// EnsureContent creates a document root that does not exist yet.
//
// It belongs to whoever owns the site's own directory. Deriving the account
// again here would be a second place for that convention to live; inheriting
// it means the new directory belongs to exactly what the rest of the site
// belongs to, whatever that turned out to be.
//
// An existing directory is left completely alone — mode and ownership both. A
// deployment may have set them deliberately, and an update that silently
// re-owned them would break the site it was asked to reconfigure.
func (p *Provisioner) EnsureContent(layout Layout) error {
	if _, err := os.Stat(layout.Content); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check %s: %w", layout.Content, err)
	}

	info, err := os.Stat(layout.Root)
	if err != nil {
		return fmt.Errorf("read the site directory: %w", err)
	}

	// The site's own mode, not a fresh one. Its directories are 0750 — the
	// account and the web server's group, nobody else — and a new one created
	// 0755 would be the one directory in the tree the rest of the host can
	// walk into.
	mode := info.Mode().Perm()
	if err := os.MkdirAll(layout.Content, mode); err != nil {
		return fmt.Errorf("create %s: %w", layout.Content, err)
	}
	// MkdirAll applies the mode only to directories it creates and is subject
	// to the umask; set it explicitly so the result is the one asked for.
	if err := os.Chmod(layout.Content, mode); err != nil {
		return fmt.Errorf("secure %s: %w", layout.Content, err)
	}

	uid, gid, ok := ownerAndGroupOf(info)
	if !ok {
		// A platform that does not report an owner. The directory is created
		// and left to root, which is visible and fixable, rather than the
		// operation failing over something cosmetic.
		return nil
	}
	if err := os.Chown(layout.Content, uid, gid); err != nil {
		return fmt.Errorf("give %s to the site's account: %w", layout.Content, err)
	}
	return nil
}

// Domains lists the sites this host has directories for.
//
// From the filesystem rather than from the panel's records, because this is
// the Agent and the filesystem is what it knows. A directory that is not a
// domain is skipped by the caller; a site the panel has forgotten still has
// logs worth reading.
func (p *Provisioner) Domains() ([]string, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.root, err)
	}

	domains := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		domains = append(domains, entry.Name())
	}
	return domains, nil
}

// SiteDir is where a domain's own directory lives.
//
// One function, so nothing has to reconstruct the convention. The domain is
// already validated by the time it reaches here; this composes rather than
// checks, and LayoutIn resolves the result against the root regardless.
func (p *Provisioner) SiteDir(domain string) string {
	return filepath.Join(p.root, domain)
}

// LayoutFor resolves the paths for a site without creating anything.
//
// The document root is supplied by the caller and must resolve inside the site
// root: the Agent runs as root, so a document root of "/etc" would otherwise
// hand a website's user ownership of the system's configuration.
// LayoutIn resolves a layout for a site whose directory is known.
//
// LayoutFor below infers the site root from the document root's parent, which
// is right only while the document root is exactly one level down. Once an
// operator can set it themselves — "public/dist" for a built front end — that
// inference puts the log directory *inside* the served one, and a site's own
// access log becomes a file anybody can fetch by guessing its name.
//
// So where the caller knows which site it is dealing with, it says so, and the
// document root is checked to be inside that site rather than trusted to imply
// it.
func (p *Provisioner) LayoutIn(siteDir, documentRoot string) (Layout, error) {
	root, err := p.validator.Resolve(siteDir)
	if err != nil {
		if errors.Is(err, pathsec.ErrOutsideRoot) {
			return Layout{}, fmt.Errorf("%w: %s", ErrOutsideRoot, p.root)
		}
		return Layout{}, err
	}
	if root == p.root || root == "/" {
		return Layout{}, fmt.Errorf("%w: a site must be nested inside %s",
			ErrOutsideRoot, p.root)
	}

	content, err := p.validator.Resolve(documentRoot)
	if err != nil {
		if errors.Is(err, pathsec.ErrOutsideRoot) {
			return Layout{}, fmt.Errorf("%w: %s", ErrOutsideRoot, p.root)
		}
		return Layout{}, err
	}

	// Inside the site, and checked on the resolved paths so a symlink cannot
	// carry the document root out of a directory it appears to be under.
	if content != root && !strings.HasPrefix(content, root+string(filepath.Separator)) {
		return Layout{}, fmt.Errorf("%w: the document root must be inside %s",
			ErrOutsideRoot, root)
	}

	logs := filepath.Join(root, LogsDir)
	return Layout{
		Root:      root,
		Content:   content,
		Logs:      logs,
		AccessLog: filepath.Join(logs, "access.log"),
		ErrorLog:  filepath.Join(logs, "error.log"),
	}, nil
}

func (p *Provisioner) LayoutFor(documentRoot string) (Layout, error) {
	resolved, err := p.validator.Resolve(documentRoot)
	if err != nil {
		if errors.Is(err, pathsec.ErrOutsideRoot) {
			return Layout{}, fmt.Errorf("%w: %s", ErrOutsideRoot, p.root)
		}
		return Layout{}, err
	}

	// The site root is the document root's parent, which is where logs live
	// alongside content rather than inside it — a log file under the document
	// root would be served to anyone who guessed its name.
	siteRoot := filepath.Dir(resolved)
	if siteRoot == p.root || siteRoot == "/" {
		return Layout{}, fmt.Errorf("%w: the document root must be nested inside %s",
			ErrOutsideRoot, p.root)
	}

	logs := filepath.Join(siteRoot, LogsDir)
	return Layout{
		Root:      siteRoot,
		Content:   resolved,
		Logs:      logs,
		AccessLog: filepath.Join(logs, "access.log"),
		ErrorLog:  filepath.Join(logs, "error.log"),
	}, nil
}

// Provision creates a site's directories and hands them to its account.
//
// It is idempotent for the account it is provisioning: that account's own
// existing directory is re-owned and re-permissioned rather than treated as an
// error, so a retried job converges instead of failing on its second attempt.
//
// It is deliberately *not* idempotent against somebody else's directory. A
// directory that already holds files belonging to another account is refused,
// because adopting it means chowning one customer's content to another — and
// the panel cannot tell a leftover from a deployment somebody is about to miss.
// Being idempotent used to mean adopting whatever was there, which is how a
// directory left by a deleted site became the property of the next site
// created once the uid was handed out again.
//
// gid is the site account's own group and is deliberately not used for the
// directories: they are group-owned by the web server so it can read them.
func (p *Provisioner) Provision(layout Layout, uid, gid int) error {
	if !p.HasWebGroup() {
		return fmt.Errorf("%w: set AGENT_WEB_GROUP to the group the web server runs as",
			ErrNoWebGroup)
	}
	gid = p.webGID

	directories := []struct {
		path string
		mode os.FileMode
	}{
		{layout.Root, siteRootMode},
		{layout.Content, contentMode},
		{layout.Logs, logsMode},
	}

	// Every directory is checked before any of them is touched. Refusing
	// halfway through would leave a half-provisioned site behind and make the
	// refusal itself the thing that needed cleaning up.
	//
	// The site root is judged without counting the two directories the layout
	// itself puts there. They are structure, not content, and each is checked
	// on its own a line later — so a refusal names the directory that actually
	// holds somebody's files rather than the one above it.
	if err := p.checkVacant(layout.Root, uid, ContentDir, LogsDir); err != nil {
		return err
	}
	if err := p.checkVacant(layout.Content, uid); err != nil {
		return err
	}
	if err := p.checkVacant(layout.Logs, uid); err != nil {
		return err
	}

	for _, dir := range directories {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return fmt.Errorf("create %s: %w", dir.path, err)
		}
		// MkdirAll leaves an existing directory's mode alone, so it is set
		// explicitly; a directory created before a umask change would
		// otherwise keep the wrong permissions forever.
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return fmt.Errorf("chmod %s: %w", dir.path, err)
		}
		if uid >= 0 && gid >= 0 {
			// os.Chown rather than shelling out: one syscall, no argument to
			// validate, and nothing for a hostile path to exploit.
			if err := os.Chown(dir.path, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", dir.path, err)
			}
		}
	}

	return nil
}

// WritePlaceholder creates a landing page if the site has no content.
//
// A brand-new site with an empty document root returns 403 from nginx, which
// looks like a broken deployment rather than an empty one.
func (p *Provisioner) WritePlaceholder(layout Layout, domain string, uid, gid int) error {
	index := filepath.Join(layout.Content, "index.html")

	// The placeholder is served by the web server, so it carries the same
	// group ownership as the directory holding it.
	if p.HasWebGroup() {
		gid = p.webGID
	}

	if _, err := os.Stat(index); err == nil {
		// Content already exists; overwriting it would destroy a deployment.
		//
		// Its ownership is still corrected, but the reach of that is now much
		// smaller than it was. This used to be the panel's answer to a
		// directory left behind by a deleted site: adopt the leftover so the
		// new site did not return 403. It cured the symptom and spread the
		// disease, because adopting a leftover is exactly how one customer's
		// files end up owned by another. Provision now refuses that directory
		// outright, so the only tree that reaches this line belongs to the
		// account being provisioned already — a retried job, or a site whose
		// index was written before a chown failed.
		if uid >= 0 && gid >= 0 {
			if err := os.Chown(index, uid, gid); err != nil {
				return fmt.Errorf("own the existing index: %w", err)
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat index: %w", err)
	}

	// The domain is inserted into HTML, so it is escaped. It is already
	// validated to be a hostname, but escaping costs nothing and means this
	// stays safe if the validation is ever relaxed.
	page := placeholderPage(domain)

	if err := os.WriteFile(index, []byte(page), indexMode); err != nil {
		return fmt.Errorf("write placeholder: %w", err)
	}
	// WriteFile's mode is filtered by the process umask, and the Agent runs
	// with 0077 — so the group bit indexMode asks for was being dropped and
	// the placeholder landed 0600. The directories above it are already
	// chmodded explicitly for exactly this reason; this file was the one that
	// was not, and nginx cannot open what it cannot read: every brand-new
	// site answered 403 until something else rewrote the file.
	if err := os.Chmod(index, indexMode); err != nil {
		return fmt.Errorf("secure the placeholder: %w", err)
	}
	if uid >= 0 && gid >= 0 {
		if err := os.Chown(index, uid, gid); err != nil {
			return fmt.Errorf("chown placeholder: %w", err)
		}
	}
	return nil
}

// Remove deletes a site's directory tree.
//
// The path is re-validated immediately before deletion rather than trusting
// the caller's earlier check: this is a recursive delete running as root, and
// the cost of being wrong is unbounded.
func (p *Provisioner) Remove(layout Layout) error {
	resolved, err := p.validator.Resolve(layout.Root)
	if err != nil {
		return fmt.Errorf("%w: refusing to remove %s", ErrOutsideRoot, layout.Root)
	}
	if resolved == p.root {
		return fmt.Errorf("%w: refusing to remove the site root itself", ErrOutsideRoot)
	}

	if err := os.RemoveAll(resolved); err != nil {
		return fmt.Errorf("remove site directory: %w", err)
	}
	return nil
}

// Exists reports whether a site's directory is present.
func (p *Provisioner) Exists(layout Layout) (bool, error) {
	if _, err := os.Stat(layout.Content); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat site content: %w", err)
	}
	return true, nil
}

// checkVacant refuses a directory that already holds another account's files.
//
// The three cases it has to tell apart:
//
//   - It does not exist. Nothing to adopt; provisioning creates it.
//   - It exists and is empty. Adopting it is harmless whoever owns it —
//     there is no content to hand over — and this is the ordinary case for a
//     site root whose logs and content directories are made a moment later.
//   - It exists and holds files. Then ownership decides. The same account is
//     a retried job converging, which must keep working. A different account
//     is either a live customer's data or a dead one's, and neither belongs
//     to the site being created.
//
// Emptiness rather than existence is the test because the alternative —
// refusing any directory that exists — would break the retry path that made
// Provision idempotent in the first place.
//
// ignore names entries that do not count towards emptiness. It exists for the
// site root, which holds the content and logs directories by definition and
// would otherwise be reported as occupied by its own layout.
func (p *Provisioner) checkVacant(path string, uid int, ignore ...string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s exists and is not a directory", ErrOccupied, path)
	}

	owner, ok := ownerOf(info)
	if !ok || owner == uid {
		// Not a Unix filesystem, or already ours. Either way there is nothing
		// here that belongs to somebody else.
		return nil
	}

	empty, err := isEmptyDir(path, ignore...)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}

	return fmt.Errorf("%w: %s is owned by uid %d and is not empty; "+
		"remove it or reassign it before creating this site", ErrOccupied, path, owner)
}

// Neutralize strips a retained site tree of its former owner.
//
// This runs when a website is deleted with its files kept — the default, and a
// deliberate one: a deleted vhost can be recreated, deleted content cannot.
// What is *not* deliberate is what used to happen next. The account was
// removed a moment later, its uid went back into the allocation pool, and the
// files kept carrying that number. The next few sites created took uids from
// the same pool, and one of them was eventually handed the number written on a
// previous customer's files. Nothing announced it; the directory simply began
// belonging to somebody else.
//
// So the tree is given to root, which is the one uid the system will never
// hand out, and closed to everybody else. The files survive, which is the
// point of keeping them; what does not survive is the claim on them.
//
// Lchown rather than Chown, and no symlink is followed. The Agent is root and
// this walks a tree a customer controlled: a symlink to /etc/shadow would
// otherwise be handed straight to whatever this is asked to reassign.
func (p *Provisioner) Neutralize(layout Layout) error {
	resolved, err := p.validator.Resolve(layout.Root)
	if err != nil {
		return fmt.Errorf("%w: refusing to reassign %s", ErrOutsideRoot, layout.Root)
	}
	if resolved == p.root {
		return fmt.Errorf("%w: refusing to reassign the site root itself", ErrOutsideRoot)
	}

	if _, err := os.Lstat(resolved); err != nil {
		if os.IsNotExist(err) {
			// Files already gone. Nothing carries the uid, so nothing to do.
			return nil
		}
		return fmt.Errorf("stat %s: %w", resolved, err)
	}

	walkErr := filepath.WalkDir(resolved, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if err := os.Lchown(path, orphanUID, orphanGID); err != nil {
			return fmt.Errorf("reassign %s: %w", path, err)
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	// Only the top of the tree is re-permissioned. Root owns every inode now,
	// so the modes underneath grant nothing to anybody; rewriting them all
	// would destroy the permissions a restored backup would want back.
	if err := os.Chmod(resolved, orphanMode); err != nil {
		return fmt.Errorf("close %s: %w", resolved, err)
	}
	return nil
}

// OwnershipFinding is one site directory whose owner is not what it should be.
type OwnershipFinding struct {
	Path string `json:"path"`
	UID  int    `json:"uid"`
	// Owner is the account that holds the uid, empty if none does.
	Owner string `json:"owner,omitempty"`
	// OwnerHome is that account's home directory. A site account's home is its
	// own site root, so a home pointing somewhere else means this directory is
	// owned by an account that belongs to a different site.
	OwnerHome string `json:"owner_home,omitempty"`
	// Orphaned means no account holds the uid at all. This is the unambiguous
	// case: nothing can justify it, and the number is queued for reuse.
	Orphaned bool `json:"orphaned"`
}

// AuditOwnership reports site directories owned by the wrong account.
//
// Two kinds of wrong, and they are not equally certain, which is the whole
// reason this reports rather than repairs:
//
//   - Orphaned: no account holds the uid. Nothing explains this and nothing
//     can justify it. The number is sitting in the allocation pool waiting to
//     be handed to the next account created, at which point the directory
//     silently changes hands.
//   - Misowned: an account holds the uid, but its home is a different site's
//     root. Usually this is the above, one step later — the uid was already
//     recycled. But it is also exactly what a subdomain that inherits its
//     parent's account looks like, which is legitimate and common.
//
// The Agent cannot tell those two apart. It knows what is on the disk; only
// the panel knows which sites are live and which of them share an account. So
// misowned directories are reported and left alone, and only the orphaned ones
// are safe to act on without asking. Guessing here would mean reassigning a
// live subdomain's directory and taking a working site off the air.
func (p *Provisioner) AuditOwnership() ([]OwnershipFinding, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.root, err)
	}

	var findings []OwnershipFinding
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(p.root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			// A directory that vanished between the listing and the stat is
			// not a finding; anything else is worth knowing about.
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}

		uid, ok := ownerOf(info)
		if !ok || uid == orphanUID {
			// Root-owned is the safe state, not a finding. It covers both an
			// already-neutralised leftover and the directories the web server's
			// own package installs alongside the sites — on Alpine, nginx puts
			// localhost, logs, modules and run under /var/www.
			continue
		}

		account, err := user.LookupId(strconv.Itoa(uid))
		if err != nil {
			var unknown user.UnknownUserIdError
			if errors.As(err, &unknown) {
				findings = append(findings, OwnershipFinding{
					Path: path, UID: uid, Orphaned: true,
				})
				continue
			}
			return nil, fmt.Errorf("look up uid %d: %w", uid, err)
		}

		// A site account's home is its own site root, set when the account is
		// created. Anything else means this directory belongs to another site.
		if filepath.Clean(account.HomeDir) != path {
			findings = append(findings, OwnershipFinding{
				Path: path, UID: uid, Owner: account.Username,
				OwnerHome: account.HomeDir,
			})
		}
	}
	return findings, nil
}

// RepairOrphans reassigns the directories no account owns at all.
//
// Only the orphaned findings, and deliberately so — see AuditOwnership for why
// the misowned ones need a person. An orphan is safe because the uid resolves
// to nobody: there is no site it could belong to and no account whose access
// is being taken away. There is only a number waiting to be reused.
//
// Returns the paths it reassigned. A failure on one directory stops the sweep
// rather than pressing on, because the reason one chown failed is usually the
// reason the next one will.
func (p *Provisioner) RepairOrphans() ([]string, error) {
	findings, err := p.AuditOwnership()
	if err != nil {
		return nil, err
	}

	var repaired []string
	for _, finding := range findings {
		if !finding.Orphaned {
			continue
		}
		layout, err := p.LayoutFor(filepath.Join(finding.Path, ContentDir))
		if err != nil {
			return repaired, fmt.Errorf("resolve %s: %w", finding.Path, err)
		}
		if err := p.Neutralize(layout); err != nil {
			return repaired, fmt.Errorf("reassign %s: %w", finding.Path, err)
		}
		repaired = append(repaired, finding.Path)
	}
	return repaired, nil
}
