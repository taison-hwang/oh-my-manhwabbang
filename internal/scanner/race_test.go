//go:build race

package scanner

// raceEnabled is true in a `-race` build.
//
// It exists for exactly one reason: the FR-IDX-003 / NFR-PRF-004 measurement is
// a *ratio* between two timings, and under the race detector both sides are
// dominated by shadow-memory bookkeeping rather than by the work being compared.
// Asserting a speed-up there would measure the detector, not the scanner — and
// it would put a 26 s fixture into `make test`, which runs the whole repo with
// -race on every commit.
const raceEnabled = true
