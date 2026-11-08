package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"shelf/internal/principal"
	"shelf/internal/userdata"
)

func TestRunAuth_usageErrors(t *testing.T) {
	for _, argv := range [][]string{
		{},
		{"unknown"},
		{"restore-owner"},
		{"restore-owner", "--sub", ""},
		{"restore-owner", "--sub", "x", "extra"},
	} {
		var out, errb bytes.Buffer
		if code := runAuth(argv, &out, &errb); code != exitFailure {
			t.Errorf("runAuth(%v) = %d, want %d", argv, code, exitFailure)
		}
		if !strings.Contains(errb.String(), "usage: shelf auth restore-owner") {
			t.Errorf("runAuth(%v) printed no usage: %q", argv, errb.String())
		}
	}
}

// restoreOwner opens the file by path, copies the user's newer rows over the
// single-user tables and leaves the user's rows alone.
func TestRestoreOwner_copiesTheUsersRowsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.db")
	ctx := context.Background()
	db, err := userdata.Open(ctx, userdata.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	owner := principal.With(ctx, principal.Principal{Sub: "owner"})
	if _, err := db.PutProgress(owner, userdata.ProgressUpdate{
		BookID: "b1", SeriesID: "s1", RootName: "r", BookPath: "p", Page: 4, PageCount: 9,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := restoreOwner(ctx, path, "owner")
	if err != nil || res.Progress != 1 {
		t.Fatalf("restoreOwner = %+v, %v", res, err)
	}
	db, err = userdata.Open(ctx, userdata.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, err := db.GetProgress(ctx, "b1"); err != nil || got.LastPage != 4 {
		t.Fatalf("single-user row after restore = %+v, %v", got, err)
	}
}

// A sub with no rows is a typo or the wrong database, not an empty restore: the
// tool refuses, writes nothing, and the error is the one runAuth turns into the
// "no data" message and a non-zero exit.
func TestRestoreOwner_unknownSubWritesNothingAndFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.db")
	ctx := context.Background()
	db, err := userdata.Open(ctx, userdata.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutProgress(ctx, userdata.ProgressUpdate{
		BookID: "b0", SeriesID: "s", RootName: "r", BookPath: "p", Page: 1, PageCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutProgress(principal.With(ctx, principal.Principal{Sub: "owner"}), userdata.ProgressUpdate{
		BookID: "b1", SeriesID: "s", RootName: "r", BookPath: "p", Page: 2, PageCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if _, err := restoreOwner(ctx, path, "ownr"); !errors.Is(err, userdata.ErrNoUserData) {
		t.Fatalf("restoreOwner(typo) = %v, want ErrNoUserData", err)
	}
	db, err = userdata.Open(ctx, userdata.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.GetProgress(ctx, "b1"); !errors.Is(err, userdata.ErrNotFound) {
		t.Fatalf("a refused restore wrote to the single-user table: %v", err)
	}
}
