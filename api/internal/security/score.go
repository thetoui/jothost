package security

import (
	"fmt"

	"github.com/jothost/panel/shared/validate"
)

// The score.
//
// # Why a number at all
//
// Because "you have 14 findings" is not a sentence anybody can act on, and
// because the question an operator actually has is "is this getting better or
// worse". A number answers that; a list does not.
//
// # Why this number
//
// It starts at 100 and subtracts a weight per open finding. That is the
// simplest thing that could work, and simple matters here more than clever: a
// score nobody can predict is a score nobody trusts, and the first time it
// moves for a reason somebody cannot reconstruct they stop reading it.
//
// The weights are deliberately far apart. One critical finding costs more than
// every low finding a host is likely to have put together, because that is
// true: a database on 0.0.0.0 is not four hygiene issues, it is a different
// kind of problem. A scale where enough small things add up to one big thing
// would let a host with a wide-open Redis score better than one with untidy
// file modes.
//
// Info costs nothing. It exists so the panel can say things that are worth
// knowing without either inflating them into a problem or leaving them out.
//
// # The rule that matters more than the arithmetic
//
// **A check that did not run is not a check that passed.** A host whose
// firewall cannot be read does not score as a host with a good firewall. The
// score is reported with the number of checks that answered, everywhere, and a
// score from four checks out of seven is presented as exactly that rather than
// as a number.
//
// TASKS.md says this in the phase's dependency note — "or the score is computed
// from blanks" — and it is the failure this phase is most able to commit,
// because a score computed from blanks looks identical to a good one.

// Weights subtracted from 100 per open finding.
const (
	weightCritical = 40
	weightHigh     = 20
	weightMedium   = 10
	weightLow      = 4
	weightInfo     = 0
)

// weightFor returns the score cost of one finding.
//
// An unrecognised severity costs the same as medium rather than nothing. A
// severity this panel does not understand must not be free.
func weightFor(severity string) int {
	switch severity {
	case validate.FindingCritical:
		return weightCritical
	case validate.FindingHigh:
		return weightHigh
	case validate.FindingMedium:
		return weightMedium
	case validate.FindingLow:
		return weightLow
	case validate.FindingInfo:
		return weightInfo
	default:
		return weightMedium
	}
}

// Score is the computed posture of a host.
type Score struct {
	Value int `json:"value"`
	// Grade is a word rather than a letter. "Poor" tells somebody what to do
	// with the number; "C" makes them work out the scale first.
	Grade string `json:"grade"`
	// ChecksRun and ChecksTotal travel with the score everywhere. A 100 from
	// two checks is not a 100.
	ChecksRun   int `json:"checks_run"`
	ChecksTotal int `json:"checks_total"`
	// Complete reports whether every check answered.
	Complete bool `json:"complete"`
	// Summary is the sentence to show beside the number, and it says outright
	// when the score is built on an incomplete picture.
	Summary string `json:"summary"`
}

// ComputeScore turns the open findings and the scanner outcomes into a score.
//
// Accepted findings are not counted. That is what makes accepting worth
// offering: a risk somebody has assessed and written down a reason for is not
// the same as one nobody has looked at. It is also why the accepted count is
// returned alongside and shown next to the score forever — a score carried by
// accepted risk is a different thing from a clean one, and hiding that would
// turn accepting into a way to reach 100 by writing sentences.
func ComputeScore(counts Counts, outcomes []ScannerOutcome) Score {
	score := Score{Value: 100, ChecksTotal: len(outcomes)}

	for _, outcome := range outcomes {
		if outcome.Ran {
			score.ChecksRun++
		}
	}
	score.Complete = score.ChecksRun == score.ChecksTotal

	score.Value -= counts.Critical * weightCritical
	score.Value -= counts.High * weightHigh
	score.Value -= counts.Medium * weightMedium
	score.Value -= counts.Low * weightLow

	if score.Value < 0 {
		score.Value = 0
	}
	if score.Value > 100 {
		score.Value = 100
	}

	score.Grade = gradeFor(score.Value)
	score.Summary = summarise(score, counts, outcomes)
	return score
}

// gradeFor turns a number into a word.
//
// The bands are wide and there are four of them. A finer scale would imply a
// precision the arithmetic does not have.
func gradeFor(value int) string {
	switch {
	case value >= 90:
		return "good"
	case value >= 70:
		return "fair"
	case value >= 40:
		return "poor"
	default:
		return "at risk"
	}
}

// summarise writes the sentence shown beside the number.
//
// The incomplete case comes first and is unambiguous, because a score built on
// a partial picture is the one somebody is most likely to misread — and the
// misreading is always in the reassuring direction.
func summarise(score Score, counts Counts, outcomes []ScannerOutcome) string {
	if score.ChecksTotal == 0 {
		return "Nothing has been checked yet."
	}

	if !score.Complete {
		missing := []string{}
		for _, outcome := range outcomes {
			if !outcome.Ran {
				missing = append(missing, outcome.Scanner)
			}
		}
		return fmt.Sprintf(
			"Built from %d of %d checks — %s could not be run, so this score is "+
				"incomplete rather than reassuring.",
			score.ChecksRun, score.ChecksTotal, joinWords(missing))
	}

	switch {
	case counts.Critical > 0:
		return fmt.Sprintf(
			"%d thing(s) here can be exploited as they stand. Start with those.",
			counts.Critical)
	case counts.High > 0:
		return fmt.Sprintf(
			"%d thing(s) here need one more condition to be exploitable.", counts.High)
	case counts.Open > 0:
		return fmt.Sprintf("%d thing(s) worth tidying, none of them urgent.", counts.Open)
	case counts.Accepted > 0:
		return fmt.Sprintf(
			"Nothing outstanding, with %d accepted risk(s) carried.", counts.Accepted)
	default:
		return "Every check ran and found nothing."
	}
}

// joinWords renders a list the way a sentence needs it.
func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return "nothing"
	case 1:
		return words[0]
	case 2:
		return words[0] + " and " + words[1]
	default:
		out := ""
		for i, word := range words[:len(words)-1] {
			if i > 0 {
				out += ", "
			}
			out += word
		}
		return out + " and " + words[len(words)-1]
	}
}
