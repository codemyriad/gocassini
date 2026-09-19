package annotations

import (
	"crypto/rand"
	"fmt"
	"math"
	"slices"
)

// Mutate finalizes a validated batch into the next complete snapshot.
func Mutate(current *Annotations, ops []Op, duration int64, audio, namespace string, stamp Stamp) (Outcome, error) {
	return MutateBatch(current, Batch{Ops: ops}, duration, audio, namespace, stamp)
}

func MutateBatch(current *Annotations, batch Batch, duration int64, audio, namespace string, stamp Stamp) (Outcome, error) {
	unresolved := current != nil && len(current.Items) > 0 && !current.Resolved(audio)
	if unresolved {
		if slices.ContainsFunc(batch.Ops, func(op Op) bool { return op.Op == "mark" }) {
			return Outcome{}, fail(5, "marks are bound to different audio; remove or relabel them before adding marks")
		}
		duration = math.MaxInt64
	}
	outcome, err := ApplyBatch(current, batch, duration, stamp)
	if err != nil || !outcome.Changed {
		return outcome, err
	}
	doc := outcome.Doc
	doc.Format = AnnotationsFormatV1
	doc.Revision = 1
	doc.AudioOpusSHA256 = audio
	doc.TagNamespace = namespace
	if current != nil {
		doc.Revision = current.Revision + 1
		doc.TagNamespace = current.TagNamespace
		if unresolved && len(doc.Items) > 0 {
			doc.AudioOpusSHA256 = current.AudioOpusSHA256
		}
	}
	if doc.TagNamespace == "" {
		doc.TagNamespace, err = NewNamespace()
		if err != nil {
			return Outcome{}, err
		}
	}
	if err := ValidateAnnotations(doc, duration); err != nil {
		return Outcome{}, fail(4, "invalid annotations: %v", err)
	}
	return outcome, nil
}

func NewNamespace() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
