package pma

import (
	"fmt"
	"strings"

	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/shared/validate"
)

// requiredExtensions are the PHP extensions phpMyAdmin needs, by the name the
// extension has rather than the name of its package.
//
// mysqli is the one it cannot start without — a phpMyAdmin whose only error is
// "the mysqli extension is missing" is a phpMyAdmin nobody can use, and that is
// exactly what installing the package alone produces. The rest are what its
// documentation asks for, and what the difference is between a working
// installation and one that fails the first time somebody exports a table.
var requiredExtensions = []string{
	// Without these it does not run at all.
	"mysqli", "mbstring", "session", "ctype", "iconv",
	// Cookie authentication encrypts the session with one of these. Without
	// either, signing in fails with an error about the blowfish secret.
	"openssl", "sodium",
	// Import and export.
	"dom", "xml", "simplexml", "zip", "bz2", "fileinfo",
	// Charts, and the designer view.
	"gd",
	// Some pages use PDO rather than mysqli.
	"pdo_mysql",
}

// aptExtensionPackages maps an extension to its Debian package where the name
// differs. Debian bundles many of these into php-common or the core package.
var aptExtensionPackages = map[string]string{
	"mysqli":    "mysql",
	"pdo_mysql": "mysql",
	"simplexml": "xml",
	"dom":       "xml",
	"session":   "", // in the core package
	"ctype":     "", // in the core package
	"iconv":     "", // in the core package
	"openssl":   "", // in the core package
	"fileinfo":  "", // in the core package
	"sodium":    "sodium",
}

// extensionPackages returns the packages to install for one PHP version.
//
// Duplicates are collapsed, because several extensions share a package on
// Debian and asking apt for the same one five times is noise in a log an
// operator may be reading to work out why an install failed.
func extensionPackages(manager, version string) ([]string, error) {
	if err := validate.PHPVersion(version); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(requiredExtensions))
	packages := make([]string, 0, len(requiredExtensions))

	for _, extension := range requiredExtensions {
		name, ok := extensionPackage(manager, version, extension)
		if !ok || name == "" {
			continue
		}
		if _, repeat := seen[name]; repeat {
			continue
		}
		seen[name] = struct{}{}
		packages = append(packages, name)
	}

	if len(packages) == 0 {
		return nil, fmt.Errorf("%w: no extension packages are known for %s",
			ErrUnsupported, manager)
	}
	return packages, nil
}

// extensionPackage names the package providing one extension.
func extensionPackage(manager, version, extension string) (string, bool) {
	switch manager {
	case php.ManagerAPK:
		// Alpine: php84-mysqli, php83-mbstring, and so on.
		return "php" + validate.PHPVersionCompact(version) + "-" + extension, true
	case php.ManagerAPT:
		name, known := aptExtensionPackages[extension]
		if !known {
			name = extension
		}
		if name == "" {
			// Already in the core package; nothing to install.
			return "", true
		}
		// Debian: php8.4-mysql, php8.3-mbstring.
		return "php" + strings.TrimSpace(version) + "-" + name, true
	default:
		return "", false
	}
}
