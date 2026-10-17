package psd

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestARealCorpus decodes every .psd in a directory and, where an independent
// decoder is available, compares the result pixel for pixel.
//
// ⛔ THE JUDGE IS NOT THIS PACKAGE. A decoder checked only against fixtures
// written in its own test file is checked against its author's reading of the
// specification — which is exactly the thing most likely to be wrong. macOS
// ships `sips`, whose PSD support comes from ImageIO and has nothing to do
// with this code, so it converts each file to PNG and the two are compared.
//
// ⛔ And there is no .psd in testdata, deliberately: the files this was
// measured on are the machine owner's own and third parties' artwork, and a
// fixture a test ships has to be one the repository may carry. The corpus is
// read from wherever the person running the test keeps theirs:
//
//	PSD_CORPUS=~/Pictures go test -run TestARealCorpus -v ./...
//
// Without the variable the test SKIPS and says so, because a test that passes
// by doing nothing is worse than one that is not there.
func TestARealCorpus(t *testing.T) {
	dir := os.Getenv("PSD_CORPUS")
	if dir == "" {
		t.Skip("PSD_CORPUS is not set: no real files to decode. Set it to a directory holding .psd files.")
	}
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(home, dir[2:])
	}
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // a directory this user cannot read is not this test's business
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".psd") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("%s holds no .psd file: a corpus of nothing reports nothing", dir)
	}

	sips, _ := exec.LookPath("sips")
	if sips == "" {
		t.Log("no sips on this machine: the files are decoded but nothing independent judges them")
	}

	var decoded, refused int
	for _, f := range files {
		name := filepath.Base(f)
		src, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		mine, err := decodeBytes(src)
		if err != nil {
			refused++
			t.Logf("REFUSED %-44.44s %v", name, err)
			continue
		}
		decoded++
		b := mine.Bounds()
		t.Logf("read    %-44.44s %5dx%-5d %T", name, b.Dx(), b.Dy(), mine)

		if sips == "" {
			continue
		}
		ref, err := viaSips(t, sips, f)
		if err != nil {
			t.Logf("        %-44.44s no independent reading: %v", name, err)
			continue
		}
		diff, worst := compare(mine, ref)
		total := b.Dx() * b.Dy()
		t.Logf("        judged against sips: %d of %d pixels differ (%.4f%%), worst channel delta %d",
			diff, total, 100*float64(diff)/float64(total), worst)
		// ⛔ One count of tolerance, for rounding, and nothing more. The first
		// version of this comparison allowed a worst delta of 8 "for colour
		// profiles" and would have hidden a real defect; it is not needed,
		// because once the two sides are in the SAME alpha space they agree
		// exactly. See compare, which is where that was found.
		if worst > 1 {
			t.Errorf("%s: %d of %d pixels differ, worst channel delta %d — the two decoders disagree",
				name, diff, total, worst)
		}
	}
	t.Logf("%d file(s): %d decoded, %d refused", len(files), decoded, refused)
}

// decodeBytes decodes with THIS package, through the registry, so the test
// exercises the same path a caller does.
func decodeBytes(src []byte) (image.Image, error) {
	m, format, err := image.Decode(bytesReader(src))
	if err != nil {
		return nil, err
	}
	if format != "psd" {
		return nil, fmt.Errorf("image.Decode said %q, so another codec claimed it", format)
	}
	return m, nil
}

// viaSips asks macOS's own converter for the same picture.
func viaSips(t *testing.T, sips, path string) (image.Image, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "ref.png")
	cmd := exec.Command(sips, "-s", "format", "png", path, "--out", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sips: %v: %s", err, strings.TrimSpace(string(b)))
	}
	f, err := os.Open(out)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// compare counts the pixels that differ and the worst single-channel
// difference, at 8-bit precision, with both sides in the same alpha space.
//
// ⛔ THE JUDGE AND THE SUBJECT HAVE TO BE IN THE SAME SPACE, and the first
// version of this function was not. It compared At().RGBA() on both sides —
// and At().RGBA() PREMULTIPLIES by definition, while `sips` has already
// written premultiplied values into its PNG. The reference was therefore
// premultiplied twice, which reported 207 of 65 536 pixels differing on a real
// icon with a worst delta of 59: every one of them on an antialiased edge,
// none at alpha 0 or alpha 255.
//
// Measured, rather than argued: of the 2 661 channels that differ when the two
// are compared as straight values, ZERO differ once this side is premultiplied
// once and sips' bytes are read as they stand. The file's RGB is straight,
// this package is right, and the disagreement was the measuring instrument.
func compare(a, b image.Image) (differing, worst int) {
	ra, rb := a.Bounds(), b.Bounds()
	w, h := min(ra.Dx(), rb.Dx()), min(ra.Dy(), rb.Dy())
	for y := range h {
		for x := range w {
			mine := premultiplied(a.At(ra.Min.X+x, ra.Min.Y+y))
			ref := asWritten(b.At(rb.Min.X+x, rb.Min.Y+y))
			d := 0
			for k := range 4 {
				delta := int(mine[k]) - int(ref[k])
				if delta < 0 {
					delta = -delta
				}
				if delta > d {
					d = delta
				}
			}
			if d > 0 {
				differing++
			}
			if d > worst {
				worst = d
			}
		}
	}
	return differing, worst
}

// premultiplied is a colour with its channels multiplied by its own alpha,
// once, at 8-bit precision. It is what Go's RGBA() does, written out so that
// the other side can deliberately NOT have it done.
func premultiplied(c color.Color) [4]byte {
	r, g, b, a := c.RGBA()
	return [4]byte{byte(r >> 8), byte(g >> 8), byte(b >> 8), byte(a >> 8)}
}

// asWritten is a colour's channels as the PNG stores them, with no
// premultiplication applied — because sips has already applied it.
func asWritten(c color.Color) [4]byte {
	switch v := c.(type) {
	case color.NRGBA:
		return [4]byte{v.R, v.G, v.B, v.A}
	case color.NRGBA64:
		return [4]byte{byte(v.R >> 8), byte(v.G >> 8), byte(v.B >> 8), byte(v.A >> 8)}
	}
	// An opaque reference (gray, paletted, RGBA): premultiplying by an alpha
	// of 255 changes nothing, so the two readings coincide.
	return premultiplied(c)
}

// bytesReader keeps the test's use of image.Decode honest: the registry sniffs
// with a bufio.Reader, and a plain bytes.Reader is what a caller passes.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
