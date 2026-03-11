package main

import (
	"fmt"
	"gocassini/internal/inspect"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <meeting.mkv|meeting.opus|session.json|session-dir|archive.csr>\n", os.Args[0])
		os.Exit(2)
	}
	if err := inspect.InspectPath(os.Stdout, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
