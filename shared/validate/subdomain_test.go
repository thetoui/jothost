package validate

import (
	"errors"
	"testing"
)

func TestSubdomainAcceptsNamesUnderTheParent(t *testing.T) {
	valid := []struct{ name, parent string }{
		{"shop.example.com", "example.com"},
		{"dev.shop.example.com", "example.com"},
		{"*.example.com", "example.com"},
		{"a.example.co.uk", "example.co.uk"},
		{"SHOP.Example.com", "example.com"},
	}
	for _, tc := range valid {
		if err := Subdomain(tc.name, tc.parent); err != nil {
			t.Errorf("Subdomain(%q, %q) = %v, want nil", tc.name, tc.parent, err)
		}
	}
}

// A subdomain the panel would serve for a name its owner has no claim to is
// the fault this test exists for: on a shared host it means one customer
// serving content at another customer's domain.
func TestSubdomainRefusesNamesOutsideTheParent(t *testing.T) {
	invalid := []struct{ name, parent, why string }{
		{"shop.other.com", "example.com", "a different domain entirely"},
		{"notexample.com", "example.com", "a suffix match without the separating dot"},
		{"example.com", "example.com", "the parent's own domain"},
		{"example.com.evil.com", "example.com", "the parent as a prefix"},
		{".example.com", "example.com", "an empty label"},
		{"shop.example.com", "example", "a single-label parent"},
		{"*.shop.*.example.com", "example.com", "a wildcard in the middle"},
		{"*example.com", "example.com", "a wildcard that is not its own label"},
		{"shop example.com", "example.com", "a space, which would close the directive"},
		{"shop.example.com\nserver{}", "example.com", "a newline"},
	}
	for _, tc := range invalid {
		if err := Subdomain(tc.name, tc.parent); err == nil {
			t.Errorf("Subdomain(%q, %q) = nil, want an error: %s", tc.name, tc.parent, tc.why)
		}
	}
}

func TestSubdomainReportsWhyItRefused(t *testing.T) {
	err := Subdomain("shop.other.com", "example.com")
	if !errors.Is(err, ErrInvalidSubdomain) {
		t.Fatalf("expected ErrInvalidSubdomain, got %v", err)
	}
}

func TestServerNameAcceptsBothShapes(t *testing.T) {
	if err := ServerName("example.com"); err != nil {
		t.Errorf("plain domain: %v", err)
	}
	if err := ServerName("*.example.com"); err != nil {
		t.Errorf("wildcard: %v", err)
	}
	if err := ServerName("*"); err == nil {
		t.Error("a bare wildcard must be refused: it would answer for every name on the host")
	}
	if err := ServerName("localhost"); err == nil {
		t.Error("a single label must be refused")
	}
}

func TestIsWildcard(t *testing.T) {
	if !IsWildcard("*.example.com") {
		t.Error("*.example.com is a wildcard")
	}
	if IsWildcard("example.com") {
		t.Error("example.com is not a wildcard")
	}
}

func TestSubdomainName(t *testing.T) {
	cases := []struct{ label, parent, want string }{
		{"shop", "example.com", "shop.example.com"},
		{"  Shop  ", "Example.com", "shop.example.com"},
		{"dev.shop", "example.com", "dev.shop.example.com"},
		{".shop.", "example.com", "shop.example.com"},
	}
	for _, tc := range cases {
		if got := SubdomainName(tc.label, tc.parent); got != tc.want {
			t.Errorf("SubdomainName(%q, %q) = %q, want %q", tc.label, tc.parent, got, tc.want)
		}
	}
}

func TestModesRefuseAnythingUnknown(t *testing.T) {
	if err := DocumentRootMode("nested"); err != nil {
		t.Errorf("nested: %v", err)
	}
	if err := DocumentRootMode("somewhere-else"); !errors.Is(err, ErrInvalidDocumentRootMode) {
		t.Errorf("expected ErrInvalidDocumentRootMode, got %v", err)
	}
	if err := PHPPoolMode("inherit"); err != nil {
		t.Errorf("inherit: %v", err)
	}
	if err := PHPPoolMode(""); !errors.Is(err, ErrInvalidPHPPoolMode) {
		t.Errorf("expected ErrInvalidPHPPoolMode, got %v", err)
	}
	if err := SystemUserMode("dedicated"); err != nil {
		t.Errorf("dedicated: %v", err)
	}
	if err := SystemUserMode("parent"); !errors.Is(err, ErrInvalidSystemUserMode) {
		t.Errorf("expected ErrInvalidSystemUserMode, got %v", err)
	}
}
