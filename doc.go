// Package psd decodes the composite image of an Adobe Photoshop file (.psd)
// or a large-document one (.psb).
//
// It registers itself with [image.Decode], so importing it for its side
// effect is enough:
//
//	import _ "github.com/go-images/psd"
//
//	m, _, err := image.Decode(f)   // a .psd now decodes like a .png
//
// # ⛔ The COMPOSITE, and deliberately nothing else
//
// A Photoshop file holds two different pictures: the layers an editor works
// with, and the flattened composite Photoshop writes at the END of the file so
// that anything which is not Photoshop has something to show. This package
// reads the second one.
//
// That is not a shortcut, it is the job. Reading layers means understanding
// blend modes, clipping, masks, adjustment layers, smart objects and layer
// effects — re-implementing a compositor — and the result would still differ
// from what Photoshop shows. The composite IS what Photoshop shows.
//
// What a caller loses is the ability to turn a layer off. What a caller gains
// is the picture, at full resolution, from a package with no dependencies
// outside the standard library.
//
// # What it reads
//
//	colour mode    Bitmap, Grayscale, Indexed, RGB, CMYK, Duotone, Lab
//	depth          1, 8 and 16 bits a channel
//	compression    raw, and RLE (PackBits)
//	container      PSD and PSB, which differ in their length fields
//
// Alpha is honoured where the mode has room for it: a second channel of a
// grayscale file, a fourth of an RGB one, a fifth of a CMYK one.
//
// ⛔ CMYK is stored INVERTED by Photoshop — 255 means no ink — and is
// un-inverted here. That inversion is the single most common way a CMYK reader
// comes out looking like a photographic negative.
//
// # What it refuses, by name
//
// 32-bit (floating-point) channels, a ZIP-compressed composite, and the
// Multichannel mode, each with an error that says which. A refusal that names
// its reason is something somebody can close; "unsupported format" is not.
//
// # ⛔ Two conversions no file in the measured corpus exercises
//
// Bitmap mode (1 bit, where Photoshop writes 0 for WHITE) and Lab mode (which
// Photoshop writes against a D50 white point) are implemented from the
// specification and tested against fixtures written here — which is to say
// against this package's own reading of the specification, and not against a
// file Photoshop produced. They are marked as such rather than presented as
// measured. Everything else was checked against real files, pixel for pixel,
// using an independent decoder as the judge.
//
// Nothing here is CGO, and it builds for GOOS=js.
package psd
