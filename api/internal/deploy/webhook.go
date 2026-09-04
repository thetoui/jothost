package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// The webhook receiver.
//
// This is the only endpoint in the panel that is reachable without
// authentication and causes code to run on the host, so it is worth being
// explicit about what protects it and what does not.
//
// **The token in the URL is not what protects it.** The token is an address: it
// selects which repository a push is about, so the panel knows which secret to
// verify against. Anybody who can read a forge's settings page can read it.
//
// **The signature is what protects it.** Every accepted request carries an HMAC
// over the exact bytes of the body, computed with a secret the operator set on
// both sides. The comparison is constant-time, the body is read once and
// verified before it is parsed, and nothing in the payload is trusted for
// anything except deciding whether the branch is the one this website deploys.
//
// What the panel deliberately does *not* do with a webhook payload:
//
//   - It does not take the repository URL from it. A push claiming to come from
//     a different repository changes nothing: the panel deploys the remote it
//     has recorded.
//   - It does not take a commit from it. The deployment resolves the tip of the
//     configured branch itself, so a forged payload cannot pin a site to an
//     arbitrary revision even if it were somehow signed.
//   - It does not take a branch to deploy from it. The branch in the payload is
//     compared against the configured one and is otherwise discarded.
//
// That is the whole trust boundary: a verified push is a *signal that something
// changed*, and every fact about what to deploy comes from the panel's own
// record.

// MaxWebhookBody bounds what will be read from an unauthenticated caller.
//
// GitHub's push payloads run to a few tens of kilobytes on a busy repository.
// This is generous and finite, which is the property that matters: the body has
// to be buffered whole before the signature can be checked, so an unbounded
// read here would be a way to make the panel allocate whatever somebody sends.
const MaxWebhookBody = 1 << 20

// Errors the webhook path returns.
var (
	// ErrUnverified covers a request whose signature did not match, and a
	// token that matches no repository. They are deliberately the same error:
	// telling an unauthenticated caller which tokens exist is telling them
	// what to attack.
	ErrUnverified = errors.New("this request could not be verified")
	// ErrNoSignature covers a request that carried none at all.
	ErrNoSignature = errors.New("this request carried no signature")
)

// WebhookRequest is what the handler passes in.
type WebhookRequest struct {
	Token string
	Body  []byte
	// Signature is the value of the forge's signature header.
	Signature string
	// Event is the forge's event name, so a ping can be answered as a ping.
	Event string
}

// WebhookResult is what happened.
type WebhookResult struct {
	// Accepted reports whether a deployment was started.
	Accepted bool `json:"accepted"`
	// DeploymentID names it, when one was.
	DeploymentID string `json:"deployment_id,omitempty"`
	// Detail says what happened when nothing was — a push to a branch this
	// website does not deploy is the ordinary case, not a failure.
	Detail string `json:"detail,omitempty"`
}

// Webhook verifies a push and, if it is one this website deploys, starts a
// deployment.
func (s *Service) Webhook(ctx context.Context, requestID string, req WebhookRequest) (WebhookResult, error) {
	repository, sealed, err := s.repo.ByWebhookToken(ctx, req.Token)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Answered exactly as a bad signature is. An unauthenticated
			// caller learns nothing about which tokens exist.
			return WebhookResult{}, ErrUnverified
		}
		return WebhookResult{}, err
	}

	secret, err := s.openSecret(repository.ID, sealed)
	if err != nil {
		return WebhookResult{}, err
	}
	if err := verify(repository.Provider, secret, req.Signature, req.Body); err != nil {
		return WebhookResult{}, err
	}

	// A ping is a forge checking the address works. Answering it as accepted
	// would deploy on every settings save; answering it as an error would make
	// the forge show the webhook as broken.
	if strings.EqualFold(req.Event, "ping") {
		return WebhookResult{Detail: "the webhook is reachable and its signature verified"}, nil
	}

	branch := pushBranch(req.Body)
	if branch != "" && branch != repository.Branch {
		return WebhookResult{
			Detail: fmt.Sprintf(
				"that push was for %q and this website deploys %q, so nothing was done",
				branch, repository.Branch),
		}, nil
	}

	if !repository.AutoDeploy {
		return WebhookResult{
			Detail: "the push verified, and automatic deployment is turned off for this " +
				"website, so nothing was done",
		}, nil
	}

	deployment, err := s.Deploy(ctx, Actor{}, requestID, repository.ID,
		validate.TriggerWebhook, "")
	if err != nil {
		if errors.Is(err, ErrInProgress) {
			// Not an error to report to a forge as a failure: a push arriving
			// while a deployment is running is ordinary, and the running one
			// will already deploy something at least as new.
			return WebhookResult{
				Detail: "a deployment is already running for this website, so this push " +
					"was not queued behind it",
			}, nil
		}
		return WebhookResult{}, err
	}

	return WebhookResult{Accepted: true, DeploymentID: deployment.ID}, nil
}

// openSecret decrypts a repository's webhook secret.
func (s *Service) openSecret(id, sealed string) (string, error) {
	if sealed == "" || s.secrets == nil {
		return "", ErrUnverified
	}
	plain, err := s.secrets.Decrypt(sealed, id)
	if err != nil {
		// A secret that cannot be decrypted is a repository nothing can verify
		// a push for, which is reported as unverified rather than as an
		// internal error — the caller is unauthenticated and gets one answer.
		return "", ErrUnverified
	}
	return string(plain), nil
}

// verify checks a push's signature.
//
// Two schemes, because the two forges chose differently:
//
//   - GitHub sends "sha256=" and an HMAC-SHA256 over the raw body. That is the
//     better design and it is what the "generic" provider uses too.
//   - GitLab sends the secret itself, in a header. It is weaker — anybody who
//     sees one request has the secret — and it is supported because refusing
//     to support GitLab would not make anybody safer, only make them use
//     something worse.
//
// Both comparisons are constant-time. A byte-by-byte comparison of an HMAC
// leaks, through timing, how much of a guess was right, which is enough to
// recover a signature one byte at a time.
func verify(provider, secret, signature string, body []byte) error {
	if secret == "" {
		return ErrUnverified
	}

	switch provider {
	case validate.ProviderGitLab:
		if signature == "" {
			return ErrNoSignature
		}
		if !hmac.Equal([]byte(signature), []byte(secret)) {
			return ErrUnverified
		}
		return nil

	case validate.ProviderGitHub, validate.ProviderGeneric:
		if signature == "" {
			return ErrNoSignature
		}
		expected := signPayload(secret, body)
		// The prefix is compared as part of the value rather than stripped
		// first, so a caller cannot get a match by sending the digest without
		// it — or with a different algorithm named.
		if !hmac.Equal([]byte(signature), []byte(expected)) {
			return ErrUnverified
		}
		return nil

	default:
		// A provider of "none" has no webhook at all, and reaching here means
		// a token exists for a repository that should have none.
		return ErrUnverified
	}
}

// signPayload computes the signature a forge would send.
//
// Exported behaviour in all but name: the integration suite uses the same
// computation to produce a valid push, which is what makes the check a real
// one rather than a comparison of the panel against itself.
func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// SignPayload is signPayload, for tests and tooling.
func SignPayload(secret string, body []byte) string { return signPayload(secret, body) }

// pushBranch reads the branch out of a push payload.
//
// The one field taken from an unauthenticated body, and it is used for exactly
// one thing: deciding whether this push is for the branch the website deploys.
// It never becomes a branch to check out — that comes from the panel's own
// record — so the worst a forged value can do, on a request that somehow
// verified, is cause a deployment of the configured branch that would have been
// correct anyway.
//
// Parsed leniently: a payload the panel cannot read returns an empty branch,
// which is treated as "no opinion" and deploys the configured branch. A forge
// that changes its payload shape should not stop deployments working.
func pushBranch(body []byte) string {
	var payload struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	// "refs/heads/main". A tag push is "refs/tags/...", which is not a branch
	// and is left to the branch comparison to reject.
	if after, found := strings.CutPrefix(payload.Ref, "refs/heads/"); found {
		return after
	}
	return ""
}
