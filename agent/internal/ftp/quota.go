package ftp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Per-account disk limits.
//
// mod_quotatab keeps two tables: a limit table saying what each account may
// use, and a tally table counting what it has. Both are maintained with
// ftpquota, and the panel owns both files.
//
// # Hard limits, not soft ones
//
// ftpquota offers both. A soft limit stores the file that goes over and denies
// the *next* upload; a hard limit refuses the file that goes over.
//
// Hard, here. A soft limit means the account that exceeded its quota is over it
// — the disk is fuller than the number the panel showed — and the failure lands
// on some later, possibly tiny, file with no obvious relationship to the one
// that caused it. A hard limit fails the upload that does not fit, which is
// what the client reports and what the person doing the uploading can act on.

// quotaLimitType is what happens at the limit. See the comment above.
const quotaLimitType = "hard"

// Quota is one account's disk limit and what it has used.
type Quota struct {
	Name string `json:"name"`
	// LimitMB is what the account may store, zero for no limit.
	LimitMB int `json:"limit_mb"`
	// UsedMB is what it has stored, as mod_quotatab counts it.
	UsedMB float64 `json:"used_mb"`
}

// EnsureQuotaTables creates the tables if they are not there.
//
// Creating a table that exists would empty it — every account's limit and every
// account's usage — so it is guarded by the file existing rather than by
// ftpquota's own behaviour.
func (p *Provider) EnsureQuotaTables(ctx context.Context) error {
	if err := p.ensureDirs(); err != nil {
		return err
	}
	for _, table := range []struct {
		kind string
		path string
	}{
		{"limit", p.paths.QuotaLimit()},
		{"tally", p.paths.QuotaTally()},
	} {
		if _, err := os.Stat(table.path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check the FTP quota table: %w", err)
		}
		err := p.runQuota(ctx, "--create-table", "--type="+table.kind,
			"--table-path="+table.path)
		if err != nil {
			return err
		}
	}
	return nil
}

// SetQuota gives an account a disk limit, or removes it when megabytes is zero.
func (p *Provider) SetQuota(ctx context.Context, name string, megabytes int) error {
	if err := validate.FTPUsername(name); err != nil {
		return err
	}
	if err := validate.FTPQuotaMB(megabytes); err != nil {
		return err
	}
	if err := p.EnsureQuotaTables(ctx); err != nil {
		return err
	}

	if megabytes == 0 {
		return p.ClearQuota(ctx, name)
	}

	// add or update: ftpquota has no upsert, and adding a record that exists
	// produces a second one the daemon will read the first of.
	verb := "--add-record"
	if p.hasQuotaRecord(ctx, name) {
		verb = "--update-record"
	}
	// Every limit is passed on every call, deliberately. --update-record
	// resets what it is not given to the default, so a partial update would
	// quietly clear the limits it did not mention.
	return p.runQuota(ctx,
		verb,
		"--type=limit",
		"--table-path="+p.paths.QuotaLimit(),
		"--name="+name,
		"--quota-type=user",
		"--bytes-upload="+strconv.Itoa(megabytes),
		"--units=Mb",
		"--limit-type="+quotaLimitType,
	)
}

// ClearQuota removes an account's limit.
//
// Removing one that is not there succeeds: the caller wanted the account
// unlimited, and it is.
func (p *Provider) ClearQuota(ctx context.Context, name string) error {
	if err := validate.FTPUsername(name); err != nil {
		return err
	}
	if !p.hasQuotaRecord(ctx, name) {
		return nil
	}
	return p.runQuota(ctx, "--delete-record", "--type=limit",
		"--table-path="+p.paths.QuotaLimit(),
		"--name="+name, "--quota-type=user")
}

// ClearTally forgets what an account has used.
//
// Needed when an account's files are removed outside FTP — through the file
// manager, or by a deploy — because mod_quotatab counts what it saw uploaded,
// not what is on the disk. Without this an account whose files were deleted by
// hand stays at its limit and cannot upload anything.
func (p *Provider) ClearTally(ctx context.Context, name string) error {
	if err := validate.FTPUsername(name); err != nil {
		return err
	}
	err := p.runQuota(ctx, "--delete-record", "--type=tally",
		"--table-path="+p.paths.QuotaTally(),
		"--name="+name, "--quota-type=user")
	if err != nil && strings.Contains(err.Error(), "no such") {
		// Nothing recorded yet is the state this asks for.
		return nil
	}
	return err
}

// Quotas reads every limit, with what each account has used.
func (p *Provider) Quotas(ctx context.Context) (map[string]Quota, error) {
	quotas := map[string]Quota{}
	limits, err := p.showRecords(ctx, "limit", p.paths.QuotaLimit())
	if err != nil {
		return quotas, err
	}
	for name, value := range limits {
		quotas[name] = Quota{Name: name, LimitMB: int(value)}
	}

	// A tally for an account with no limit is not interesting: it is a count
	// nothing is measured against.
	tallies, err := p.showRecords(ctx, "tally", p.paths.QuotaTally())
	if err != nil {
		return quotas, err
	}
	for name, value := range tallies {
		if quota, ok := quotas[name]; ok {
			quota.UsedMB = value
			quotas[name] = quota
		}
	}
	return quotas, nil
}

// hasQuotaRecord reports whether an account already has a limit.
func (p *Provider) hasQuotaRecord(ctx context.Context, name string) bool {
	records, err := p.showRecords(ctx, "limit", p.paths.QuotaLimit())
	if err != nil {
		return false
	}
	_, found := records[name]
	return found
}

// showRecords parses `ftpquota --show-records`, which prints stanzas:
//
//	Name: demo
//	Quota Type: User
//	Per Session: False
//	Limit Type: Hard
//	  Uploaded Bytes: 10.00 Mb
//
// Only the name and the uploaded-bytes limit are read. The other fields are
// this package's own constants, and reading them back to check would be
// checking that ftpquota stored what ftpquota was told.
func (p *Provider) showRecords(ctx context.Context, kind, path string) (map[string]float64, error) {
	values := map[string]float64{}
	if _, err := os.Stat(path); err != nil {
		// No table is no records, which is what a host with no quotas has.
		return values, nil
	}

	result, err := p.runner.Run(ctx, CommandFtpquota,
		"--show-records", "--type="+kind, "--table-path="+path)
	if err != nil {
		return values, wrap("read the FTP quota table", err)
	}
	if !result.Succeeded() {
		return values, fmt.Errorf("the FTP quota table could not be read: %s",
			firstLine(result.Stderr, result.Stdout))
	}

	name := ""
	for _, line := range strings.Split(result.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch {
		case strings.EqualFold(key, "Name"):
			// A name that is not one means this parser misread a line, and the
			// name would go on to a page and into an argument.
			if validate.FTPUsername(value) != nil {
				name = ""
				continue
			}
			name = value
			if _, exists := values[name]; !exists {
				values[name] = 0
			}
		case name != "" && strings.EqualFold(key, "Uploaded Bytes"):
			values[name] = megabytes(value)
		}
	}
	return values, nil
}

// megabytes reads "10.00 Mb", "1.50 Gb" or a bare byte count.
func megabytes(value string) float64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	amount, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	unit := ""
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	}
	switch unit {
	case "gb", "giga":
		return amount * 1024
	case "mb", "mega":
		return amount
	case "kb", "kilo":
		return amount / 1024
	default:
		return amount / (1024 * 1024)
	}
}

// runQuota runs one ftpquota verb.
func (p *Provider) runQuota(ctx context.Context, args ...string) error {
	result, err := p.runner.Run(ctx, CommandFtpquota, args...)
	if err != nil {
		return wrap("write the FTP quota table", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("the FTP quota table could not be written: %s",
			firstLine(result.Stderr, result.Stdout))
	}
	return nil
}
