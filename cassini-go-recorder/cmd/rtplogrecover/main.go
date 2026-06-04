// Command rtplogrecover remuxes a captured VP8 .rtplog into an IVF file the
// way the production depacketizer SHOULD: packets sorted by extended RTP
// sequence number, original RTP timestamps preserved. Used to demonstrate
// that the smeared video in recording.mkv is a remux artifact (arrival-order
// + recv-time timestamp rewriting), not unrecoverable network loss.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"

	"gocassini/pkg/core/store"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: rtplogrecover <stream.rtplog> <out.ivf>")
		os.Exit(2)
	}
	r, err := store.OpenReader(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer r.Close()

	type entry struct {
		ext int64
		pkt *rtp.Packet
	}
	var entries []entry
	var haveFirst bool
	var highest uint16
	var highestExt int64
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			break
		}
		if rec.Kind != store.KindRTP {
			continue
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(rec.WireBytes); err != nil {
			continue
		}
		if !haveFirst {
			haveFirst = true
			highest = pkt.SequenceNumber
			highestExt = int64(pkt.SequenceNumber)
			entries = append(entries, entry{highestExt, pkt})
			continue
		}
		delta := int16(pkt.SequenceNumber - highest) // wrap-aware
		ext := highestExt + int64(delta)
		if delta > 0 {
			highest = pkt.SequenceNumber
			highestExt = ext
		}
		entries = append(entries, entry{ext, pkt})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ext < entries[j].ext })
	// drop duplicates (keep first occurrence per seq)
	out := entries[:0]
	var lastExt int64 = -1 << 62
	for _, e := range entries {
		if e.ext == lastExt {
			continue
		}
		lastExt = e.ext
		out = append(out, e)
	}

	w, err := ivfwriter.New(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ivf:", err)
		os.Exit(1)
	}
	for _, e := range out {
		if err := w.WriteRTP(e.pkt); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}
	if err := w.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "close:", err)
		os.Exit(1)
	}
	fmt.Printf("recovered %d packets (deduped from %d) -> %s\n", len(out), len(entries), os.Args[2])
}
