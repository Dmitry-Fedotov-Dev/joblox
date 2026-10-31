package shop

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders for uploads
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Photo sizes: the full picture for the product page and the thumbnail for the grid. Both are JPEGs encoded
// from the pixels alone, so whatever the camera put into the file (place, device, time) is left behind.
const (
	FullSide  = 1600
	ThumbSide = 720
	MaxUpload = 25 << 20
	maxPixels = 60_000_000 // a decoded picture bigger than this is refused before it eats the memory
)

var ErrNotImage = errors.New("это не фото: нужен JPEG, PNG, WebP или GIF")

var fileRe = regexp.MustCompile(`^[0-9a-f]{24}(_t)?\.jpg$`)

// ValidFile says whether name is a photo file this package wrote; the web server serves nothing else.
func ValidFile(name string) bool { return fileRe.MatchString(name) }

// Photos writes processed pictures into a directory.
type Photos struct {
	Dir string
}

// Save decodes an upload, turns it upright by its EXIF orientation, writes the full picture and the
// thumbnail and returns the image record (without ids).
func (ph *Photos) Save(r io.Reader) (Image, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxUpload+1))
	if err != nil {
		return Image{}, err
	}
	if len(raw) > MaxUpload {
		return Image{}, fmt.Errorf("фото больше %d МБ", MaxUpload>>20)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return Image{}, ErrNotImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return Image{}, errors.New("фото слишком большое по размеру в пикселях")
	}
	// a phone photo decodes to tens of megabytes: the decoded picture lives only inside upright, and the
	// memory goes back when done, so a dozen photos fit into a small container one after another
	defer debug.FreeOSMemory()
	full, err := upright(raw)
	if err != nil {
		return Image{}, err
	}
	if err := os.MkdirAll(ph.Dir, 0o750); err != nil {
		return Image{}, err
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return Image{}, err
	}
	name := hex.EncodeToString(id)
	if err := writeJPEG(filepath.Join(ph.Dir, name+".jpg"), full, 85); err != nil {
		return Image{}, err
	}
	if err := writeJPEG(filepath.Join(ph.Dir, name+"_t.jpg"), fit(full, ThumbSide), 80); err != nil {
		_ = os.Remove(filepath.Join(ph.Dir, name+".jpg"))
		return Image{}, err
	}
	b := full.Bounds()
	return Image{File: name, W: b.Dx(), H: b.Dy()}, nil
}

// upright decodes the upload and returns it scaled to FullSide and turned by its EXIF orientation. Scale
// first, then turn: the longer side stays the longer side, and turning a small picture is cheap.
func upright(raw []byte) (image.Image, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrNotImage
	}
	return orient(fit(src, FullSide), exifOrientation(raw)), nil
}

// shrink brings a big picture close to side by averaging blocks of pixels, reading a JPEG's YCbCr planes
// directly. A 12-megapixel phone photo then costs its decoded planes and one small RGBA copy, instead of
// the hundreds of megabytes the general scaler spends on it.
func shrink(src image.Image, side int) image.Image {
	b := src.Bounds()
	k := max(b.Dx(), b.Dy()) / side
	if k < 2 {
		return src
	}
	w, h := b.Dx()/k, b.Dy()/k
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	yc, isYCbCr := src.(*image.YCbCr)
	n := uint32(k * k)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					px, py := b.Min.X+x*k+dx, b.Min.Y+y*k+dy
					if isYCbCr {
						yi, ci := yc.YOffset(px, py), yc.COffset(px, py)
						cr, cg, cb := color.YCbCrToRGB(yc.Y[yi], yc.Cb[ci], yc.Cr[ci])
						r, g, bl = r+uint32(cr), g+uint32(cg), bl+uint32(cb)
						continue
					}
					cr, cg, cb, _ := src.At(px, py).RGBA()
					r, g, bl = r+cr>>8, g+cg>>8, bl+cb>>8
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return dst
}

// Remove deletes both files of a photo; missing files are fine.
func (ph *Photos) Remove(file string) {
	for _, n := range []string{file + ".jpg", file + "_t.jpg"} {
		if ValidFile(n) {
			_ = os.Remove(filepath.Join(ph.Dir, n))
		}
	}
}

func writeJPEG(path string, img image.Image, quality int) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// fit scales the picture down so its longer side is at most side; smaller pictures are only copied onto an
// opaque canvas (JPEG has no transparency: transparent parts become white).
func fit(src image.Image, side int) image.Image {
	// average blocks down to near the size, then a light bilinear step: no buffer the size of the picture
	src = shrink(src, side)
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > side || h > side {
		if w >= h {
			w, h = side, max(1, h*side/b.Dx())
		} else {
			w, h = max(1, w*side/b.Dy()), side
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// exifOrientation reads the orientation tag (1–8) of a JPEG; 1 when there is none.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // image data starts: no EXIF before it
			return 1
		}
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation, so the picture stands as the phone showed it.
func orient(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	rgba, ok := src.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	}
	b := rgba.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			si, di := rgba.PixOffset(b.Min.X+x, b.Min.Y+y), dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], rgba.Pix[si:si+4])
		}
	}
	return dst
}

// SmallPNG turns an uploaded picture (a bank's QR from a phone, say) into a PNG no bigger than side: turned
// upright, scaled down, light enough to keep in the site's settings.
func SmallPNG(raw []byte, side int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrNotImage
	}
	img := orient(fit(src, side), exifOrientation(raw))
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	if out.Len() > 900<<10 { // a photo rather than a clean QR: a JPEG keeps it light
		out.Reset()
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 88}); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}
