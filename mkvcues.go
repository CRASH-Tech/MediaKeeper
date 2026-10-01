package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

// The times of a Matroska file's video key frames, from its index (Cues),
// which tools like mkvmerge write at the end of the file: a few kilobytes
// to read instead of the whole film. A copied video track can only be cut
// at key frames, so these are where the segments of its HLS playlist begin.

// EBML element IDs, with their length marker bits, as in the specification.
const (
	mkvSegment       = 0x18538067
	mkvSeekHead      = 0x114D9B74
	mkvSeek          = 0x4DBB
	mkvSeekID        = 0x53AB
	mkvSeekPosition  = 0x53AC
	mkvInfo          = 0x1549A966
	mkvTimecodeScale = 0x2AD7B1
	mkvTracks        = 0x1654AE6B
	mkvTrackEntry    = 0xAE
	mkvTrackNumber   = 0xD7
	mkvTrackType     = 0x83
	mkvCues          = 0x1C53BB6B
	mkvCuePoint      = 0xBB
	mkvCueTime       = 0xB3
	mkvCueTrackPos   = 0xB7
	mkvCueTrack      = 0xF7
	mkvCluster       = 0x1F43B675
)

var errNoCues = errors.New("the file has no index of its key frames")

// mkvKeyframes returns the times (seconds, ascending) of the video key
// frames listed in the file's index.
func mkvKeyframes(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}

	// The EBML header, then the Segment that holds everything else.
	id, size, err := readElementHeader(f)
	if err != nil || id != 0x1A45DFA3 {
		return nil, errors.New("not a Matroska file")
	}
	if _, err := f.Seek(int64(size), io.SeekCurrent); err != nil {
		return nil, err
	}
	if id, _, err = readElementHeader(f); err != nil || id != mkvSegment {
		return nil, errors.New("not a Matroska file")
	}
	segmentStart, _ := f.Seek(0, io.SeekCurrent)

	// Where the top-level parts are: the SeekHead says; failing that, walk
	// the top-level elements, which is cheap — each one says its size.
	at := map[uint64]int64{}
	walk := segmentStart
	for i := 0; i < 64 && walk < st.Size(); i++ {
		if _, err := f.Seek(walk, io.SeekStart); err != nil {
			break
		}
		id, size, err := readElementHeader(f)
		if err != nil {
			break
		}
		body, _ := f.Seek(0, io.SeekCurrent)
		if _, known := at[id]; !known {
			at[id] = walk
		}
		if id == mkvSeekHead {
			// A SeekHead may point to another one (mkvmerge puts a second
			// at the end of the file, with the Cues in it): follow those.
			heads, seen := []int64{walk}, map[int64]bool{}
			for len(heads) > 0 {
				head := heads[0]
				heads = heads[1:]
				if seen[head] || len(seen) > 8 {
					continue
				}
				seen[head] = true
				if _, err := f.Seek(head, io.SeekStart); err != nil {
					continue
				}
				hid, hsize, err := readElementHeader(f)
				if err != nil || hid != mkvSeekHead {
					continue
				}
				data, err := readBody(f, hsize, 1<<20)
				if err != nil {
					continue
				}
				for _, seek := range children(data, mkvSeek) {
					var target uint64
					var pos int64 = -1
					for _, e := range parseElements(seek) {
						switch e.id {
						case mkvSeekID:
							target = readUint(e.data)
						case mkvSeekPosition:
							pos = int64(readUint(e.data))
						}
					}
					switch {
					case pos < 0:
					case target == mkvSeekHead:
						heads = append(heads, segmentStart+pos)
					default:
						if _, known := at[target]; !known {
							at[target] = segmentStart + pos
						}
					}
				}
			}
		}
		if size == unknownSize || (at[mkvCues] != 0 && at[mkvTracks] != 0 && at[mkvInfo] != 0) {
			break
		}
		walk = body + int64(size)
	}
	if at[mkvCues] == 0 || at[mkvTracks] == 0 {
		return nil, errNoCues
	}

	element := func(id uint64, limit int64) ([]byte, error) {
		if _, err := f.Seek(at[id], io.SeekStart); err != nil {
			return nil, err
		}
		got, size, err := readElementHeader(f)
		if err != nil || got != id {
			return nil, fmt.Errorf("broken index of the file")
		}
		return readBody(f, size, limit)
	}

	scale := uint64(1_000_000) // nanoseconds per timecode unit, by default
	if at[mkvInfo] != 0 {
		if info, err := element(mkvInfo, 1<<20); err == nil {
			for _, e := range parseElements(info) {
				if e.id == mkvTimecodeScale {
					scale = readUint(e.data)
				}
			}
		}
	}
	tracks, err := element(mkvTracks, 4<<20)
	if err != nil {
		return nil, err
	}
	video := uint64(0)
	for _, entry := range children(tracks, mkvTrackEntry) {
		var number, kind uint64
		for _, e := range parseElements(entry) {
			switch e.id {
			case mkvTrackNumber:
				number = readUint(e.data)
			case mkvTrackType:
				kind = readUint(e.data)
			}
		}
		if kind == 1 { // video
			video = number
			break
		}
	}
	if video == 0 {
		return nil, errors.New("no video track")
	}
	cues, err := element(mkvCues, 64<<20)
	if err != nil {
		return nil, err
	}
	var times []float64
	for _, point := range children(cues, mkvCuePoint) {
		var t uint64
		ours := false
		for _, e := range parseElements(point) {
			switch e.id {
			case mkvCueTime:
				t = readUint(e.data)
			case mkvCueTrackPos:
				for _, p := range parseElements(e.data) {
					if p.id == mkvCueTrack && readUint(p.data) == video {
						ours = true
					}
				}
			}
		}
		if ours {
			times = append(times, float64(t*scale)/1e9)
		}
	}
	if len(times) == 0 {
		return nil, errNoCues
	}
	sort.Float64s(times)
	return times, nil
}

const unknownSize = ^uint64(0)

// readElementHeader reads an element's ID (with its marker bits) and the
// size of its body.
func readElementHeader(r io.Reader) (id, size uint64, err error) {
	if id, _, err = readVint(r, true); err != nil {
		return 0, 0, err
	}
	size, _, err = readVint(r, false)
	return id, size, err
}

// readVint reads an EBML variable-length integer; an ID keeps its marker.
func readVint(r io.Reader, keepMarker bool) (uint64, int, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return 0, 0, err
	}
	length := 1
	for mask := byte(0x80); length <= 8 && first[0]&mask == 0; mask >>= 1 {
		length++
	}
	if length > 8 {
		return 0, 0, errors.New("bad EBML number")
	}
	value := uint64(first[0])
	if !keepMarker {
		value &= uint64(0xFF >> length)
	}
	allOnes := value == uint64(0xFF>>length)
	rest := make([]byte, length-1)
	if _, err := io.ReadFull(r, rest); err != nil {
		return 0, 0, err
	}
	for _, b := range rest {
		value = value<<8 | uint64(b)
		allOnes = allOnes && b == 0xFF
	}
	if !keepMarker && allOnes {
		return unknownSize, length, nil
	}
	return value, length, nil
}

func readBody(r io.Reader, size uint64, limit int64) ([]byte, error) {
	if size == unknownSize || int64(size) > limit {
		return nil, errors.New("index too large")
	}
	data := make([]byte, size)
	_, err := io.ReadFull(r, data)
	return data, err
}

type ebmlElement struct {
	id   uint64
	data []byte
}

// parseElements splits a body into its child elements.
func parseElements(data []byte) []ebmlElement {
	var out []ebmlElement
	r := &byteReader{data: data}
	for r.pos < len(data) {
		id, _, err := readVint(r, true)
		if err != nil {
			break
		}
		size, _, err := readVint(r, false)
		if err != nil || size == unknownSize || r.pos+int(size) > len(data) {
			break
		}
		out = append(out, ebmlElement{id, data[r.pos : r.pos+int(size)]})
		r.pos += int(size)
	}
	return out
}

func children(data []byte, id uint64) [][]byte {
	var out [][]byte
	for _, e := range parseElements(data) {
		if e.id == id {
			out = append(out, e.data)
		}
	}
	return out
}

func readUint(b []byte) uint64 {
	if len(b) > 8 {
		return 0
	}
	var buf [8]byte
	copy(buf[8-len(b):], b)
	return binary.BigEndian.Uint64(buf[:])
}

type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
