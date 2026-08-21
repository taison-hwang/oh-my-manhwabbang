// pixelbaseline — the gate that makes docs/e2e-shots/ mean something.
//
// # The hole this fills
//
// scripts/e2e.sh step 11 proves every shot() call fired. Nothing proved that
// what they drew is what they drew last time: docs/ui-shots/README.md opened,
// until session 22, by saying so in its own first heading. The reference set has
// gone stale four times — E-32, session 10, E-42, E-44 — and each time a person
// had to notice. Item `x` is that hole; `aj` and `bi` are two invalidations it
// let through.
//
// # Why a digest rather than the images
//
// The obvious answer is Playwright's toHaveScreenshot with committed PNGs, and
// it is closed by measurement rather than taste: the 121 shots are **263 MB** at
// the 2x DPR the four projects capture at (median 2.0 MB, largest 5.6 MB). No
// repository carries that per revision. A SHA-256 costs 80 bytes and, where the
// render is reproducible, is not an approximation of the comparison — it is the
// comparison.
//
// # Where the render is reproducible, measured rather than assumed
//
// It was assumed at first, on good grounds: the grain is feTurbulence with a
// fixed seed (tokens.css `--paper-grain`), the fixture is built with a fixed
// mtime and no rand (scripts/mkfixture), and re-photographing one server gave
// byte-identical frames across separate Chrome processes. Three full synthetic
// rounds said otherwise, and the shape of the disagreement is the reason this
// tool has an exclusion list instead of a tolerance:
//
//	76 shots   byte-identical in every round
//	34 shots   a 23px-tall strip of text differs, max channel delta 150-223 —
//	           the settings dialog's cache usage and scan log, and a relative
//	           time on the series and 이어보기 rows. These change every round
//	           because the product is telling the truth about a clock.
//	11 shots   the same content rendered with a max channel delta of 3-18 over
//	           a wide area — image decode and resampling variance, invisible to
//	           a reader and fatal to a byte comparison.
//
// A tolerance would have to be loose enough to pass the second group's 223,
// which is most of a colour change, so a tolerance is not a weaker version of
// this gate — it is a gate that would let through the thing it exists to catch.
// The 45 are excluded by name with the measured delta attached, and the tool
// prints how many it is *not* watching on every run. A gate that quietly covers
// 63 % while reading as total is HANDOFF §6.5's whole subject.
//
// Lifting either group is real follow-up work with a known shape: mask the
// clock-bearing rectangles for the 34, pin image decode for the 11. Neither is
// guessed at here.
//
// # Synthetic only, and why that is not a shortcut
//
// Two rounds write to the same directory: `make e2e-synthetic` (the hermetic
// fixture) and `make e2e` (the user's real collection). They do not produce the
// same pixels and never will — different series, different covers, different
// counts — and the real round's content does not exist on any other machine, so
// no committed manifest could describe it. The baseline is taken from the
// synthetic round and the real round is skipped **out loud**, because §6.5's
// rule is that a green tick has to say which round it belongs to.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// thumbWidth is the downsample target: wide enough to tell a layout change from
// a colour change by eye, small enough that the whole set is a couple of
// megabytes.
const thumbWidth = 320

// thumbQuality: the thumbnails are JPEG, and that is considered rather than
// default. Measured on one round's 121 shots, all at 320px:
//
//	PNG BestCompression   9.4 MB
//	JPEG q85              1.6 MB
//
// Lossless is the instinct and here it buys nothing: the exact comparison is the
// SHA-256 in manifest.json, and the thumbnail exists so a person can see *what*
// moved when that comparison goes red. Eight megabytes per revision for a
// property nothing reads is how a baseline gets deleted a year later.
const thumbQuality = 85

// contentDelta separates the two reasons a shot fails to reproduce. Below it,
// the same content rendered slightly differently (observed max 18); at or above
// it, different content (observed min 150). The gap between 18 and 150 is wide
// enough that this threshold is a label rather than a judgement — but it is
// still a threshold, so -classify prints the deltas it saw.
const contentDelta = 40

type manifest struct {
	// Round names which e2e round produced these shots. A manifest that does
	// not say is one whose next reader has to guess.
	Round string `json:"round"`
	Note  string `json:"note"`
	// Shots is the watched set: name -> SHA-256 of the PNG.
	Shots map[string]string `json:"shots"`
	// Excluded is the unwatched set: name -> why, with the measured delta. Not
	// a skip list — a record of what this gate does not cover, printed on every
	// run so the coverage claim stays honest.
	Excluded map[string]string `json:"excluded"`
}

type reference struct {
	CapturedAgainst string `json:"captured_against"`
	Note            string `json:"note"`
}

func main() {
	var (
		shotsDir = flag.String("shots", "docs/e2e-shots", "directory the round wrote its PNGs to")
		baseDir  = flag.String("baseline", "docs/pixel-baseline/synthetic", "committed baseline directory")
		round    = flag.String("round", "synthetic", "which e2e round produced -shots")
		update   = flag.Bool("update", false, "rewrite the digests from -shots, keeping the exclusion list")
		classify = flag.String("classify", "", "comma-separated archived round directories; re-derives the exclusion list from them")
		retaken  = flag.Bool("reference-retaken", false, "record that docs/ui-shots/built/ was recaptured from these renders")
	)
	flag.Parse()

	if *classify != "" {
		if err := classifyRounds(strings.Split(*classify, ","), *baseDir, *round); err != nil {
			fail("%v", err)
		}
		return
	}

	if *round != "synthetic" {
		fmt.Printf("pixelbaseline: skipped — round %q has no committed baseline (only the synthetic round is reproducible off this machine)\n", *round)
		return
	}

	shots, err := digestDir(*shotsDir)
	if err != nil {
		fail("reading %s: %v", *shotsDir, err)
	}
	if len(shots) == 0 {
		fail("no PNGs under %s — did step 11 run?", *shotsDir)
	}

	if *retaken {
		if err := writeReference(*baseDir, fingerprint(shots)); err != nil {
			fail("recording the reference capture: %v", err)
		}
		fmt.Printf("pixelbaseline: docs/ui-shots/built/ recorded as captured from these renders\n")
		return
	}

	if *update {
		prev, _ := readManifest(filepath.Join(*baseDir, "manifest.json"))
		excluded := map[string]string{}
		if prev != nil {
			excluded = prev.Excluded
		}
		if err := writeBaseline(*baseDir, *shotsDir, *round, shots, excluded); err != nil {
			fail("writing the baseline: %v", err)
		}
		watched := 0
		for name := range shots {
			if _, skip := excluded[name]; !skip {
				watched++
			}
		}
		fmt.Printf("pixelbaseline: baseline updated — %d watched, %d excluded, thumbnails in %s\n",
			watched, len(excluded), filepath.Join(*baseDir, "thumbs"))
		fmt.Printf("pixelbaseline: the review reference set in docs/ui-shots/built/ describes the renders you\n" +
			"  just replaced. Retake it (docs/ui-shots/README.md has the recipe) and then:\n\n" +
			"      go run ./scripts/pixelbaseline -reference-retaken\n\n")
		return
	}

	want, err := readManifest(filepath.Join(*baseDir, "manifest.json"))
	if err != nil {
		fail("reading the baseline: %v\n\n  There is no committed baseline yet. Build one from rounds you have\n  archived and reviewed:\n\n      go run ./scripts/pixelbaseline -classify /path/round1,/path/round2,/path/round3\n", err)
	}
	if want.Round != *round {
		fail("the baseline is for round %q but these shots are from %q", want.Round, *round)
	}

	changed, added, missing := compare(want, shots)
	if len(changed) == 0 && len(added) == 0 && len(missing) == 0 {
		fmt.Printf("pixelbaseline: %d of %d shots match the %s baseline byte for byte; %d not watched (see manifest.json \"excluded\")%s\n",
			len(want.Shots), len(shots), *round, len(want.Excluded), referenceNote(*baseDir, fingerprint(shots)))
		return
	}

	report("changed — these render differently than the baseline", changed)
	report("new — no baseline entry, so nothing decided whether to watch them", added)
	report("missing — the baseline has them, this round did not write them", missing)
	fmt.Printf("\n  The thumbnails in %s show what the baseline held.\n", filepath.Join(*baseDir, "thumbs"))
	fmt.Printf("  If the change is intended, review the new renders and then:\n\n      go run ./scripts/pixelbaseline -update\n\n")
	os.Exit(1)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pixelbaseline: "+format+"\n", args...)
	os.Exit(1)
}

func report(heading string, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Printf("\n%s (%d):\n", heading, len(names))
	for _, n := range names {
		fmt.Printf("    %s\n", n)
	}
}

func compare(want *manifest, got map[string]string) (changed, added, missing []string) {
	for name, sum := range got {
		if _, skip := want.Excluded[name]; skip {
			continue
		}
		prev, ok := want.Shots[name]
		switch {
		case !ok:
			added = append(added, name)
		case prev != sum:
			changed = append(changed, name)
		}
	}
	for name := range want.Shots {
		if _, ok := got[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(missing)
	return changed, added, missing
}

// classifyRounds derives the watched and excluded sets from archived rounds.
//
// The exclusion list is the one part of this gate that is a judgement, so it is
// made re-derivable rather than written down by whoever went first: point this
// at N rounds and it says which shots reproduced across all of them and, for the
// ones that did not, by how much and therefore why. Two rounds is the minimum
// and is not enough — a shot that varies one time in three looks stable in two.
func classifyRounds(dirs []string, baseDir, round string) error {
	if len(dirs) < 2 {
		return fmt.Errorf("-classify needs at least two rounds; three is the first count that can see a one-in-three flake")
	}
	if len(dirs) == 2 {
		fmt.Printf("pixelbaseline: WARNING — two rounds cannot distinguish a stable shot from one that\n" +
			"  varies less than half the time. Archive a third before trusting the watched set.\n\n")
	}
	base := dirs[0]
	baseDigests, err := digestDir(base)
	if err != nil {
		return fmt.Errorf("reading %s: %w", base, err)
	}
	excluded := map[string]string{}
	watched := map[string]string{}

	names := make([]string, 0, len(baseDigests))
	for name := range baseDigests {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		worst := 0
		stable := true
		for _, other := range dirs[1:] {
			otherSum, err := digestFile(filepath.Join(other, name))
			if err != nil {
				stable = false
				excluded[name] = fmt.Sprintf("not written by every round (%s)", filepath.Base(other))
				break
			}
			if otherSum == baseDigests[name] {
				continue
			}
			stable = false
			d, err := maxChannelDelta(filepath.Join(base, name), filepath.Join(other, name))
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if d > worst {
				worst = d
			}
		}
		switch {
		case stable:
			watched[name] = baseDigests[name]
		case excluded[name] != "":
			// already recorded above
		case worst >= contentDelta:
			excluded[name] = fmt.Sprintf(
				"content changes every round (max channel delta %d): a clock-bearing or size-bearing value is on this frame", worst)
		default:
			excluded[name] = fmt.Sprintf(
				"image decode / resampling variance (max channel delta %d): same content, different pixels", worst)
		}
	}

	if err := writeBaseline(baseDir, base, round, baseDigests, excluded); err != nil {
		return err
	}
	fmt.Printf("pixelbaseline: classified %d shots over %d rounds — %d watched, %d excluded\n",
		len(baseDigests), len(dirs), len(watched), len(excluded))
	content, raster, absent := 0, 0, 0
	for _, why := range excluded {
		switch {
		case strings.HasPrefix(why, "content changes"):
			content++
		case strings.HasPrefix(why, "image decode"):
			raster++
		default:
			absent++
		}
	}
	fmt.Printf("  excluded: %d carry a value that changes every round, %d carry decode variance, %d were not written by every round\n",
		content, raster, absent)
	return nil
}

func digestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func digestDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		sum, err := digestFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = sum
	}
	return out, nil
}

// maxChannelDelta is what separates "the same picture" from "a different one".
// A count of differing pixels would not: the decode variance touches a third of
// some frames and a changed digit touches a few thousand.
func maxChannelDelta(a, b string) (int, error) {
	ia, err := loadPNG(a)
	if err != nil {
		return 0, err
	}
	ib, err := loadPNG(b)
	if err != nil {
		return 0, err
	}
	if ia.Bounds() != ib.Bounds() {
		return 255, nil
	}
	worst := 0
	bounds := ia.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, _ := ia.At(x, y).RGBA()
			r2, g2, b2, _ := ib.At(x, y).RGBA()
			for _, d := range [3]int{
				abs(int(r1>>8) - int(r2>>8)),
				abs(int(g1>>8) - int(g2>>8)),
				abs(int(b1>>8) - int(b2>>8)),
			} {
				if d > worst {
					worst = d
				}
			}
		}
	}
	return worst, nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func readManifest(path string) (*manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if len(m.Shots) == 0 {
		return nil, fmt.Errorf("%s lists no watched shots", path)
	}
	if m.Excluded == nil {
		m.Excluded = map[string]string{}
	}
	return &m, nil
}

func writeBaseline(baseDir, shotsDir, round string, shots, excluded map[string]string) error {
	thumbs := filepath.Join(baseDir, "thumbs")
	// Start from empty so a shot that stopped being taken stops being carried;
	// otherwise the directory only grows and stops describing a round.
	if err := os.RemoveAll(thumbs); err != nil {
		return err
	}
	if err := os.MkdirAll(thumbs, 0o755); err != nil {
		return err
	}
	watched := map[string]string{}
	for name, sum := range shots {
		if _, skip := excluded[name]; skip {
			// No thumbnail for an unwatched shot: it would be a picture of one
			// arbitrary round, and a stale picture next to an exact digest is
			// how a reader comes to trust the wrong one.
			continue
		}
		watched[name] = sum
		thumb := strings.TrimSuffix(name, ".png") + ".jpg"
		if err := writeThumb(filepath.Join(shotsDir, name), filepath.Join(thumbs, thumb)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	m := manifest{
		Round: round,
		Note: "SHA-256 of every PNG the synthetic e2e round writes to docs/e2e-shots/ that was " +
			"measured reproducible across three full rounds. The comparison is exact, not sampled " +
			"and not tolerant. \"excluded\" lists what this gate does NOT watch and why; scripts/" +
			"pixelbaseline explains the measurement. Rebuild the exclusion list with -classify, " +
			"refresh the digests with -update.",
		Shots:    watched,
		Excluded: excluded,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(baseDir, "manifest.json"), append(data, '\n'), 0o644)
}

// fingerprint reduces a whole round to one value, so "have the renders moved
// since the reference set was taken" is a string comparison rather than a
// judgement someone has to remember to make.
func fingerprint(shots map[string]string) string {
	names := make([]string, 0, len(shots))
	for name := range shots {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%s %s\n", name, shots[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeReference(baseDir, fp string) error {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reference{
		CapturedAgainst: fp,
		Note: "The renders docs/ui-shots/built/v3-*.png was captured from. When the round's " +
			"fingerprint stops matching this, that reference set is provably stale — item `aj`. " +
			"Record a fresh capture with: go run ./scripts/pixelbaseline -reference-retaken",
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(baseDir, "reference.json"), append(data, '\n'), 0o644)
}

// referenceNote is item `aj`: the review reference set has gone stale four times
// and each time a person had to notice and write a paragraph about it.
//
// Deliberately a line of output and not a failure. docs/ui-shots/ is gitignored,
// so nothing here can confirm the set even exists on this machine, and failing a
// round over an artifact the repository does not carry would train the next
// author to pass -update without reading. What it can do is turn "is the
// reference stale" from something remembered into something derived.
func referenceNote(baseDir, fp string) string {
	data, err := os.ReadFile(filepath.Join(baseDir, "reference.json"))
	if err != nil {
		return " · docs/ui-shots/built/ has no recorded capture (item `aj`)"
	}
	var ref reference
	if err := json.Unmarshal(data, &ref); err != nil {
		return " · docs/ui-shots/built/'s capture record is unreadable (item `aj`)"
	}
	if ref.CapturedAgainst == fp {
		return " · docs/ui-shots/built/ was captured from these same renders"
	}
	return " · BUT docs/ui-shots/built/ was captured from different renders and is stale (item `aj`)"
}

// writeThumb box-filters src down to thumbWidth and writes it as JPEG.
//
// A box filter written out here rather than golang.org/x/image/draw: the module
// has no x/image dependency, and a resampler whose output is committed has to be
// reproducible for the same input on every machine and every Go version, which
// is easier to guarantee for twenty lines of integer averaging than for a
// library's chosen kernel.
func writeThumb(srcPath, dstPath string) error {
	src, err := loadPNG(srcPath)
	if err != nil {
		return err
	}
	b := src.Bounds()
	if b.Dx() <= thumbWidth {
		// Already small: keep the pixels rather than upscaling them.
		return encode(dstPath, src)
	}
	scale := float64(b.Dx()) / float64(thumbWidth)
	dstH := int(float64(b.Dy()) / scale)
	if dstH < 1 {
		dstH = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, thumbWidth, dstH))
	for y := 0; y < dstH; y++ {
		y0 := b.Min.Y + int(float64(y)*scale)
		y1 := b.Min.Y + int(float64(y+1)*scale)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < thumbWidth; x++ {
			x0 := b.Min.X + int(float64(x)*scale)
			x1 := b.Min.X + int(float64(x+1)*scale)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1 && sy < b.Max.Y; sy++ {
				for sx := x0; sx < x1 && sx < b.Max.X; sx++ {
					sr, sg, sb, sa := src.At(sx, sy).RGBA()
					r += uint64(sr >> 8)
					g += uint64(sg >> 8)
					bl += uint64(sb >> 8)
					a += uint64(sa >> 8)
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n)
			dst.Pix[i+1] = uint8(g / n)
			dst.Pix[i+2] = uint8(bl / n)
			dst.Pix[i+3] = uint8(a / n)
		}
	}
	return encode(dstPath, dst)
}

func encode(path string, img image.Image) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	// Deterministic for the same input: these files are committed, so an
	// encoder that varied would churn the repository on every -update even when
	// the renders had not moved.
	return jpeg.Encode(out, img, &jpeg.Options{Quality: thumbQuality})
}
