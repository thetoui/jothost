// Command capacity measures what one host running JotHost Panel can carry.
//
// It answers the three questions the production roadmap's capacity item asks —
// how many websites a host can provision and how fast, how the panel behaves
// under concurrent users, and how long a full backup takes — by driving a real,
// installed panel through its own API and timing what comes back.
//
// # What it is not
//
// A number from this tool is a supported limit only when it was measured on the
// reference hardware the limits are published for. Run on a laptop, in a
// container, or on a shared VM, it measures that machine on that day: useful
// for spotting a regression against an earlier run on the same host, useless as
// a promise to an operator. The report says so at the top, every time, so a
// figure cannot be lifted out of context.
//
// # Usage
//
//	capacity -url https://panel.example.com -user admin -password '…' \
//	         -host-spec "Hetzner CPX21, 3 vCPU / 4 GB, NVMe" -out report.md
//
// Each phase can be skipped, so a run can measure one thing without the side
// effects of the others. The provisioning phase creates real websites; pass
// -cleanup to delete them afterwards, or leave them to measure a loaded host.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	opts := parseFlags()

	client := newClient(opts)
	if err := client.login(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "capacity: sign in failed: %v\n", err)
		os.Exit(1)
	}

	report := &report{
		target:   opts.url,
		hostSpec: opts.hostSpec,
		started:  time.Now(),
	}

	if !opts.skipSites {
		report.sites = runProvisioning(client, opts)
	}
	if !opts.skipLoad {
		report.load = runLoad(client, opts)
	}
	if !opts.skipBackup {
		report.backup = runBackup(client, opts)
	}

	report.finished = time.Now()
	out := report.render()
	fmt.Print(out)
	if opts.out != "" {
		if err := os.WriteFile(opts.out, []byte(out), 0o644); err != nil { //nolint:gosec // a report the operator asked to write
			fmt.Fprintf(os.Stderr, "capacity: could not write %s: %v\n", opts.out, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "\nwritten to %s\n", opts.out)
	}
}

// ------------------------------------------------------------------ options

type options struct {
	url       string
	user      string
	password  string
	insecure  bool
	out       string
	hostSpec  string
	domainTLD string

	sites        int
	provTimeout  time.Duration
	cleanup      bool
	users        int
	loadDuration time.Duration
	destination  string

	skipSites, skipLoad, skipBackup bool
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.url, "url", "https://localhost", "the panel's base URL")
	flag.StringVar(&o.user, "user", "admin", "administrator username")
	flag.StringVar(&o.password, "password", "", "administrator password (required)")
	flag.BoolVar(&o.insecure, "insecure", false, "accept a self-signed certificate")
	flag.StringVar(&o.out, "out", "", "also write the report to this file")
	flag.StringVar(&o.hostSpec, "host-spec", "", "a description of this host, recorded in the report")
	flag.StringVar(&o.domainTLD, "domain-suffix", "capacity.test", "suffix for the throwaway domains created")

	flag.IntVar(&o.sites, "sites", 25, "how many websites to provision in the provisioning phase")
	flag.DurationVar(&o.provTimeout, "provision-timeout", 90*time.Second, "how long to wait for one site to become active")
	flag.BoolVar(&o.cleanup, "cleanup", false, "delete the websites created by the provisioning phase afterwards")
	flag.IntVar(&o.users, "users", 20, "concurrent panel users in the load phase")
	flag.DurationVar(&o.loadDuration, "load-duration", 30*time.Second, "how long the load phase runs")
	flag.StringVar(&o.destination, "destination-id", "", "an existing backup destination id for the backup phase; empty skips it")

	flag.BoolVar(&o.skipSites, "skip-sites", false, "skip the provisioning phase")
	flag.BoolVar(&o.skipLoad, "skip-load", false, "skip the concurrent-load phase")
	flag.BoolVar(&o.skipBackup, "skip-backup", false, "skip the backup-timing phase")
	flag.Parse()

	if o.password == "" {
		fmt.Fprintln(os.Stderr, "capacity: -password is required")
		os.Exit(2)
	}
	return o
}

// ------------------------------------------------------------------- client

type client struct {
	http  *http.Client
	base  string
	user  string
	pass  string
	token string
}

func newClient(o options) *client {
	transport := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 512,
		MaxConnsPerHost:     0,
	}
	if o.insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // a self-signed install is a deliberate mode
	}
	return &client{
		http: &http.Client{Timeout: 60 * time.Second, Transport: transport},
		base: strings.TrimRight(o.url, "/"),
		user: o.user,
		pass: o.password,
	}
}

// envelope is the API's standard response wrapper.
type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *client) login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"username": c.user, "password": c.pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	var pair struct {
		AccessToken string `json:"access_token"`
		MFARequired bool   `json:"mfa_required"`
	}
	if err := json.Unmarshal(env.Data, &pair); err != nil {
		return err
	}
	if pair.MFARequired {
		return fmt.Errorf("this account requires two-factor authentication; use one without it for load testing")
	}
	if pair.AccessToken == "" {
		return fmt.Errorf("no access token in the response")
	}
	c.token = pair.AccessToken
	return nil
}

// do issues one authenticated request and returns status and body.
func (c *client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

// ------------------------------------------------------- provisioning phase

type sitesResult struct {
	requested int
	active    int
	failed    int
	latencies []time.Duration
	wallTime  time.Duration
	firstErr  string
	created   []string // ids, for cleanup
}

// runProvisioning creates websites one after another and times how long each
// takes to become active. Serial on purpose: this measures how quickly a host
// can turn a request into a working site — nginx config, system user, PHP pool
// — not how many requests the API will accept at once, which the load phase
// covers. A host that provisions the 200th site as fast as the 2nd is one
// answer; one that slows as the tree fills is the answer worth knowing.
func runProvisioning(c *client, o options) *sitesResult {
	fmt.Fprintf(os.Stderr, "provisioning %d websites...\n", o.sites)
	res := &sitesResult{requested: o.sites}
	stamp := time.Now().Unix()
	start := time.Now()

	for i := 0; i < o.sites; i++ {
		domain := fmt.Sprintf("cap-%d-%d.%s", stamp, i, o.domainTLD)
		siteStart := time.Now()
		status, raw, err := c.do(context.Background(), http.MethodPost, "/api/v1/websites",
			map[string]string{"domain": domain, "name": domain})
		if err != nil || status >= 300 {
			res.failed++
			if res.firstErr == "" {
				res.firstErr = fmt.Sprintf("create %s: status %d %s", domain, status, firstLine(raw))
			}
			continue
		}
		// The create response is {"website": {...}, "job": {...}}: the id is
		// on the nested website, not at the top level.
		id := extractNestedString(raw, "website", "id")
		if id == "" {
			res.failed++
			if res.firstErr == "" {
				res.firstErr = fmt.Sprintf("create %s: no website id in response: %s", domain, firstLine(raw))
			}
			continue
		}
		res.created = append(res.created, id)

		if waitActive(c, id, o.provTimeout) {
			res.active++
			res.latencies = append(res.latencies, time.Since(siteStart))
		} else {
			res.failed++
			if res.firstErr == "" {
				res.firstErr = fmt.Sprintf("%s did not become active within %s", domain, o.provTimeout)
			}
		}
		if (i+1)%10 == 0 {
			fmt.Fprintf(os.Stderr, "  %d/%d\n", i+1, o.sites)
		}
	}
	res.wallTime = time.Since(start)

	if o.cleanup {
		fmt.Fprintf(os.Stderr, "cleaning up %d websites...\n", len(res.created))
		for _, id := range res.created {
			_, _, _ = c.do(context.Background(), http.MethodDelete, "/api/v1/websites/"+id, nil)
		}
	}
	return res
}

// waitActive polls one site until it is active or failed.
func waitActive(c *client, id string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, raw, err := c.do(context.Background(), http.MethodGet, "/api/v1/websites/"+id, nil)
		if err == nil && status == http.StatusOK {
			switch extractString(raw, "status") {
			case "active":
				return true
			case "failed":
				return false
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// --------------------------------------------------------------- load phase

type loadResult struct {
	users     int
	duration  time.Duration
	requests  int64
	errors    int64
	statuses  map[int]int64
	latencies []time.Duration
}

// runLoad drives concurrent authenticated readers against the pages a panel
// user actually opens, and reports the latency distribution and error rate.
//
// Reads only: this measures the panel under load, not a stress test that
// mutates the host, and every request is one a signed-in operator makes by
// clicking around. One shared token, because a real session reuses one and the
// login limiter is not what this is measuring.
func runLoad(c *client, o options) *loadResult {
	fmt.Fprintf(os.Stderr, "load: %d concurrent users for %s...\n", o.users, o.loadDuration)
	endpoints := []string{
		"/api/v1/dashboard",
		"/api/v1/websites",
		"/api/v1/jobs",
		"/api/v1/monitoring",
		"/api/v1/backups",
		"/api/v1/audit?limit=25",
	}
	res := &loadResult{users: o.users, duration: o.loadDuration, statuses: map[int]int64{}}

	ctx, cancel := context.WithTimeout(context.Background(), o.loadDuration)
	defer cancel()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		reqs    atomic.Int64
		errs    atomic.Int64
		perUser = make([][]time.Duration, o.users)
		codes   = make([]map[int]int64, o.users)
	)
	for u := 0; u < o.users; u++ {
		codes[u] = map[int]int64{}
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			i := u // stagger which endpoint each worker starts on
			for ctx.Err() == nil {
				path := endpoints[i%len(endpoints)]
				i++
				begin := time.Now()
				status, _, err := c.do(ctx, http.MethodGet, path, nil)
				elapsed := time.Since(begin)
				if ctx.Err() != nil {
					return // deadline hit mid-request; do not count a torn-off call
				}
				reqs.Add(1)
				perUser[u] = append(perUser[u], elapsed)
				if err != nil {
					errs.Add(1)
					codes[u][0]++
					continue
				}
				codes[u][status]++
				if status >= 400 {
					errs.Add(1)
				}
			}
		}(u)
	}
	wg.Wait()

	res.requests = reqs.Load()
	res.errors = errs.Load()
	for u := 0; u < o.users; u++ {
		mu.Lock()
		res.latencies = append(res.latencies, perUser[u]...)
		for code, n := range codes[u] {
			res.statuses[code] += n
		}
		mu.Unlock()
	}
	return res
}

// ------------------------------------------------------------- backup phase

type backupResult struct {
	skipped     bool
	reason      string
	wallTime    time.Duration
	sizeBytes   int64
	status      string
	backupType  string
	destination string
}

// runBackup takes one panel backup and times it end to end, including the
// read-back verification the panel does before it reports success — because
// that is the time an operator actually waits, and the part that grows with the
// data.
func runBackup(c *client, o options) *backupResult {
	res := &backupResult{backupType: "panel", destination: o.destination}
	if o.destination == "" {
		res.skipped = true
		res.reason = "no -destination-id given"
		return res
	}
	fmt.Fprintln(os.Stderr, "backup: taking a panel backup...")

	start := time.Now()
	status, raw, err := c.do(context.Background(), http.MethodPost, "/api/v1/backups",
		map[string]string{"type": "panel", "destination_id": o.destination})
	if err != nil || status >= 300 {
		res.skipped = true
		res.reason = fmt.Sprintf("could not start: status %d %s", status, firstLine(raw))
		return res
	}
	id := extractString(raw, "id")
	if id == "" {
		res.skipped = true
		res.reason = "no backup id returned"
		return res
	}

	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		st, body, err := c.do(context.Background(), http.MethodGet, "/api/v1/backups/"+id, nil)
		if err == nil && st == http.StatusOK {
			res.status = extractString(body, "status")
			switch res.status {
			case "completed", "verified":
				res.wallTime = time.Since(start)
				res.sizeBytes = extractInt(body, "size_bytes")
				return res
			case "failed":
				res.wallTime = time.Since(start)
				res.reason = extractString(body, "error")
				return res
			}
		}
		time.Sleep(time.Second)
	}
	res.skipped = true
	res.reason = "did not finish within 30 minutes"
	return res
}

// ------------------------------------------------------------------ report

type report struct {
	target            string
	hostSpec          string
	started, finished time.Time
	sites             *sitesResult
	load              *loadResult
	backup            *backupResult
}

func (r *report) render() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# JotHost Panel — capacity measurement\n\n")
	w("> **These numbers describe one host on one day, not a supported limit.**\n")
	w("> A supported limit is a figure measured on the reference hardware it is\n")
	w("> published for. Read on any other machine — a laptop, a container, a\n")
	w("> shared VM — this is a baseline for that machine only, useful for catching\n")
	w("> a regression against an earlier run on the same host and nothing more.\n\n")

	spec := r.hostSpec
	if spec == "" {
		spec = "_(not given — pass -host-spec to record what this ran on)_"
	}
	w("| | |\n|---|---|\n")
	w("| Host | %s |\n", spec)
	w("| Target | `%s` |\n", r.target)
	w("| Run at | %s |\n", r.started.UTC().Format(time.RFC3339))
	w("| Duration | %s |\n\n", r.finished.Sub(r.started).Round(time.Second))

	r.renderSites(&b)
	r.renderLoad(&b)
	r.renderBackup(&b)
	return b.String()
}

func (r *report) renderSites(b *strings.Builder) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("## Website provisioning\n\n")
	if r.sites == nil {
		w("_Skipped._\n\n")
		return
	}
	s := r.sites
	w("Created **%d of %d** websites; %d failed.\n\n", s.active, s.requested, s.failed)
	if len(s.latencies) > 0 {
		p := percentiles(s.latencies)
		perMin := float64(s.active) / s.wallTime.Minutes()
		w("| Metric | Value |\n|---|---|\n")
		w("| Provisioned per minute | %.1f |\n", perMin)
		w("| Time to active, median | %s |\n", p.p50.Round(time.Millisecond))
		w("| Time to active, p95 | %s |\n", p.p95.Round(time.Millisecond))
		w("| Time to active, slowest | %s |\n", p.max.Round(time.Millisecond))
		w("| Total wall time | %s |\n\n", s.wallTime.Round(time.Second))
	}
	if s.firstErr != "" {
		w("First failure: `%s`\n\n", s.firstErr)
	}
}

func (r *report) renderLoad(b *strings.Builder) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("## Concurrent panel users\n\n")
	if r.load == nil {
		w("_Skipped._\n\n")
		return
	}
	l := r.load
	if l.requests == 0 {
		w("No requests completed.\n\n")
		return
	}
	p := percentiles(l.latencies)
	rps := float64(l.requests) / l.duration.Seconds()
	errRate := 100 * float64(l.errors) / float64(l.requests)
	w("**%d** concurrent users over **%s**, reading the pages a panel user opens.\n\n", l.users, l.duration.Round(time.Second))
	w("| Metric | Value |\n|---|---|\n")
	w("| Requests | %d |\n", l.requests)
	w("| Requests per second | %.0f |\n", rps)
	w("| Errors | %d (%.2f%%) |\n", l.errors, errRate)
	w("| Latency, median | %s |\n", p.p50.Round(time.Millisecond))
	w("| Latency, p95 | %s |\n", p.p95.Round(time.Millisecond))
	w("| Latency, p99 | %s |\n", p.p99.Round(time.Millisecond))
	w("| Latency, slowest | %s |\n\n", p.max.Round(time.Millisecond))

	w("Status codes: ")
	var codes []int
	for code := range l.statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		label := fmt.Sprintf("%d", code)
		if code == 0 {
			label = "transport error"
		}
		parts = append(parts, fmt.Sprintf("%s: %d", label, l.statuses[code]))
	}
	w("%s\n\n", strings.Join(parts, ", "))
}

func (r *report) renderBackup(b *strings.Builder) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("## Backup timing\n\n")
	if r.backup == nil {
		w("_Skipped._\n\n")
		return
	}
	bk := r.backup
	if bk.skipped {
		w("_Skipped: %s._\n\n", bk.reason)
		return
	}
	w("| Metric | Value |\n|---|---|\n")
	w("| Type | %s |\n", bk.backupType)
	w("| Status | %s |\n", bk.status)
	w("| Wall time (incl. read-back verify) | %s |\n", bk.wallTime.Round(time.Millisecond))
	if bk.sizeBytes > 0 {
		w("| Archive size | %s |\n", humanBytes(bk.sizeBytes))
	}
	w("\n")
	if bk.reason != "" {
		w("Detail: `%s`\n\n", bk.reason)
	}
}

// ------------------------------------------------------------------ helpers

type dist struct{ p50, p95, p99, max time.Duration }

func percentiles(latencies []time.Duration) dist {
	if len(latencies) == 0 {
		return dist{}
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(q float64) time.Duration {
		idx := int(q * float64(len(sorted)-1))
		return sorted[idx]
	}
	return dist{p50: at(0.50), p95: at(0.95), p99: at(0.99), max: sorted[len(sorted)-1]}
}

func extractString(raw []byte, field string) string {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &m); err != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(m[field], &s)
	return s
}

// extractNestedString reads data.<outer>.<field> from an enveloped response.
func extractNestedString(raw []byte, outer, field string) string {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &m); err != nil {
		return ""
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(m[outer], &inner); err != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(inner[field], &s)
	return s
}

func extractInt(raw []byte, field string) int64 {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &m); err != nil {
		return 0
	}
	var n int64
	_ = json.Unmarshal(m[field], &n)
	return n
}

func firstLine(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
