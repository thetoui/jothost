package ssl

import (
	"strings"
	"testing"
)

// A rate limit is the one certbot failure where trying again makes things
// worse, so it has to be recognised and said plainly — not left as the last
// six lines of certbot's output for an operator to decode, and then retry.

func TestARepeatedCertificateLimitIsRecognisedWithItsResetTime(t *testing.T) {
	// The wording certbot printed for years.
	stderr := `An unexpected error occurred:
Error creating new order :: too many certificates (5) already issued for this exact set of domains in the last 168 hours: example.test, retry after 2026-09-27T10:00:00Z: see https://letsencrypt.org/docs/rate-limits/
Ask for help or search for solutions at https://community.letsencrypt.org.`

	detail, ok := rateLimited(stderr, "")
	if !ok {
		t.Fatal("a rate-limit refusal was not recognised")
	}
	if !strings.Contains(detail, "do not retry before 2026-09-27T10:00:00Z") {
		t.Errorf("the message does not say when to try again: %s", detail)
	}
	if !strings.Contains(detail, "too many certificates") {
		t.Errorf("the message dropped which limit it was: %s", detail)
	}
}

func TestTheCurrentRateLimitWordingIsRecognised(t *testing.T) {
	// Let's Encrypt's newer limits say "identifiers", use a duration, and give
	// the reset time with a space and "UTC".
	stderr := `An unexpected error occurred:
too many failed authorizations (5) for "example.test" in the last 1h0m0s, retry after 2026-09-26 11:04:12 UTC: see https://letsencrypt.org/docs/rate-limits/#authorization-failures-per-hostname-per-account`

	detail, ok := rateLimited(stderr)
	if !ok {
		t.Fatal("a failed-authorization limit was not recognised")
	}
	if !strings.Contains(detail, "2026-09-26 11:04:12 UTC") {
		t.Errorf("the reset time was not found: %s", detail)
	}
}

func TestARateLimitWithoutAResetTimeStillSaysNotToRetry(t *testing.T) {
	detail, ok := rateLimited("urn:ietf:params:acme:error:rateLimited :: slow down")
	if !ok {
		t.Fatal("the ACME rateLimited problem type was not recognised")
	}
	if !strings.Contains(detail, "do not retry before the limit resets") {
		t.Errorf("no guidance on when to retry: %s", detail)
	}
}

func TestAnOrdinaryFailureIsNotCalledARateLimit(t *testing.T) {
	// Calling a DNS problem a rate limit would tell the operator to wait for
	// something that is never going to fix itself.
	for _, output := range []string{
		`Certbot failed to authenticate some domains (authenticator: webroot). The Certificate Authority reported these problems:
  Domain: example.test
  Type:   dns
  Detail: DNS problem: NXDOMAIN looking up A for example.test`,
		`Timeout during connect (likely firewall problem)`,
		"",
	} {
		if detail, ok := rateLimited(output); ok {
			t.Errorf("an ordinary failure was reported as a rate limit: %s", detail)
		}
	}
}
