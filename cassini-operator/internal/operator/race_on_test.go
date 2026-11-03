//go:build race

package operator

// raceEnabled: the race detector slows tight loops many times over, so
// timing bounds do not hold under it.
const raceEnabled = true
