package transcribe

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// EncoderProvenance is what the encoder's own ONNX metadata says it is. The
// model id names what Cassini asked for; these name the export that actually
// loaded, which is what sherpa keys its frontend selection on (url) and where
// a Cassini export records its weights and toolchain (comment).
type EncoderProvenance struct {
	URL     string `json:"url,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// maxEncoderCommentBytes bounds the comment copied into every manifest.
const maxEncoderCommentBytes = 2048

// encoderProvenance reads url and comment from an encoder's metadata, or nil
// when the file has neither or cannot be read. It is a note, never a reason
// to fail a build.
func encoderProvenance(path string) *EncoderProvenance {
	if path == "" {
		return nil
	}
	props, err := readONNXMetadata(path)
	if err != nil {
		return nil
	}
	p := &EncoderProvenance{URL: props["url"], Comment: props["comment"]}
	if len(p.Comment) > maxEncoderCommentBytes {
		p.Comment = p.Comment[:maxEncoderCommentBytes]
	}
	if p.URL == "" && p.Comment == "" {
		return nil
	}
	return p
}

// readONNXMetadata returns the metadata_props of an ONNX ModelProto. It walks
// only the top-level fields and seeks past the rest, so the graph and any
// embedded weights (hundreds of MB for an int8 encoder) are never read.
func readONNXMetadata(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	props := map[string]string{}
	var offset int64
	r := bufio.NewReader(f)
	for offset < size {
		key, n, err := readUvarint(r)
		if err != nil {
			return nil, err
		}
		offset += int64(n)
		field, wire := key>>3, key&7
		switch wire {
		case 0:
			_, n, err := readUvarint(r)
			if err != nil {
				return nil, err
			}
			offset += int64(n)
			continue
		case 1, 5:
			skip := int64(8)
			if wire == 5 {
				skip = 4
			}
			if _, err := r.Discard(int(skip)); err != nil {
				return nil, err
			}
			offset += skip
			continue
		case 2:
		default:
			return nil, fmt.Errorf("%s: unsupported protobuf wire type %d", path, wire)
		}
		length, n, err := readUvarint(r)
		if err != nil {
			return nil, err
		}
		offset += int64(n)
		if length > uint64(size-offset) {
			return nil, fmt.Errorf("%s: field %d overruns the file", path, field)
		}
		if field != 14 { // ModelProto.metadata_props
			offset += int64(length)
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return nil, err
			}
			r.Reset(f)
			continue
		}
		if length > 1<<20 {
			return nil, fmt.Errorf("%s: metadata entry of %d bytes", path, length)
		}
		entry := make([]byte, length)
		if _, err := io.ReadFull(r, entry); err != nil {
			return nil, err
		}
		offset += int64(length)
		if k, v, ok := parseStringStringEntry(entry); ok {
			props[k] = v
		}
	}
	return props, nil
}

// parseStringStringEntry decodes a StringStringEntryProto {key=1, value=2}.
func parseStringStringEntry(b []byte) (key, value string, ok bool) {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag&7 != 2 {
			return "", "", false
		}
		b = b[n:]
		length, n := binary.Uvarint(b)
		if n <= 0 || length > uint64(len(b)-n) {
			return "", "", false
		}
		s := string(b[n : n+int(length)])
		b = b[n+int(length):]
		switch tag >> 3 {
		case 1:
			key = s
		case 2:
			value = s
		}
	}
	return key, value, key != ""
}

func readUvarint(r io.ByteReader) (uint64, int, error) {
	var x uint64
	for i := 0; i < binary.MaxVarintLen64; i++ {
		c, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && i > 0 {
				err = io.ErrUnexpectedEOF
			}
			return 0, i, err
		}
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1, nil
		}
	}
	return 0, binary.MaxVarintLen64, errors.New("varint overflows 64 bits")
}
