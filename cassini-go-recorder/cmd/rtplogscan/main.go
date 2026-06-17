// Command rtplogscan reports RTP sequence-number continuity for a captured
// .rtplog stream: total packets, gaps (lost packets never seen), and whether
// any gap was later backfilled by a retransmission (out-of-order arrival).
// Used to verify whether NACK-based loss recovery is effective end-to-end.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/pion/rtp"

	"gocassini/pkg/core/store"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: rtplogscan <stream.rtplog>")
		os.Exit(2)
	}
	r, err := store.OpenReader(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer r.Close()
	hdr := r.Header()
	fmt.Printf("stream %s codec=%s clock=%d\n", hdr.Stream.StreamID, hdr.Stream.Codec, hdr.Stream.ClockRate)

	var (
		total, reordered, dupes int
		haveFirst               bool
		startMono               uint64
		highest                 uint16
		highestExt              int64 // extended highest seq
		seen                    = map[int64]bool{}
		gapsAt                  []float64
		lostTotal               int64
	)
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
		var pkt rtp.Packet
		if err := pkt.Unmarshal(rec.WireBytes); err != nil {
			continue
		}
		total++
		if !haveFirst {
			haveFirst = true
			startMono = rec.RecvMonoNS
			highest = pkt.SequenceNumber
			highestExt = int64(pkt.SequenceNumber)
			seen[highestExt] = true
			continue
		}
		delta := int16(pkt.SequenceNumber - highest) // wrap-aware
		ext := highestExt + int64(delta)
		tSec := float64(rec.RecvMonoNS-startMono) / 1e9
		switch {
		case delta == 1:
			highest = pkt.SequenceNumber
			highestExt = ext
		case delta > 1:
			// jump forward: delta-1 packets currently missing
			lostTotal += int64(delta - 1)
			if len(gapsAt) < 2000 {
				gapsAt = append(gapsAt, tSec)
			}
			highest = pkt.SequenceNumber
			highestExt = ext
		default: // late or duplicate packet
			if seen[ext] {
				dupes++
			} else {
				reordered++ // a gap member arriving late = retransmission/reorder
				lostTotal--
			}
		}
		seen[ext] = true
	}
	fmt.Printf("rtp packets: %d\n", total)
	fmt.Printf("gap events (forward jumps): %d, packets never recovered: %d\n", len(gapsAt), lostTotal)
	fmt.Printf("late arrivals filling gaps (reorder/retransmit): %d, duplicates: %d\n", reordered, dupes)
	if len(gapsAt) > 0 {
		fmt.Printf("gap times (s since stream start): ")
		for i, t := range gapsAt {
			if i >= 40 {
				fmt.Printf("… (+%d more)", len(gapsAt)-40)
				break
			}
			fmt.Printf("%.1f ", t)
		}
		fmt.Println()
	}
}
