package commandmemory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertGeneratedDeduplicatesByArtifactShellAndContext(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	first, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "list hidden files",
		CommandText: "ls -a",
		OutputKind:  "single_command",
		Shell:       "zsh",
		CWD:         "/tmp/project",
	})
	if err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}
	second, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "show hidden entries",
		CommandText: "  ls   -a  ",
		OutputKind:  "single_command",
		Shell:       "zsh",
		CWD:         "/tmp/project",
	})
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected duplicate artifact to reuse same row id, got first=%d second=%d", first.ID, second.ID)
	}
	items, err := store.Search(ctx, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item after duplicate upsert, got %d", len(items))
	}
	if strings.TrimSpace(items[0].Prompt) != "show hidden entries" {
		t.Fatalf("expected latest prompt to be stored, got %q", items[0].Prompt)
	}
}

func TestSearchRankingPrefersPinnedItem(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	a, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "show git status",
		CommandText: "git status --short",
		OutputKind:  "single_command",
		Shell:       "zsh",
		CWD:         "/repo",
	})
	if err != nil {
		t.Fatalf("upsert a failed: %v", err)
	}
	b, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "check disk usage by folder",
		CommandText: "du -sh ./*",
		OutputKind:  "single_command",
		Shell:       "zsh",
		CWD:         "/repo",
	})
	if err != nil {
		t.Fatalf("upsert b failed: %v", err)
	}
	if err := store.SetPinned(ctx, b.ID, true); err != nil {
		t.Fatalf("pin failed: %v", err)
	}

	items, err := store.Search(ctx, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ID != b.ID {
		t.Fatalf("expected pinned item first, got first=%d pinned=%d", items[0].ID, b.ID)
	}
	if items[1].ID != a.ID {
		t.Fatalf("unexpected second item id=%d expected=%d", items[1].ID, a.ID)
	}
}

func TestHideRemovesItemFromSearch(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	item, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "list files",
		CommandText: "ls -la",
		OutputKind:  "single_command",
		Shell:       "zsh",
	})
	if err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if err := store.Hide(ctx, item.ID); err != nil {
		t.Fatalf("hide failed: %v", err)
	}
	items, err := store.Search(ctx, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected hidden item to be excluded, got %d entries", len(items))
	}
}

func TestSearchMatchesPromptAndCommand(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	if _, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "check disk usage by directory",
		CommandText: "du -sh ./*",
		OutputKind:  "single_command",
		Shell:       "zsh",
	}); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if _, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "show hidden files",
		CommandText: "ls -a",
		OutputKind:  "single_command",
		Shell:       "zsh",
	}); err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}

	byPrompt, err := store.Search(ctx, SearchOptions{Query: "disk", Limit: 10})
	if err != nil {
		t.Fatalf("search by prompt failed: %v", err)
	}
	if len(byPrompt) == 0 || !strings.Contains(strings.ToLower(byPrompt[0].Prompt), "disk") {
		t.Fatalf("expected prompt match for query=disk, got %+v", byPrompt)
	}

	byCommand, err := store.Search(ctx, SearchOptions{Query: "ls -a", Limit: 10})
	if err != nil {
		t.Fatalf("search by command failed: %v", err)
	}
	if len(byCommand) == 0 || !strings.Contains(strings.ToLower(byCommand[0].CommandText), "ls -a") {
		t.Fatalf("expected command match for query=ls -a, got %+v", byCommand)
	}
}

func TestSearchRankingPrefersProjectMatchAndUseCount(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	projectItem, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "list hidden files",
		CommandText: "ls -a",
		OutputKind:  "single_command",
		Shell:       "zsh",
		ProjectRoot: "/repo/a",
		CWD:         "/repo/a",
	})
	if err != nil {
		t.Fatalf("upsert project item failed: %v", err)
	}
	otherItem, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "list hidden files",
		CommandText: "ls -a",
		OutputKind:  "single_command",
		Shell:       "zsh",
		ProjectRoot: "/repo/b",
		CWD:         "/repo/b",
	})
	if err != nil {
		t.Fatalf("upsert other item failed: %v", err)
	}
	if err := store.RecordRun(ctx, otherItem.ID, 0, true); err != nil {
		t.Fatalf("record run failed: %v", err)
	}
	if err := store.RecordRun(ctx, otherItem.ID, 0, true); err != nil {
		t.Fatalf("record second run failed: %v", err)
	}

	items, err := store.Search(ctx, SearchOptions{
		Query:       "hidden files",
		ProjectRoot: "/repo/a",
		CWD:         "/repo/a",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected at least two items, got %d", len(items))
	}
	if items[0].ID != projectItem.ID {
		t.Fatalf("expected project-matching item first, got id=%d", items[0].ID)
	}
}

func TestRecordRunUpdatesUsageStats(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	ctx := context.Background()

	item, err := store.UpsertGenerated(ctx, UpsertInput{
		Prompt:      "print directory",
		CommandText: "pwd",
		OutputKind:  "single_command",
		Shell:       "zsh",
	})
	if err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if err := store.RecordRun(ctx, item.ID, 0, true); err != nil {
		t.Fatalf("record success failed: %v", err)
	}
	if err := store.RecordRun(ctx, item.ID, 2, false); err != nil {
		t.Fatalf("record failure failed: %v", err)
	}
	got, found, err := store.GetByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("get by id failed: %v", err)
	}
	if !found {
		t.Fatalf("expected item %d to exist", item.ID)
	}
	if got.UseCount != 2 {
		t.Fatalf("expected use_count=2, got %d", got.UseCount)
	}
	if got.SuccessCount != 1 || got.FailureCount != 1 {
		t.Fatalf("unexpected success/failure counts: success=%d failure=%d", got.SuccessCount, got.FailureCount)
	}
	if got.LastExitCode == nil || *got.LastExitCode != 2 {
		t.Fatalf("expected last_exit_code=2, got %+v", got.LastExitCode)
	}
}

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "command-memory.db")
	store, err := OpenSQLite(path, nil)
	if err != nil {
		t.Fatalf("open sqlite store failed: %v", err)
	}
	return store
}
