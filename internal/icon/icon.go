// Package icon turns an uploaded template icon into the one form PagerArchive
// stores: a 64×64 PNG encoded here, keeping nothing else from the uploaded
// file (R65, ADR 0011). The file's type is decided from its bytes, never its
// name or Content-Type, and an image's declared size is checked before it is
// decoded, so a small file can't claim a canvas that takes gigabytes to
// decode.
package icon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	// MaxBytes is the largest file accepted: 100 KB (R65).
	MaxBytes = 100 << 10
	// MaxSide is the largest width or height an image may declare. A
	// 1024×1024 image decodes to at most 4 MB.
	MaxSide = 1024
	// Size is the stored icon's width and height.
	Size = 64
)

// Refusal is why a file wasn't accepted as an icon, in words an admin can
// act on. Code is stable for clients.
type Refusal struct {
	Code string
	Msg  string
}

func (r *Refusal) Error() string { return r.Msg }

func refuse(code, format string, a ...any) *Refusal {
	return &Refusal{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// TooLarge is the refusal for a file over MaxBytes.
func TooLarge() *Refusal {
	return refuse("icon_too_large", "An icon file can be at most 100 KB, and this one is larger. Make it smaller (64×64 pixels is enough) and upload it again.")
}

func unreadable(format string) *Refusal {
	return refuse("icon_unreadable", "This file starts like a %s image but couldn't be read: it may be damaged. Save it again in an image editor and upload that.", format)
}

// one makes conversions run one at a time, so uploads at the same moment
// can't add up to more than one decoded image's memory.
var one sync.Mutex

// Convert checks an uploaded file and returns a new 64×64 PNG made from it:
// the image scaled to fit, keeping its aspect ratio, centred on a
// transparent square. An animated GIF gives its first frame, and an ICO its
// one image best suited to 64×64. A refused file gives a *Refusal.
func Convert(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, refuse("icon_empty", "The file is empty. Choose an image file.")
	}
	if len(data) > MaxBytes {
		return nil, TooLarge()
	}
	one.Lock()
	defer one.Unlock()
	img, err := decode(data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, fit(img)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// decode reads the image after deciding its type from its first bytes.
func decode(b []byte) (image.Image, error) {
	switch {
	case bytes.HasPrefix(b, pngSignature):
		return decodeChecked(b, "PNG", png.DecodeConfig, png.Decode)
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return decodeGIF(b)
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return decodeChecked(b, "JPEG", jpeg.DecodeConfig, jpeg.Decode)
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		// An animated WebP's frames are chunks x/image doesn't read.
		if len(b) >= 21 && string(b[12:16]) == "VP8X" && b[20]&0x02 != 0 {
			return nil, refuse("icon_animated_webp", "Animated WebP icons aren't accepted. Save one frame as a PNG and upload that.")
		}
		return decodeChecked(b, "WebP", webp.DecodeConfig, webp.Decode)
	case len(b) >= 6 && b[0] == 0 && b[1] == 0 && b[2] == 1 && b[3] == 0:
		return decodeICO(b)
	}
	return nil, unknown(b)
}

// unknown says why a file that's none of the accepted types was refused.
func unknown(b []byte) *Refusal {
	head := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(b[:min(len(b), 1024)]), string(rune(0xFEFF)))))
	switch {
	case strings.HasPrefix(head, "http://"), strings.HasPrefix(head, "https://"):
		return refuse("icon_url", "PagerArchive never fetches icons from other sites. Download the image and upload the file (the PowerShell module's -Uri does that on your own computer).")
	case strings.Contains(head, "<svg"):
		return refuse("icon_svg", "SVG icons aren't accepted, because an SVG file can hold scripts and links to other sites. Save it as a PNG and upload that.")
	}
	return refuse("icon_type", "Icons must be PNG, ICO, GIF, JPEG or WebP images, and this file is none of those. Its type is read from its contents, not its name.")
}

func checkSize(w, h int) *Refusal {
	if w < 1 || h < 1 {
		return refuse("icon_unreadable", "This image has no pixels.")
	}
	if w > MaxSide || h > MaxSide {
		return refuse("icon_dimensions", "This image is %d×%d pixels, and an icon can be at most %d×%d. Make it smaller and upload it again.", w, h, MaxSide, MaxSide)
	}
	return nil
}

// decodeChecked reads the image's declared size, refuses one too large, and
// only then decodes it. The decoders used allocate by the size their
// DecodeConfig reports (ADR 0011).
func decodeChecked(b []byte, format string, config func(io.Reader) (image.Config, error), dec func(io.Reader) (image.Image, error)) (image.Image, error) {
	c, err := config(bytes.NewReader(b))
	if err != nil {
		return nil, unreadable(format)
	}
	if r := checkSize(c.Width, c.Height); r != nil {
		return nil, r
	}
	m, err := dec(bytes.NewReader(b))
	if err != nil {
		return nil, unreadable(format)
	}
	return m, nil
}

// decodeGIF gives the first frame, placed on the GIF's canvas. gif.Decode
// stops after the first frame and refuses frames larger than the canvas.
func decodeGIF(b []byte) (image.Image, error) {
	c, err := gif.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, unreadable("GIF")
	}
	if r := checkSize(c.Width, c.Height); r != nil {
		return nil, r
	}
	frame, err := gif.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, unreadable("GIF")
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, c.Width, c.Height))
	draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Src)
	return canvas, nil
}

// fit scales the image to fit a Size×Size square, keeping its aspect
// ratio, centred on transparency.
func fit(src image.Image) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, Size, Size))
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := Size, Size
	if w > h {
		dh = max(1, (h*Size+w/2)/w)
	} else if h > w {
		dw = max(1, (w*Size+h/2)/h)
	}
	x, y := (Size-dw)/2, (Size-dh)/2
	xdraw.CatmullRom.Scale(dst, image.Rect(x, y, x+dw, y+dh), src, b, xdraw.Src, nil)
	return dst
}

// decodeICO picks the one image best suited to a 64×64 icon from the ICO's
// directory and decodes only that one: a PNG, or a bitmap (DIB).
func decodeICO(b []byte) (image.Image, error) {
	bad := unreadable("ICO")
	n := int(binary.LittleEndian.Uint16(b[4:]))
	if n == 0 || len(b) < 6+16*n {
		return nil, bad
	}
	best, bestSide, bestBits := -1, 0, 0
	for i := range n {
		e := b[6+16*i:]
		side := max(entrySide(e[0]), entrySide(e[1]))
		bits := int(binary.LittleEndian.Uint16(e[6:]))
		if best < 0 || better(side, bits, bestSide, bestBits) {
			best, bestSide, bestBits = i, side, bits
		}
	}
	e := b[6+16*best:]
	size := uint64(binary.LittleEndian.Uint32(e[8:]))
	off := uint64(binary.LittleEndian.Uint32(e[12:]))
	if size == 0 || off+size > uint64(len(b)) {
		return nil, bad
	}
	img := b[off : off+size]
	if bytes.HasPrefix(img, pngSignature) {
		return decodeChecked(img, "ICO", png.DecodeConfig, png.Decode)
	}
	return decodeDIB(img)
}

// entrySide is an ICO directory entry's width or height: 0 means 256.
func entrySide(v byte) int {
	if v == 0 {
		return 256
	}
	return int(v)
}

// better reports whether an ICO entry suits a Size×Size icon better than the
// best so far: the smallest at least Size wide, or else the largest; then
// the most colours.
func better(side, bits, bestSide, bestBits int) bool {
	switch {
	case side == bestSide:
		return bits > bestBits
	case side >= Size && bestSide >= Size:
		return side < bestSide
	case side >= Size || bestSide >= Size:
		return side >= Size
	}
	return side > bestSide
}

// decodeDIB reads an ICO's bitmap: a BITMAPINFOHEADER, a palette for 1, 4
// and 8 bits per pixel, the pixel rows bottom-up, then the 1-bit AND mask
// (1 is transparent). The header's height covers both, so it's doubled.
func decodeDIB(b []byte) (image.Image, error) {
	bad := unreadable("ICO")
	le16 := func(o int) int { return int(binary.LittleEndian.Uint16(b[o:])) }
	le32 := func(o int) int64 { return int64(int32(binary.LittleEndian.Uint32(b[o:]))) }
	if len(b) < 40 {
		return nil, bad
	}
	hdr, w, h2, bits, compression, used := le32(0), le32(4), le32(8), le16(14), le32(16), le32(32)
	if hdr < 40 || hdr > int64(len(b)) || compression != 0 || w <= 0 || h2 <= 0 || h2%2 != 0 {
		return nil, bad
	}
	if r := checkSize(int(min(w, MaxSide+1)), int(min(h2/2, MaxSide+1))); r != nil {
		return nil, r
	}
	width, height, p := int(w), int(h2/2), int(hdr)
	var palette []color.NRGBA
	switch bits {
	case 1, 4, 8:
		n := int(used)
		if n <= 0 || n > 1<<bits {
			n = 1 << bits
		}
		if len(b) < p+4*n {
			return nil, bad
		}
		palette = make([]color.NRGBA, n)
		for i := range palette {
			palette[i] = color.NRGBA{R: b[p+4*i+2], G: b[p+4*i+1], B: b[p+4*i], A: 0xff}
		}
		p += 4 * n
	case 24, 32:
	default:
		return nil, bad
	}
	stride := (width*bits + 31) / 32 * 4
	maskStride := (width + 31) / 32 * 4
	mask := p + stride*height
	hasMask := len(b) >= mask+maskStride*height
	if len(b) < mask || (bits != 32 && !hasMask) {
		return nil, bad
	}
	m := image.NewNRGBA(image.Rect(0, 0, width, height))
	ownAlpha := false
	for y := range height {
		row := b[p+(height-1-y)*stride:]
		for x := range width {
			var c color.NRGBA
			switch bits {
			case 32:
				c = color.NRGBA{R: row[4*x+2], G: row[4*x+1], B: row[4*x], A: row[4*x+3]}
				ownAlpha = ownAlpha || c.A != 0
			case 24:
				c = color.NRGBA{R: row[3*x+2], G: row[3*x+1], B: row[3*x], A: 0xff}
			default:
				per := 8 / bits
				i := int(row[x/per]>>(8-bits*(x%per+1))) & (1<<bits - 1)
				if i >= len(palette) {
					return nil, bad
				}
				c = palette[i]
			}
			m.SetNRGBA(x, y, c)
		}
	}
	// A 32-bit image with alpha of its own ignores the mask. One whose alpha
	// is all zero is opaque except where the mask says otherwise.
	if bits == 32 && ownAlpha {
		return m, nil
	}
	for y := range height {
		for x := range width {
			a := uint8(0xff)
			if hasMask && b[mask+(height-1-y)*maskStride+x/8]>>(7-x%8)&1 == 1 {
				a = 0
			}
			m.Pix[m.PixOffset(x, y)+3] = a
		}
	}
	return m, nil
}
