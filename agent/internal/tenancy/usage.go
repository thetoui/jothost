package tenancy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// maxLogScan bounds how much of an access log one measurement reads.
//
// A busy site's log is gigabytes, and a measurement that read all of it every
// few minutes would be a measurement that costs more than the thing it
// measures. Past this the reading is reported as truncated and the figure is a
// floor — which is said out loud rather than passed off as a total.
const maxLogScan = 256 << 20

// maxLogLine bounds one line. An access log line is a few hundred bytes; a
// megabyte of it is a request somebody sent to see what would happen.
const maxLogLine = 1 << 20

// Usage measures what a set of websites uses.
//
// documentRoots are the paths the panel recorded when it created each site.
// Each is resolved through the same provisioner that created it, so a path
// outside the allowed root is refused by the code that owns that rule rather
// than by a check written again here.
//
// A website that cannot be measured does not fail the whole measurement: one
// site with a directory somebody moved would otherwise mean a subscription
// reports nothing at all, which is worse than reporting the rest and saying so.
func (p *Provider) Usage(ctx context.Context, documentRoots []string) UsageResult {
	result := UsageResult{Sites: make([]SiteUsage, 0, len(documentRoots))}

	var (
		diskTotal      int64
		bandwidthTotal int64
		sawDisk        bool
		sawBandwidth   bool
	)

	for _, root := range documentRoots {
		site := p.measureSite(ctx, root)
		result.Sites = append(result.Sites, site)

		if site.DiskBytes != nil {
			diskTotal += *site.DiskBytes
			sawDisk = true
		}
		if site.BandwidthBytes != nil {
			bandwidthTotal += *site.BandwidthBytes
			sawBandwidth = true
		}
		if site.Error != "" || site.DiskBytes == nil || site.BandwidthBytes == nil {
			result.Partial = true
		}
		if site.LogTruncated {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s: the access log is longer than one measurement reads, "+
					"so its bandwidth figure is a floor", root))
		}
	}

	// nil rather than zero when nothing was measured. The distinction survives
	// all the way to the page: "not measured" and "using nothing" are opposite
	// things to tell a customer about a quota.
	if sawDisk {
		result.DiskBytes = &diskTotal
	}
	if sawBandwidth {
		result.BandwidthBytes = &bandwidthTotal
	}
	return result
}

// measureSite measures one website.
func (p *Provider) measureSite(ctx context.Context, documentRoot string) SiteUsage {
	usage := SiteUsage{DocumentRoot: documentRoot}

	if p.sites == nil {
		usage.Error = ErrInvalidPath.Error()
		return usage
	}
	layout, err := p.sites.LayoutFor(documentRoot)
	if err != nil {
		usage.Error = fmt.Sprintf("%s: %v", ErrInvalidPath, err)
		return usage
	}

	// The whole site directory, not just what is served. A customer's disk
	// usage includes their logs, their uploads and whatever a deployment
	// built; measuring only the document root would report a figure that is
	// always smaller than the truth and never explains why.
	if bytes, err := p.diskUsage(ctx, layout.Root); err != nil {
		usage.Error = err.Error()
	} else {
		usage.DiskBytes = &bytes
	}

	bandwidth, truncated, err := logBytes(layout.AccessLog)
	switch {
	case err != nil && usage.Error == "":
		usage.Error = err.Error()
	case err == nil:
		usage.BandwidthBytes = &bandwidth
		usage.LogTruncated = truncated
	}
	return usage
}

// diskUsage reads how much disk a directory occupies.
//
// `du -sk` rather than a walk in Go, and the reason is hard links: this panel's
// own backups make them, and a walk summing every entry's size would count one
// file once for every name it has and tell a customer they are using several
// times what they have. du also counts blocks rather than apparent size, which
// is what a disk quota is actually about — a sparse file occupies what it
// occupies, not what it claims.
//
// The argument is a path this Agent resolved itself, never one from a request.
func (p *Provider) diskUsage(ctx context.Context, dir string) (int64, error) {
	if p.runner == nil || !p.runner.Available(CommandDu) {
		return 0, ErrNoDiskTool
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("%w: %s", ErrInvalidPath, dir)
	}

	result, err := p.runner.Run(ctx, CommandDu, "-sk", dir)
	if err != nil {
		return 0, fmt.Errorf("measure the disk used by %s: %w", dir, err)
	}
	if !result.Succeeded() {
		// du reports a non-zero exit for an unreadable subdirectory while
		// still printing a total for everything else. That total is a floor
		// and is better than nothing, so it is used when there is one.
		if kb, ok := parseDu(result.Stdout); ok {
			return kb * 1024, nil
		}
		return 0, fmt.Errorf("du could not measure %s: %s", dir,
			strings.TrimSpace(firstLine(result.Stderr, result.Stdout)))
	}

	kb, ok := parseDu(result.Stdout)
	if !ok {
		return 0, fmt.Errorf("du printed something unexpected for %s", dir)
	}
	return kb * 1024, nil
}

// parseDu reads the kilobyte count from du's output.
//
// `du -sk` prints "1234\t/path". Only the first field is read, and only as a
// number: a path with a tab in it would otherwise shift the parse.
func parseDu(out string) (int64, bool) {
	line := firstLine(out)
	if line == "" {
		return 0, false
	}
	field := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(field) == 0 {
		return 0, false
	}
	kb, err := strconv.ParseInt(field[0], 10, 64)
	if err != nil || kb < 0 {
		return 0, false
	}
	return kb, true
}

// logBytes sums what an access log says the site has served.
//
// nginx's combined format puts the response body size in the tenth field:
//
//	1.2.3.4 - - [09/Feb/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 396 "-" "curl"
//
// which is a cumulative figure for the life of the current log file. It resets
// when the log rotates, and the panel turns successive readings into deltas —
// a reading lower than the last means a rotation, and the whole reading is the
// delta. What that loses is whatever was served between the last reading and
// the rotation, which is stated in docs/PHASE22.md rather than papered over.
//
// A missing log is not an error: a site that has had no visitors since it was
// created has no access log yet, and calling that a measurement failure would
// mark every new subscription as unmeasurable.
func logBytes(path string) (int64, bool, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the access log %s: %w", path, err)
	}
	defer file.Close()

	var (
		total     int64
		truncated bool
	)

	limited := &io.LimitedReader{R: file, N: maxLogScan}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLogLine)

	for scanner.Scan() {
		if bytes, ok := bodyBytes(scanner.Text()); ok {
			total += bytes
		}
	}
	if err := scanner.Err(); err != nil {
		// A line longer than the buffer, or an unreadable log. Whatever was
		// counted so far is a floor and is returned as one rather than thrown
		// away, because a partial figure with a flag on it is more useful than
		// no figure at all.
		return total, true, nil
	}
	if limited.N == 0 {
		truncated = true
	}
	return total, truncated, nil
}

// bodyBytes extracts the response size from one combined-format line.
//
// The status and the size are the two fields immediately after the closing
// quote of the request. They are found that way rather than by splitting the
// line on spaces and taking the tenth field, because the request itself
// contains spaces and the user agent contains more — and because the request
// line is the part of an access log a visitor writes, so counting from the
// left is counting from something an attacker chooses.
//
// A line that does not parse is skipped rather than guessed at.
func bodyBytes(line string) (int64, bool) {
	open := strings.IndexByte(line, '"')
	if open < 0 {
		return 0, false
	}
	offset := strings.IndexByte(line[open+1:], '"')
	if offset < 0 {
		return 0, false
	}
	closing := open + 1 + offset

	rest := strings.Fields(line[closing+1:])
	if len(rest) < 2 {
		return 0, false
	}
	// rest[0] is the status, rest[1] is the body size. nginx writes "-" for a
	// response with no body, which is not zero bytes of anything countable.
	if rest[1] == "-" {
		return 0, true
	}
	size, err := strconv.ParseInt(rest[1], 10, 64)
	if err != nil || size < 0 {
		return 0, false
	}
	return size, true
}

// firstLine returns the first non-empty line of the first non-empty input.
func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
