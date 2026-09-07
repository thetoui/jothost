package ssl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jothost/panel/api/internal/dns"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/php"
	"github.com/jothost/panel/api/internal/ssl"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/websites"
)

// Issuance points the certificate's names at this host before asking a
// certificate authority for anything.
//
// The authority resolves every name on the certificate and fetches a file from
// whatever answers. A name resolving nowhere fails, and a failed HTTP-01
// challenge is spent: Let's Encrypt allows a handful per hour and then stops
// looking. So the check that matters is not that the panel *can* add a record
// but that it does so before the job is queued, and not at all when nothing is
// going to resolve anything.

type recordingAligner struct {
	calls  [][]string
	report dns.Alignment
	err    error
}

func (a *recordingAligner) AlignForCertificate(_ context.Context, _ dns.Actor, _ string,
	names []string,
) (dns.Alignment, error) {
	a.calls = append(a.calls, names)
	if a.err != nil {
		return dns.Alignment{}, a.err
	}
	return a.report, nil
}

// alignerFixture is newFixture with a stub name server attached.
func alignerFixture(t *testing.T, aligner ssl.DNSAligner) *fixture {
	t.Helper()

	deps := testsupport.Require(t)
	serverID := registerServer(t, deps.Pool)

	repo := ssl.NewRepository(deps.Pool)
	websiteRepo := websites.NewRepository(deps.Pool)
	jobRepo := jobs.NewRepository(deps.Pool)

	service := ssl.NewService(ssl.ServiceOptions{
		Repository: repo,
		Websites:   websiteRepo,
		PHP:        php.NewRepository(deps.Pool),
		Jobs:       jobRepo,
		DNS:        aligner,
	})

	return &fixture{
		service:  service,
		repo:     repo,
		websites: websiteRepo,
		jobs:     jobRepo,
		renewer:  ssl.NewRenewer(repo, websiteRepo, service, nil),
		pool:     deps.Pool,
		siteSvc: websites.NewService(websites.ServiceOptions{
			Repository: websiteRepo,
			Jobs:       jobRepo,
			ServerID:   serverID,
		}),
	}
}

func TestIssuingFromLetsEncryptPointsTheNamesAtThisHostFirst(t *testing.T) {
	aligner := &recordingAligner{report: dns.Alignment{
		Address: "203.0.113.10",
		Added:   1,
		Names: []dns.NameOutcome{
			{Name: "example.test", Status: dns.AlignAdded, Zone: "example.test"},
		},
	}}
	f := alignerFixture(t, aligner)
	ctx := context.Background()
	site := f.activeSite(t, "example.test")

	issued, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderLetsEncrypt,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if len(aligner.calls) != 1 {
		t.Fatalf("DNS was asked about the names %d times, want 1", len(aligner.calls))
	}
	if len(aligner.calls[0]) == 0 || aligner.calls[0][0] != "example.test" {
		t.Fatalf("the names sent to DNS were %v", aligner.calls[0])
	}

	// And it comes back to the caller. A record the panel added, or a name it
	// could not point here, is the usual reason issuance fails or succeeds —
	// and the moment to say so is while somebody is still looking at the
	// dialog they clicked Issue in, not in a log.
	if issued.DNS == nil {
		t.Fatal("the DNS report was not returned with the job")
	}
	if issued.DNS.Added != 1 {
		t.Fatalf("added = %d, want 1", issued.DNS.Added)
	}
}

func TestASelfSignedCertificateTouchesNoDNS(t *testing.T) {
	// Nothing resolves anything for a self-signed certificate: it is written
	// on this host and no authority ever looks the name up. Adding records for
	// one would change a zone on the internet to satisfy a file on disk.
	aligner := &recordingAligner{}
	f := alignerFixture(t, aligner)
	ctx := context.Background()
	site := f.activeSite(t, "example.test")

	issued, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderSelfSigned,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(aligner.calls) != 0 {
		t.Fatalf("a self-signed certificate changed DNS: %v", aligner.calls)
	}
	if issued.DNS != nil {
		t.Fatal("a self-signed certificate reported a DNS alignment")
	}
}

func TestDNSFailingDoesNotStopIssuance(t *testing.T) {
	// The panel's zones are one of several places a name can be served from.
	// Refusing to issue because this one could not be read would block every
	// host whose DNS lives at a registrar, which is most of them.
	aligner := &recordingAligner{err: errors.New("the name server is not answering")}
	f := alignerFixture(t, aligner)
	ctx := context.Background()
	site := f.activeSite(t, "example.test")

	issued, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderLetsEncrypt,
	})
	if err != nil {
		t.Fatalf("issuance was refused because DNS failed: %v", err)
	}
	if issued.Job.Type != jobs.TypeSSLIssue {
		t.Fatalf("job type = %q", issued.Job.Type)
	}
	if issued.DNS != nil {
		t.Fatal("a failed alignment was reported as though it had happened")
	}
}
