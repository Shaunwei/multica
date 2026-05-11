package commenttrigger

import "regexp"

var closedIssueImperativeActionPattern = regexp.MustCompile(`(?i)(^|[.!?\n]\s*)(please\s+)?(re-?open|re-?do|re-?run|retry|fix|compare|diff|investigate|check|look(?:\s+into)?|address|follow[- ]?up|continue)\b`)
var closedIssueRequestedActionPattern = regexp.MustCompile(`(?i)\b(please|pls|can you|could you|would you|need(?:ed|s)?|must|should|request(?:ing)?|todo|action)\b.{0,80}\b(re-?open|re-?do|re-?run|retry|fix|compare|diff|investigate|check|look(?:\s+into)?|address|follow[- ]?up|continue|change|update|revise|revision)\b|\b(re-?open|re-?do|re-?run|retry|fix|compare|diff|investigate|check|look(?:\s+into)?|address|follow[- ]?up|continue|change|update|revise|revision)\b.{0,80}\b(please|needed|required|request(?:ing)?|todo|action)\b`)
var closedIssueProblemSignalPattern = regexp.MustCompile(`(?i)\b(bug|problem|wrong|broken|missing|regression|failed|failing|failure)\b`)

// IsClosedIssueStatus returns true for issue states where passive comments
// should not wake an agent back up. Explicitly actionable comments can still
// create work on closed issues.
func IsClosedIssueStatus(status string) bool {
	return status == "done" || status == "cancelled"
}

// LooksActionableOnClosedIssue is a conservative lexical gate for closed issue
// comments. It intentionally recognizes only comments that ask for more work
// (reopen/change/redo/compare/check/fix/etc.) so post-completion chatter like
// "thanks" or integration summaries does not burn a follow-up run.
func LooksActionableOnClosedIssue(content string) bool {
	return closedIssueImperativeActionPattern.MatchString(content) ||
		closedIssueRequestedActionPattern.MatchString(content) ||
		closedIssueProblemSignalPattern.MatchString(content)
}
