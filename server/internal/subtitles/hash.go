package subtitles

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// hashChunk is how much of each end of the file the moviehash reads.
const hashChunk = 64 << 10

// RangeReader reads n bytes of the playing file from off, and says how
// big the whole file is. The stream proxy implements it with one Range
// request, so the hash costs two small reads however big the file is.
type RangeReader func(ctx context.Context, off int64, n int) (data []byte, size int64, err error)

// ErrTooSmall is a file too short for a moviehash (under 128 KB).
var ErrTooSmall = errors.New("file too small for a moviehash")

// MovieHash is OpenSubtitles' hash of a file: its size plus every 64-bit
// little-endian word of its first and last 64 KB, wrapping at 2^64, as 16
// hex digits. A subtitle uploaded with the same hash was timed against
// this very file, which is why a match ranks first.
func MovieHash(ctx context.Context, read RangeReader) (string, error) {
	head, size, err := read(ctx, 0, hashChunk)
	if err != nil {
		return "", err
	}
	if size < 2*hashChunk {
		return "", ErrTooSmall
	}
	tail, _, err := read(ctx, size-hashChunk, hashChunk)
	if err != nil {
		return "", err
	}
	sum := uint64(size)
	for _, b := range [][]byte{head, tail} {
		if len(b) != hashChunk {
			return "", fmt.Errorf("moviehash: read %d bytes, wanted %d", len(b), hashChunk)
		}
		for i := 0; i < hashChunk; i += 8 {
			sum += binary.LittleEndian.Uint64(b[i:])
		}
	}
	return fmt.Sprintf("%016x", sum), nil
}
