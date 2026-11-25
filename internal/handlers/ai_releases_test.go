package handlers

import (
	"strings"
	"testing"

	"windshift/internal/llm"
	"windshift/internal/services"
)

// completedProgress builds a two-item milestone where only the "Done" category
// counts as completed, matching what GetMilestoneProgress returns.
func completedProgress() *services.MilestoneProgressReport {
	return &services.MilestoneProgressReport{
		TotalItems:      2,
		CompletedItems:  1,
		PercentComplete: 50,
		StatusBreakdown: []services.StatusBreakdown{
			{CategoryName: "Done", ItemCount: 1, IsCompleted: true},
			{CategoryName: "To Do", ItemCount: 1},
		},
		ItemsByCategory: map[string][]services.ProgressItem{
			"Done":  {{WorkspaceKey: "WI", ItemNumber: 42, Title: "Add grounded release notes"}},
			"To Do": {{WorkspaceKey: "WI", ItemNumber: 43, Title: "Unreleased work"}},
		},
	}
}

func TestBuildReleaseNotesUserPromptIncludesGroundedCompletedItems(t *testing.T) {
	progress := completedProgress()
	items := collectReleaseNotesItems(progress)
	if len(items) != 1 {
		t.Fatalf("collectReleaseNotesItems() returned %d items, want 1 (only the completed category)", len(items))
	}

	prompt := buildReleaseNotesUserPrompt(
		&services.MilestoneResult{Name: "0.8.2"},
		progress,
		nil,
		items,
		len(items),
		0,
	)
	if !strings.Contains(prompt, "WI-42: Add grounded release notes") {
		t.Fatalf("prompt does not contain completed item: %s", prompt)
	}
	if strings.Contains(prompt, "WI-43") {
		t.Fatalf("prompt contains incomplete item: %s", prompt)
	}
	// The grounding constraint now lives in the overridable system prompt that
	// is sent alongside this user message.
	systemPrompt := llm.NewPromptStore("").Get(llm.PromptReleaseNotes)
	if !strings.Contains(systemPrompt, "Use only facts explicitly present") {
		t.Fatalf("system prompt does not contain grounding constraint: %s", systemPrompt)
	}
}

// A milestone whose only items sit in a non-completed status category yields no
// prompt items at all; GenerateReleaseNotes turns that into a 409 conflict
// instead of asking the model to invent release notes from nothing.
func TestCollectReleaseNotesItemsRejectsEmptyGrounding(t *testing.T) {
	progress := &services.MilestoneProgressReport{
		TotalItems:      1,
		StatusBreakdown: []services.StatusBreakdown{{CategoryName: "Done", ItemCount: 1}},
		ItemsByCategory: map[string][]services.ProgressItem{
			"Done": {{WorkspaceKey: "WI", ItemNumber: 42, Title: "Completed feature"}},
		},
	}
	if items := collectReleaseNotesItems(progress); len(items) != 0 {
		t.Fatalf("collectReleaseNotesItems() returned %d items, want 0 when no category is completed", len(items))
	}
}
