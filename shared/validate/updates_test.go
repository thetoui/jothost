package validate

import "testing"

func TestPackageNameRefusesWhatWouldBeReadAsAnOption(t *testing.T) {
	// The whole point of this rule: "--allow-untrusted" is not a package, it is
	// a flag, and passing it through would let a request change how the package
	// manager behaves rather than what it operates on.
	for _, bad := range []string{
		"--allow-untrusted",
		"-y",
		"nginx=1.2.3",
		"nginx>1.2",
		"nginx;rm -rf /",
		"nginx nginx",
		"",
	} {
		if err := PackageName(bad); err == nil {
			t.Errorf("PackageName(%q) was accepted", bad)
		}
	}
}

func TestPackageNameAcceptsTheNamesBothManagersUse(t *testing.T) {
	// Alpine and Debian names, including the shapes this panel installs.
	for _, name := range []string{
		"nginx", "php84-fpm", "proftpd-mod_tls", "libgcrypt20",
		"g++", "ca-certificates", "linux-image-6.1.0-18-amd64",
	} {
		if err := PackageName(name); err != nil {
			t.Errorf("PackageName(%q) refused: %v", name, err)
		}
	}
}

func TestPackageVersionAcceptsBothGrammars(t *testing.T) {
	// Alpine's, and Debian's with an epoch — neither is worth reimplementing,
	// so what is checked is that they cannot be read as an option.
	for _, version := range []string{"2.47.3-r0", "1:2.38.1-5+deb12u3", "1.10.1-3~bpo12+1"} {
		if err := PackageVersion(version); err != nil {
			t.Errorf("PackageVersion(%q) refused: %v", version, err)
		}
	}
	for _, bad := range []string{"", "-1.0", "1.0 && reboot", "1.0;x"} {
		if err := PackageVersion(bad); err == nil {
			t.Errorf("PackageVersion(%q) was accepted", bad)
		}
	}
}

func TestUpdatePolicyIsAnAllowlist(t *testing.T) {
	for _, policy := range UpdatePolicies {
		if err := UpdatePolicy(policy); err != nil {
			t.Errorf("UpdatePolicy(%q) refused: %v", policy, err)
		}
	}
	for _, bad := range []string{"", "on", "yes", "ALL"} {
		if err := UpdatePolicy(bad); err == nil {
			t.Errorf("UpdatePolicy(%q) was accepted", bad)
		}
	}
}

func TestUpdateWindowBounds(t *testing.T) {
	if err := UpdateWindow(EveryDay, 3, 30); err != nil {
		t.Fatalf("a nightly window was refused: %v", err)
	}
	if err := UpdateWindow(0, 0, 0); err != nil {
		t.Fatalf("Sunday midnight was refused: %v", err)
	}
	for _, window := range [][3]int{{7, 3, 0}, {-2, 3, 0}, {0, 24, 0}, {0, 3, 60}, {0, -1, 0}} {
		if err := UpdateWindow(window[0], window[1], window[2]); err == nil {
			t.Errorf("UpdateWindow%v was accepted", window)
		}
	}
}
