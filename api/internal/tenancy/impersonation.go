package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/shared/validate"
)

// Impersonation is the panel's answer to a support call.
//
// Its whole design is shaped by one problem: the moment somebody uses the
// panel as somebody else, every audit row written afterwards has the wrong
// name on it. So the record is written *first*, the actor's id is carried on
// the session's tokens, and both facts reach the audit trail — whose account
// acted, and who made it act. An impersonation that recorded only one of those
// would be worse than none, because it would look like evidence.
//
// The rules, all of them refusals:
//
//   - Only downwards. An account may be impersonated only by somebody strictly
//     above it in the hierarchy, which is the same check every other operation
//     here makes.
//   - No nesting. An impersonated session cannot impersonate. Otherwise the
//     second hop's record would name the first hop's subject as the actor, and
//     the chain back to a person would be broken exactly where it matters.
//   - Nothing gained. The session carries the subject's permissions minus the
//     tenancy ones and user.manage; see auth/impersonation.go.
//   - It ends. The session is short-lived, and ending it drops the access
//     tokens too, so "stop impersonating" stops it now rather than at the end
//     of a token's life.

// StartImpersonation opens a session as another account.
func (s *Service) StartImpersonation(ctx context.Context, actor Actor, subjectID, reason string,
	rc auth.RequestContext, authService *auth.Service,
) (auth.ImpersonationResult, error) {
	if actor.ImpersonatorUserID != "" {
		return auth.ImpersonationResult{}, ErrAlreadyImpersonating
	}
	if !rbac.Has(actor.Permissions, rbac.PermTenantImpersonate) {
		return auth.ImpersonationResult{}, ErrForbidden
	}
	if err := validate.Reason(reason); err != nil {
		return auth.ImpersonationResult{}, err
	}
	if subjectID == actor.UserID {
		return auth.ImpersonationResult{}, ErrCannotImpersonate
	}

	// Strictly below, and the actor's own account does not count. The
	// descendant query includes the actor, so the equality above is what stops
	// somebody impersonating themselves into a session with fewer refusals on
	// it than the one they already hold.
	if err := s.requireDescendant(ctx, actor, subjectID); err != nil {
		return auth.ImpersonationResult{}, err
	}
	subjectTier, err := s.repo.TierOf(ctx, subjectID)
	if err != nil {
		return auth.ImpersonationResult{}, err
	}
	if !validate.TierMayOwn(actor.Tier, subjectTier) {
		return auth.ImpersonationResult{}, fmt.Errorf(
			"%w: a %s account cannot be impersonated by a %s account",
			ErrCannotImpersonate, subjectTier, actor.Tier)
	}

	subject, err := s.users.GetByID(ctx, subjectID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			return auth.ImpersonationResult{}, ErrNotFound
		}
		return auth.ImpersonationResult{}, err
	}

	result, err := authService.Impersonate(ctx, actor.UserID, subject, rc)
	if err != nil {
		return auth.ImpersonationResult{}, err
	}

	// Recorded in its own table as well as the audit log. The audit row says
	// it happened; this row is what an operator joins against months later to
	// answer "which of these changes were really the customer's".
	if err := s.recordImpersonation(ctx, actor, subject.ID, result.SessionID, reason); err != nil {
		// The session exists and would otherwise be untraceable, so it is torn
		// down rather than left running unrecorded. An impersonation nothing
		// recorded is the one outcome this feature must not have.
		if endErr := authService.EndImpersonation(ctx, result.SessionID); endErr != nil {
			s.log.Error("could not end an unrecorded impersonation",
				"session_id", result.SessionID, "error", endErr.Error())
		}
		return auth.ImpersonationResult{}, err
	}

	s.record(ctx, actor, ActionImpersonateStart, ResourceTypeAccount, subject.ID,
		audit.StatusSuccess, map[string]any{
			"subject": subject.Username, "reason": reason, "session_id": result.SessionID,
		})
	return result, nil
}

// recordImpersonation writes the row that outlives the session.
func (s *Service) recordImpersonation(ctx context.Context, actor Actor,
	subjectID, sessionID, reason string,
) error {
	_, err := s.repo.pool.Exec(ctx, `
		INSERT INTO impersonation_sessions
			(actor_user_id, subject_user_id, session_id, reason, ip_address, user_agent)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, NULLIF($5, '')::inet, $6)`,
		actor.UserID, subjectID, sessionID, reason, actor.IPAddress, actor.UserAgent)
	if err != nil {
		return fmt.Errorf("record the impersonation: %w", err)
	}
	return nil
}

// EndImpersonation closes the session the caller is using.
//
// It takes the session from the caller's own claims rather than from the
// request body. A session id in a body would be an endpoint for ending
// somebody else's session, which is a way to log an operator out of a panel
// they are working in.
func (s *Service) EndImpersonation(ctx context.Context, claims auth.AccessClaims,
	authService *auth.Service, ip, userAgent string,
) error {
	if !claims.Impersonated() {
		return fmt.Errorf("%w: this session is not an impersonation", ErrNotFound)
	}

	if err := authService.EndImpersonation(ctx, claims.SessionID); err != nil {
		return err
	}

	tag, err := s.repo.pool.Exec(ctx, `
		UPDATE impersonation_sessions SET ended_at = now()
		WHERE session_id = $1::uuid AND ended_at IS NULL`, claims.SessionID)
	if err != nil {
		return fmt.Errorf("close the impersonation record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The session was ended and there was nothing open to close. Logged
		// rather than returned: the caller asked for the impersonation to stop
		// and it has, and failing here would leave them holding a dead session
		// while being told the operation failed.
		s.log.Warn("ended an impersonated session with no open record",
			"session_id", claims.SessionID)
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       claims.ImpersonatorUserID,
		Action:       ActionImpersonateEnd,
		ResourceType: ResourceTypeAccount,
		ResourceID:   claims.UserID,
		IPAddress:    ip,
		UserAgent:    userAgent,
		Status:       audit.StatusSuccess,
		Metadata:     map[string]any{"subject": claims.Username, "session_id": claims.SessionID},
	})
	return nil
}

// CurrentImpersonation describes the session a caller is in, if any.
//
// Read from the database rather than from the token so the panel can show who
// the actor is by name. A banner saying "you are signed in as acme_ltd" is
// only useful if it also says who by.
func (s *Service) CurrentImpersonation(ctx context.Context, claims auth.AccessClaims) (*Impersonation, error) {
	if !claims.Impersonated() {
		return nil, nil
	}
	var record Impersonation
	err := s.repo.pool.QueryRow(ctx, `
		SELECT i.id::text, i.actor_user_id::text, a.username,
		       i.subject_user_id::text, u.username, i.reason, i.started_at, i.ended_at
		FROM impersonation_sessions i
		JOIN users a ON a.id = i.actor_user_id
		JOIN users u ON u.id = i.subject_user_id
		WHERE i.session_id = $1::uuid AND i.ended_at IS NULL
		ORDER BY i.started_at DESC
		LIMIT 1`, claims.SessionID).
		Scan(&record.ID, &record.ActorUserID, &record.ActorUsername,
			&record.SubjectUserID, &record.SubjectUsername, &record.Reason,
			&record.StartedAt, &record.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The token says this is an impersonation and no open record backs it.
		// The token is still the truth about what the session may do, so the
		// caller is told they are impersonating without a name attached rather
		// than being told they are not.
		return &Impersonation{
			SubjectUserID:   claims.UserID,
			SubjectUsername: claims.Username,
			ActorUserID:     claims.ImpersonatorUserID,
			StartedAt:       claims.IssuedAt,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the impersonation record: %w", err)
	}
	return &record, nil
}

// ImpersonationHistory lists recent impersonations of accounts an actor may
// see.
func (s *Service) ImpersonationHistory(ctx context.Context, actor Actor, limit int) ([]Impersonation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	query := descendantsCTE + `
		SELECT i.id::text, i.actor_user_id::text, a.username,
		       i.subject_user_id::text, u.username, i.reason, i.started_at, i.ended_at
		FROM impersonation_sessions i
		JOIN users a ON a.id = i.actor_user_id
		JOIN users u ON u.id = i.subject_user_id
		WHERE i.subject_user_id IN (SELECT id FROM descendants)
		ORDER BY i.started_at DESC
		LIMIT $2`
	if seesEverything(actor) {
		query = `
		SELECT i.id::text, i.actor_user_id::text, a.username,
		       i.subject_user_id::text, u.username, i.reason, i.started_at, i.ended_at
		FROM impersonation_sessions i
		JOIN users a ON a.id = i.actor_user_id
		JOIN users u ON u.id = i.subject_user_id
		WHERE $1::uuid IS NOT NULL
		ORDER BY i.started_at DESC
		LIMIT $2`
	}

	rows, err := s.repo.pool.Query(ctx, query, actor.UserID, limit)
	if err != nil {
		return nil, fmt.Errorf("select impersonations: %w", err)
	}
	defer rows.Close()

	records := []Impersonation{}
	for rows.Next() {
		var record Impersonation
		if err := rows.Scan(&record.ID, &record.ActorUserID, &record.ActorUsername,
			&record.SubjectUserID, &record.SubjectUsername, &record.Reason,
			&record.StartedAt, &record.EndedAt); err != nil {
			return nil, fmt.Errorf("scan impersonation: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// CloseStaleImpersonations ends records whose session is gone.
//
// Run at startup. Without it, a panel restarted while somebody was
// impersonating shows an impersonation that is still open for ever — the
// session was dropped with the process, and nothing would ever write the end
// time.
func (s *Service) CloseStaleImpersonations(ctx context.Context) (int64, error) {
	tag, err := s.repo.pool.Exec(ctx, `
		UPDATE impersonation_sessions SET ended_at = now()
		WHERE ended_at IS NULL
		  AND (session_id IS NULL
		       OR NOT EXISTS (
		           SELECT 1 FROM sessions se
		           WHERE se.id = impersonation_sessions.session_id
		             AND se.revoked_at IS NULL
		             AND se.expires_at > now()))`)
	if err != nil {
		return 0, fmt.Errorf("close stale impersonations: %w", err)
	}
	return tag.RowsAffected(), nil
}
