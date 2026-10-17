package psd

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"
)

// ⛔ THE REPEAT RUN WAS NEVER EXERCISED, and the coverage profile is what said
// so: the fixture builder only emits a repeat for a run of three or more, and
// every earlier fixture had runs of two. A PSD of a photograph is mostly
// literals; a PSD of a screenshot, a logo or anything with flat colour is
// mostly repeats — which is to say the untested half was the half that real
// files of the commonest kind are made of.
func TestRLEWithRealRepeats(t *testing.T) {
	// 16 pixels: a long flat run, then a gradient, then another flat run.
	plane := append(solid(6, 0x11), ramp(4, 0x20)...)
	plane = append(plane, solid(6, 0x77)...)
	f := file{channels: 1, width: 16, height: 1, depth: 8, mode: modeGrayscale,
		rle: true, planes: [][]byte{plane}}
	raw := f.bytes()

	// The fixture really is compressed, or this test proves nothing: 16 bytes
	// of pixels in fewer than 16 bytes of scanline.
	table := 2
	payload := len(raw) - (26 + 4 + 4 + 4 + 2 + table)
	if payload >= 16 {
		t.Fatalf("the scanline is %d bytes for 16 pixels: the fixture is not compressing, so the repeat branch is still untested", payload)
	}

	m := decode(t, raw)
	for x := range 16 {
		got := m.At(x, 0).(color.Gray).Y
		if got != plane[x] {
			t.Errorf("pixel %d = %#02x, want %#02x", x, got, plane[x])
		}
	}
}

func TestUnpack(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		out  int
		want []byte
		says string
	}{
		{
			name: "a literal run", in: []byte{2, 'a', 'b', 'c'}, out: 3, want: []byte("abc"),
		},
		{
			name: "a repeat run", in: []byte{0xfd, 'x'}, out: 4, want: []byte("xxxx"),
		},
		{
			// ⛔ The off-by-one that a wrong reading of the format produces:
			// 0xfd is −3, so the count is 1−(−3) = 4, not 3 and not 5.
			name: "the repeat count is 1 minus the signed byte",
			in:   []byte{0xff, 'y'}, out: 2, want: []byte("yy"),
		},
		{
			name: "a no-operation byte, which Photoshop writes",
			in:   []byte{0x80, 1, 'a', 'b'}, out: 2, want: []byte("ab"),
		},
		{
			name: "a literal run that runs off the end",
			in:   []byte{9, 'a'}, out: 10, says: "a literal run of 10 bytes with 1 left",
		},
		{
			name: "a literal run longer than the row",
			in:   []byte{3, 'a', 'b', 'c', 'd'}, out: 2, says: "unpacks to more than its 2 bytes",
		},
		{
			name: "a repeat with no byte to repeat",
			in:   []byte{0xfd}, out: 4, says: "a repeat of 4 with no byte to repeat",
		},
		{
			name: "a repeat longer than the row",
			in:   []byte{0xf0, 'z'}, out: 2, says: "unpacks to more than its 2 bytes",
		},
		{
			name: "a scanline that unpacks short",
			in:   []byte{1, 'a', 'b'}, out: 4, says: "unpacks to 2 bytes, not its 4",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := make([]byte, tc.out)
			err := unpack(tc.in, out)
			if tc.says != "" {
				if err == nil {
					t.Fatalf("unpack accepted it and gave %q", out)
				}
				if !strings.Contains(err.Error(), tc.says) {
					t.Errorf("error = %v, want it to say %q", err, tc.says)
				}
				return
			}
			if err != nil {
				t.Fatalf("unpack: %v", err)
			}
			if !bytes.Equal(out, tc.want) {
				t.Errorf("unpack = %q, want %q", out, tc.want)
			}
		})
	}
}

// ⛔ pack and unpack are each other's inverse, over data that exercises both
// branches — and pack is in the TEST, so this is not a function checking
// itself: it is two independent implementations of the same format agreeing.
func TestPackAndUnpackAgree(t *testing.T) {
	for _, in := range [][]byte{
		{},
		{1},
		solid(1, 9),
		solid(300, 7),
		ramp(300, 0),
		append(solid(130, 3), ramp(130, 0)...),
		append(ramp(5, 0), solid(200, 0xff)...),
	} {
		packed := pack(in)
		out := make([]byte, len(in))
		if err := unpack(packed, out); err != nil {
			t.Fatalf("%d bytes: unpack: %v", len(in), err)
		}
		if !bytes.Equal(in, out) {
			t.Errorf("%d bytes did not round-trip", len(in))
		}
	}
}

func TestRLERefusals(t *testing.T) {
	// A scanline claiming more compressed bytes than PackBits could ever
	// produce for the row: the bound is what stops a tiny file asking for a
	// large buffer per row.
	f := file{channels: 1, width: 4, height: 1, depth: 8, mode: modeGrayscale,
		rle: true, rawTable: []byte{0x10, 0x00}, rawRows: [][]byte{make([]byte, 0x1000)}}
	_, err := Decode(bytes.NewReader(f.bytes()))
	if err == nil || !strings.Contains(err.Error(), "compressed bytes for 4 uncompressed") {
		t.Fatalf("error = %v, want the scanline bound", err)
	}

	// A table that stops in the middle.
	f = file{channels: 3, width: 4, height: 4, depth: 8, mode: modeGrayscale,
		rle: true, rawTable: []byte{0, 1, 0}}
	_, err = Decode(bytes.NewReader(f.bytes()))
	if err == nil || !strings.Contains(err.Error(), "ends in the middle") {
		t.Fatalf("error = %v, want the truncated table", err)
	}

	// A table that is whole and a row that is not.
	f = file{channels: 1, width: 4, height: 1, depth: 8, mode: modeGrayscale,
		rle: true, rawTable: []byte{0x00, 0x05}, rawRows: [][]byte{{3, 'a'}}}
	_, err = Decode(bytes.NewReader(f.bytes()))
	if err == nil || !strings.Contains(err.Error(), "ends in the middle") {
		t.Fatalf("error = %v, want the truncated scanline", err)
	}

	// A row whose bytes are whole and do not unpack to the row's length.
	f = file{channels: 1, width: 4, height: 1, depth: 8, mode: modeGrayscale,
		rle: true, rawTable: []byte{0x00, 0x02}, rawRows: [][]byte{{0, 'a'}}}
	_, err = Decode(bytes.NewReader(f.bytes()))
	if err == nil || !strings.Contains(err.Error(), "unpacks to 1 bytes, not its 4") {
		t.Fatalf("error = %v, want the short scanline", err)
	}
}

// failAfter is a reader that hands over n bytes and then fails with an error
// that is NOT EOF — a disk going away mid-read, which is the one case
// "ends in the middle of the composite" would describe wrongly.
type failAfter struct {
	b []byte
	n int
}

var errBroken = errors.New("the disk went away")

func (r *failAfter) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errBroken
	}
	n := min(len(p), r.n, len(r.b))
	copy(p, r.b[:n])
	r.b = r.b[n:]
	r.n -= n
	return n, nil
}

func TestAReadThatFailsForAReasonOfItsOwn(t *testing.T) {
	f := file{channels: 3, width: 4, height: 4, depth: 8, mode: modeRGB,
		planes: [][]byte{solid(16, 1), solid(16, 2), solid(16, 3)}}
	src := f.bytes()

	// ⛔ Each of these stops at a different place, which is the point: the
	// error has to travel out of whichever read was in progress, and four
	// separate reads can swallow it.
	for _, after := range []int{10, 26, 30, 40, len(src) - 5} {
		_, err := Decode(&failAfter{b: src, n: after})
		if err == nil {
			t.Errorf("after %d bytes: accepted", after)
			continue
		}
		if errors.Is(err, errBroken) {
			continue // reported, which is what is wanted
		}
		if after >= 26 && !strings.Contains(err.Error(), "psd:") {
			t.Errorf("after %d bytes: error = %v, want it to name this package", after, err)
		}
	}
}

func TestDecodeConfigRefusesWhatDecodeRefuses(t *testing.T) {
	_, err := DecodeConfig(bytes.NewReader([]byte("not a psd at all")))
	if err != image.ErrFormat {
		t.Errorf("error = %v, want image.ErrFormat", err)
	}
	src := file{channels: 3, width: 1, height: 1, depth: 32, mode: modeRGB}.bytes()
	if _, err := DecodeConfig(bytes.NewReader(src)); err == nil {
		t.Error("DecodeConfig accepted a 32-bit file that Decode refuses")
	}
}

// ⛔ The dark end of the sRGB transfer function is a straight line, not the
// power curve, and the two meet at 0.0031308. A conversion that used the curve
// everywhere crushes near-blacks — which is invisible on a chart and obvious
// on a photograph of a night sky.
func TestTheLinearSegmentOfTheTransferFunction(t *testing.T) {
	// L* = 1 is about 0.11% luminance, well inside the linear segment.
	f := file{channels: 3, width: 1, height: 1, depth: 8, mode: modeLab, planes: [][]byte{
		{3}, {128}, {128},
	}}
	got := decode(t, f.bytes()).At(0, 0).(color.NRGBA)
	if got.R == 0 || got.R > 12 {
		t.Errorf("L*≈1 came back as %v: the linear segment is what keeps a near-black from collapsing", got)
	}
}

// ⛔ A LEGAL Lab triple that sRGB cannot hold. L=50 with a=b=−128 is far
// outside the gamut and gives a negative linear value for two channels; the
// clamp has to catch it BEFORE the conversion to byte, because Go says a
// float-to-integer conversion the target cannot represent is
// implementation-dependent — 0 on one architecture and 251 on another.
//
// The mutation sweep found this: deleting the lower clamp changed nothing any
// test could see, because every other fixture was inside the gamut.
func TestLabOutsideTheSRGBGamut(t *testing.T) {
	f := file{channels: 3, width: 2, height: 1, depth: 8, mode: modeLab, planes: [][]byte{
		{128, 128}, // L ≈ 50
		{0, 255},   // a = −128, then +127
		{0, 255},   // b = −128, then +127
	}}
	m := decode(t, f.bytes())
	for x := range 2 {
		c := m.At(x, 0).(color.NRGBA)
		for i, v := range []byte{c.R, c.G, c.B} {
			if v == 0 || v == 0xff {
				continue // clamped, which is the point
			}
			if v > 0xff {
				t.Errorf("pixel %d channel %d = %d", x, i, v)
			}
		}
	}
	// What the clamp must produce, named: the blue-most corner saturates blue
	// and extinguishes red.
	c := m.At(0, 0).(color.NRGBA)
	if c.B != 0xff {
		t.Errorf("a = b = −128 gave blue %d, want it clamped to 255", c.B)
	}
	if c.R != 0 {
		t.Errorf("a = b = −128 gave red %d, want it clamped to 0", c.R)
	}
}
