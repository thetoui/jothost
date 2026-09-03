package security

import (
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The score, tested without a database or a host.
//
// Almost every test here is about one rule: a check that did not run is not a
// check that passed. It is the failure this phase is most able to commit,
// because a score computed from blanks looks exactly like a good one.

func outcomes(ran ...bool) []ScannerOutcome {
	out := make([]ScannerOutcome, 0, len(ran))
	for i, ok := range ran {
		out = append(out, ScannerOutcome{
			Scanner: validate.Scanners[i%len(validate.Scanners)], Ran: ok,
		})
	}
	return out
}

func allRan() []ScannerOutcome {
	return outcomes(true, true, true, true, true, true, true)
}

func TestACleanHostScoresFull(t *testing.T) {
	score := ComputeScore(Counts{}, allRan())

	if score.Value != 100 {
		t.Errorf("score = %d, want 100", score.Value)
	}
	if score.Grade != "good" {
		t.Errorf("grade = %q, want good", score.Grade)
	}
	if !score.Complete {
		t.Error("a scan where every check ran was reported as incomplete")
	}
	if score.Summary != "Every check ran and found nothing." {
		t.Errorf("summary = %q", score.Summary)
	}
}

func TestAScoreSaysHowManyChecksItIsBuiltFrom(t *testing.T) {
	// This is the rule the phase turns on. A host whose firewall cannot be read
	// is not a host with a good firewall, and the number that says so has to
	// travel with the score everywhere it goes.
	score := ComputeScore(Counts{}, outcomes(true, false, true, true, true, true, true))

	if score.ChecksRun != 6 || score.ChecksTotal != 7 {
		t.Fatalf("checks = %d of %d, want 6 of 7", score.ChecksRun, score.ChecksTotal)
	}
	if score.Complete {
		t.Error("a scan with a check that could not run was reported as complete")
	}
	if !strings.Contains(score.Summary, "incomplete rather than reassuring") {
		t.Errorf("summary does not say the score is incomplete: %q", score.Summary)
	}
	if !strings.Contains(score.Summary, "firewall") {
		t.Errorf("summary does not name the check that could not run: %q", score.Summary)
	}
}

func TestAnUnreadableCheckDoesNotLowerTheScoreEither(t *testing.T) {
	// It must not be scored as a failure, any more than as a pass. A panel that
	// docked points for its own inability to look would push an operator into
	// chasing a number rather than a problem.
	clean := ComputeScore(Counts{}, allRan())
	partial := ComputeScore(Counts{}, outcomes(true, false, true, true, true, true, true))

	if partial.Value != clean.Value {
		t.Errorf("a check that could not run changed the score: %d vs %d",
			partial.Value, clean.Value)
	}
	// What changes is what the panel *says* about it, not the arithmetic.
	if partial.Summary == clean.Summary {
		t.Error("a partial scan reads exactly like a complete one")
	}
}

func TestOneCriticalCostsMoreThanEveryLowFindingLikely(t *testing.T) {
	// A database on 0.0.0.0 is not four hygiene issues. A scale where enough
	// small things added up to one big thing would let a host with a wide-open
	// Redis score better than one with untidy file modes.
	critical := ComputeScore(Counts{Critical: 1, Open: 1}, allRan())
	lows := ComputeScore(Counts{Low: 8, Open: 8}, allRan())

	if critical.Value >= lows.Value {
		t.Errorf("one critical (%d) did not cost more than eight lows (%d)",
			critical.Value, lows.Value)
	}
}

func TestInfoCostsNothing(t *testing.T) {
	// It exists so the panel can say things worth knowing without either
	// inflating them into a problem or leaving them out.
	score := ComputeScore(Counts{Info: 5, Open: 5}, allRan())

	if score.Value != 100 {
		t.Errorf("score = %d, want 100: info findings must not cost anything", score.Value)
	}
}

func TestTheScoreNeverGoesBelowZero(t *testing.T) {
	score := ComputeScore(Counts{Critical: 20, Open: 20}, allRan())

	if score.Value != 0 {
		t.Errorf("score = %d, want 0", score.Value)
	}
	if score.Grade != "at risk" {
		t.Errorf("grade = %q, want at risk", score.Grade)
	}
}

func TestAcceptedFindingsDoNotCostScoreAndAreStillCounted(t *testing.T) {
	// Accepting has to be worth doing — a risk somebody assessed and wrote down
	// a reason for is not the same as one nobody has looked at — and it must
	// not be a way to reach 100 by writing sentences. So it costs no score and
	// the count is never hidden.
	score := ComputeScore(Counts{Accepted: 3}, allRan())

	if score.Value != 100 {
		t.Errorf("score = %d, want 100", score.Value)
	}
	if !strings.Contains(score.Summary, "3 accepted risk") {
		t.Errorf("summary does not carry the accepted count: %q", score.Summary)
	}
}

func TestTheSummaryLeadsWithWhatToFixFirst(t *testing.T) {
	// The page's job is to say what to do next, and a critical finding is
	// always the answer when there is one.
	score := ComputeScore(Counts{Critical: 2, High: 5, Low: 9, Open: 16}, allRan())

	if !strings.Contains(score.Summary, "exploited as they stand") {
		t.Errorf("summary does not lead with the criticals: %q", score.Summary)
	}
}

func TestAnUnknownSeverityIsNotFree(t *testing.T) {
	// A severity this panel does not understand must not cost nothing, which is
	// what an unlisted value would do if the weights were a bare map.
	if weightFor("catastrophic") != weightMedium {
		t.Errorf("weightFor(unknown) = %d, want the medium weight %d",
			weightFor("catastrophic"), weightMedium)
	}
}

func TestGradesCoverTheWholeRange(t *testing.T) {
	for value, want := range map[int]string{
		100: "good", 90: "good", 89: "fair", 70: "fair",
		69: "poor", 40: "poor", 39: "at risk", 0: "at risk",
	} {
		if got := gradeFor(value); got != want {
			t.Errorf("gradeFor(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestAHostWithNoChecksAtAllSaysSo(t *testing.T) {
	score := ComputeScore(Counts{}, nil)

	if score.Summary != "Nothing has been checked yet." {
		t.Errorf("summary = %q", score.Summary)
	}
}

func TestJoinWordsReadsAsASentence(t *testing.T) {
	cases := map[string][]string{
		"nothing":                 {},
		"ssl":                     {"ssl"},
		"ssl and ports":           {"ssl", "ports"},
		"ssl, ports and firewall": {"ssl", "ports", "firewall"},
	}
	for want, words := range cases {
		if got := joinWords(words); got != want {
			t.Errorf("joinWords(%v) = %q, want %q", words, got, want)
		}
	}
}

// ----------------------------------------------------- severity mapping

func TestSSHSeveritiesMapOntoThisScale(t *testing.T) {
	// Phase 17 speaks in critical/warning/info because that is what an SSH page
	// needs. What matters is that nothing it says is quietly downgraded.
	cases := map[string]string{
		"critical": validate.FindingCritical,
		"warning":  validate.FindingMedium,
		"info":     validate.FindingInfo,
	}
	for from, want := range cases {
		if got := mapSSHSeverity(from); got != want {
			t.Errorf("mapSSHSeverity(%q) = %q, want %q", from, got, want)
		}
	}
}

func TestAnUngradableSSHFindingIsNotTreatedAsHarmless(t *testing.T) {
	// A finding this panel cannot grade must not be graded as info, which is
	// the tempting default and the wrong one.
	if got := mapSSHSeverity("something-new"); got != validate.FindingMedium {
		t.Errorf("mapSSHSeverity(unknown) = %q, want medium", got)
	}
}

// ------------------------------------------------------- fingerprints

func TestFingerprintsSurviveAnAwkwardDomain(t *testing.T) {
	// A fingerprint is the key of a unique index. One containing a character
	// the index will not take is a finding that never matches itself, and a
	// table that grows by one duplicate per scan forever.
	for _, value := range []string{
		"exámple.com", "a site/with spaces", "", "***", "UPPER.CASE.COM",
	} {
		got := sanitiseFingerprint(value)
		if err := validate.Fingerprint("ssl:missing:" + got); err != nil {
			t.Errorf("sanitiseFingerprint(%q) = %q, which is not a usable fingerprint: %v",
				value, got, err)
		}
	}
}

func TestFingerprintsAreStableForTheSameThing(t *testing.T) {
	// The whole point: two scans of the same host must produce the same
	// fingerprint, or first_seen_at means nothing.
	if sanitiseFingerprint("example.com") != sanitiseFingerprint("example.com") {
		t.Fatal("the same input produced two fingerprints")
	}
	if sanitiseFingerprint("a.example") == sanitiseFingerprint("b.example") {
		t.Fatal("two different sites produced the same fingerprint")
	}
}

// --------------------------------------------------------- scanner shape

func TestAnUnavailableScannerCarriesNoFindings(t *testing.T) {
	// It has to carry none, because the caller uses "ran" to decide whether to
	// resolve what is missing — and a scanner that could not answer while
	// reporting an empty list would resolve everything it had ever found.
	result := unavailable(validate.ScannerFirewall, "ufw could not be read")

	if result.Ran {
		t.Error("an unavailable scanner reported that it ran")
	}
	if len(result.Findings) != 0 {
		t.Error("an unavailable scanner reported findings")
	}
	if result.Reason == "" {
		t.Error("an unavailable scanner gave no reason")
	}
}

func TestAScannerThatRanAndFoundNothingIsNotUnavailable(t *testing.T) {
	// The two look identical in a naive implementation and mean opposite
	// things: one resolves old findings, the other must not.
	result := ran(validate.ScannerFirewall)

	if !result.Ran {
		t.Error("a scanner that answered was recorded as unavailable")
	}
	if result.Findings == nil {
		t.Error("a scanner that found nothing returned a nil list rather than an empty one")
	}
}

// --------------------------------------------------- grouped findings

func TestGroupedFindingsStillNameWhatTheyCover(t *testing.T) {
	// A grouped finding that did not say which sites would be one nobody can
	// act on. The count is always exact; the names are the first few, because a
	// description listing four hundred domains is one nothing can display.
	if got := describeSites([]string{"a.example"}); got != "a.example is" {
		t.Errorf("describeSites(one) = %q", got)
	}
	if got := describeSites([]string{"a.example", "b.example"}); got != "a.example, b.example are" {
		t.Errorf("describeSites(two) = %q", got)
	}

	many := []string{"a", "b", "c", "d", "e", "f", "g"}
	got := describeSites(many)
	if !strings.Contains(got, "and 2 more") {
		t.Errorf("describeSites(seven) = %q, want it to say how many were not named", got)
	}
	if strings.Contains(got, "g") {
		t.Errorf("describeSites(seven) listed every domain: %q", got)
	}
}
