package main

import (
	"bytes"
	"context"
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
