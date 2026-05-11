package commenttrigger

import "regexp"

var closedIssueDirectActionPattern = regexp.MustCompile(`(?i)\b(re-?open|re-?do|re-?run|retry|fix|bug|problem|compare|diff|investigate|check|look|address|follow[- ]?up|continue|again|wrong|broken|missing|regression|failed|failing|failure)\b`)
var closedIssueChangeRequestPattern = regexp.MustCompile(`(?i)\b(please|pls|can you|could you|would you|need(?:ed|s)?|must|should|request(?:ing)?|todo|action)\b.{0,80}\b(change|update|revise|revision)\b|\b(change|update|revise|revision)\b.{0,80}\b(please|needed|required|request|todo|action)\b`)

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
	return closedIssueDirectActionPattern.MatchString(content) || closedIssueChangeRequestPattern.MatchString(content)
}
