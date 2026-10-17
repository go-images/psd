package psd

import (
	"image"
	"image/color"
	"math"
)

// assemble turns the planar channels into one picture.
//
// ⛔ PLANAR IS THE WHOLE SHAPE OF THIS FORMAT: all of the red, then all of the
// green, then all of the blue. Every interleaved read of a PSD produces the
// same recognisable failure — a picture in three vertical bands, each a single
// colour — which is why the assembling is one function with the channel order
// written down rather than a loop per mode scattered through the reader.
func assemble(h header, planes [][]byte) (image.Image, error) {
	rect := image.Rect(0, 0, h.width, h.height)
	switch h.mode {
	case modeBitmap:
		return bitmap(h, planes[0], rect), nil
	case modeIndexed:
		return indexed(h, planes[0], rect), nil
	case modeGrayscale, modeDuotone:
		return gray(h, planes, rect), nil
	case modeCMYK:
		return cmyk(h, planes, rect), nil
	case modeLab:
		return lab(h, planes, rect), nil
	}
	return rgb(h, planes, rect), nil
}

// bitmap is 1 bit a pixel.
//
// ⛔ ZERO IS WHITE. Photoshop's bitmap mode stores ink, not light: a set bit
// is black. Every other 1-bit format in this ecosystem is the other way round,
// and a reader that forgets comes out as a photographic negative — of a page
// of text, which is the one picture where that is obvious and the one place
// nobody looks.
//
// ⛔ Implemented from the specification and tested against a fixture written
// here. No file in the measured corpus is in bitmap mode, so this is read from
// the specification rather than from Photoshop's output; the package
// documentation says so.
func bitmap(h header, plane []byte, rect image.Rectangle) image.Image {
	out := image.NewGray(rect)
	stride := h.rowBytes()
	for y := range h.height {
		row := plane[y*stride : (y+1)*stride]
		for x := range h.width {
			bit := row[x/8] >> (7 - uint(x%8)) & 1
			v := byte(0xff)
			if bit == 1 {
				v = 0
			}
			out.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return out
}

// indexed is one byte a pixel into the palette the header carried.
func indexed(h header, plane []byte, rect image.Rectangle) image.Image {
	out := image.NewPaletted(rect, h.palette)
	for y := range h.height {
		copy(out.Pix[y*out.Stride:], plane[y*h.width:(y+1)*h.width])
	}
	return out
}

// gray is one channel, with a second one as alpha when the file has it.
func gray(h header, planes [][]byte, rect image.Rectangle) image.Image {
	if h.channels > 1 {
		// Grayscale with transparency: the only honest target is an RGBA
		// image, because Go has no gray-with-alpha.
		return withAlpha(h, rect, func(x, y int) (r, g, b uint32) {
			v := sample(h, planes[0], x, y)
			return v, v, v
		}, planes[1])
	}
	if h.depth == 16 {
		out := image.NewGray16(rect)
		for y := range h.height {
			for x := range h.width {
				out.SetGray16(x, y, color.Gray16{Y: uint16(sample(h, planes[0], x, y))})
			}
		}
		return out
	}
	out := image.NewGray(rect)
	for y := range h.height {
		copy(out.Pix[y*out.Stride:], planes[0][y*h.width:(y+1)*h.width])
	}
	return out
}

// rgb is three channels, with a fourth as alpha when the file has it.
func rgb(h header, planes [][]byte, rect image.Rectangle) image.Image {
	var alpha []byte
	if h.channels > 3 {
		alpha = planes[3]
	}
	return withAlpha(h, rect, func(x, y int) (r, g, b uint32) {
		return sample(h, planes[0], x, y), sample(h, planes[1], x, y), sample(h, planes[2], x, y)
	}, alpha)
}

// cmyk is four channels, INVERTED, with a fifth as alpha when the file has it.
//
// ⛔ 255 MEANS NO INK. Photoshop stores CMYK complemented, and a reader that
// takes the bytes as written produces a negative: a white page comes out
// black. There is no flag in the file saying so — it is simply how the format
// is — which is why this is the one conversion in the package with its own
// test asserting that a white pixel is white.
func cmyk(h header, planes [][]byte, rect image.Rectangle) image.Image {
	if h.channels > 4 {
		// CMYK with transparency: converted to RGBA, because image.CMYK has
		// no alpha and dropping it would lose what the file went out of its
		// way to carry.
		return withAlpha(h, rect, func(x, y int) (r, g, b uint32) {
			c := 0xffff - sample(h, planes[0], x, y)
			m := 0xffff - sample(h, planes[1], x, y)
			yy := 0xffff - sample(h, planes[2], x, y)
			k := 0xffff - sample(h, planes[3], x, y)
			r8, g8, b8 := color.CMYKToRGB(byte(c>>8), byte(m>>8), byte(yy>>8), byte(k>>8))
			return uint32(r8) * 0x101, uint32(g8) * 0x101, uint32(b8) * 0x101
		}, planes[4])
	}
	out := image.NewCMYK(rect)
	for y := range h.height {
		for x := range h.width {
			i := out.PixOffset(x, y)
			out.Pix[i+0] = byte((0xffff - sample(h, planes[0], x, y)) >> 8)
			out.Pix[i+1] = byte((0xffff - sample(h, planes[1], x, y)) >> 8)
			out.Pix[i+2] = byte((0xffff - sample(h, planes[2], x, y)) >> 8)
			out.Pix[i+3] = byte((0xffff - sample(h, planes[3], x, y)) >> 8)
		}
	}
	return out
}

// lab is Photoshop's Lab mode, converted to sRGB.
//
// ⛔ D50, not D65. Photoshop's Lab is relative to the D50 white point, and
// using sRGB's own D65 shifts every colour towards blue — a cast that looks
// like a slightly cold photograph rather than like a conversion error.
//
// ⛔ Implemented from the specification and tested against fixtures written
// here, like bitmap mode: no file in the measured corpus is in Lab mode.
func lab(h header, planes [][]byte, rect image.Rectangle) image.Image {
	var alpha []byte
	if h.channels > 3 {
		alpha = planes[3]
	}
	return withAlpha(h, rect, func(x, y int) (uint32, uint32, uint32) {
		// 8-bit Lab: L is 0…255 for 0…100, a and b are 0…255 for −128…127.
		// 16-bit Lab: L is 0…65535 for 0…100, a and b are offset by 32768.
		var l, a, bb float64
		if h.depth == 16 {
			l = float64(sample(h, planes[0], x, y)) * 100 / 65535
			a = float64(sample(h, planes[1], x, y))/257 - 128
			bb = float64(sample(h, planes[2], x, y))/257 - 128
		} else {
			l = float64(sample(h, planes[0], x, y)>>8) * 100 / 255
			a = float64(sample(h, planes[1], x, y)>>8) - 128
			bb = float64(sample(h, planes[2], x, y)>>8) - 128
		}
		r, g, b := labToSRGB(l, a, bb)
		return uint32(r) * 0x101, uint32(g) * 0x101, uint32(b) * 0x101
	}, alpha)
}

// labToSRGB is CIE L*a*b* (D50) to sRGB, with the Bradford adaptation to D65
// folded into the matrix.
func labToSRGB(l, a, b float64) (byte, byte, byte) {
	fy := (l + 16) / 116
	fx := fy + a/500
	fz := fy - b/200
	finv := func(t float64) float64 {
		if t3 := t * t * t; t3 > 0.008856 {
			return t3
		}
		return (t - 16.0/116) / 7.787
	}
	// D50 white point.
	x := 0.96422 * finv(fx)
	y := 1.00000 * finv(fy)
	z := 0.82521 * finv(fz)

	// XYZ (D50) to linear sRGB, Bradford-adapted.
	rl := 3.1338561*x - 1.6168667*y - 0.4906146*z
	gl := -0.9787684*x + 1.9161415*y + 0.0334540*z
	bl := 0.0719453*x - 0.2289914*y + 1.4052427*z

	// ⛔ CLAMPED BEFORE THE CONVERSION, both ends, and that is not belt and
	// braces: Lab holds colours sRGB cannot, so a legal Lab triple gives a
	// NEGATIVE linear value — and Go says a float-to-integer conversion whose
	// value the target cannot represent is IMPLEMENTATION-DEPENDENT. A
	// negative value handed to byte() may be 0 on one architecture and 251 on
	// another, which is a difference no single-architecture test can see. This
	// package builds for eleven targets.
	//
	// The mutation sweep is what found it: deleting the lower clamp changed
	// nothing any test could see, because every fixture was inside the gamut.
	gamma := func(v float64) byte {
		switch {
		case v <= 0.0031308:
			v *= 12.92
		default:
			v = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		return byte(math.Round(math.Max(0, math.Min(v, 1)) * 255))
	}
	return gamma(rl), gamma(gl), gamma(bl)
}

// withAlpha builds an RGBA picture from a colour function and an optional
// alpha plane.
//
// ⛔ NRGBA and not RGBA: a PSD's alpha is NOT premultiplied, and handing
// straight bytes to a premultiplied image makes every semi-transparent pixel
// too bright. The difference is invisible on an opaque file, which is how it
// survives a casual look at an icon.
func withAlpha(h header, rect image.Rectangle, colour func(x, y int) (r, g, b uint32), alpha []byte) image.Image {
	if h.depth == 16 {
		out := image.NewNRGBA64(rect)
		for y := range h.height {
			for x := range h.width {
				r, g, b := colour(x, y)
				a := uint32(0xffff)
				if alpha != nil {
					a = sample(h, alpha, x, y)
				}
				out.SetNRGBA64(x, y, color.NRGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)})
			}
		}
		return out
	}
	out := image.NewNRGBA(rect)
	for y := range h.height {
		for x := range h.width {
			r, g, b := colour(x, y)
			a := uint32(0xffff)
			if alpha != nil {
				a = sample(h, alpha, x, y)
			}
			out.SetNRGBA(x, y, color.NRGBA{R: byte(r >> 8), G: byte(g >> 8), B: byte(b >> 8), A: byte(a >> 8)})
		}
	}
	return out
}

// sample is one channel's value at one pixel, scaled to 16 bits so that every
// mode's arithmetic is written once.
func sample(h header, plane []byte, x, y int) uint32 {
	if h.depth == 16 {
		i := (y*h.width + x) * 2
		return uint32(plane[i])<<8 | uint32(plane[i+1])
	}
	v := uint32(plane[y*h.width+x])
	// ⛔ 0x101 and not 0x100: scaling a byte to 16 bits by shifting leaves
	// white at 0xff00 instead of 0xffff, so a white pixel comes back very
	// slightly grey and a round trip never closes.
	return v * 0x101
}
