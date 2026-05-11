package commenttrigger

import "testing"

func TestLooksActionableOnClosedIssue(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"passive thanks", "Integrated and marked done. Thanks!", false},
		{"passive update summary", "Updated the parent description and marked the issue done.", false},
		{"passive later check", "I'll check this later after lunch", false},
		{"passive fix landed", "The fix landed in main", false},
		{"redo request", "Please redo this and compare the output", true},
		{"change request", "Can you change the final summary?", true},
		{"explicit check request", "Check this against the latest run", true},
		{"explicit continue request", "Continue from the last result", true},
		{"explicit update request", "Please update the final summary", true},
		{"bug report", "This is still broken", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksActionableOnClosedIssue(tt.content); got != tt.want {
				t.Fatalf("LooksActionableOnClosedIssue(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
