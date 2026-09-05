package services

import (
	"context"
	"fmt"
)

// BootFinding is one service and whether it would survive a reboot.
type BootFinding struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Unit is the init unit that answered, empty where none did.
	Unit string `json:"unit,omitempty"`
	// Running is what the host says now.
	Running bool `json:"running"`
	// Enabled reports whether it starts at boot. Nil is unknown, which is a
	// different answer from "no" and is reported as such.
	Enabled *bool `json:"enabled"`
	// Essential means the websites on this host stop working while it is down.
	Essential bool `json:"essential"`
	// AtRisk is the conclusion: running now, and not coming back.
	AtRisk bool `json:"at_risk"`
	// Reason explains an AtRisk that the panel cannot fix by itself.
	Reason string `json:"reason,omitempty"`
}

// BootReport is what a sweep found and what it did about it.
type BootReport struct {
	Findings []BootFinding `json:"findings"`
	// Enabled lists the services this sweep made persistent.
	Enabled []string `json:"enabled"`
	// Failed lists the ones it could not, with the reason.
	Failed map[string]string `json:"failed,omitempty"`
	// Unmanaged counts services running with no init unit to enable. On a host
	// with no init system that is all of them, and it is not a fault of any
	// one service.
	Unmanaged int `json:"unmanaged"`
}

// BootAudit reports which running services would not come back after a reboot.
//
// The rule is deliberately "running now", not "installed": Apache is *meant*
// to be stopped on a host that serves everything from nginx, and PHP-FPM 8.2
// is meant to be stopped on a host that only runs 8.4. Enabling everything
// installed would start daemons on boot that the operator had deliberately
// turned off. Whatever is running now should be running after a restart —
// that is the whole promise, and it is the one an operator actually made.
func (p *Provider) BootAudit(ctx context.Context, extra []Definition, processes ProcessFinder) BootReport {
	report := BootReport{Findings: []BootFinding{}}

	for _, service := range p.Detect(ctx, extra, processes) {
		if !service.Running {
			continue
		}

		finding := BootFinding{
			Key:       service.Key,
			Label:     service.Label,
			Unit:      service.Unit,
			Running:   true,
			Enabled:   service.Enabled,
			Essential: service.Essential,
		}

		switch {
		case service.Unit == "":
			// Running, but nothing supervises it. Nothing can be enabled, and
			// saying "at risk" of each one individually would report the same
			// fact a dozen times: the host has no init system.
			finding.AtRisk = true
			finding.Reason = "no init unit: this host has nothing that starts services at boot"
			report.Unmanaged++
		case service.Enabled == nil:
			// Unknown is not "no". Reported, not acted on.
			finding.Reason = "the init system did not say whether this starts at boot"
		case !*service.Enabled:
			finding.AtRisk = true
			finding.Reason = "running now, but not set to start at boot"
		}

		report.Findings = append(report.Findings, finding)
	}

	return report
}

// EnsureBootPersistence enables every running service that would not come back.
//
// This is the fix for a defect that outlived several phases: only Node.js
// applications ever called Enable. Everything the panel installs on demand —
// Apache for the hybrid arrangement, BIND, the mail server, fail2ban, the FTP
// server, each PHP-FPM version — was started and never made persistent, so a
// reboot left the host serving nothing and the panel reporting it as healthy
// until somebody looked.
//
// It is safe to run repeatedly and safe to run at startup: enabling a unit
// that is already enabled is a no-op on both init systems.
//
// A service it cannot enable is recorded rather than returned as an error. One
// unit refusing must not stop the other eleven being made persistent, and the
// caller gets the whole picture instead of the first problem.
func (p *Provider) EnsureBootPersistence(ctx context.Context, extra []Definition, processes ProcessFinder) BootReport {
	report := p.BootAudit(ctx, extra, processes)
	report.Enabled = []string{}

	if !p.Available() {
		// Nothing to enable anything with. The findings still stand and still
		// say so; pretending otherwise would report success for work that was
		// never possible.
		return report
	}

	for i, finding := range report.Findings {
		if !finding.AtRisk || finding.Unit == "" {
			continue
		}

		if err := p.Enable(ctx, finding.Unit); err != nil {
			if report.Failed == nil {
				report.Failed = map[string]string{}
			}
			report.Failed[finding.Key] = err.Error()
			report.Findings[i].Reason = fmt.Sprintf("could not be enabled: %v", err)
			continue
		}

		enabled := true
		report.Findings[i].Enabled = &enabled
		report.Findings[i].AtRisk = false
		report.Findings[i].Reason = ""
		report.Enabled = append(report.Enabled, finding.Key)
	}

	return report
}

// AtRisk returns the findings that would not survive a reboot.
func (r BootReport) AtRisk() []BootFinding {
	out := make([]BootFinding, 0, len(r.Findings))
	for _, finding := range r.Findings {
		if finding.AtRisk {
			out = append(out, finding)
		}
	}
	return out
}
