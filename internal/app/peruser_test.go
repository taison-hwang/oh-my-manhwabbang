package app

import (
	"context"
	"testing"

	"shelf/internal/index"
	"shelf/internal/principal"
	"shelf/internal/userdata"
)

func asUser(sub string) context.Context {
	return principal.With(context.Background(), principal.Principal{Sub: sub})
}

func userReadTo(t *testing.T, a *App, sub string, b index.SplitVolume, rel string, page int) {
	t.Helper()
	if _, err := a.user.PutProgress(asUser(sub), userdata.ProgressUpdate{
		BookID: b.BookID, SeriesID: b.SeriesID, RootName: "fx", BookPath: rel,
		Page: page, PageCount: b.PageCount,
	}); err != nil {
		t.Fatalf("PutProgress %s %s: %v", sub, b.BookID, err)
	}
}

func userProgress(t *testing.T, a *App, sub, id string) (userdata.Progress, bool) {
	t.Helper()
	p, err := a.user.GetProgress(asUser(sub), id)
	if err != nil {
		return userdata.Progress{}, false
	}
	return p, true
}

// A rename moves every reader's place, not just the single-user table's
// (ruling E-74 §2.13: the post-scan cleanup applies the same rules per user).
func TestAfterScan_perUser_renameCarriesEveryUsersProgressAndPrefs(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	old := bookAt(t, a, "낱권.zip")
	readTo(t, a, old, "낱권.zip", 2) // the single-user reader
	userReadTo(t, a, "alice", old, "낱권.zip", 3)
	userReadTo(t, a, "bob", old, "낱권.zip", 5)
	if _, err := a.user.PutPrefs(asUser("alice"), old.BookID, userdata.PrefsPatch{ReadingDir: userdata.SetPatch("rtl")}); err != nil {
		t.Fatal(err)
	}

	mustRename(t, media, "낱권.zip", "낱권 (완).zip")
	scanNow(t, a, false)
	moved := bookAt(t, a, "낱권 (완).zip")

	for sub, want := range map[string]int{"alice": 3, "bob": 5} {
		if p, ok := userProgress(t, a, sub, moved.BookID); !ok || p.LastPage != want {
			t.Errorf("%s: progress on the moved book = (%+v, %v), want page %d", sub, p, ok, want)
		}
		if _, ok := userProgress(t, a, sub, old.BookID); ok {
			t.Errorf("%s still has a row under the old id", sub)
		}
	}
	if p, ok := progressRow(t, a, moved.BookID); !ok || p.LastPage != 2 {
		t.Errorf("single-user progress on the moved book = (%+v, %v), want page 2", p, ok)
	}
	if p, err := a.user.GetPrefs(asUser("alice"), moved.BookID); err != nil || p.ReadingDir == nil || *p.ReadingDir != "rtl" {
		t.Errorf("alice's prefs did not follow the book: %+v, %v", p, err)
	}
	if p, _ := a.user.GetPrefs(asUser("bob"), moved.BookID); p.ReadingDir != nil {
		t.Errorf("bob received alice's prefs: %+v", p)
	}
}

// A vanished book is purged from each user's rows; a user's row for a book that
// is still there is kept, and one user's purge never touches another's.
func TestAfterScan_perUser_purgeIsPerUser(t *testing.T) {
	media := cleanupLibrary(t)
	a := newTestApp(t, testConfig(t, media), false)
	scanNow(t, a, false)

	gone := bookAt(t, a, "낱권.zip")
	stays := bookAt(t, a, "남을 시리즈/01권.zip")
	userReadTo(t, a, "alice", gone, "낱권.zip", 2)
	userReadTo(t, a, "bob", gone, "낱권.zip", 4)
	userReadTo(t, a, "bob", stays, "남을 시리즈/01권.zip", 1)

	mustRemove(t, media, "낱권.zip")
	scanNow(t, a, false)

	for _, sub := range []string{"alice", "bob"} {
		if _, ok := userProgress(t, a, sub, gone.BookID); ok {
			t.Errorf("%s: progress for the deleted book survived a completed walk", sub)
		}
	}
	if p, ok := userProgress(t, a, "bob", stays.BookID); !ok || p.LastPage != 1 {
		t.Errorf("bob's progress for a book that is still there = (%+v, %v)", p, ok)
	}
}
