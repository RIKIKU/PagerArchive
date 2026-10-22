package icon

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The test icons are made up here, from drawn shapes and generated pixels,
// never from a real logo (CLAUDE.md, ADR 0011). go test -run TestFixtures
// -update writes them to testdata/icons; without -update the committed
// files must match what this code makes.
var update = flag.Bool("update", false, "write the test icons to testdata/icons")

const fixtures = "../../testdata/icons"

var (
	navy   = color.NRGBA{0x1f, 0x2a, 0x44, 0xff}
	yellow = color.NRGBA{0xf2, 0xc2, 0x30, 0xff}
	red    = color.NRGBA{0xc6, 0x28, 0x28, 0xff}
	green  = color.NRGBA{0x2e, 0x7d, 0x32, 0xff}
	blue   = color.NRGBA{0x15, 0x65, 0xc0, 0xff}
	clear  = color.NRGBA{}
)

// drawn is a made-up icon: a circle of the given colour with a yellow
// triangle in it, on transparency.
func drawn(size int, circle color.NRGBA) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	for y := range size {
		for x := range size {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			dx, dy := fx-s/2, fy-s/2
			if dx*dx+dy*dy > s*s*0.45*0.45 {
				continue
			}
			c := circle
			// A triangle pointing up, from 0.3 to 0.72 of the height.
			if fy > s*0.3 && fy < s*0.72 && abs(dx) < (fy-s*0.3)*0.6 {
				c = yellow
			}
			m.SetNRGBA(x, y, c)
		}
	}
	return m
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return m
}

func encodePNG(t testing.TB, m image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func chunk(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), data...)))
}

// withChunk inserts a chunk straight after the PNG's IHDR.
func withChunk(p []byte, typ string, data []byte) []byte {
	const afterIHDR = 8 + 8 + 13 + 4
	out := append([]byte{}, p[:afterIHDR]...)
	out = append(out, chunk(typ, data)...)
	return append(out, p[afterIHDR:]...)
}

// claimingPNG is a tiny PNG whose header claims w×h RGBA pixels, with a few
// bytes of image data.
func claimingPNG(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	idat := []byte{0x78, 0x9c, 0x63, 0x60, 0x00, 0x00, 0x00, 0x02, 0x00, 0x01}
	out := append([]byte{}, pngSignature...)
	out = append(out, chunk("IHDR", ihdr)...)
	out = append(out, chunk("IDAT", idat)...)
	return append(out, chunk("IEND", nil)...)
}

// noise is a PNG of random pixels, which doesn't compress: 3 bytes a pixel,
// as they are opaque. 260×260 is about 200 KB.
func noise(t testing.TB, side int) []byte {
	m := image.NewNRGBA(image.Rect(0, 0, side, side))
	r := rand.New(rand.NewPCG(10, 2026))
	for i := range m.Pix {
		m.Pix[i] = byte(r.Uint32())
	}
	for i := 3; i < len(m.Pix); i += 4 {
		m.Pix[i] = 0xff
	}
	return encodePNG(t, m)
}

func animatedGIF(t testing.TB) []byte {
	g := &gif.GIF{}
	for _, c := range []color.NRGBA{blue, red, green} {
		p := image.NewPaletted(image.Rect(0, 0, 32, 32), color.Palette{clear, c, yellow})
		src := drawn(32, c)
		for y := range 32 {
			for x := range 32 {
				p.Set(x, y, src.At(x, y))
			}
		}
		g.Image = append(g.Image, p)
		g.Delay = append(g.Delay, 50)
		g.Disposal = append(g.Disposal, gif.DisposalBackground)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// jpegWithExif is a JPEG with an APP1 Exif segment holding marker text.
func jpegWithExif(t testing.TB, m image.Image, marker string) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	app1 := append([]byte("Exif\x00\x00"), marker...)
	seg := binary.BigEndian.AppendUint16([]byte{0xff, 0xe1}, uint16(len(app1)+2))
	out := append([]byte{0xff, 0xd8}, seg...)
	out = append(out, app1...)
	return append(out, b.Bytes()[2:]...)
}

// claimingJPEG is a real JPEG whose frame header is changed to claim w×h.
func claimingJPEG(t testing.TB, w, h uint16) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, solid(8, 8, red), nil); err != nil {
		t.Fatal(err)
	}
	j := b.Bytes()
	i := bytes.Index(j, []byte{0xff, 0xc0})
	binary.BigEndian.PutUint16(j[i+5:], h)
	binary.BigEndian.PutUint16(j[i+7:], w)
	return j
}

// claimingGIF is a GIF header claiming a w×h canvas, with one 1×1 frame.
func claimingGIF(w, h uint16) []byte {
	out := []byte("GIF89a")
	out = binary.LittleEndian.AppendUint16(out, w)
	out = binary.LittleEndian.AppendUint16(out, h)
	out = append(out, 0x80, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff) // 2-colour global table
	out = append(out, 0x2c, 0, 0, 0, 0, 1, 0, 1, 0, 0, 2, 2, 0x4c, 0x01, 0, 0x3b)
	return out
}

// bitWriter writes bits least significant first, as VP8L reads them.
type bitWriter struct {
	b    []byte
	n    uint
	acc  uint64
	bits uint
}

func (w *bitWriter) write(v uint64, n uint) {
	w.acc |= v << w.bits
	w.bits += n
	for w.bits >= 8 {
		w.b = append(w.b, byte(w.acc))
		w.acc >>= 8
		w.bits -= 8
	}
}

func (w *bitWriter) bytes() []byte {
	if w.bits > 0 {
		return append(w.b, byte(w.acc))
	}
	return w.b
}

func riff(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body)))
	return append(out, body...)
}

func webpChunk(id string, data []byte) []byte {
	out := binary.LittleEndian.AppendUint32([]byte(id), uint32(len(data)))
	out = append(out, data...)
	if len(data)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// losslessWebP is a w×h WebP of one colour: VP8L with a one-symbol prefix
// code for each channel, so every pixel takes no bits.
func losslessWebP(w, h int, c color.NRGBA) []byte {
	var bw bitWriter
	bw.write(0x2f, 8)
	bw.write(uint64(w-1), 14)
	bw.write(uint64(h-1), 14)
	bw.write(1, 1) // alpha is used
	bw.write(0, 3) // version
	bw.write(0, 1) // no transform
	bw.write(0, 1) // no colour cache
	bw.write(0, 1) // no meta prefix codes
	for _, sym := range []uint8{c.G, c.R, c.B, c.A, 0} {
		bw.write(1, 1) // a simple code
		bw.write(0, 1) // one symbol
		bw.write(1, 1) // of 8 bits
		bw.write(uint64(sym), 8)
	}
	return riff(webpChunk("VP8L", bw.bytes()))
}

// vp8x is a WebP extended header claiming a w×h canvas, with flags.
func vp8x(w, h int, flags byte) []byte {
	d := []byte{flags, 0, 0, 0}
	d = append(d, byte(w-1), byte((w-1)>>8), byte((w-1)>>16), byte(h-1), byte((h-1)>>8), byte((h-1)>>16))
	return riff(webpChunk("VP8X", d))
}

// icoEntry is one image in an ICO: its directory's width, height and bit
// count, and its data (a PNG or a DIB).
type icoEntry struct {
	w, h int
	bits int
	data []byte
}

func makeICO(entries ...icoEntry) []byte {
	out := []byte{0, 0, 1, 0}
	out = binary.LittleEndian.AppendUint16(out, uint16(len(entries)))
	off := 6 + 16*len(entries)
	for _, e := range entries {
		out = append(out, byte(e.w), byte(e.h), 0, 0)
		out = binary.LittleEndian.AppendUint16(out, 1)
		out = binary.LittleEndian.AppendUint16(out, uint16(e.bits))
		out = binary.LittleEndian.AppendUint32(out, uint32(len(e.data)))
		out = binary.LittleEndian.AppendUint32(out, uint32(off))
		off += len(e.data)
	}
	for _, e := range entries {
		out = append(out, e.data...)
	}
	return out
}

// dib writes m as an ICO bitmap of the given bit count, with an AND mask
// for transparency. Paletted depths take m's colours, which must fit.
// alpha false writes 32-bit pixels with zero alpha (old-style).
func dib(m *image.NRGBA, bits int, alpha bool) []byte {
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	var pal []color.NRGBA
	index := map[color.NRGBA]int{}
	if bits <= 8 {
		pal = append(pal, color.NRGBA{A: 0xff})
		index[color.NRGBA{A: 0xff}] = 0
		for y := range h {
			for x := range w {
				c := m.NRGBAAt(x, y)
				c.A = 0xff
				if _, ok := index[c]; !ok {
					index[c] = len(pal)
					pal = append(pal, c)
				}
			}
		}
	}
	out := binary.LittleEndian.AppendUint32(nil, 40)
	out = binary.LittleEndian.AppendUint32(out, uint32(w))
	out = binary.LittleEndian.AppendUint32(out, uint32(2*h))
	out = binary.LittleEndian.AppendUint16(out, 1)
	out = binary.LittleEndian.AppendUint16(out, uint16(bits))
	out = append(out, make([]byte, 16)...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(pal)))
	out = append(out, make([]byte, 4)...)
	for _, c := range pal {
		out = append(out, c.B, c.G, c.R, 0)
	}
	stride := (w*bits + 31) / 32 * 4
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, stride)
		for x := range w {
			c := m.NRGBAAt(x, y)
			switch bits {
			case 32:
				a := c.A
				if !alpha {
					a = 0
				}
				copy(row[4*x:], []byte{c.B, c.G, c.R, a})
			case 24:
				copy(row[3*x:], []byte{c.B, c.G, c.R})
			default:
				c.A = 0xff
				per := 8 / bits
				row[x/per] |= byte(index[c] << (8 - bits*(x%per+1)))
			}
		}
		out = append(out, row...)
	}
	maskStride := (w + 31) / 32 * 4
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, maskStride)
		for x := range w {
			if m.NRGBAAt(x, y).A < 0x80 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		out = append(out, row...)
	}
	return out
}

// fixtureFiles makes every test icon in testdata/icons.
func fixtureFiles(t testing.TB) map[string][]byte {
	icon := encodePNG(t, drawn(32, navy))
	return map[string][]byte{
		// A57: the icon uploaded and shown.
		"example-icon.png": icon,
		// A58: a text chunk that mustn't survive.
		"not-kept.png": withChunk(icon, "tEXt", []byte("Comment\x00NOT-KEPT")),
		"example.svg": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32">
  <circle cx="16" cy="16" r="14" fill="#1f2a44"/>
  <path d="M16 10 L21 22 H11 Z" fill="#f2c230"/>
</svg>
`),
		"large-200kb.png":  noise(t, 260),
		"not-an-image.png": []byte("This is a text file, not an image. It was only renamed .png.\n"),
		// Four sizes; 64×64 (green) is the one to pick, as a 32-bit bitmap.
		"several-sizes.ico": makeICO(
			icoEntry{16, 16, 32, dib(drawn(16, red), 32, true)},
			icoEntry{32, 32, 8, dib(drawn(32, red), 8, false)},
			icoEntry{64, 64, 32, dib(drawn(64, green), 32, true)},
			icoEntry{0, 0, 32, encodePNG(t, drawn(256, red))},
		),
		// Three frames: blue, red, green. The first is the icon.
		"animated.gif": animatedGIF(t),
		// About a hundred bytes claiming 10000×10000 pixels (400 MB decoded).
		"huge-canvas.png": claimingPNG(10000, 10000),
		// The other accepted types, also made up.
		"with-exif.jpg": jpegWithExif(t, drawn(32, navy), "NOT-KEPT-EXIF"),
		"wide.webp":     losslessWebP(40, 20, blue),
	}
}

func TestFixtures(t *testing.T) {
	for name, want := range fixtureFiles(t) {
		path := filepath.Join(fixtures, name)
		if *update {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run go test ./internal/icon -run TestFixtures -update)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from what the test makes (run go test ./internal/icon -run TestFixtures -update)", name)
		}
	}
	if size := len(fixtureFiles(t)["large-200kb.png"]); size < 195<<10 || size > 205<<10 {
		t.Errorf("large-200kb.png is %d bytes", size)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pngChunks lists a PNG's chunk types in order.
func pngChunks(t *testing.T, p []byte) []string {
	t.Helper()
	if !bytes.HasPrefix(p, pngSignature) {
		t.Fatal("not a PNG")
	}
	var out []string
	for i := len(pngSignature); i+8 <= len(p); {
		n := int(binary.BigEndian.Uint32(p[i:]))
		out = append(out, string(p[i+4:i+8]))
		i += 12 + n
	}
	return out
}

// converted decodes Convert's output, checking it's a fresh 64×64 PNG with
// only the chunks Go's encoder writes.
func converted(t *testing.T, in []byte) *image.NRGBA {
	t.Helper()
	out, err := Convert(in)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	for _, c := range pngChunks(t, out) {
		if c != "IHDR" && c != "IDAT" && c != "IEND" && c != "PLTE" && c != "tRNS" {
			t.Errorf("chunk %s kept", c)
		}
	}
	m, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if m.Bounds() != image.Rect(0, 0, Size, Size) {
		t.Fatalf("size %v", m.Bounds())
	}
	n := image.NewNRGBA(m.Bounds())
	for y := range Size {
		for x := range Size {
			n.Set(x, y, m.At(x, y))
		}
	}
	return n
}

func near(c, want color.NRGBA) bool {
	d := func(a, b uint8) bool { return a-b < 24 || b-a < 24 }
	return d(c.R, want.R) && d(c.G, want.G) && d(c.B, want.B) && d(c.A, want.A)
}

func TestAccepted(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
		at   image.Point
		want color.NRGBA
	}{
		{"PNG", fixture(t, "example-icon.png"), image.Pt(32, 6), navy},
		{"PNG centre", fixture(t, "example-icon.png"), image.Pt(32, 40), yellow},
		{"transparent corner", fixture(t, "example-icon.png"), image.Pt(1, 1), clear},
		{"JPEG", fixture(t, "with-exif.jpg"), image.Pt(32, 6), navy},
		{"WebP", fixture(t, "wide.webp"), image.Pt(32, 32), blue},
		{"ICO", fixture(t, "several-sizes.ico"), image.Pt(32, 6), green},
		{"animated GIF, first frame", fixture(t, "animated.gif"), image.Pt(32, 6), blue},
		{"large PNG scaled down", encodePNG(t, drawn(1024, green)), image.Pt(32, 6), green},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := converted(t, c.in).NRGBAAt(c.at.X, c.at.Y); !near(got, c.want) {
				t.Errorf("pixel %v is %v, want about %v", c.at, got, c.want)
			}
		})
	}
}

// Not stretched: a 40×20 image becomes 64×32, centred, with 16 transparent
// rows above and below; a tall one the same sideways.
func TestAspectRatioKept(t *testing.T) {
	wide := converted(t, fixture(t, "wide.webp"))
	tall := converted(t, encodePNG(t, solid(20, 40, red)))
	for _, y := range []int{0, 15, 48, 63} {
		if a := wide.NRGBAAt(32, y).A; a != 0 {
			t.Errorf("wide: row %d alpha %d, want transparent padding", y, a)
		}
		if a := tall.NRGBAAt(y, 32).A; a != 0 {
			t.Errorf("tall: column %d alpha %d, want transparent padding", y, a)
		}
	}
	for _, y := range []int{17, 32, 46} {
		if got := wide.NRGBAAt(32, y); !near(got, blue) {
			t.Errorf("wide: row %d is %v", y, got)
		}
		if got := tall.NRGBAAt(y, 32); !near(got, red) {
			t.Errorf("tall: column %d is %v", y, got)
		}
	}
}

// A58: nothing from the uploaded file is kept, only its pixels.
func TestMetadataNotKept(t *testing.T) {
	for _, name := range []string{"not-kept.png", "with-exif.jpg"} {
		in := fixture(t, name)
		if !bytes.Contains(in, []byte("NOT-KEPT")) {
			t.Fatalf("%s has no marker", name)
		}
		out, err := Convert(in)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out, []byte("NOT-KEPT")) {
			t.Errorf("%s: the marker survived", name)
		}
		if bytes.Equal(out, in) {
			t.Errorf("%s: stored as uploaded", name)
		}
	}
	// Even a file that's already a 64×64 PNG is encoded afresh.
	in := withChunk(encodePNG(t, drawn(64, navy)), "tEXt", []byte("Comment\x00NOT-KEPT"))
	out, err := Convert(in)
	if err != nil || bytes.Contains(out, []byte("NOT-KEPT")) || pngChunks(t, out)[1] != "IDAT" {
		t.Errorf("a 64×64 PNG kept its text: %v", err)
	}
}

func refusal(t *testing.T, in []byte) *Refusal {
	t.Helper()
	_, err := Convert(in)
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal, got %v", err)
	}
	return r
}

func TestRefused(t *testing.T) {
	for _, c := range []struct {
		name, code string
		in         []byte
	}{
		{"empty", "icon_empty", nil},
		{"SVG", "icon_svg", fixture(t, "example.svg")},
		{"SVG without a declaration", "icon_svg", []byte(string(rune(0xFEFF)) + "  <svg xmlns='http://www.w3.org/2000/svg'/>")},
		{"text renamed .png", "icon_type", fixture(t, "not-an-image.png")},
		{"HTML", "icon_type", []byte("<!doctype html><title>x</title>")},
		{"a URL", "icon_url", []byte("https://example.test/icon.png")},
		{"a URL with spaces", "icon_url", []byte("  HTTP://example.test/icon.png\n")},
		{"200 KB", "icon_too_large", fixture(t, "large-200kb.png")},
		{"one byte too many", "icon_too_large", make([]byte, MaxBytes+1)},
		{"PNG claiming 10000×10000", "icon_dimensions", fixture(t, "huge-canvas.png")},
		{"PNG claiming 1025×1", "icon_dimensions", claimingPNG(1025, 1)},
		{"JPEG claiming 60000×60000", "icon_dimensions", claimingJPEG(t, 60000, 60000)},
		{"GIF claiming 60000×60000", "icon_dimensions", claimingGIF(60000, 60000)},
		{"WebP claiming 16000×16000", "icon_dimensions", vp8x(16000, 16000, 0)},
		{"animated WebP", "icon_animated_webp", vp8x(32, 32, 0x02)},
		{"ICO bitmap claiming 30000×30000", "icon_dimensions", makeICO(icoEntry{0, 0, 32, func() []byte {
			d := dib(solid(4, 4, red), 32, true)
			binary.LittleEndian.PutUint32(d[4:], 30000)
			binary.LittleEndian.PutUint32(d[8:], 60000)
			return d
		}()})},
		{"ICO PNG claiming 10000×10000", "icon_dimensions", makeICO(icoEntry{0, 0, 32, claimingPNG(10000, 10000)})},
		{"ICO entry past the end", "icon_unreadable", func() []byte {
			b := makeICO(icoEntry{16, 16, 32, dib(solid(16, 16, red), 32, true)})
			binary.LittleEndian.PutUint32(b[6+12:], 1<<20)
			return b
		}()},
		{"ICO with no images", "icon_unreadable", []byte{0, 0, 1, 0, 0, 0}},
		{"cut-off PNG", "icon_unreadable", fixture(t, "example-icon.png")[:60]},
		{"PNG signature only", "icon_unreadable", pngSignature},
		{"JPEG marker only", "icon_unreadable", []byte{0xff, 0xd8, 0xff, 0xe0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := refusal(t, c.in)
			if r.Code != c.code {
				t.Errorf("code %s (%s), want %s", r.Code, r.Msg, c.code)
			}
			if r.Msg == "" || strings.Contains(r.Msg, "%!") {
				t.Errorf("message %q", r.Msg)
			}
		})
	}
	if r := refusal(t, fixture(t, "huge-canvas.png")); !strings.Contains(r.Msg, "10000×10000") || !strings.Contains(r.Msg, "1024×1024") {
		t.Errorf("message %q", r.Msg)
	}
}

// The declared size is refused before anything that size is allocated.
func TestHugeCanvasNotDecoded(t *testing.T) {
	in := fixture(t, "huge-canvas.png")
	if len(in) > 200 {
		t.Fatalf("the fixture is %d bytes", len(in))
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := refusal(t, in)
	runtime.ReadMemStats(&after)
	if r.Code != "icon_dimensions" {
		t.Errorf("code %s", r.Code)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Errorf("allocated %d bytes for a refused file", n)
	}
}

// Only the chosen ICO image is decoded: the others can be anything.
func TestICOPicksOneImage(t *testing.T) {
	garbage := bytes.Repeat([]byte{0xee}, 200)
	b := makeICO(
		icoEntry{16, 16, 32, garbage},
		icoEntry{64, 64, 32, dib(drawn(64, green), 32, true)},
		icoEntry{0, 0, 32, garbage},
		icoEntry{128, 128, 32, garbage},
	)
	if got := converted(t, b).NRGBAAt(32, 6); !near(got, green) {
		t.Errorf("pixel %v", got)
	}
	// With nothing 64 or larger, the largest; with two of one size, the
	// deepest colour.
	b = makeICO(
		icoEntry{16, 16, 32, garbage},
		icoEntry{48, 48, 8, garbage},
		icoEntry{48, 48, 32, encodePNG(t, drawn(48, blue))},
	)
	if got := converted(t, b).NRGBAAt(32, 6); !near(got, blue) {
		t.Errorf("pixel %v", got)
	}
}

// Every bitmap depth an ICO may hold, with its transparency.
func TestICOBitmaps(t *testing.T) {
	src := drawn(48, red)
	for _, c := range []struct {
		name  string
		bits  int
		alpha bool
	}{{"32-bit with alpha", 32, true}, {"32-bit with a mask", 32, false}, {"24-bit", 24, false}, {"8-bit", 8, false}, {"4-bit", 4, false}} {
		t.Run(c.name, func(t *testing.T) {
			m := converted(t, makeICO(icoEntry{48, 48, c.bits, dib(src, c.bits, c.alpha)}))
			for _, p := range []struct {
				at   image.Point
				want color.NRGBA
			}{{image.Pt(32, 6), red}, {image.Pt(32, 40), yellow}, {image.Pt(1, 1), clear}} {
				if got := m.NRGBAAt(p.at.X, p.at.Y); !near(got, p.want) {
					t.Errorf("pixel %v is %v, want about %v", p.at, got, p.want)
				}
			}
		})
	}
	// 1-bit: black and white.
	bw := solid(32, 32, color.NRGBA{A: 0xff})
	for x := 16; x < 32; x++ {
		for y := range 32 {
			bw.SetNRGBA(x, y, color.NRGBA{0xff, 0xff, 0xff, 0xff})
		}
	}
	m := converted(t, makeICO(icoEntry{32, 32, 1, dib(bw, 1, false)}))
	if !near(m.NRGBAAt(8, 32), color.NRGBA{A: 0xff}) || !near(m.NRGBAAt(56, 32), color.NRGBA{0xff, 0xff, 0xff, 0xff}) {
		t.Errorf("1-bit: %v %v", m.NRGBAAt(8, 32), m.NRGBAAt(56, 32))
	}
}

// Any bytes give an icon or a refusal, never a panic.
func FuzzConvert(f *testing.F) {
	for _, b := range fixtureFiles(f) {
		f.Add(b)
	}
	f.Add(makeICO(icoEntry{16, 16, 4, dib(drawn(16, red), 4, false)}))
	f.Fuzz(func(t *testing.T, b []byte) {
		out, err := Convert(b)
		var r *Refusal
		if err != nil && !errors.As(err, &r) {
			t.Fatalf("not a refusal: %v", err)
		}
		if err == nil {
			if c, err := png.DecodeConfig(bytes.NewReader(out)); err != nil || c.Width != Size || c.Height != Size {
				t.Fatalf("output %v %v", c, err)
			}
		}
	})
}
