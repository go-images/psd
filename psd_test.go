package psd

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"strings"
	"testing"
)

// a file is a Photoshop file a test builds.
//
// ⛔ THE BUILDER IS NOT THE JUDGE. Fixtures written here test this package
// against its author's reading of the specification, which is the thing most
// likely to be wrong — so the modes a real file exercises (8-bit RGB, with and
// without alpha, raw and RLE) are judged by an independent decoder in
// corpus_test.go, and that is what anchors the builder. Where a mode has no
// real file to measure against, the test says so rather than implying it was
// verified.
type file struct {
	psb      bool
	channels int
	width    int
	height   int
	depth    int
	mode     int
	palette  []byte // 768 bytes, planar, for indexed files
	rle      bool
	planes   [][]byte // one per channel, row-major, the natural width*height
	// overrides, for the malformed cases
	signature   string
	version     int
	compression int
	truncate    int // bytes to cut from the end
	rawTable    []byte
	rawRows     [][]byte
	layerLen    int  // a layer section length the file does not deliver
	stopAfter   bool // stop writing after the layer length, before the pixels
}

func (f file) bytes() []byte {
	var b bytes.Buffer
	sig := f.signature
	if sig == "" {
		sig = "8BPS"
	}
	version := f.version
	if version == 0 {
		version = 1
		if f.psb {
			version = 2
		}
	}
	b.WriteString(sig)
	be16(&b, version)
	b.Write(make([]byte, 6))
	be16(&b, f.channels)
	be32(&b, f.height)
	be32(&b, f.width)
	be16(&b, f.depth)
	be16(&b, f.mode)

	be32(&b, len(f.palette))
	b.Write(f.palette)

	be32(&b, 0) // image resources

	if f.psb { // layer and mask information
		be64(&b, f.layerLen)
	} else {
		be32(&b, f.layerLen)
	}
	if f.stopAfter {
		return b.Bytes()
	}

	compression := f.compression
	if compression == 0 && f.rle {
		compression = compressionRLE
	}
	be16(&b, compression)

	switch {
	case f.rawTable != nil || f.rawRows != nil:
		b.Write(f.rawTable)
		for _, row := range f.rawRows {
			b.Write(row)
		}
	case f.rle:
		rows := make([][]byte, 0, f.channels*f.height)
		for _, plane := range f.planes {
			stride := len(plane) / f.height
			for y := range f.height {
				rows = append(rows, pack(plane[y*stride:(y+1)*stride]))
			}
		}
		for _, row := range rows {
			if f.psb {
				be32(&b, len(row))
			} else {
				be16(&b, len(row))
			}
		}
		for _, row := range rows {
			b.Write(row)
		}
	default:
		for _, plane := range f.planes {
			b.Write(plane)
		}
	}
	out := b.Bytes()
	if f.truncate > 0 {
		out = out[:len(out)-f.truncate]
	}
	return out
}

func be16(b *bytes.Buffer, v int) { binary.Write(b, binary.BigEndian, uint16(v)) } //nolint
func be32(b *bytes.Buffer, v int) { binary.Write(b, binary.BigEndian, uint32(v)) } //nolint
func be64(b *bytes.Buffer, v int) { binary.Write(b, binary.BigEndian, uint64(v)) } //nolint

// pack is PackBits, written here so that the fixtures are RLE in the same
// sense the format means — and so that unpack is tested against something
// other than itself.
//
// It emits a literal run for everything except a repeat of three or more,
// which is what Photoshop does and what keeps the fixture readable.
func pack(in []byte) []byte {
	var out []byte
	for i := 0; i < len(in); {
		run := 1
		for i+run < len(in) && in[i+run] == in[i] && run < 128 {
			run++
		}
		if run >= 3 {
			out = append(out, byte(int8(1-run)), in[i])
			i += run
			continue
		}
		// A literal run up to the next repeat of three.
		start := i
		for i < len(in) && i-start < 128 {
			if i+2 < len(in) && in[i] == in[i+1] && in[i+1] == in[i+2] {
				break
			}
			i++
		}
		out = append(out, byte(i-start-1))
		out = append(out, in[start:i]...)
	}
	return out
}

func ramp(n int, start byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = start + byte(i)
	}
	return out
}

func solid(n int, v byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func decode(t *testing.T, src []byte) image.Image {
	t.Helper()
	m, err := Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return m
}

func TestRGB(t *testing.T) {
	f := file{channels: 3, width: 2, height: 2, depth: 8, mode: modeRGB, planes: [][]byte{
		{0xff, 0x00, 0x00, 0xff}, // red
		{0x00, 0xff, 0x00, 0xff}, // green
		{0x00, 0x00, 0xff, 0x00}, // blue
	}}
	for _, rle := range []bool{false, true} {
		f.rle = rle
		m := decode(t, f.bytes())
		if _, ok := m.(*image.NRGBA); !ok {
			t.Fatalf("rle=%v: %T, want *image.NRGBA", rle, m)
		}
		want := []color.NRGBA{
			{R: 0xff, G: 0x00, B: 0x00, A: 0xff},
			{R: 0x00, G: 0xff, B: 0x00, A: 0xff},
			{R: 0x00, G: 0x00, B: 0xff, A: 0xff},
			{R: 0xff, G: 0xff, B: 0x00, A: 0xff},
		}
		for i, w := range want {
			if got := m.At(i%2, i/2).(color.NRGBA); got != w {
				t.Errorf("rle=%v: pixel %d = %v, want %v", rle, i, got, w)
			}
		}
	}
}

// ⛔ A plane read in interleaved order gives a picture in vertical bands of one
// colour each. This asserts the planar order itself rather than a round trip.
func TestThePlanesAreNotInterleaved(t *testing.T) {
	f := file{channels: 3, width: 4, height: 1, depth: 8, mode: modeRGB, planes: [][]byte{
		{1, 2, 3, 4}, {10, 20, 30, 40}, {100, 110, 120, 130},
	}}
	m := decode(t, f.bytes())
	for x := range 4 {
		got := m.At(x, 0).(color.NRGBA)
		want := color.NRGBA{R: byte(1 + x), G: byte(10 * (x + 1)), B: byte(100 + 10*x), A: 0xff}
		if got != want {
			t.Errorf("pixel %d = %v, want %v", x, got, want)
		}
	}
}

func TestRGBWithAlpha(t *testing.T) {
	f := file{channels: 4, width: 2, height: 1, depth: 8, mode: modeRGB, planes: [][]byte{
		{0x40, 0x80}, {0x40, 0x80}, {0x40, 0x80}, {0x00, 0xff},
	}}
	m := decode(t, f.bytes())
	// ⛔ NRGBA, straight: the first pixel keeps its colour at alpha zero. A
	// premultiplied reading would have thrown the colour away.
	if got, want := m.At(0, 0).(color.NRGBA), (color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0}); got != want {
		t.Errorf("transparent pixel = %v, want %v", got, want)
	}
	if got, want := m.At(1, 0).(color.NRGBA), (color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}); got != want {
		t.Errorf("opaque pixel = %v, want %v", got, want)
	}
}

func TestGrayscale(t *testing.T) {
	f := file{channels: 1, width: 3, height: 1, depth: 8, mode: modeGrayscale,
		planes: [][]byte{{0x00, 0x80, 0xff}}}
	m := decode(t, f.bytes())
	if _, ok := m.(*image.Gray); !ok {
		t.Fatalf("%T, want *image.Gray", m)
	}
	for x, want := range []byte{0x00, 0x80, 0xff} {
		if got := m.At(x, 0).(color.Gray).Y; got != want {
			t.Errorf("pixel %d = %d, want %d", x, got, want)
		}
	}
}

func TestGrayscaleWithAlpha(t *testing.T) {
	f := file{channels: 2, width: 2, height: 1, depth: 8, mode: modeGrayscale,
		planes: [][]byte{{0x40, 0x80}, {0x00, 0xff}}}
	m := decode(t, f.bytes())
	// Go has no gray-with-alpha, so the only honest target is NRGBA.
	if got, want := m.At(0, 0).(color.NRGBA), (color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0}); got != want {
		t.Errorf("pixel 0 = %v, want %v", got, want)
	}
}

func TestDuotoneIsReadAsGrayscale(t *testing.T) {
	// The specification calls duotone's colour data undocumented; its image
	// data is one channel, and every reader shows it as grey.
	f := file{channels: 1, width: 2, height: 1, depth: 8, mode: modeDuotone,
		planes: [][]byte{{0x10, 0x20}}}
	if _, ok := decode(t, f.bytes()).(*image.Gray); !ok {
		t.Error("a duotone file did not come back as grey")
	}
}

func TestGrayscale16(t *testing.T) {
	f := file{channels: 1, width: 2, height: 1, depth: 16, mode: modeGrayscale,
		planes: [][]byte{{0x12, 0x34, 0xff, 0xff}}}
	m := decode(t, f.bytes())
	if got, want := m.At(0, 0).(color.Gray16).Y, uint16(0x1234); got != want {
		t.Errorf("pixel = %#04x, want %#04x", got, want)
	}
	if got, want := m.At(1, 0).(color.Gray16).Y, uint16(0xffff); got != want {
		t.Errorf("white = %#04x, want %#04x", got, want)
	}
}

// ⛔ 0x101 and not 0x100: an 8-bit white has to come back as 0xffff, or a
// round trip through 16 bits leaves white slightly grey.
func TestEightBitWhiteIsSixteenBitWhite(t *testing.T) {
	f := file{channels: 4, width: 1, height: 1, depth: 8, mode: modeRGB,
		planes: [][]byte{{0xff}, {0xff}, {0xff}, {0xff}}}
	r, g, b, a := decode(t, f.bytes()).At(0, 0).RGBA()
	if r != 0xffff || g != 0xffff || b != 0xffff || a != 0xffff {
		t.Errorf("white = %#04x,%#04x,%#04x,%#04x, want all 0xffff", r, g, b, a)
	}
}

func TestRGB16(t *testing.T) {
	f := file{channels: 4, width: 1, height: 1, depth: 16, mode: modeRGB, planes: [][]byte{
		{0x11, 0x22}, {0x33, 0x44}, {0x55, 0x66}, {0x77, 0x88},
	}}
	m := decode(t, f.bytes())
	got, ok := m.At(0, 0).(color.NRGBA64)
	if !ok {
		t.Fatalf("%T, want color.NRGBA64", m.At(0, 0))
	}
	want := color.NRGBA64{R: 0x1122, G: 0x3344, B: 0x5566, A: 0x7788}
	if got != want {
		t.Errorf("pixel = %v, want %v", got, want)
	}
}

func TestIndexed(t *testing.T) {
	// ⛔ The palette is PLANAR: 256 reds, then 256 greens, then 256 blues.
	// Read as triples it comes out as a gradient of reds.
	palette := make([]byte, 768)
	palette[0], palette[256], palette[512] = 0xff, 0x00, 0x00 // index 0: red
	palette[1], palette[257], palette[513] = 0x00, 0xff, 0x00 // index 1: green
	f := file{channels: 1, width: 2, height: 1, depth: 8, mode: modeIndexed,
		palette: palette, planes: [][]byte{{0, 1}}}
	m := decode(t, f.bytes())
	if _, ok := m.(*image.Paletted); !ok {
		t.Fatalf("%T, want *image.Paletted", m)
	}
	if got, want := m.At(0, 0).(color.NRGBA), (color.NRGBA{R: 0xff, A: 0xff}); got != want {
		t.Errorf("index 0 = %v, want %v", got, want)
	}
	if got, want := m.At(1, 0).(color.NRGBA), (color.NRGBA{G: 0xff, A: 0xff}); got != want {
		t.Errorf("index 1 = %v, want %v", got, want)
	}
}

// ⛔ THE INVERSION. Photoshop stores CMYK complemented: 255 means no ink. A
// reader that takes the bytes as written turns a white page black, and this is
// the test that says which way round it is.
func TestCMYKIsStoredInverted(t *testing.T) {
	// All channels 255 = no ink anywhere = white.
	f := file{channels: 4, width: 1, height: 1, depth: 8, mode: modeCMYK,
		planes: [][]byte{{0xff}, {0xff}, {0xff}, {0xff}}}
	m := decode(t, f.bytes())
	if _, ok := m.(*image.CMYK); !ok {
		t.Fatalf("%T, want *image.CMYK", m)
	}
	r, g, b, _ := m.At(0, 0).RGBA()
	if r != 0xffff || g != 0xffff || b != 0xffff {
		t.Errorf("a page with no ink came back as %#04x,%#04x,%#04x — the inversion is missing", r, g, b)
	}
	// And all zero = full ink = black.
	f.planes = [][]byte{{0}, {0}, {0}, {0}}
	r, g, b, _ = decode(t, f.bytes()).At(0, 0).RGBA()
	if r != 0 || g != 0 || b != 0 {
		t.Errorf("full ink came back as %#04x,%#04x,%#04x, want black", r, g, b)
	}
}

func TestCMYKWithAlpha(t *testing.T) {
	f := file{channels: 5, width: 2, height: 1, depth: 8, mode: modeCMYK, planes: [][]byte{
		{0xff, 0xff}, {0xff, 0xff}, {0xff, 0xff}, {0xff, 0x00}, {0x00, 0xff},
	}}
	m := decode(t, f.bytes())
	if _, ok := m.(*image.NRGBA); !ok {
		t.Fatalf("%T, want *image.NRGBA — a fifth channel is alpha and image.CMYK has none", m)
	}
	if got := m.At(0, 0).(color.NRGBA).A; got != 0 {
		t.Errorf("alpha = %d, want 0", got)
	}
	// The second pixel is full black ink, opaque.
	if got, want := m.At(1, 0).(color.NRGBA), (color.NRGBA{A: 0xff}); got != want {
		t.Errorf("pixel = %v, want %v", got, want)
	}
}

func TestCMYK16(t *testing.T) {
	f := file{channels: 4, width: 1, height: 1, depth: 16, mode: modeCMYK, planes: [][]byte{
		{0xff, 0xff}, {0xff, 0xff}, {0xff, 0xff}, {0xff, 0xff},
	}}
	r, g, b, _ := decode(t, f.bytes()).At(0, 0).RGBA()
	if r != 0xffff || g != 0xffff || b != 0xffff {
		t.Errorf("16-bit no ink = %#04x,%#04x,%#04x, want white", r, g, b)
	}
}

// ⛔ Read from the specification, not measured against Photoshop: no file in
// the corpus is in bitmap mode, and the test says so rather than implying it
// was verified. What it pins is the direction: a SET bit is black.
func TestBitmapZeroIsWhite(t *testing.T) {
	// One row of eight pixels: 0b10000001 -> black, white×6, black.
	f := file{channels: 1, width: 8, height: 1, depth: 1, mode: modeBitmap,
		planes: [][]byte{{0x81}}}
	m := decode(t, f.bytes())
	if _, ok := m.(*image.Gray); !ok {
		t.Fatalf("%T, want *image.Gray", m)
	}
	for x, want := range []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0} {
		if got := m.At(x, 0).(color.Gray).Y; got != want {
			t.Errorf("pixel %d = %d, want %d", x, got, want)
		}
	}
}

func TestBitmapRowPadding(t *testing.T) {
	// 9 pixels need 2 bytes a row, and the second row must start on the byte
	// boundary rather than continuing the bit stream.
	f := file{channels: 1, width: 9, height: 2, depth: 1, mode: modeBitmap,
		planes: [][]byte{{0x80, 0x00, 0x00, 0x80}}}
	m := decode(t, f.bytes())
	if got := m.At(0, 0).(color.Gray).Y; got != 0 {
		t.Errorf("(0,0) = %d, want black", got)
	}
	if got := m.At(0, 1).(color.Gray).Y; got != 0xff {
		t.Errorf("(0,1) = %d, want white", got)
	}
	if got := m.At(8, 1).(color.Gray).Y; got != 0 {
		t.Errorf("(8,1) = %d, want black: the ninth bit of the second row", got)
	}
}

// ⛔ Read from the specification as well, and the D50 white point is the part
// worth pinning: L=100 with no chroma is white, L=0 is black, and a D65
// conversion would shift the grey axis.
func TestLab(t *testing.T) {
	f := file{channels: 3, width: 3, height: 1, depth: 8, mode: modeLab, planes: [][]byte{
		{255, 0, 128}, // L: white, black, mid grey
		{128, 128, 128},
		{128, 128, 128},
	}}
	m := decode(t, f.bytes())
	white := m.At(0, 0).(color.NRGBA)
	if white.R < 250 || white.G < 250 || white.B < 250 {
		t.Errorf("L=100 came back as %v, want white", white)
	}
	black := m.At(1, 0).(color.NRGBA)
	if black.R > 5 || black.G > 5 || black.B > 5 {
		t.Errorf("L=0 came back as %v, want black", black)
	}
	grey := m.At(2, 0).(color.NRGBA)
	if d := int(grey.R) - int(grey.B); d < -4 || d > 4 {
		t.Errorf("a neutral grey came back as %v: the grey axis is tilted, which is what the wrong white point does", grey)
	}
}

func TestLab16AndAlpha(t *testing.T) {
	f := file{channels: 4, width: 1, height: 1, depth: 16, mode: modeLab, planes: [][]byte{
		{0xff, 0xff}, {0x80, 0x00}, {0x80, 0x00}, {0x80, 0x00},
	}}
	got := decode(t, f.bytes()).At(0, 0).(color.NRGBA64)
	if got.R < 0xf000 || got.G < 0xf000 || got.B < 0xf000 {
		t.Errorf("16-bit L=100 came back as %v, want white", got)
	}
	if got.A != 0x8000 {
		t.Errorf("alpha = %#04x, want 0x8000", got.A)
	}
}

func TestPSB(t *testing.T) {
	// ⛔ A PSB differs in its LENGTH fields — eight bytes for the layer
	// section, four for each RLE scanline count — and in nothing else. A
	// reader that got that wrong would read the composite from the middle of
	// the layer data.
	f := file{psb: true, channels: 3, width: 2, height: 2, depth: 8, mode: modeRGB, rle: true,
		planes: [][]byte{
			{0xff, 0x00, 0x00, 0xff}, {0x00, 0xff, 0x00, 0xff}, {0x00, 0x00, 0xff, 0x00},
		}}
	m := decode(t, f.bytes())
	if got, want := m.At(0, 0).(color.NRGBA), (color.NRGBA{R: 0xff, A: 0xff}); got != want {
		t.Errorf("pixel = %v, want %v", got, want)
	}
}

func TestDecodeConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		f     file
		model color.Model
	}{
		{"rgb", file{channels: 3, width: 5, height: 7, depth: 8, mode: modeRGB, planes: [][]byte{solid(35, 1), solid(35, 2), solid(35, 3)}}, color.NRGBAModel},
		{"rgb 16-bit", file{channels: 3, width: 1, height: 1, depth: 16, mode: modeRGB, planes: [][]byte{{0, 0}, {0, 0}, {0, 0}}}, color.NRGBA64Model},
		{"gray", file{channels: 1, width: 5, height: 7, depth: 8, mode: modeGrayscale, planes: [][]byte{solid(35, 1)}}, color.GrayModel},
		{"gray 16-bit", file{channels: 1, width: 1, height: 1, depth: 16, mode: modeGrayscale, planes: [][]byte{{0, 0}}}, color.Gray16Model},
		{"gray with alpha", file{channels: 2, width: 1, height: 1, depth: 8, mode: modeGrayscale, planes: [][]byte{{1}, {2}}}, color.NRGBAModel},
		{"bitmap", file{channels: 1, width: 8, height: 1, depth: 1, mode: modeBitmap, planes: [][]byte{{0}}}, color.GrayModel},
		{"cmyk", file{channels: 4, width: 1, height: 1, depth: 8, mode: modeCMYK, planes: [][]byte{{0}, {0}, {0}, {0}}}, color.CMYKModel},
		{"cmyk with alpha", file{channels: 5, width: 1, height: 1, depth: 8, mode: modeCMYK, planes: [][]byte{{0}, {0}, {0}, {0}, {0}}}, color.NRGBAModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := DecodeConfig(bytes.NewReader(tc.f.bytes()))
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if cfg.Width != tc.f.width || cfg.Height != tc.f.height {
				t.Errorf("size = %dx%d, want %dx%d", cfg.Width, cfg.Height, tc.f.width, tc.f.height)
			}
			if cfg.ColorModel != tc.model {
				t.Errorf("ColorModel = %T, want %T", cfg.ColorModel, tc.model)
			}
		})
	}
	// An indexed file's model IS its palette, so a caller can read the colours
	// without the pixels.
	palette := make([]byte, 768)
	palette[0] = 0xff
	f := file{channels: 1, width: 1, height: 1, depth: 8, mode: modeIndexed, palette: palette, planes: [][]byte{{0}}}
	cfg, err := DecodeConfig(bytes.NewReader(f.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.ColorModel.(color.Palette)
	if !ok {
		t.Fatalf("ColorModel = %T, want a color.Palette", cfg.ColorModel)
	}
	if got, want := p[0].(color.NRGBA), (color.NRGBA{R: 0xff, A: 0xff}); got != want {
		t.Errorf("palette[0] = %v, want %v", got, want)
	}
}

// ⛔ Through image.Decode, which is the whole point of the package: a caller
// imports it for the side effect and never names it again.
func TestItRegistersItself(t *testing.T) {
	f := file{channels: 3, width: 1, height: 1, depth: 8, mode: modeRGB,
		planes: [][]byte{{0xff}, {0x80}, {0x00}}}
	m, format, err := image.Decode(bytes.NewReader(f.bytes()))
	if err != nil {
		t.Fatalf("image.Decode: %v", err)
	}
	if format != "psd" {
		t.Errorf("format = %q, want psd", format)
	}
	if got, want := m.At(0, 0).(color.NRGBA), (color.NRGBA{R: 0xff, G: 0x80, A: 0xff}); got != want {
		t.Errorf("pixel = %v, want %v", got, want)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(f.bytes()))
	if err != nil {
		t.Fatalf("image.DecodeConfig: %v", err)
	}
	if format != "psd" || cfg.Width != 1 {
		t.Errorf("DecodeConfig gave %q %dx%d", format, cfg.Width, cfg.Height)
	}
}

// ⛔ ErrFormat for "not mine", and a NAMED error for everything else. The
// distinction decides whether image.Decode goes on to try the other codecs or
// reports what is actually wrong: a PSB mistaken for "not a Photoshop file"
// comes back as "unknown format", which sends the reader to look at the wrong
// thing entirely.
func TestWhatItRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   []byte
		says  string
		isFmt bool
	}{
		{name: "empty", src: nil, isFmt: true},
		{name: "a JPEG named .psd", src: []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x01\x00H\x00H\x00\x00"), isFmt: true},
		{name: "a signature one byte short", src: []byte("8BP"), isFmt: true},
		{
			name: "version 3",
			src:  file{version: 3, channels: 3, width: 1, height: 1, depth: 8, mode: modeRGB}.bytes(),
			says: "version 3, which is neither PSD (1) nor PSB (2)",
		},
		{
			name: "no channels",
			src:  file{channels: 0, width: 1, height: 1, depth: 8, mode: modeRGB}.bytes(),
			says: "0 channels",
		},
		{
			name: "fifty-seven channels",
			src:  file{channels: 57, width: 1, height: 1, depth: 8, mode: modeRGB}.bytes(),
			says: "57 channels",
		},
		{
			name: "no pixels",
			src:  file{channels: 3, width: 0, height: 1, depth: 8, mode: modeRGB}.bytes(),
			says: "0 by 1 pixels",
		},
		{
			name: "larger than the format allows",
			src:  file{channels: 3, width: 30001, height: 1, depth: 8, mode: modeRGB}.bytes(),
			says: "over the 30000 the format allows",
		},
		{
			name: "32-bit channels",
			src:  file{channels: 3, width: 1, height: 1, depth: 32, mode: modeRGB}.bytes(),
			says: "32 bits a channel (floating point)",
		},
		{
			name: "seven bits a channel",
			src:  file{channels: 3, width: 1, height: 1, depth: 7, mode: modeRGB}.bytes(),
			says: "7 bits a channel, which the format does not allow",
		},
		{
			name: "multichannel",
			src:  file{channels: 3, width: 1, height: 1, depth: 8, mode: modeMultichannel}.bytes(),
			says: "multichannel mode",
		},
		{
			name: "colour mode 42",
			src:  file{channels: 3, width: 1, height: 1, depth: 8, mode: 42}.bytes(),
			says: "colour mode 42",
		},
		{
			name: "bitmap mode at eight bits",
			src:  file{channels: 1, width: 1, height: 1, depth: 8, mode: modeBitmap}.bytes(),
			says: "bitmap mode at 8 bits",
		},
		{
			name: "bitmap mode with three channels",
			src:  file{channels: 3, width: 1, height: 1, depth: 1, mode: modeBitmap}.bytes(),
			says: "bitmap mode with 3 channels",
		},
		{
			name: "indexed mode at sixteen bits",
			src:  file{channels: 1, width: 1, height: 1, depth: 16, mode: modeIndexed}.bytes(),
			says: "indexed mode at 16 bits",
		},
		{
			name: "one channel for an RGB file",
			src:  file{channels: 1, width: 1, height: 1, depth: 8, mode: modeRGB}.bytes(),
			says: "1 channels, and colour mode 3 needs 3",
		},
		{
			name: "an indexed file with half a palette",
			src: file{channels: 1, width: 1, height: 1, depth: 8, mode: modeIndexed,
				palette: make([]byte, 384), planes: [][]byte{{0}}}.bytes(),
			says: "384 bytes of palette, not 768",
		},
		{
			name: "a ZIP-compressed composite",
			src: file{channels: 3, width: 1, height: 1, depth: 8, mode: modeRGB,
				compression: compressionZip}.bytes(),
			says: "ZIP-compressed (2)",
		},
		{
			name: "compression 9",
			src: file{channels: 3, width: 1, height: 1, depth: 8, mode: modeRGB,
				compression: 9}.bytes(),
			says: "compression 9",
		},
		{
			name: "a file that stops before its pixels",
			src: file{channels: 3, width: 4, height: 4, depth: 8, mode: modeRGB,
				planes: [][]byte{solid(16, 1), solid(16, 2), solid(16, 3)}, truncate: 20}.bytes(),
			says: "ends in the middle of the composite",
		},
		{
			name: "a file that stops before its header is done",
			src:  file{channels: 3, width: 4, height: 4, depth: 8, mode: modeRGB, truncate: 4}.bytes(),
			says: "ends in the middle of the composite",
		},
		{
			// ⛔ Twenty-six bytes of something else. The shorter cases above
			// fail on the READ and this one on the SIGNATURE, which are two
			// different lines and only one of them was reached.
			name:  "twenty-six bytes that are not a Photoshop file",
			src:   []byte("this is plainly not a PSD, it is prose"),
			isFmt: true,
		},
		{
			name: "a palette the file does not deliver",
			src: file{channels: 1, width: 1, height: 1, depth: 8, mode: modeIndexed,
				palette: make([]byte, 768), planes: [][]byte{{0}}, truncate: 700}.bytes(),
			says: "ends in the middle of the composite",
		},
		{
			name: "a layer section the file does not deliver",
			src: file{channels: 3, width: 1, height: 1, depth: 8, mode: modeRGB,
				layerLen: 4096, planes: [][]byte{{1}, {2}, {3}}}.bytes(),
			says: "ends in the middle of the composite",
		},
		{
			name: "a file that stops where the compression word should be",
			src: file{channels: 3, width: 1, height: 1, depth: 8, mode: modeRGB,
				stopAfter: true}.bytes(),
			says: "ends in the middle of the composite",
		},
		{
			name: "a PSB that stops where the compression word should be",
			src: file{psb: true, channels: 3, width: 1, height: 1, depth: 8, mode: modeRGB,
				stopAfter: true}.bytes(),
			says: "ends in the middle of the composite",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(bytes.NewReader(tc.src))
			if err == nil {
				t.Fatal("accepted")
			}
			if tc.isFmt {
				if err != image.ErrFormat {
					t.Fatalf("error = %v, want image.ErrFormat so the registry tries another codec", err)
				}
				return
			}
			if err == image.ErrFormat {
				t.Fatal("ErrFormat: the registry will report \"unknown format\" instead of this")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error = %v, want it to say %q", err, tc.says)
			}
			if !strings.HasPrefix(err.Error(), "psd:") {
				t.Errorf("error = %v, want it to name this package", err)
			}
		})
	}
}

// ⛔ MaxPixels is what stands between a 26-byte header and a gigabyte. The
// variable is lowered here, which is the only reason a test can reach it.
func TestMaxPixels(t *testing.T) {
	old := MaxPixels
	defer func() { MaxPixels = old }()
	MaxPixels = 100

	src := file{channels: 3, width: 20, height: 20, depth: 8, mode: modeRGB}.bytes()
	_, err := Decode(bytes.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "400 pixels, over the 100") {
		t.Fatalf("error = %v, want the pixel ceiling", err)
	}
	// And the section lengths are bounded by it too: a colour mode section
	// claiming more than the whole budget is refused before it is allocated.
	big := file{channels: 1, width: 1, height: 1, depth: 8, mode: modeGrayscale,
		palette: make([]byte, 200), planes: [][]byte{{0}}}.bytes()
	_, err = Decode(bytes.NewReader(big))
	if err == nil || !strings.Contains(err.Error(), "200 byte section, over the 100") {
		t.Fatalf("error = %v, want the section ceiling", err)
	}
}

// ⛔ A PSB may declare a length that does not fit in an int64. The guard is
// cheap and the alternative is a negative length handed to io.CopyN.
func TestAnAbsurdPSBSectionLength(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("8BPS")
	be16(&b, 2)
	b.Write(make([]byte, 6))
	be16(&b, 3)
	be32(&b, 1)
	be32(&b, 1)
	be16(&b, 8)
	be16(&b, modeRGB)
	be32(&b, 0) // colour mode data
	be32(&b, 0) // image resources
	b.Write([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	_, err := Decode(bytes.NewReader(b.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "section length") {
		t.Fatalf("error = %v, want the length refused", err)
	}
}
