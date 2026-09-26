package mail

import (
	"context"
	"fmt"

	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

// InstallWebmail unpacks webmail into a website the panel already created.
//
// A website rather than a directory, and that is the whole boundary. Webmail is
// PHP served over HTTP: it needs a vhost, a PHP pool, a document root owned by
// an unprivileged account, and — because it is about to be handed every mailbox
// password on this host — a certificate. All of that is what a website *is* in
// this panel, and Phases 4, 5 and 6 already build it correctly.
//
// So this does not build one. It asks for a site that exists, which means the
// operator has already decided which name webmail is served on and has issued a
// certificate for it, and the panel does not have to invent a second, weaker
// path to the same thing.
//
// A queued job, and webmail is recorded as installed only when it succeeds. It
// used to be sent straight to the Agent as asynchronous work and recorded the
// moment the Agent accepted it — before a byte was downloaded. A download that
// failed, or a checksum that did not match, left the panel reporting webmail
// installed on a site serving nothing, and the failure went nowhere anyone
// could see it. Now it is a job whose progress and outcome the panel shows, and
// JobFinished records the result.
func (s *Service) InstallWebmail(ctx context.Context, actor Actor, websiteID string) (jobs.Job, error) {
	if websiteID == "" {
		return jobs.Job{}, ErrNoWebsite
	}
	site, err := s.websites.LookupForMail(ctx, websiteID)
	if err != nil {
		return jobs.Job{}, err
	}
	if site.DocumentRoot == "" || site.SystemUser == "" {
		return jobs.Job{}, fmt.Errorf(
			"%w: %s has no document root or system account yet", ErrNoWebsite, site.Domain)
	}

	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return jobs.Job{}, err
	}
	if settings.Hostname == "" {
		return jobs.Job{}, ErrNoHostname
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type: jobs.TypeWebmailInstall,
		Payload: map[string]any{
			"document_root": site.DocumentRoot,
			"owner":         site.SystemUser,
			"domain":        site.Domain,
			// Always this host. Webmail exists to serve the mailboxes here,
			// and a configurable IMAP host would make the login page a
			// credential collector pointed wherever somebody typed.
			"imap_host": settings.Hostname,
			"smtp_host": settings.Hostname,
		},
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeWebmail,
		ResourceID:   websiteID,
	})
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionWebmailInstall, ResourceTypeWebmail, websiteID, map[string]any{
		"domain": site.Domain, "job_id": job.ID,
	})
	return job, nil
}

// JobFinished records webmail as installed once its job has succeeded.
//
// It satisfies jobs.Observer and ignores every other job. A failed install
// changes nothing: whatever webmail was recorded before — on this site or
// another — is still what the panel reports, because it is still what is there.
func (s *Service) JobFinished(ctx context.Context, job jobs.Job, state jobs.State,
	_ map[string]any, failure string,
) {
	if job.Type != jobs.TypeWebmailInstall || job.ResourceID == nil {
		return
	}
	websiteID := *job.ResourceID
	if state != jobs.StateSuccess {
		s.log.Warn("webmail was not installed", "website_id", websiteID, "reason", failure)
		return
	}
	if err := s.repo.SaveWebmail(ctx, s.serverID, websiteID, webmailVersion); err != nil {
		s.log.Error("webmail was installed but could not be recorded",
			"website_id", websiteID, logger.KeyError, err.Error())
	}
}

// RemoveWebmail deletes the application from its document root.
func (s *Service) RemoveWebmail(ctx context.Context, actor Actor, requestID string) error {
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return err
	}
	if settings.WebmailWebsiteID == "" {
		return nil
	}
	site, err := s.websites.LookupForMail(ctx, settings.WebmailWebsiteID)
	if err != nil {
		return err
	}

	if _, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationWebmailRemove,
		RequestID: requestID,
		Payload:   map[string]any{"document_root": site.DocumentRoot},
	}); err != nil {
		return err
	}
	if err := s.repo.SaveWebmail(ctx, s.serverID, "", ""); err != nil {
		return err
	}
	s.record(ctx, actor, ActionWebmailRemove, ResourceTypeWebmail,
		settings.WebmailWebsiteID, map[string]any{"domain": site.Domain})
	return nil
}

// webmailVersion is the release the Agent installs.
//
// Recorded here as well as compiled into the Agent so the panel can show what
// is running. The Agent's copy is the authority — it is what actually gets
// downloaded and checksum-verified — and this is a label; the integration suite
// asserts the two agree, because a version the page reports and the host does
// not have is worse than no version at all.
const webmailVersion = "1.6.9"
