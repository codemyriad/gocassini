package topics

// Word is one timed word of a transcript, the only transcript shape this
// package reads. Callers build it from the default words transcript of a
// meeting (internal/cassini/topics_source.go), so nothing here depends on the
// portable file format.
type Word struct {
	Text    string
	StartMS int64
	EndMS   int64
	Speaker string
}
