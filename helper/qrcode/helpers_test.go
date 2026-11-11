package qrcode

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/makiuchi-d/gozxing"
	zxqr "github.com/makiuchi-d/gozxing/qrcode"
)

// decodeImage reads a QR Code with gozxing (a port of ZXing), an encoder-
// independent decoder used only by the tests.
func decodeImage(img image.Image) (string, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", err
	}
	res, err := zxqr.NewQRCodeReader().Decode(bmp, map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true})
	if err != nil {
		return "", err
	}
	return res.GetText(), nil
}

func decodePNG(t *testing.T, data []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	// ZXing wants an opaque image; composite over white like a scanner would.
	b := img.Bounds()
	flat := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			a := uint32(c.A)
			flat.Set(x, y, color.RGBA{
				uint8((uint32(c.R)*a + 255*(255-a)) / 255),
				uint8((uint32(c.G)*a + 255*(255-a)) / 255),
				uint8((uint32(c.B)*a + 255*(255-a)) / 255), 255})
		}
	}
	s, err := decodeImage(flat)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s
}

// matrixImage draws a matrix at scale px per module with a 4-module quiet zone.
func matrixImage(m [][]bool, scale int) image.Image {
	n := len(m)
	side := (n + 8) * scale
	img := image.NewGray(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := range n {
		for x := range n {
			if !m[y][x] {
				continue
			}
			for dy := range scale {
				for dx := range scale {
					img.SetGray((x+4)*scale+dx, (y+4)*scale+dy, color.Gray{0})
				}
			}
		}
	}
	return img
}

// testLogoPNG is a small two-tone logo drawn in code.
func testLogoPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if (x-w/2)*(x-w/2)+(y-h/2)*(y-h/2) < (min(w, h)/2)*(min(w, h)/2) {
				img.SetNRGBA(x, y, color.NRGBA{200, 30, 30, 255})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{30, 30, 200, 90})
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testLogoJPEG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			img.Set(x, y, color.RGBA{uint8(x * 4), 80, uint8(y * 5), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const testLogoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
  <rect x="5" y="5" width="90" height="90" rx="20" fill="#c81e1e"/>
  <circle cx="50" cy="50" r="25" fill="white"/>
  <path d="M30 50 L50 30 L70 50 L50 70 Z" fill="#1e1ec8"/>
</svg>`
