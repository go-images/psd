# psd

**Adobe Photoshop (`.psd`) and large-document (`.psb`) files, for
`image.Decode`.** Pure Go, CGO-free, including `GOOS=js`, with no dependency
outside the standard library.

```go
import _ "github.com/go-images/psd"

m, _, err := image.Decode(f)   // a .psd now decodes like a .png
```

## ⛔ The composite, and deliberately nothing else

A Photoshop file holds two different pictures: the **layers** an editor works
with, and the flattened **composite** Photoshop writes at the end of the file
so that anything which is not Photoshop has something to show. This package
reads the second one.

That is not a shortcut, it is the job. Reading layers means understanding blend
modes, clipping, masks, adjustment layers, smart objects and layer effects —
re-implementing a compositor — and the result would still differ from what
Photoshop shows. **The composite is what Photoshop shows.**

What a caller loses is the ability to turn a layer off. What a caller gains is
the picture, at full resolution, from about 600 lines of Go.

| | |
|---|---|
| colour mode | Bitmap, Grayscale, Indexed, RGB, CMYK, Duotone, Lab |
| depth | 1, 8 and 16 bits a channel |
| compression | raw, and RLE (PackBits) |
| container | PSD and PSB, which differ only in their length fields |

Alpha is honoured wherever the mode has room for it: a second channel of a
grayscale file, a fourth of an RGB one, a fifth of a CMYK one.

## ⛔ The four decisions that make or break a PSD reader

**1. Everything is planar.** All of the red, then all of the green, then all of
the blue — scanline counts included. An interleaved read produces the same
recognisable failure every time: a picture in vertical bands of one colour
each.

**2. CMYK is stored inverted.** 255 means *no ink*. A reader that takes the
bytes as written turns a white page black. There is no flag in the file saying
so; it is simply how the format is, which is why the test for it asserts that a
page with no ink comes back white.

**3. PackBits, read as signed.** `0…127` copies the next *n+1* bytes;
`-1…-127` repeats the next byte *1−n* times; `-128` is a no-operation, and
Photoshop writes it. A summary that says "128…255 means 1 to 129 repetitions"
is off by one for every run in the file — which looks like a slight shear
rather than like a broken decoder, so it survives a casual look.

**4. Alpha is straight, not premultiplied.** The result is `NRGBA`, because a
PSD's alpha is unassociated. Handing straight bytes to a premultiplied image
makes every semi-transparent pixel too bright, and the difference is invisible
on an opaque file — which is how it survives a casual look at an icon.

## ⛔ The limits are the security surface

```go
psd.MaxPixels = 64 << 20   // the default
```

A PSD declares its size in a 26-byte header, and the RLE scanline table lets a
tiny file claim an enormous picture: 30 000 × 30 000 is inside the format's own
limit and is 900 million pixels — 3.6 GB as RGBA. The declaration is a number
in a file, and a reader that believes it allocates what it is told to.

- the pixel count is multiplied as `int64` **before** being compared, because
  `300000*300000` overflows an `int` on a 32-bit build and a guard that
  overflows waves the bomb through. This package builds for `386` and `arm`;
- a declared scanline longer than PackBits could ever produce for that row is
  refused, which is what stops a per-row allocation from being a lie;
- the length-prefixed sections are bounded by the same ceiling, because a
  length in the colour-mode section costs exactly what one in the header does;
- the layer section — most of a working file — is skipped with `io.CopyN` to
  `io.Discard` rather than buffered, so reading the last three megabytes of a
  file does not mean holding the first hundred.

**32-bit (floating-point) channels, a ZIP-compressed composite and
Multichannel mode are refused by name.** A refusal that names its reason is
something somebody can close; "unsupported format" is not.

`ErrFormat` is returned only for "this is not a Photoshop file", so
`image.Decode` goes on to try the other codecs. Everything else — a version 3
file, a 32-bit file, a truncated one — comes back as a named error, because a
PSB reported as "unknown format" sends the reader to look at the wrong thing
entirely.

## Measured against an independent decoder

⛔ **A decoder checked only against its own fixtures is checked against its
author's reading of the specification**, which is the thing most likely to be
wrong. macOS ships `sips`, whose PSD support comes from ImageIO and has nothing
to do with this code, so the corpus test converts each file with `sips` and
compares pixel for pixel.

On 2026-10-17, over the Photoshop files on one machine:

```text
read    example.psd    1920x1080  *image.NRGBA
        judged against sips: 0 of 2073600 pixels differ (0.0000%)
read    icon.psd        256x256   *image.NRGBA
        judged against sips: 81 of 65536 pixels differ, worst channel delta 1
REFUSED servers-….psd   image: unknown format
```

- **exact** on 2 073 600 pixels of an RGB file;
- 81 pixels of an antialiased icon edge differ by **one count**, which is
  rounding in the premultiply;
- the refused file is **a JPEG with a `.psd` name** — the same shape as the 27
  PNGs named `.pdf` in go-pdfkit's corpus. Refusing it is correct.

⛔ **And the first version of that comparison was wrong in a way that blamed
this package.** It compared `At().RGBA()` on both sides — and `RGBA()`
premultiplies by definition, while `sips` has *already* written premultiplied
values into its PNG, so the reference was premultiplied twice. It reported 207
pixels differing with a worst delta of 59, every one of them on an antialiased
edge. Measured rather than argued: of the 2 661 channels that differ when the
two are compared as straight values, **zero** differ once this side is
premultiplied once and `sips`'s bytes are read as they stand. The file's RGB is
straight, this package is right, and the disagreement was the measuring
instrument. The judge and the subject have to be in the same alpha space.

⛔ **There is no `.psd` in `testdata`, deliberately**: the files measured are
one machine owner's own and third parties' artwork, and a fixture a test ships
has to be one the repository may carry. The corpus is read from wherever the
person running the test keeps theirs, and the test **skips loudly** without it:

```sh
PSD_CORPUS=~/Pictures go test -run TestARealCorpus -v ./...
```

## ⛔ Two conversions no real file exercised

**Bitmap mode** (1 bit, where Photoshop writes 0 for *white* — it stores ink,
not light) and **Lab mode** (which Photoshop writes against a **D50** white
point, where sRGB's own D65 would tilt the grey axis towards blue) are
implemented from the specification and tested against fixtures written here.
No file in the measured corpus is in either mode, so they are marked as read
from the specification rather than presented as measured.

## How it is checked

- 100% of statements, **no uncovered block**, race detector, eleven build
  targets including `386`, `arm` and `js/wasm`.
- The fixture builder in the test writes PSDs, *including* a PackBits packer —
  so `unpack` is checked against an independent implementation of the format
  rather than against itself, and the two round-trip over data that exercises
  both branches.
- ⛔ The coverage profile found the first real hole: **the repeat-run branch was
  never exercised**, because the builder only emits a repeat for a run of three
  or more and every earlier fixture had runs of two. A PSD of a photograph is
  mostly literals; a PSD of a screenshot or a logo is mostly repeats — the
  untested half was the half that files of the commonest kind are made of.
- ⛔ The mutation sweep found the second, which is a cross-architecture
  hazard: the clamp in the sRGB transfer function survived deletion, because
  every Lab fixture was inside the gamut. **Lab holds colours sRGB cannot**, so
  a legal triple gives a negative linear value — and Go says a float-to-integer
  conversion the target cannot represent is *implementation-dependent*: 0 on
  one architecture and 251 on another. It is now clamped at both ends before
  the conversion, with an out-of-gamut fixture that pins it.

## Licence

BSD-3-Clause. Written against the *Adobe Photoshop File Formats
Specification*; no Adobe code is used or needed.
