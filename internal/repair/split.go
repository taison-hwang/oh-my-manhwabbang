// Package repair puts reading progress back on the books it belongs to after
// the library changes what a book *is* or where it lives.
//
// # The failure this exists for
//
// Reading progress is keyed by book id, and a book id is a pure function of the
// root name and the root-relative path (internal/ids). That makes it survive an
// index rebuild, a moved library and a renamed machine. What it does not survive
// is the pair it is derived from changing under it. Two things did that here:
//
//   - **D-73 split containers.** The scanner learned to look inside an archive
//     for chapter directories, and 484 containers became 6,097 volumes. The
//     containers stopped being books: 23 rows.
//   - **Files were renamed.** A leading `[만화] ` tag was stripped from 28 paths
//     and one directory moved to another root. Same effect: 31 rows.
//
// Progress rows are never deleted for a missing book — that is deliberate
// (NFR-DAT-004: an unplugged drive must not destroy reading history) — so the
// rows survived, pointing at ids the index no longer has. The E-47 rollup
// LEFT JOINs them, gets a NULL page count, computes `MIN(last_page, 0)` = 0, and
// the shelf draws nothing. Measured: 21 series reading 0 %, 20 of them wrongly,
// every one of them also absent from 이어보기. `에버그린 01~23` was 531 pages
// into 768 and showed 0 %.
//
// **Rescanning does not fix either.** A scan derives ids from what is on disk
// now, so it reproduces the current id and never the old one. It reattaches
// progress only when a path comes back *unchanged*.
//
// # One shape for both causes
//
// Repairing either is the same two steps: find where the book went, then map the
// page. A rename is the one-book case of the page map, so there is no second
// code path — the walk over a container's volumes maps page P of a one-element
// partition to P.
//
// A split is a partition of the same page list in the same order, and the commit
// that introduced it says so in numbers (배틀로얄: 1,540 pages before, 1,540
// after). So page P of the old file is page P - start(v) + 1 of the volume v
// whose cumulative range contains P, and nothing about that depends on names,
// mtimes or heuristics.
//
// # What it refuses
//
// Four gates keep this arithmetic rather than hopeful. A row that fails any of
// them is left exactly as it is:
//
//   - the orphan's id must be BookID(root, book_path) — the id a whole file or
//     container has. A row that fails this was a *volume inside* a container
//     (NestedBookID folds in an inner path), and its page numbers are local, so
//     walking them against a partition would land somewhere false;
//   - exactly one candidate location may hold books. Zero is a file this machine
//     cannot find; more than one is a guess, and a guess here attaches one
//     reader's place in a book to a different book;
//   - the destination's page counts must sum to the orphan's own `page_count`
//     baseline. That column records how long the reader last agreed the file
//     was, so equality is the proof the destination is the same pages and not a
//     different edition at the same path;
//   - the page must fall inside that sum.
//
// On the live library all 54 repairable rows pass, with **zero** exceptions on
// the sum gate and **zero** ambiguous locations — the two that would have caught
// a wrong theory.
package repair

import (
	"fmt"
	"regexp"

	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/userdata"
)

// leadingTag matches the bracketed classifier this library's filenames used to
// carry — `[만화] `, `[강좌] `, `[라노벨] ` — at the head of the path.
//
// It is deliberately the *only* rename this package will undo, and it is
// anchored, non-greedy over one bracket pair, and applied to the whole
// root-relative path rather than per segment. A general similarity match would
// be the wrong tool at this stakes: attaching page 4,055 of somebody's reading
// to the wrong book is worse than leaving a row orphaned, and a fuzzy rule has
// no way to be sure. Every candidate it produces still has to pass the sum gate,
// so the rule proposes and the arithmetic disposes.
var leadingTag = regexp.MustCompile(`^\[[^\]]*\]\s*`)

// SkipReason says why one orphan was left alone. Every reason is a case where
// the repair would have had to guess.
type SkipReason string

const (
	// SkipNotAContainer: the orphan's id is not the id a whole file at that path
	// would have, so the row was a volume inside one and its pages are local.
	SkipNotAContainer SkipReason = "not-a-container"
	// SkipUnresolved: no candidate location holds any book. The file is not on
	// this machine under a name this package is willing to derive.
	SkipUnresolved SkipReason = "unresolved"
	// SkipAmbiguous: more than one candidate location holds books, so choosing
	// would be a guess.
	SkipAmbiguous SkipReason = "ambiguous"
	// SkipLengthMismatch: the destination does not sum to the baseline the row
	// carries, so it is not the same pages.
	SkipLengthMismatch SkipReason = "length-mismatch"
	// SkipPageOutOfRange: the recorded page is not inside the destination.
	SkipPageOutOfRange SkipReason = "page-out-of-range"
)

// Skip is one orphan the plan declines, with the reason and the numbers that
// produced it. Declines are returned rather than logged away: a repair that
// silently covers 20 of 23 rows and says "done" is the shape of check this
// codebase keeps getting wrong.
type Skip struct {
	BookID   string
	BookPath string
	Reason   SkipReason
	// LastPage and PageCount are the row's own; DestPages is what the chosen
	// destination sums to. DestPages is meaningless unless the reason is
	// SkipLengthMismatch or SkipPageOutOfRange.
	LastPage  int
	PageCount int
	DestPages int
}

func (s Skip) String() string {
	return fmt.Sprintf("%s (%s): %s [page %d of baseline %d, destination sums to %d]",
		s.BookPath, s.BookID, s.Reason, s.LastPage, s.PageCount, s.DestPages)
}

// Candidates lists every Location the plan may look at, so the caller can
// resolve them all in one batch instead of a query per orphan per rule.
//
// It is pure and total: it proposes, without knowing whether anything is there.
// The identity location comes first for every orphan, which is what makes an
// unmoved container (the D-73 case) cost no extra lookup.
func Candidates(orphans []index.SplitOrphan, roots []string) []index.Location {
	seen := make(map[index.Location]struct{})
	var out []index.Location
	add := func(l index.Location) {
		if _, dup := seen[l]; dup {
			return
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	for _, o := range orphans {
		for _, l := range candidatesAt(o.RootName, o.BookPath, roots) {
			add(l)
		}
	}
	return out
}

// CoverCandidates is Candidates for orphaned covers (ruling E-69 §2.11(b)): the
// same rules, so a cover and a progress row on the same old file look in the
// same places and BooksAt can answer both in one batch.
func CoverCandidates(orphans []index.CoverOrphan, roots []string) []index.Location {
	seen := make(map[index.Location]struct{})
	var out []index.Location
	for _, o := range orphans {
		for _, l := range candidatesAt(o.RootName, o.BookPath, roots) {
			if _, dup := seen[l]; dup {
				continue
			}
			seen[l] = struct{}{}
			out = append(out, l)
		}
	}
	return out
}

// candidatesAt is the rule set, in the order a reader would try them: the path
// as written, the path with its leading tag removed, and the same path under
// each other root.
func candidatesAt(root, bookPath string, roots []string) []index.Location {
	out := []index.Location{{RootName: root, RelPath: bookPath}}
	if stripped := leadingTag.ReplaceAllString(bookPath, ""); stripped != "" && stripped != bookPath {
		out = append(out, index.Location{RootName: root, RelPath: stripped})
		for _, r := range roots {
			if r != root {
				out = append(out, index.Location{RootName: r, RelPath: stripped})
			}
		}
	}
	for _, r := range roots {
		if r != root {
			out = append(out, index.Location{RootName: r, RelPath: bookPath})
		}
	}
	return out
}

// Plan turns orphaned progress rows into the moves that put them back.
//
// It is pure: no clock, no database, no ordering assumptions beyond the reading
// order the index already materialised in `ord`. `found` is what BooksAt
// answered for Candidates. The caller applies the result with
// userdata.DB.RepairSplit, which is where the one transaction and the
// newer-wins collision rule live.
func Plan(orphans []index.SplitOrphan, roots []string,
	found map[index.Location][]index.SplitVolume) (moves []userdata.SplitMove, skipped []Skip) {
	for _, o := range orphans {
		move, _, skip, ok := planOne(o, roots, found)
		if !ok {
			skipped = append(skipped, skip)
			continue
		}
		moves = append(moves, move)
	}
	return moves, skipped
}

// gate is E-48 §3, and it is the only place the gates are written. Progress
// (planOne) and a chosen cover (PlanCovers) both call it with their own row's
// identity, their own `page_count` baseline and their own page; neither has a
// copy of the conditions. A row that fails any gate is left exactly as it is.
//
// It returns the location gate 2 chose, the destination volumes in reading
// order, what they sum to, and the reason for a decline ("" when every gate
// passed). total is 0 unless the row got past gate 2, which is what
// Skip.DestPages has always reported.
func gate(bookID, root, bookPath string, baseline, page int, roots []string,
	found map[index.Location][]index.SplitVolume) (at index.Location, dest []index.SplitVolume, total int, reason SkipReason) {
	// Gate 1 — this row was a whole file, not a volume inside one.
	if ids.BookID(root, bookPath) != bookID {
		return index.Location{}, nil, 0, SkipNotAContainer
	}

	// Gate 2 — exactly one candidate location has anything.
	hits := 0
	for _, loc := range candidatesAt(root, bookPath, roots) {
		if vols, ok := found[loc]; ok && len(vols) > 0 {
			hits++
			if dest == nil {
				at, dest = loc, vols
			}
		}
	}
	switch {
	case hits == 0:
		return index.Location{}, nil, 0, SkipUnresolved
	case hits > 1:
		return index.Location{}, nil, 0, SkipAmbiguous
	}

	for _, v := range dest {
		total += v.PageCount
	}
	// Gate 3 — the destination is the same pages this row was written against.
	if total != baseline {
		return index.Location{}, nil, total, SkipLengthMismatch
	}
	// Gate 4 — the page is inside them.
	if page < 1 || page > total {
		return index.Location{}, nil, total, SkipPageOutOfRange
	}
	return at, dest, total, ""
}

// volumeAt is the page map: page P of the old file is page P - start(v) + 1 of
// the volume v whose cumulative range contains P. It returns v's index in dest
// and the local page. A zero-length volume is a book whose length is unknown
// (arch §4.11, a file that failed to open); it owns no page and is stepped
// over. ok is false only for a page past the last range. A page below 1 is
// gate 4's to refuse, and this does not repeat the check.
func volumeAt(dest []index.SplitVolume, page int) (i, local int, ok bool) {
	start := 1
	for i, v := range dest {
		if v.PageCount <= 0 {
			continue
		}
		end := start + v.PageCount - 1
		if page <= end {
			return i, page - start + 1, true
		}
		start = end + 1
	}
	return 0, 0, false
}

func planOne(o index.SplitOrphan, roots []string,
	found map[index.Location][]index.SplitVolume) (userdata.SplitMove, []index.SplitVolume, Skip, bool) {
	skip := Skip{BookID: o.BookID, BookPath: o.BookPath, LastPage: o.LastPage, PageCount: o.PageCount}

	loc, dest, total, reason := gate(o.BookID, o.RootName, o.BookPath, o.PageCount, o.LastPage, roots, found)
	skip.DestPages = total
	if reason != "" {
		skip.Reason = reason
		return userdata.SplitMove{}, nil, skip, false
	}

	move := userdata.SplitMove{OldBookID: o.BookID}

	// A completed file means every volume of it is finished. Expanding one row
	// into N is the only reading of `completed` that keeps 완독 true: the scope
	// is `count(completed = 1) >= book_count`, so leaving the other volumes
	// unwritten would move a finished series out of the 완독 shelf.
	if o.Completed {
		for _, v := range dest {
			if v.PageCount <= 0 {
				continue
			}
			move.Rows = append(move.Rows, row(o, v, loc, v.PageCount, v.PageCount, true))
		}
		return move, dest, Skip{}, true
	}

	// The volumes the reader passed through are finished, and saying so is not
	// generosity — it is the only way the number the reader sees survives the
	// move. E-47 counts a completed book at its whole length and a started one
	// at its last read page, so writing ONLY the volume the position lands in
	// would take `에버그린` from 531 pages of 768 to 3 of 768: the shelf would
	// read 0.4 % where it should read 69.1 %, which is a different wrong answer
	// than the 0 % this package was written to fix, not a repair. Marking them
	// is also exactly what PutProgress does on its own — reaching a book's last
	// page completes it — so this is the product's own rule applied to pages the
	// reader had already turned.
	//
	// Σ(completed volumes) + local == the original page, by construction. That
	// identity is what the tests assert, because it is the property the reader
	// actually sees. For a rename the partition has one element and the identity
	// is trivially the page itself.
	at, local, ok := volumeAt(dest, o.LastPage)
	if !ok {
		// Unreachable while gate 4 holds: the ranges cover [1, total] with no
		// holes, because `ord` is dense and every page belongs to one volume.
		skip.Reason = SkipPageOutOfRange
		return userdata.SplitMove{}, nil, skip, false
	}
	for _, v := range dest[:at] {
		if v.PageCount <= 0 {
			// Owns no page, so it cannot be passed through and must not be
			// stamped finished.
			continue
		}
		move.Rows = append(move.Rows, row(o, v, loc, v.PageCount, v.PageCount, true))
	}
	v := dest[at]
	move.Rows = append(move.Rows, row(o, v, loc, local, v.PageCount, local >= v.PageCount))
	return move, dest, Skip{}, true
}

// PlanCovers is Plan for chosen covers (ruling E-69 §2.11(b)). Each orphaned
// cover goes through the same gate as a progress row, against its own
// `page_count` baseline and its own page; one that fails any gate is declined
// and left for the purge (§2.11(d)). One that passes moves to the volume whose
// range holds its page, at the local page, with the region untouched and the
// volume's length as its new baseline.
//
// Identity is the destination's: its series, and the root and path of the
// location the gate chose — not the orphan's. Unlike a progress row (see row),
// nothing rewrites a cover's root_name and book_path when the reader next opens
// the book, and they are the purge's evidence of where the book was
// (§2.11(d)): a cover reattached from root A to a book in root B but still
// filed under A would be deleted by a walk of A while B sat unwalked. The
// reader's own updated_at is kept.
func PlanCovers(orphans []index.CoverOrphan, roots []string,
	found map[index.Location][]index.SplitVolume) (moves []userdata.CoverMove, skipped []Skip) {
	for _, o := range orphans {
		skip := Skip{BookID: o.BookID, BookPath: o.BookPath, LastPage: o.Page, PageCount: o.PageCount}
		loc, dest, total, reason := gate(o.BookID, o.RootName, o.BookPath, o.PageCount, o.Page, roots, found)
		skip.DestPages = total
		if reason == "" {
			at, local, ok := volumeAt(dest, o.Page)
			if ok {
				v := dest[at]
				moves = append(moves, userdata.CoverMove{OldBookID: o.BookID, Row: userdata.Cover{
					BookID: v.BookID, SeriesID: v.SeriesID, RootName: loc.RootName, BookPath: loc.RelPath,
					Page: local, PageCount: v.PageCount,
					X: o.X, Y: o.Y, W: o.W, H: o.H, UpdatedAt: o.UpdatedAt,
				}})
				continue
			}
			reason = SkipPageOutOfRange
		}
		skip.Reason = reason
		skipped = append(skipped, skip)
	}
	return moves, skipped
}

// PlanSplitPrefs carries the container's book_prefs onto its volumes (ruling
// E-69 §2.12) — only for an orphan whose PROGRESS row passed the gate, because
// prefs have no length of their own and cannot prove the destination is the
// same pages. The copy goes to every volume, unstarted ones included (the
// reader's direction was for the whole file), and only to volumes without a row
// of their own (PrefsMove.OnlyIfAbsent). The container's row is kept: §2.12
// copies ("원본은 남긴다"), because prefs cannot prove the destination on their
// own and this runs at start-up too, where nothing is deleted (E-50 §2).
func PlanSplitPrefs(orphans []index.SplitOrphan, roots []string,
	found map[index.Location][]index.SplitVolume) []userdata.PrefsMove {
	var out []userdata.PrefsMove
	for _, o := range orphans {
		_, dest, _, ok := planOne(o, roots, found)
		if !ok {
			continue
		}
		to := make([]string, 0, len(dest))
		for _, v := range dest {
			to = append(to, v.BookID)
		}
		out = append(out, userdata.PrefsMove{OldBookID: o.BookID, To: to, OnlyIfAbsent: true})
	}
	return out
}

// row builds one destination progress row. started_at and updated_at come from
// the orphan unchanged: when the reader started the book and when they last
// touched it are the only times anyone knows, and inventing new ones would put
// fiction in the column 이어보기 sorts by — a repair that stamped `now` would
// jump fifty series to the front of the shelf.
//
// `SeriesID` is the destination's, never the orphan's: a renamed file belongs to
// a renamed series, and both ids are hashes of a path. Keeping the old one files
// the reader's place under a series the index no longer has — the row resolves
// to a book, so 이어보기 shows it, while the shelf percentage and the 읽는 중
// filter group by series_id and cannot see it. Measured on the live library
// after a first cut of this package shipped without it: 27 rows reachable and
// invisible at once.
//
// Source columns follow the destination identity (E-69 §2.14).
func row(o index.SplitOrphan, v index.SplitVolume, loc index.Location, lastPage, pageCount int, completed bool) userdata.ExportItem {
	return userdata.ExportItem{
		BookID:    v.BookID,
		SeriesID:  v.SeriesID,
		RootName:  loc.RootName,
		BookPath:  loc.RelPath,
		LastPage:  lastPage,
		PageCount: pageCount,
		Completed: completed,
		StartedAt: o.StartedAt,
		UpdatedAt: o.UpdatedAt,
	}
}

// RelocationSkip is one relocation the plan declines. Same reasoning as [Skip]:
// a repair that acts on nine of ten pairings and reports the nine is a check
// that watches the part that works.
type RelocationSkip struct {
	OldBookID string
	NewBookID string
	Reason    SkipReason
	PageCount int
	NewPages  int
}

// relocationReason is the check a proven move's length has to pass against a
// row's own baseline: the content hash is (size, mtime), so a genuine move
// cannot change the length, and the row's page has to be inside it. "" means
// the pairing agrees with the row.
func relocationReason(baseline, page, newPages int) SkipReason {
	if newPages != baseline {
		return SkipLengthMismatch
	}
	if page < 1 || page > newPages {
		return SkipPageOutOfRange
	}
	return ""
}

// PlanRelocations turns the moves a scan *proved* — same content, new id — into
// progress writes, for those old ids a reader had actually opened.
//
// This is the mechanism the name rules in Candidates are a fallback for, and it
// is better than them in the way that matters: it is evidence rather than
// inference. The scan saw both halves of the move in one transaction and paired
// them on a content hash with no path in it, so nothing here has an opinion
// about how anybody names their files.
//
// It only knows about moves the scan could still see. A file renamed before this
// code existed had its old row swept in an earlier scan, and the pairing died
// with it; those rows stay orphaned and are the reason Candidates exists at all.
//
// `existing` is the progress rows for the old ids, as userdata.GetProgressMany
// returns them. An old id nobody ever opened is not in it and produces nothing —
// which is the point: without that lookup a relocation would write a reading
// position for a book that was never read.
func PlanRelocations(relocs []index.Relocation,
	existing map[string]userdata.Progress) (moves []userdata.SplitMove, skipped []RelocationSkip) {
	for _, rl := range relocs {
		p, ok := existing[rl.OldBookID]
		if !ok {
			continue // never opened; there is nothing to carry
		}
		// The content hash is (size, mtime), so a genuine move cannot change the
		// length. A disagreement means the pairing is not what it claims, and the
		// cheapest moment to find that out is before writing a page number.
		if reason := relocationReason(p.PageCount, p.LastPage, rl.NewPageCount); reason != "" {
			skipped = append(skipped, RelocationSkip{OldBookID: rl.OldBookID, NewBookID: rl.NewBookID,
				Reason: reason, PageCount: p.PageCount, NewPages: rl.NewPageCount})
			continue
		}
		moves = append(moves, userdata.SplitMove{
			OldBookID: rl.OldBookID,
			Rows: []userdata.ExportItem{{
				BookID:    rl.NewBookID,
				SeriesID:  rl.NewSeriesID,
				RootName:  rl.NewRootName,
				BookPath:  rl.NewRelPath,
				LastPage:  p.LastPage,
				PageCount: rl.NewPageCount,
				Completed: p.Completed,
				StartedAt: p.StartedAt,
				UpdatedAt: p.UpdatedAt,
			}},
		})
	}
	return moves, skipped
}

// RelocationIDs is the old-id list to look progress up by.
func RelocationIDs(relocs []index.Relocation) []string {
	out := make([]string, 0, len(relocs))
	for _, r := range relocs {
		out = append(out, r.OldBookID)
	}
	return out
}

// PlanCarry is everything a scan's proven moves carry (ruling E-49, E-69
// §2.11(a)(e), §2.12): the progress rows PlanRelocations plans, plus the chosen
// covers, the book_prefs and the series_prefs — for every pairing, whether or
// not anyone read the book. Restricting the carry to read books is exactly the
// defect §2.12 records: a direction set on a book nobody had turned a page of
// stayed on an id that is no book.
//
// # One gate: the content proof
//
// §2.11: "(a)의 관문은 `content_version` 증명 하나다. progress 행의 길이
// 불일치로 이관이 거절되는 것은 progress에만 걸리고, 표지·prefs·시리즈 1:1 판단을
// 막지 않는다." So:
//
//   - progress is PlanRelocations, unchanged — its length check refuses the
//     progress row and nothing else;
//   - a chosen cover always moves, page, region and page_count as stored, under
//     the new identity. Its own baseline is not checked here: the same content
//     is the same pages, and a baseline that disagrees only says the row is
//     older than the file, which §2.5 keeps rather than repairs. The baseline
//     check belongs to the split reattach (§2.11(b), PlanCovers);
//   - book_prefs always move (newer-wins at the destination);
//   - the series-level 1:1 is judged over every pairing, refused progress
//     included (planSeriesCarry).
//
// `progress` and `covers` are the rows filed under the old ids
// (userdata.GetProgressMany, GetCovers over RelocationIDs). `seriesNow` is
// index.SeriesAt over every OldSeriesID and NewSeriesID: which series the index
// still holds after the scan, and where.
func PlanCarry(relocs []index.Relocation, progress map[string]userdata.Progress,
	covers map[string]userdata.Cover, seriesNow map[string]index.Location) (userdata.Carry, []RelocationSkip) {
	var c userdata.Carry
	var skipped []RelocationSkip
	c.Progress, skipped = PlanRelocations(relocs, progress)

	for _, rl := range relocs {
		if cv, chosen := covers[rl.OldBookID]; chosen {
			row := cv
			row.BookID, row.SeriesID = rl.NewBookID, rl.NewSeriesID
			row.RootName, row.BookPath = rl.NewRootName, rl.NewRelPath
			c.Covers = append(c.Covers, userdata.CoverMove{OldBookID: rl.OldBookID, Row: row})
		}
		c.Prefs = append(c.Prefs, userdata.PrefsMove{OldBookID: rl.OldBookID, To: []string{rl.NewBookID}})
	}
	c.Series = planSeriesCarry(relocs, seriesNow)
	return c, skipped
}

// planSeriesCarry is E-69 §2.11(e). The scan proves moves book by book, so a
// series' direction moves only when the books say the series moved as a whole:
//
//   - the old series is no longer in the index after the scan;
//   - every pairing out of it went to one new series — every pairing, a
//     book whose progress row PlanRelocations refused included: the pairing
//     is proved by content, and that refusal is about the progress row only;
//   - and that new series received pairings from no other old series. The
//     ruling states the first two; the third is E-49 §3's "1:1 on both sides"
//     at series level — two old series merging into one is a guess about whose
//     direction wins, and it is not made.
//
// Anything else is left alone; the purge deletes the old row if its root was
// walked end to end and the series is gone.
func planSeriesCarry(relocs []index.Relocation, seriesNow map[string]index.Location) []userdata.SeriesPrefsMove {
	bySource := map[string]map[string]struct{}{}
	byDest := map[string]map[string]struct{}{}
	var order []string
	for _, rl := range relocs {
		if rl.OldSeriesID == "" || rl.NewSeriesID == "" {
			continue
		}
		if bySource[rl.OldSeriesID] == nil {
			bySource[rl.OldSeriesID] = map[string]struct{}{}
			order = append(order, rl.OldSeriesID)
		}
		bySource[rl.OldSeriesID][rl.NewSeriesID] = struct{}{}
		if byDest[rl.NewSeriesID] == nil {
			byDest[rl.NewSeriesID] = map[string]struct{}{}
		}
		byDest[rl.NewSeriesID][rl.OldSeriesID] = struct{}{}
	}

	var out []userdata.SeriesPrefsMove
	for _, old := range order {
		to := bySource[old]
		if len(to) != 1 {
			continue
		}
		var dest string
		for d := range to {
			dest = d
		}
		if dest == old {
			continue
		}
		if _, still := seriesNow[old]; still {
			continue
		}
		if len(byDest[dest]) != 1 {
			continue
		}
		loc, ok := seriesNow[dest]
		if !ok {
			continue
		}
		out = append(out, userdata.SeriesPrefsMove{
			OldSeriesID: old, NewSeriesID: dest, RootName: loc.RootName, SeriesPath: loc.RelPath,
		})
	}
	return out
}

// RelocationSeriesIDs is the series-id list PlanCarry's seriesNow is looked up
// by: every old and new series a pairing names.
func RelocationSeriesIDs(relocs []index.Relocation) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range relocs {
		for _, id := range []string{r.OldSeriesID, r.NewSeriesID} {
			if _, dup := seen[id]; id == "" || dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}
