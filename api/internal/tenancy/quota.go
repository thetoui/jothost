package tenancy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// EffectiveLimits folds a subscription's add-ons into its plan.
//
// The arithmetic has one rule worth stating: **unlimited absorbs everything.**
// If the plan's mailbox limit is nil, no add-on makes it more unlimited, and
// the answer stays nil. An add-on's nil means it contributes nothing to that
// dimension — there is no meaning for an increment of infinity — which is why
// the two nils are read differently depending on which side they are on.
func EffectiveLimits(plan Limits, addons []Addon) Limits {
	out := plan
	for _, addon := range addons {
		out.DiskMB = addLimit(out.DiskMB, addon.Limits.DiskMB, addon.Quantity)
		out.BandwidthMB = addLimit(out.BandwidthMB, addon.Limits.BandwidthMB, addon.Quantity)
		out.MaxWebsites = addLimit(out.MaxWebsites, addon.Limits.MaxWebsites, addon.Quantity)
		out.MaxDatabases = addLimit(out.MaxDatabases, addon.Limits.MaxDatabases, addon.Quantity)
		out.MaxMailboxes = addLimit(out.MaxMailboxes, addon.Limits.MaxMailboxes, addon.Quantity)
		out.MaxFTPUsers = addLimit(out.MaxFTPUsers, addon.Limits.MaxFTPUsers, addon.Quantity)
		out.MaxCronJobs = addLimit(out.MaxCronJobs, addon.Limits.MaxCronJobs, addon.Quantity)
		out.MaxSubdomains = addLimit(out.MaxSubdomains, addon.Limits.MaxSubdomains, addon.Quantity)
	}
	return out
}

// addLimit adds an add-on's increment to a plan's limit.
func addLimit(base, increment *int, quantity int) *int {
	// Unlimited stays unlimited. Nothing added to "no limit" is a smaller
	// number, and a scheme that treated nil as zero here would turn an
	// unlimited plan into a limited one the moment an add-on was attached —
	// which is the opposite of what buying an add-on means.
	if base == nil || increment == nil || quantity <= 0 {
		return base
	}
	total := *base + (*increment * quantity)
	if total > validate.MaxLimitValue {
		total = validate.MaxLimitValue
	}
	return &total
}

// Used reports the count for one dimension.
func (u Usage) Used(dimension string) int {
	switch dimension {
	case validate.DimensionWebsites:
		return u.Websites
	case validate.DimensionDatabases:
		return u.Databases
	case validate.DimensionMailboxes:
		return u.Mailboxes
	case validate.DimensionFTPUsers:
		return u.FTPUsers
	case validate.DimensionCronJobs:
		return u.CronJobs
	case validate.DimensionSubdomains:
		return u.Subdomains
	default:
		return 0
	}
}

// Limit returns the limit for one dimension, or nil for unlimited.
func (l Limits) Limit(dimension string) *int {
	switch dimension {
	case validate.DimensionWebsites:
		return l.MaxWebsites
	case validate.DimensionDatabases:
		return l.MaxDatabases
	case validate.DimensionMailboxes:
		return l.MaxMailboxes
	case validate.DimensionFTPUsers:
		return l.MaxFTPUsers
	case validate.DimensionCronJobs:
		return l.MaxCronJobs
	case validate.DimensionSubdomains:
		return l.MaxSubdomains
	case validate.DimensionDisk:
		return l.DiskMB
	case validate.DimensionBandwidth:
		return l.BandwidthMB
	default:
		return nil
	}
}

// Decision is the answer to "may this subscription have one more".
type Decision struct {
	Allowed   bool   `json:"allowed"`
	Dimension string `json:"dimension"`
	Used      int    `json:"used"`
	// Limit is nil for unlimited.
	Limit *int `json:"limit"`
	// OverSoftLimit says the limit was passed and the plan allows it anyway.
	// It is a separate field from Allowed because "yes, and you are over your
	// quota" is a different answer from "yes", and a caller that could not tell
	// them apart could not warn anybody.
	OverSoftLimit bool   `json:"over_soft_limit"`
	Reason        string `json:"reason"`
}

// CheckQuota decides whether a subscription may take one more of a dimension.
//
// Only counted dimensions can be answered here, and that is not a gap: disk and
// bandwidth are measured on the host after the fact, so there is no moment at
// which the panel could refuse "one more megabyte". A caller asking about one
// of those gets a refusal to answer rather than a fabricated yes.
func (s *Service) CheckQuota(ctx context.Context, subscriptionID, dimension string) (Decision, error) {
	if err := validate.QuotaDimension(dimension); err != nil {
		return Decision{}, err
	}
	switch dimension {
	case validate.DimensionDisk, validate.DimensionBandwidth:
		return Decision{}, fmt.Errorf(
			"%s is measured on the host rather than counted, so it cannot be checked "+
				"before the fact", dimension)
	}

	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return Decision{}, err
	}
	plan, err := s.repo.GetPlan(ctx, subscription.PlanID)
	if err != nil {
		return Decision{}, err
	}
	addons, err := s.repo.ListAddons(ctx, subscriptionID)
	if err != nil {
		return Decision{}, err
	}
	usage, err := s.repo.CountUsage(ctx, subscriptionID)
	if err != nil {
		return Decision{}, err
	}

	limits := EffectiveLimits(plan.Limits, addons)
	decision := Decision{
		Dimension: dimension,
		Used:      usage.Used(dimension),
		Limit:     limits.Limit(dimension),
	}

	// A suspended subscription may not grow, whatever its limits say. Checked
	// before the limits rather than after, because "you are suspended" is the
	// answer somebody needs and "you have room for two more websites" would be
	// true and useless.
	if subscription.Status == StatusSuspended {
		decision.Reason = "this subscription is suspended: " + subscription.SuspendedReason
		return decision, nil
	}

	if decision.Limit == nil {
		decision.Allowed = true
		return decision, nil
	}
	if decision.Used < *decision.Limit {
		decision.Allowed = true
		return decision, nil
	}

	if plan.Enforcement == validate.EnforcementSoft {
		decision.Allowed = true
		decision.OverSoftLimit = true
		decision.Reason = fmt.Sprintf(
			"this subscription is over its %s limit (%d of %d) and its plan allows it",
			dimension, decision.Used, *decision.Limit)
		return decision, nil
	}

	decision.Reason = fmt.Sprintf(
		"this subscription's plan allows %d %s and %d are in use",
		*decision.Limit, strings.ReplaceAll(dimension, "_", " "), decision.Used)
	return decision, nil
}

// ------------------------------------------------------------- middleware

// Guard refuses a request that would take a subscription past a hard limit.
//
// It is one middleware around the whole router rather than a check inside each
// feature's service, and the reason is uniformity: a quota enforced in six
// services is a quota with six chances to be forgotten by the seventh, and the
// seventh is always the feature added after the person who wrote the rule has
// moved on. Here the guarded routes are one table in server.go.
//
// The routes are matched with an http.ServeMux of their own rather than by
// comparing strings, so the pattern "POST /api/v1/mail/domains/{id}/mailboxes"
// is matched by exactly the code that will route it. A guard cannot come to
// disagree with the router about which requests it covers.
//
// **Whose quota is charged is the decision that matters here**, and it is not
// the caller's. A reseller creating a database inside a customer's website is
// spending the customer's plan, not their own, and a guard that looked only at
// who was asking would let every limit be walked around by having somebody
// senior press the button. So each rule says how to find the *owning*
// subscription — from the website in the path, the website in the body, the
// mail domain being added to — and falls back to the caller's own subscription
// only when the request names no resource at all.
//
// What it does not do is decide who owns what. It resolves a subscription and
// asks CheckQuota; the route's own handler still decides whether the caller may
// create the thing at all. A guard that also did authorization would be a
// second permission system, quietly disagreeing with the first.
type Guard struct {
	service *Service
	auth    *auth.Service
	matcher *http.ServeMux
	rules   map[string]Rule
}

// Subject names where a rule finds the subscription to charge.
type Subject string

// The ways a request says which subscription it is spending.
const (
	// SubjectCaller charges whoever is asking. Used only where the request
	// names no resource at all.
	SubjectCaller Subject = "caller"
	// SubjectBodySubscription reads an explicit subscription_id from the body,
	// falling back to the caller. This is how somebody senior creates a
	// website *inside* a customer's subscription and is charged for it.
	SubjectBodySubscription Subject = "body.subscription_id"
	// SubjectPathWebsite reads a website id from the {id} path value.
	SubjectPathWebsite Subject = "path.website"
	// SubjectBodyWebsite reads a website id from the body.
	SubjectBodyWebsite Subject = "body.website_id"
	// SubjectPathMailDomain reads a mail domain id from {id} and follows it to
	// the website it belongs to.
	SubjectPathMailDomain Subject = "path.mail_domain"
)

// Rule is one guarded route.
type Rule struct {
	Dimension string
	Subject   Subject
}

// NewGuard builds a Guard over a table of route patterns.
func NewGuard(service *Service, authService *auth.Service, rules map[string]Rule) (*Guard, error) {
	matcher := http.NewServeMux()
	known := make(map[string]Rule, len(rules))
	for pattern, rule := range rules {
		if err := validate.QuotaDimension(rule.Dimension); err != nil {
			return nil, fmt.Errorf("the quota guard was given %q for %q: %w",
				rule.Dimension, pattern, err)
		}
		switch rule.Dimension {
		case validate.DimensionDisk, validate.DimensionBandwidth:
			// Refused at wiring time rather than at request time. Disk and
			// bandwidth are measured on the host after the fact, so there is no
			// request this guard could refuse in order to keep one inside its
			// limit — and a route claiming otherwise would be a promise nothing
			// keeps.
			return nil, fmt.Errorf(
				"the quota guard cannot enforce %s: it is measured on the host, "+
					"not counted here", rule.Dimension)
		}
		switch rule.Subject {
		case SubjectCaller, SubjectBodySubscription, SubjectPathWebsite,
			SubjectBodyWebsite, SubjectPathMailDomain:
		default:
			return nil, fmt.Errorf("the quota guard does not know how to find the "+
				"subscription for %q: %q", pattern, rule.Subject)
		}
		// The handler does nothing but hand the routed request back, so the
		// guard can read the path values the real router would produce.
		matched := rule
		matcher.Handle(pattern, http.HandlerFunc(func(_ http.ResponseWriter, routed *http.Request) {
			if match, ok := routed.Context().Value(guardMatchKey{}).(*guardMatch); ok {
				match.rule = matched
				match.request = routed
			}
		}))
		known[pattern] = rule
	}
	return &Guard{service: service, auth: authService, matcher: matcher, rules: known}, nil
}

// guardMatch is where the matcher puts what it found.
//
// A pointer through the request context rather than a field on the Guard,
// because a Guard serves every request the panel receives at once and a field
// would be two requests writing to one place.
type guardMatch struct {
	rule    Rule
	request *http.Request
}

type guardMatchKey struct{}

// discard is the ResponseWriter the matcher writes its 404 into.
//
// The matcher is only ever asked *which* pattern a request matches; nothing it
// writes should reach anybody. A nil writer would panic in the default
// not-found handler, so it gets one that throws the bytes away.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// resolve returns the rule a request is subject to and the request as the
// router will see it.
//
// The second half is the part that is easy to get wrong, and was: a pattern
// like "POST /api/v1/mail/domains/{id}/mailboxes" only has an {id} once
// something has *routed* the request. Asking the matcher which handler it would
// pick — mux.Handler(r) — answers the pattern question and leaves PathValue
// empty, so a rule that reads {id} silently reads "". That is not a visible
// failure: the lookup returns nothing, the guard charges the caller instead,
// and the limit it was meant to enforce quietly stops binding.
//
// So the request is served through the matcher, whose handlers do nothing but
// hand back the request they were given — with its path values filled in by
// the same routing code the real mux uses.
func (g *Guard) resolve(r *http.Request) (Rule, *http.Request, bool) {
	match := &guardMatch{}
	probe := r.WithContext(context.WithValue(r.Context(), guardMatchKey{}, match))
	g.matcher.ServeHTTP(discard{}, probe)
	if match.request == nil {
		return Rule{}, nil, false
	}
	return match.rule, match.request, true
}

// maxGuardBody bounds how much of a request body the guard reads.
//
// It only looks for one identifier near the front of a JSON object, but it has
// to buffer whatever it reads so the handler still receives the whole body. A
// body larger than this is passed through unread, and the request then charges
// the caller's own subscription — the safe direction to fail, because the
// request is still checked against somebody's quota rather than nobody's.
const maxGuardBody = 64 << 10

// Middleware wraps the router.
func (g *Guard) Middleware(next http.Handler) http.Handler {
	if g == nil || g.service == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rule, routed, guarded := g.resolve(r)
		if !guarded {
			next.ServeHTTP(w, r)
			return
		}

		// The guard sits outside the router, so it authenticates for itself. A
		// token it does not accept is passed straight through: rejecting it
		// here would turn every authentication failure on a create route into a
		// quota error, and the route's own RequireAuth owns that refusal.
		claims, err := g.authenticate(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		body := g.readBody(r)
		// routed carries the path values; r is what continues down the chain.
		subscriptionID, err := g.subjectFor(routed, rule, body, claims.UserID)
		if err != nil {
			httpx.Error(w, r, httpx.Internal(err))
			return
		}
		// No subscription means no quota. The server's own administrator owns
		// the machine and is not sold a slice of it; a panel that refused them
		// because they had no plan would be a panel nobody could set up.
		if subscriptionID == "" {
			next.ServeHTTP(w, r)
			return
		}

		decision, err := g.service.CheckQuota(r.Context(), subscriptionID, rule.Dimension)
		if err != nil {
			// A subscription that has gone while the request was in flight is
			// not a server fault. The request proceeds and the handler decides;
			// the alternative is a 500 for a race nobody caused.
			if errors.Is(err, ErrNotFound) {
				next.ServeHTTP(w, r)
				return
			}
			httpx.Error(w, r, httpx.Internal(err))
			return
		}

		if !decision.Allowed {
			g.service.recordRefusal(r.Context(), claims.UserID, subscriptionID, decision, r)
			// 409 rather than 403: the caller has the right to do this, and it
			// is the state of the account that prevents it — which is what a
			// conflict is. A 403 would send somebody looking at permissions
			// that are not the problem.
			httpx.Error(w, r, httpx.Conflict(decision.Reason))
			return
		}

		if decision.OverSoftLimit {
			// The request proceeds. The header is how a page can say so without
			// every feature's response growing a quota field.
			w.Header().Set("X-JotHost-Quota-Warning", decision.Reason)
			logger.FromContext(r.Context(), g.service.log).Warn(
				"a subscription is over a soft limit",
				"subscription_id", subscriptionID,
				"dimension", rule.Dimension,
				"used", decision.Used)
		}

		next.ServeHTTP(w, r)
	})
}

// subjectFor resolves which subscription a request charges against.
func (g *Guard) subjectFor(r *http.Request, rule Rule, body map[string]any,
	callerID string,
) (string, error) {
	// A website that belongs to no subscription is an administrator's own
	// site: there is nothing to charge, and falling back to the caller here
	// would charge a reseller's plan for work inside a site that is not in it.
	fromWebsite := func(websiteID string) (string, error) {
		if websiteID == "" {
			return g.callerSubscription(r, callerID)
		}
		id, err := g.service.repo.SubscriptionForWebsite(r.Context(), websiteID)
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return id, nil
	}

	switch rule.Subject {
	case SubjectCaller:
		return g.callerSubscription(r, callerID)

	case SubjectBodySubscription:
		if id := stringField(body, "subscription_id"); id != "" {
			return id, nil
		}
		return g.callerSubscription(r, callerID)

	case SubjectPathWebsite:
		return fromWebsite(r.PathValue("id"))

	case SubjectBodyWebsite:
		return fromWebsite(stringField(body, "website_id"))

	case SubjectPathMailDomain:
		websiteID, err := g.service.repo.WebsiteForMailDomain(r.Context(), r.PathValue("id"))
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return fromWebsite(websiteID)

	default:
		return "", fmt.Errorf("unknown quota subject %q", rule.Subject)
	}
}

// callerSubscription returns the subscription the caller acts under.
//
// A caller with several is the case worth being explicit about: the first is
// charged, so somebody with two subscriptions cannot spend one's spare
// capacity on the other.
func (g *Guard) callerSubscription(r *http.Request, callerID string) (string, error) {
	ids, err := g.service.repo.SubscriptionsForOwner(r.Context(), callerID)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

// readBody buffers the request body so the guard can read an identifier out of
// it and the handler still receives every byte.
//
// A body that is not JSON, or is larger than the cap, yields nothing and the
// request falls back to the caller's subscription. Deliberately lenient: this
// is not the validator, and a body the handler will reject anyway must not be
// rejected here with a quota error that explains nothing.
func (g *Guard) readBody(r *http.Request) map[string]any {
	if r.Body == nil {
		return nil
	}
	buffered, err := io.ReadAll(io.LimitReader(r.Body, maxGuardBody+1))
	if err != nil {
		return nil
	}
	if len(buffered) > maxGuardBody {
		// Put back what was read and give up on parsing. Truncating the body
		// would corrupt the request the handler is about to read.
		r.Body = readCloser{
			Reader: io.MultiReader(bytes.NewReader(buffered), r.Body),
			Closer: r.Body,
		}
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(buffered))

	var fields map[string]any
	if err := json.Unmarshal(buffered, &fields); err != nil {
		return nil
	}
	return fields
}

// readCloser rejoins a buffered prefix to the rest of a body.
type readCloser struct {
	io.Reader
	io.Closer
}

// stringField reads one string out of a decoded body.
func stringField(body map[string]any, name string) string {
	if body == nil {
		return ""
	}
	value, _ := body[name].(string)
	return strings.TrimSpace(value)
}

// authenticate reads the caller's identity from the Authorization header.
//
// Bearer only, and only from the header — the same rule the authentication
// middleware follows, for the same reason: a token in a query string ends up in
// access logs and browser history.
func (g *Guard) authenticate(r *http.Request) (auth.AccessClaims, error) {
	header := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return auth.AccessClaims{}, auth.ErrInvalidToken
	}
	return g.auth.Authenticate(r.Context(), strings.TrimSpace(token))
}
