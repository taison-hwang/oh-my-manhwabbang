package userdata_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"shelf/internal/principal"
	"shelf/internal/userdata"
)

func asSub(sub string) context.Context {
	return principal.With(context.Background(), principal.Principal{Sub: sub})
}

// Every per-user table is closed to the other users and to the single-user
// tables, and the single-user tables stay what a no-principal call reads.
func TestPerUser_rowsAreIsolatedBetweenUsersAndFromTheSingleUserTables(t *testing.T) {
	db, _, _ := newDB(t)
	alice, bob, legacy := asSub("alice"), asSub("bob"), context.Background()

	if _, err := db.PutProgress(legacy, update("b1", "s1", 1, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutProgress(alice, update("b1", "s1", 7, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutProgress(alice, update("b2", "s1", 2, 10)); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		ctx  context.Context
		page int
		ok   bool
	}{{"alice", alice, 7, true}, {"legacy", legacy, 1, true}, {"bob", bob, 0, false}} {
		got, err := db.GetProgress(c.ctx, "b1")
		switch {
		case c.ok && (err != nil || got.LastPage != c.page):
			t.Errorf("%s: GetProgress = %+v, %v; want page %d", c.name, got, err, c.page)
		case !c.ok && !errors.Is(err, userdata.ErrNotFound):
			t.Errorf("%s: GetProgress = %+v, %v; want ErrNotFound", c.name, got, err)
		}
	}

	many, _ := db.GetProgressMany(bob, []string{"b1", "b2"})
	if len(many) != 0 {
		t.Errorf("bob's GetProgressMany = %v", many)
	}
	if n, _ := db.CountProgress(alice); n != 2 {
		t.Errorf("alice count = %d, want 2", n)
	}
	if n, _ := db.CountProgress(bob); n != 0 {
		t.Errorf("bob count = %d, want 0", n)
	}
	if n, _ := db.CountProgress(legacy); n != 1 {
		t.Errorf("legacy count = %d, want 1", n)
	}
	if rows, _ := db.Continue(bob, 10); len(rows) != 0 {
		t.Errorf("bob's Continue = %v", rows)
	}
	agg, _ := db.SeriesAggregates(alice, nil)
	if a := agg["s1"]; a.BooksStarted != 2 {
		t.Errorf("alice's aggregate = %+v, want 2 started", a)
	}
	if agg, _ := db.SeriesAggregates(bob, []string{"s1"}); len(agg) != 0 {
		t.Errorf("bob's aggregate = %v", agg)
	}

	// book prefs, settings, view state
	if _, err := db.PutPrefs(alice, "b1", userdata.PrefsPatch{ReadingDir: userdata.SetPatch("rtl")}); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.GetPrefs(bob, "b1"); p.ReadingDir != nil {
		t.Errorf("bob inherits alice's book prefs: %+v", p)
	}
	if p, _ := db.GetPrefs(legacy, "b1"); p.ReadingDir != nil {
		t.Errorf("the single-user prefs picked up alice's: %+v", p)
	}
	if list, _ := db.ListPrefs(alice); len(list) != 1 {
		t.Errorf("alice's ListPrefs = %v", list)
	}
	if list, _ := db.ListPrefs(bob); len(list) != 0 {
		t.Errorf("bob's ListPrefs = %v", list)
	}
	for name, kv := range map[string]*userdata.KV{"settings": db.Settings(), "view_state": db.ViewState()} {
		if err := kv.Put(alice, "k", "alice-value"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := kv.Get(bob, "k"); ok {
			t.Errorf("%s: bob reads alice's key", name)
		}
		if _, ok, _ := kv.Get(legacy, "k"); ok {
			t.Errorf("%s: the single-user table received alice's key", name)
		}
		if all, _ := kv.All(bob); len(all) != 0 {
			t.Errorf("%s: bob's All = %v", name, all)
		}
		if err := kv.Delete(bob, "k"); err != nil {
			t.Fatal(err)
		}
		if v, ok, _ := kv.Get(alice, "k"); !ok || v != "alice-value" {
			t.Errorf("%s: bob's Delete reached alice's key", name)
		}
	}

	// Deleting is scoped too.
	if err := db.DeleteProgress(bob, "b1"); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetProgress(alice, "b1"); err != nil || got.LastPage != 7 {
		t.Errorf("bob's DeleteProgress removed alice's row: %+v, %v", got, err)
	}
}

func TestPerUser_exportAndImportAreScopedAndCarryNoUserKey(t *testing.T) {
	db, _, _ := newDB(t)
	alice, bob := asSub("alice"), asSub("bob")
	if _, err := db.PutProgress(alice, update("b1", "s1", 3, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutPrefs(alice, "b1", userdata.PrefsPatch{FitMode: userdata.SetPatch("width")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutProgress(context.Background(), update("legacy-book", "s9", 1, 5)); err != nil {
		t.Fatal(err)
	}

	doc, err := db.Export(alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 1 || doc.Items[0].BookID != "b1" || len(doc.Prefs) != 1 {
		t.Fatalf("alice's export = %+v", doc)
	}
	if other, _ := db.Export(bob); len(other.Items) != 0 || len(other.Prefs) != 0 {
		t.Fatalf("bob's export = %+v", other)
	}

	if _, err := db.Import(bob, doc, userdata.StrategyMerge); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetProgress(bob, "b1"); err != nil || got.LastPage != 3 {
		t.Fatalf("bob's imported progress = %+v, %v", got, err)
	}
	if p, _ := db.GetPrefs(bob, "b1"); p.FitMode == nil || *p.FitMode != "width" {
		t.Fatalf("bob's imported prefs = %+v", p)
	}
	if _, err := db.GetProgress(context.Background(), "b1"); !errors.Is(err, userdata.ErrNotFound) {
		t.Fatalf("the import reached the single-user table: %v", err)
	}
}

func TestMigrateLegacyOwner_copiesOnce_andNeverForASecondUser(t *testing.T) {
	db, _, _ := newDB(t)
	legacy := context.Background()
	if _, err := db.PutProgress(legacy, update("b1", "s1", 4, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutPrefs(legacy, "b1", userdata.PrefsPatch{ReadingDir: userdata.SetPatch("rtl")}); err != nil {
		t.Fatal(err)
	}
	if err := db.Settings().Put(legacy, "theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := db.ViewState().Put(legacy, "lib", "grid"); err != nil {
		t.Fatal(err)
	}

	moved, err := db.MigrateLegacyOwner(legacy, "owner")
	if err != nil || !moved {
		t.Fatalf("first migration = %v, %v; want true", moved, err)
	}
	owner := asSub("owner")
	if got, err := db.GetProgress(owner, "b1"); err != nil || got.LastPage != 4 {
		t.Fatalf("owner's progress = %+v, %v", got, err)
	}
	if p, _ := db.GetPrefs(owner, "b1"); p.ReadingDir == nil || *p.ReadingDir != "rtl" {
		t.Fatalf("owner's prefs = %+v", p)
	}
	if v, ok, _ := db.Settings().Get(owner, "theme"); !ok || v != "dark" {
		t.Fatalf("owner's settings = %q, %v", v, ok)
	}
	if v, ok, _ := db.ViewState().Get(owner, "lib"); !ok || v != "grid" {
		t.Fatalf("owner's view state = %q, %v", v, ok)
	}
	if got, _ := db.GetProgress(legacy, "b1"); got.LastPage != 4 {
		t.Fatalf("the single-user table was altered: %+v", got)
	}
	if v, ok, _ := db.Meta(legacy, "legacy_migrated_to"); !ok || v != "owner" {
		t.Fatalf("meta.legacy_migrated_to = %q, %v", v, ok)
	}

	// The owner moves on; running it again must not bring the old rows back.
	if err := db.DeleteProgress(owner, "b1"); err != nil {
		t.Fatal(err)
	}
	if moved, err := db.MigrateLegacyOwner(legacy, "owner"); err != nil || moved {
		t.Fatalf("second migration = %v, %v; want false", moved, err)
	}
	if _, err := db.GetProgress(owner, "b1"); !errors.Is(err, userdata.ErrNotFound) {
		t.Fatalf("a repeated migration restored a deleted row: %v", err)
	}
	// And nobody else gets a second copy.
	if moved, err := db.MigrateLegacyOwner(legacy, "intruder"); err != nil || moved {
		t.Fatalf("migration for another user = %v, %v; want false", moved, err)
	}
	if _, err := db.GetProgress(asSub("intruder"), "b1"); !errors.Is(err, userdata.ErrNotFound) {
		t.Fatalf("a second user received the single-user data: %v", err)
	}
	if _, err := db.MigrateLegacyOwner(legacy, ""); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Fatalf("empty sub = %v, want ErrInvalidArgument", err)
	}
}

func TestMigrateLegacyOwner_keepsRowsTheUserAlreadyHas(t *testing.T) {
	db, _, _ := newDB(t)
	legacy, owner := context.Background(), asSub("owner")
	_, _ = db.PutProgress(legacy, update("b1", "s1", 4, 10))
	_, _ = db.PutProgress(owner, update("b1", "s1", 9, 10))
	if _, err := db.MigrateLegacyOwner(legacy, "owner"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetProgress(owner, "b1"); got.LastPage != 9 {
		t.Fatalf("the migration overwrote the user's own row: %+v", got)
	}
}

func TestRestoreOwner_newerUpdatedAtWins_andTiesKeepTheSingleUserRow(t *testing.T) {
	db, clk, _ := newDB(t)
	legacy, owner := context.Background(), asSub("owner")

	// older legacy row, newer user row -> user wins
	_, _ = db.PutProgress(legacy, update("old-legacy", "s", 1, 10))
	clk.Advance(time.Minute)
	_, _ = db.PutProgress(owner, update("old-legacy", "s", 8, 10))

	// newer legacy row, older user row -> legacy stays
	_, _ = db.PutProgress(owner, update("new-legacy", "s", 8, 10))
	clk.Advance(time.Minute)
	_, _ = db.PutProgress(legacy, update("new-legacy", "s", 2, 10))

	// same instant -> legacy stays
	_, _ = db.PutProgress(legacy, update("tie", "s", 3, 10))
	_, _ = db.PutProgress(owner, update("tie", "s", 9, 10))

	// only the user has it -> copied
	_, _ = db.PutProgress(owner, update("user-only", "s", 5, 10))
	_, _ = db.PutPrefs(owner, "user-only", userdata.PrefsPatch{FitMode: userdata.SetPatch("width")})
	_ = db.Settings().Put(owner, "theme", "dark")
	_ = db.ViewState().Put(owner, "lib", "list")

	res, err := db.RestoreOwner(legacy, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if res.Progress != 2 || res.Prefs != 1 || res.Settings != 1 || res.ViewState != 1 {
		t.Fatalf("restore counts = %+v", res)
	}
	for id, want := range map[string]int{"old-legacy": 8, "new-legacy": 2, "tie": 3, "user-only": 5} {
		if got, err := db.GetProgress(legacy, id); err != nil || got.LastPage != want {
			t.Errorf("%s: single-user page = %d (%v), want %d", id, got.LastPage, err, want)
		}
	}
	if v, ok, _ := db.Settings().Get(legacy, "theme"); !ok || v != "dark" {
		t.Errorf("settings not restored: %q %v", v, ok)
	}
	// The user's rows are untouched.
	if got, _ := db.GetProgress(owner, "tie"); got.LastPage != 9 {
		t.Errorf("restore altered the user's row: %+v", got)
	}
	if _, err := db.RestoreOwner(legacy, ""); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Errorf("empty sub = %v", err)
	}
}

func TestRecordLogin_andUserSubs(t *testing.T) {
	db, clk, _ := newDB(t)
	ctx := context.Background()
	if err := db.RecordLogin(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if err := db.RecordLogin(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	_, _ = db.PutProgress(asSub("bob"), update("b", "s", 1, 3))
	_, _ = db.PutPrefs(asSub("carol"), "b", userdata.PrefsPatch{FitMode: userdata.SetPatch("width")})
	subs, err := db.UserSubs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 || subs[0] != "alice" || subs[1] != "bob" || subs[2] != "carol" {
		t.Fatalf("UserSubs = %v", subs)
	}
	if err := db.RecordLogin(ctx, ""); !errors.Is(err, userdata.ErrInvalidArgument) {
		t.Fatalf("empty sub = %v", err)
	}
}

// The post-scan cleanup acts on the rows of the user whose context it runs
// under and on nobody else's (ruling E-74 §2.13, last bullet).
func TestPerUser_cleanupActsOnlyOnTheScopesUser(t *testing.T) {
	db, _, _ := newDB(t)
	alice, bob, legacy := asSub("alice"), asSub("bob"), context.Background()
	for _, ctx := range []context.Context{alice, bob, legacy} {
		if _, err := db.PutProgress(ctx, update("old", "s-old", 3, 10)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.PutPrefs(ctx, "old", userdata.PrefsPatch{FitMode: userdata.SetPatch("width")}); err != nil {
			t.Fatal(err)
		}
	}

	// reattach: old -> new for alice only
	rows := []userdata.ExportItem{{BookID: "new", SeriesID: "s-new", RootName: "r", BookPath: "n",
		LastPage: 3, PageCount: 10, StartedAt: 1, UpdatedAt: 5}}
	res, err := db.ApplyCarry(alice, userdata.Carry{
		Progress: []userdata.SplitMove{{OldBookID: "old", Rows: rows}},
		Prefs:    []userdata.PrefsMove{{OldBookID: "old", To: []string{"new"}}},
	})
	if err != nil || res.Progress.Retired != 1 || res.Prefs.Written != 1 {
		t.Fatalf("ApplyCarry = %+v, %v", res, err)
	}
	if _, err := db.GetProgress(alice, "new"); err != nil {
		t.Errorf("alice's progress did not move: %v", err)
	}
	if _, err := db.GetProgress(alice, "old"); !errors.Is(err, userdata.ErrNotFound) {
		t.Errorf("alice still has the old id: %v", err)
	}
	for name, ctx := range map[string]context.Context{"bob": bob, "single-user": legacy} {
		if _, err := db.GetProgress(ctx, "old"); err != nil {
			t.Errorf("%s lost the old row to alice's cleanup: %v", name, err)
		}
		if _, err := db.GetProgress(ctx, "new"); !errors.Is(err, userdata.ErrNotFound) {
			t.Errorf("%s received alice's moved row: %v", name, err)
		}
		if p, _ := db.GetPrefs(ctx, "old"); p.FitMode == nil {
			t.Errorf("%s lost the old prefs to alice's cleanup", name)
		}
	}

	// refile
	if n, err := db.RefileProgress(bob, []userdata.Refile{{BookID: "old", SeriesID: "s-moved"}}); err != nil || n != 1 {
		t.Fatalf("RefileProgress(bob) = %d, %v", n, err)
	}
	if got, _ := db.GetProgress(bob, "old"); got.SeriesID != "s-moved" {
		t.Errorf("bob's row not refiled: %+v", got)
	}
	if got, _ := db.GetProgress(legacy, "old"); got.SeriesID != "s-old" {
		t.Errorf("bob's refile reached the single-user row: %+v", got)
	}

	// purge
	if r, err := db.Purge(bob, userdata.PurgeSet{Books: []string{"old"}}); err != nil || r.Progress != 1 || r.Prefs != 1 {
		t.Fatalf("Purge(bob) = %+v, %v", r, err)
	}
	if _, err := db.GetProgress(legacy, "old"); err != nil {
		t.Errorf("bob's purge deleted the single-user row: %v", err)
	}
	if _, err := db.GetProgress(alice, "new"); err != nil {
		t.Errorf("bob's purge deleted alice's row: %v", err)
	}
}

// A file left at schema 3 gains the per-user tables on the next start and keeps
// every authored row (ruling E-74 §2.13: additive only).
func TestOpen_upgradeFromV3_addsTheUserTablesAndKeepsEveryAuthoredRow(t *testing.T) {
	dir := t.TempDir()
	clk := newClock()
	first := openIn(t, dir, clk)
	if _, err := first.PutProgress(context.Background(), update("b1", "s1", 5, 10)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	raw := openRaw(t, dir+"/user.db")
	for _, stmt := range []string{
		`DROP TABLE user_progress`, `DROP TABLE user_book_prefs`, `DROP TABLE user_settings`,
		`DROP TABLE user_view_state`, `DROP TABLE users`,
		`UPDATE meta SET value = '3' WHERE key = 'schema_version'`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = raw.Close()

	up := openIn(t, dir, clk)
	if v, err := up.SchemaVersion(context.Background()); err != nil || v != 4 {
		t.Fatalf("schema version after the upgrade = %d (%v), want 4", v, err)
	}
	if got, err := up.GetProgress(context.Background(), "b1"); err != nil || got.LastPage != 5 {
		t.Fatalf("the single-user row after the upgrade = %+v, %v", got, err)
	}
	if _, err := up.PutProgress(asSub("alice"), update("b1", "s1", 2, 10)); err != nil {
		t.Fatalf("writing the new table after the upgrade: %v", err)
	}
}
