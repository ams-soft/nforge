package qrcode_test

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/ams-soft/nforge/helper/qrcode"
)

func Example() {
	qr, err := qrcode.New("https://example.com")
	if err != nil {
		panic(err)
	}
	png, err := qr.PNG()
	if err != nil {
		panic(err)
	}
	fmt.Println(len(png) > 0, qr.Version(), qr.ErrorCorrection() == qrcode.Medium)
	// Output: true 2 true
}

func ExampleNew_styled() {
	qr, err := qrcode.New("https://example.com/pay",
		qrcode.WithSize(512),
		qrcode.WithColors(color.NRGBA{R: 20, G: 40, B: 90, A: 255}, color.White),
		qrcode.WithRoundedModules(0.25),
		qrcode.WithRoundedFinders(0.15),
	)
	if err != nil {
		panic(err)
	}
	svg, _ := qr.SVG()
	fmt.Println(strings.HasPrefix(string(svg), "<svg "), qr.Size())
	// Output: true 495
}

func ExampleQRCode_DataURI() {
	qr, _ := qrcode.New("hello")
	uri, _ := qr.DataURI(qrcode.FormatPNG)
	fmt.Println(strings.HasPrefix(uri, "data:image/png;base64,"))
	// Output: true
}

func ExampleWithLogo() {
	// logoBytes would come from the caller (a file, an embed); a one-pixel
	// SVG keeps the example self-contained.
	logoBytes := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="5" fill="#c81e1e"/></svg>`)
	qr, err := qrcode.New("https://example.com/pay",
		qrcode.WithLogo(logoBytes, qrcode.LogoSize(0.15), qrcode.LogoPadding(1), qrcode.LogoBorderRadius(0.2)),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(qr.ErrorCorrection() == qrcode.High) // a logo selects level H
	// Output: true
}
