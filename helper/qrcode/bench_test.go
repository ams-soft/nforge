package qrcode

import "testing"

func benchQR(b *testing.B, size int, opts ...Option) *QRCode {
	b.Helper()
	q, err := New("https://example.com/benchmark?id=teste123", append([]Option{WithSize(size)}, opts...)...)
	if err != nil {
		b.Fatal(err)
	}
	return q
}

func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		if _, err := New("https://example.com/benchmark?id=teste123"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPNG(b *testing.B) {
	logo := testLogoPNG(b, 64, 64)
	cases := []struct {
		name string
		size int
		opts []Option
	}{
		{"256", 256, nil},
		{"512", 512, nil},
		{"512-rounded", 512, []Option{WithRoundedModules(0.3), WithRoundedFinders(0.2)}},
		{"512-logo", 512, []Option{WithLogo(logo)}},
		{"512-logo-rounded", 512, []Option{WithRoundedModules(0.3), WithRoundedFinders(0.2), WithLogo(logo, LogoBorderRadius(0.2))}},
	}
	for _, c := range cases {
		q := benchQR(b, c.size, c.opts...)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := q.PNG(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSVG(b *testing.B) {
	logo := testLogoPNG(b, 64, 64)
	cases := []struct {
		name string
		size int
		opts []Option
	}{
		{"256", 256, nil},
		{"512", 512, nil},
		{"512-rounded", 512, []Option{WithRoundedModules(0.3), WithRoundedFinders(0.2)}},
		{"512-logo-png", 512, []Option{WithLogo(logo)}},
		{"512-logo-svg", 512, []Option{WithLogo([]byte(testLogoSVG))}},
	}
	for _, c := range cases {
		q := benchQR(b, c.size, c.opts...)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := q.SVG(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBase64AndDataURI(b *testing.B) {
	q := benchQR(b, 512)
	b.Run("Base64PNG", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := q.Base64(FormatPNG); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("DataURISVG", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := q.DataURI(FormatSVG); err != nil {
				b.Fatal(err)
			}
		}
	})
}
