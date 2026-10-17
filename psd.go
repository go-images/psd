package psd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
)

// MaxPixels is the largest composite this package will allocate, in pixels.
//
// ⛔ A VARIABLE and not a constant, and that is not a style choice: a ceiling
// measured in hundreds of megabytes cannot be reached by a test that has to
// run in a second, so a constant here would be a guard nobody could exercise.
// A test lowers it; nothing else should.
//
// ⛔ It exists because a PSD declares its size in the header and the RLE
// scanline table lets a tiny file claim an enormous picture: 30 000 × 30 000
// is inside the specification's own limit and is 900 million pixels, which as
// RGBA is 3.6 GB. The declaration is a number in a file, and a reader that
// believes it allocates what it is told to.
var MaxPixels = 64 << 20

// Colour modes, from the file header.
const (
	modeBitmap       = 0
	modeGrayscale    = 1
	modeIndexed      = 2
	modeRGB          = 3
	modeCMYK         = 4
	modeMultichannel = 7
	modeDuotone      = 8
	modeLab          = 9
)

// Compression of the image data section.
const (
	compressionRaw          = 0
	compressionRLE          = 1
	compressionZip          = 2
	compressionZipPredicted = 3
)

// ErrFormat is returned when the stream is not a Photoshop file at all. It
// matches [image.ErrFormat] so that image.Decode's registry treats it the same
// way as every other codec's "not mine".
var ErrFormat = image.ErrFormat

// a header is the file's own description of itself.
type header struct {
	psb      bool // the large-document variant: 8-byte lengths, 4-byte RLE counts
	channels int
	height   int
	width    int
	depth    int
	mode     int
	palette  color.Palette // Indexed mode only
}

// rowBytes is how many bytes one scanline of one channel occupies.
func (h header) rowBytes() int {
	switch h.depth {
	case 1:
		return (h.width + 7) / 8
	case 16:
		return h.width * 2
	}
	return h.width
}

// Decode reads the composite image of a Photoshop file.
func Decode(r io.Reader) (image.Image, error) {
	h, err := readHeader(r)
	if err != nil {
		return nil, err
	}
	planes, err := readPlanes(r, h)
	if err != nil {
		return nil, err
	}
	return assemble(h, planes)
}

// DecodeConfig reads the dimensions and colour model without the pixels.
//
// It is what [image.Decode]'s documentation asks a caller to use first on
// untrusted input, and it reads only the header and the colour table: the
// picture's size is known 26 bytes in.
func DecodeConfig(r io.Reader) (image.Config, error) {
	h, err := readHeader(r)
	if err != nil {
		return image.Config{}, err
	}
	return image.Config{
		ColorModel: colorModel(h),
		Width:      h.width,
		Height:     h.height,
	}, nil
}

func init() {
	// ⛔ The magic is the signature alone. The next field is the version, and
	// matching it here would make a PSB look like "not a Photoshop file"
	// rather than like one this package can read — which it can.
	image.RegisterFormat("psd", "8BPS", Decode, DecodeConfig)
}

// readHeader reads the 26-byte header, then the colour mode data (which holds
// the palette of an Indexed file), then skips the image resources and the
// layer and mask information to leave the reader on the image data section.
func readHeader(r io.Reader) (header, error) {
	var buf [26]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return header{}, ErrFormat
	}
	if string(buf[0:4]) != "8BPS" {
		return header{}, ErrFormat
	}
	version := int(binary.BigEndian.Uint16(buf[4:6]))
	h := header{
		psb:      version == 2,
		channels: int(binary.BigEndian.Uint16(buf[12:14])),
		height:   int(binary.BigEndian.Uint32(buf[14:18])),
		width:    int(binary.BigEndian.Uint32(buf[18:22])),
		depth:    int(binary.BigEndian.Uint16(buf[22:24])),
		mode:     int(binary.BigEndian.Uint16(buf[24:26])),
	}
	if version != 1 && version != 2 {
		// ⛔ A version this does not know is NOT ErrFormat: the signature says
		// it is a Photoshop file, so "not mine" would send image.Decode on to
		// try every other codec and report the wrong thing entirely.
		return header{}, fmt.Errorf("psd: version %d, which is neither PSD (1) nor PSB (2)", version)
	}
	if err := h.check(); err != nil {
		return header{}, err
	}

	// The colour mode data: a palette for Indexed, nothing for most modes.
	colourData, err := readSection(r, 4)
	if err != nil {
		return header{}, err
	}
	if h.mode == modeIndexed {
		if len(colourData) != 768 {
			return header{}, fmt.Errorf("psd: an indexed file with %d bytes of palette, not 768", len(colourData))
		}
		// ⛔ PLANAR, like everything else in this format: 256 reds, then 256
		// greens, then 256 blues. Read as triples it comes out as a gradient
		// of reds, which looks like a corrupt image rather than like a
		// misread palette.
		h.palette = make(color.Palette, 256)
		for i := range 256 {
			h.palette[i] = color.NRGBA{
				R: colourData[i], G: colourData[256+i], B: colourData[512+i], A: 0xff,
			}
		}
	}

	// The image resources, and then the layer and mask information: neither
	// says anything about the composite, and both can be large.
	if _, err := readSection(r, 4); err != nil {
		return header{}, err
	}
	layerLen := 4
	if h.psb {
		layerLen = 8
	}
	if err := skipSection(r, layerLen); err != nil {
		return header{}, err
	}
	return h, nil
}

// check refuses what the header itself says is impossible, and what this
// package will not do.
func (h header) check() error {
	if h.channels < 1 || h.channels > 56 {
		return fmt.Errorf("psd: %d channels, outside the 1 to 56 the format allows", h.channels)
	}
	if h.width < 1 || h.height < 1 {
		return fmt.Errorf("psd: %d by %d pixels", h.width, h.height)
	}
	limit := 30000
	if h.psb {
		limit = 300000
	}
	if h.width > limit || h.height > limit {
		return fmt.Errorf("psd: %d by %d pixels, over the %d the format allows", h.width, h.height, limit)
	}
	// ⛔ Multiplied as int64 before being compared: on a 32-bit build
	// 300000*300000 overflows an int, and a guard that overflows is a guard
	// that waves the bomb through. This package is built for 386 and arm.
	if int64(h.width)*int64(h.height) > int64(MaxPixels) {
		return fmt.Errorf("psd: %d by %d is %d pixels, over the %d this package will allocate",
			h.width, h.height, int64(h.width)*int64(h.height), MaxPixels)
	}
	switch h.depth {
	case 1, 8, 16:
	case 32:
		return errors.New("psd: 32 bits a channel (floating point), which this package does not read")
	default:
		return fmt.Errorf("psd: %d bits a channel, which the format does not allow", h.depth)
	}
	switch h.mode {
	case modeBitmap:
		if h.depth != 1 {
			return fmt.Errorf("psd: bitmap mode at %d bits a channel", h.depth)
		}
	case modeGrayscale, modeIndexed, modeRGB, modeCMYK, modeDuotone, modeLab:
	case modeMultichannel:
		return errors.New("psd: multichannel mode, which has no fixed meaning for a composite")
	default:
		return fmt.Errorf("psd: colour mode %d, which the format does not define", h.mode)
	}
	if h.mode == modeIndexed && h.depth != 8 {
		return fmt.Errorf("psd: indexed mode at %d bits a channel", h.depth)
	}
	if h.mode == modeBitmap && h.channels != 1 {
		return fmt.Errorf("psd: bitmap mode with %d channels", h.channels)
	}
	if need := channelsFor(h.mode); h.channels < need {
		return fmt.Errorf("psd: %d channels, and colour mode %d needs %d", h.channels, h.mode, need)
	}
	return nil
}

// channelsFor is how many channels a mode's colour needs, before any alpha.
func channelsFor(mode int) int {
	switch mode {
	case modeRGB, modeLab:
		return 3
	case modeCMYK:
		return 4
	}
	return 1
}

// readSection reads a length-prefixed section whole.
func readSection(r io.Reader, lenBytes int) ([]byte, error) {
	n, err := readLength(r, lenBytes)
	if err != nil {
		return nil, err
	}
	// ⛔ Bounded by MaxPixels as well, because the colour mode data and the
	// image resources are lengths in the file like any other: a 4 GiB claim
	// here costs the same as one in the header.
	if n > int64(MaxPixels) {
		return nil, fmt.Errorf("psd: a %d byte section, over the %d this package will allocate", n, MaxPixels)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, unexpected(err)
	}
	return b, nil
}

// skipSection reads past a length-prefixed section without holding it.
//
// ⛔ io.CopyN to io.Discard rather than a Seek: this takes an io.Reader, and
// the layer section of a working file is most of the file. A reader that
// buffered it to skip it would hold a hundred megabytes to reach the last
// three.
func skipSection(r io.Reader, lenBytes int) error {
	n, err := readLength(r, lenBytes)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(io.Discard, r, n); err != nil {
		return unexpected(err)
	}
	return nil
}

func readLength(r io.Reader, lenBytes int) (int64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:lenBytes]); err != nil {
		return 0, unexpected(err)
	}
	if lenBytes == 8 {
		n := binary.BigEndian.Uint64(buf[:8])
		if n > 1<<62 {
			return 0, fmt.Errorf("psd: a section length of %d bytes", n)
		}
		return int64(n), nil
	}
	return int64(binary.BigEndian.Uint32(buf[:4])), nil
}

// unexpected turns a truncated read into something that says so.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("psd: the file ends in the middle of the composite: %w", err)
	}
	return fmt.Errorf("psd: %w", err)
}

// readPlanes reads the image data section: one plane per channel, in order.
func readPlanes(r io.Reader, h header) ([][]byte, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, unexpected(err)
	}
	compression := int(binary.BigEndian.Uint16(buf[:]))

	rowBytes := h.rowBytes()
	switch compression {
	case compressionRaw:
		planes := make([][]byte, h.channels)
		for c := range planes {
			plane := make([]byte, rowBytes*h.height)
			if _, err := io.ReadFull(r, plane); err != nil {
				return nil, unexpected(err)
			}
			planes[c] = plane
		}
		return planes, nil

	case compressionRLE:
		return readRLE(r, h, rowBytes)

	case compressionZip, compressionZipPredicted:
		// ⛔ Named rather than passed over: ZIP compression appears in LAYER
		// channel data, and Photoshop does not write it for the composite. A
		// file that does is a file somebody else wrote, and the gap is worth
		// knowing about rather than arriving as "not a Photoshop file".
		return nil, fmt.Errorf("psd: the composite is ZIP-compressed (%d), which this package does not read", compression)
	}
	return nil, fmt.Errorf("psd: compression %d, which the format does not define", compression)
}

// colorModel is the model a mode and depth decode into.
func colorModel(h header) color.Model {
	switch h.mode {
	case modeIndexed:
		return h.palette
	case modeBitmap:
		return color.GrayModel
	case modeGrayscale, modeDuotone:
		if h.channels > 1 {
			return alphaModel(h.depth)
		}
		if h.depth == 16 {
			return color.Gray16Model
		}
		return color.GrayModel
	case modeCMYK:
		if h.channels > 4 {
			return color.NRGBAModel
		}
		return color.CMYKModel
	}
	// RGB and Lab.
	return alphaModel(h.depth)
}

func alphaModel(depth int) color.Model {
	if depth == 16 {
		return color.NRGBA64Model
	}
	return color.NRGBAModel
}
