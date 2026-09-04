package mail

import (
	"context"
	"fmt"

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
func (s *Service) InstallWebmail(ctx context.Context, actor Actor, requestID,
	websiteID string,
) (map[string]any, error) {
	if websiteID == "" {
		return nil, ErrNoWebsite
	}
	site, err := s.websites.LookupForMail(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	if site.DocumentRoot == "" || site.SystemUser == "" {
		return nil, fmt.Errorf(
			"%w: %s has no document root or system account yet", ErrNoWebsite, site.Domain)
	}

	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return nil, err
	}
	if settings.Hostname == "" {
		return nil, ErrNoHostname
	}

	response, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationWebmailInstall,
		RequestID: requestID,
		// Asynchronous: the download is several megabytes and the unpack is
		// thousands of files, which is well past what a request should hold
		// open.
		Mode: protocol.ModeAsync,
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
	})
	if err != nil {
		return nil, err
	}

	if err := s.repo.SaveWebmail(ctx, s.serverID, websiteID, webmailVersion); err != nil {
		return nil, err
	}
	s.record(ctx, actor, ActionWebmailInstall, ResourceTypeWebmail, websiteID, map[string]any{
		"domain": site.Domain,
	})
	return response.Data, nil
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
