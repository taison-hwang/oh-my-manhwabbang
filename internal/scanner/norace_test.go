//go:build !race

package scanner

// raceEnabled is false in an ordinary build. See race_test.go.
const raceEnabled = false
