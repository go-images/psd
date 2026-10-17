package psd

import (
	"encoding/binary"
	"fmt"
	"io"
)

// readRLE reads an RLE-compressed image data section.
//
// ⛔ THE WHOLE TABLE COMES FIRST. Every scanline of every channel has its
// compressed length in a table before any pixel data, which is what makes the
// section seekable for a program that wants one channel — and what makes a
// reader that starts unpacking at the table's first byte produce noise.
func readRLE(r io.Reader, h header, rowBytes int) ([][]byte, error) {
	countBytes := 2
	if h.psb {
		countBytes = 4
	}
	rows := h.channels * h.height
	table := make([]byte, rows*countBytes)
	if _, err := io.ReadFull(r, table); err != nil {
		return nil, unexpected(err)
	}

	planes := make([][]byte, h.channels)
	row := 0
	for c := range planes {
		plane := make([]byte, rowBytes*h.height)
		for y := range h.height {
			var n int
			if h.psb {
				n = int(binary.BigEndian.Uint32(table[row*4:]))
			} else {
				n = int(binary.BigEndian.Uint16(table[row*2:]))
			}
			row++
			// ⛔ Bounded by the UNCOMPRESSED row, doubled and a little: PackBits
			// cannot expand a row by more than a byte every 128, so a declared
			// length far past that is a lie about a buffer rather than a
			// scanline. Without this a 65 535-byte count per row times a
			// declared height is an allocation nobody asked for.
			if max := 2*rowBytes + 2; n > max {
				return nil, fmt.Errorf("psd: a scanline of %d compressed bytes for %d uncompressed, which cannot be",
					n, rowBytes)
			}
			packed := make([]byte, n)
			if _, err := io.ReadFull(r, packed); err != nil {
				return nil, unexpected(err)
			}
			if err := unpack(packed, plane[y*rowBytes:(y+1)*rowBytes]); err != nil {
				return nil, err
			}
		}
		planes[c] = plane
	}
	return planes, nil
}

// unpack undoes PackBits into exactly len(out) bytes.
//
// ⛔ THE SEMANTICS, because one summary of them is wrong in a way that decodes
// to plausible rubbish. Reading the control byte as SIGNED:
//
//	 0 … 127   copy the next n+1 bytes literally  (1 to 128 of them)
//	-1 … -127  repeat the next byte 1-n times     (2 to 128 of them)
//	-128       no operation, and it does happen
//
// A reader that treats 128…255 as "1 to 129 repetitions" is off by one for
// every run in the file, which looks like a slight shear rather than like a
// broken decoder.
//
// ⛔ And the output length is the ROW's, not the input's: a row that unpacks
// short is a truncated file and a row that unpacks long is a malformed one,
// and both are said rather than silently cropped.
func unpack(in, out []byte) error {
	o := 0
	for i := 0; i < len(in); {
		n := int(int8(in[i]))
		i++
		switch {
		case n == -128:
			// No operation. Photoshop writes it; so does every PackBits
			// implementation that pads.
			continue
		case n >= 0:
			count := n + 1
			if i+count > len(in) {
				return fmt.Errorf("psd: a literal run of %d bytes with %d left in the scanline", count, len(in)-i)
			}
			if o+count > len(out) {
				return fmt.Errorf("psd: a scanline unpacks to more than its %d bytes", len(out))
			}
			copy(out[o:], in[i:i+count])
			o += count
			i += count
		default:
			count := 1 - n
			if i >= len(in) {
				return fmt.Errorf("psd: a repeat of %d with no byte to repeat", count)
			}
			if o+count > len(out) {
				return fmt.Errorf("psd: a scanline unpacks to more than its %d bytes", len(out))
			}
			b := in[i]
			i++
			for range count {
				out[o] = b
				o++
			}
		}
	}
	if o != len(out) {
		return fmt.Errorf("psd: a scanline unpacks to %d bytes, not its %d", o, len(out))
	}
	return nil
}
