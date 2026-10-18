package jose

import (
	"strings"
	"testing"
)

// TestMalformedInputAllocations bounds the work done on hostile input before
// any cryptography: oversized and malformed tokens are rejected with a small,
// constant number of allocations regardless of their size.
func TestMalformedInputAllocations(t *testing.T) {
	s := newService(t, testConfig(t))
	huge := strings.Repeat("A", 1<<20)
	cases := map[string]string{
		"oversized":       huge,
		"bad alphabet":    strings.Repeat("A", 10000) + "!",
		"segment count":   strings.Repeat("A", 10000),
		"oversize header": strings.Repeat("A", 4000) + ".e30.AA",
	}
	for name, token := range cases {
		allocs := testing.AllocsPerRun(20, func() {
			_, _ = s.ValidateJWT(ctx, "access", token, nil)
		})
		if allocs > 12 {
			t.Errorf("%s: %.0f allocations, want <= 12", name, allocs)
		}
	}
}

func benchService(b *testing.B) *Service {
	return newService(b, testConfig(b))
}

func BenchmarkSignVerify(b *testing.B) {
	s := benchService(b)
	for _, profile := range []string{"msg-ed", "msg-es", "msg-ps", "msg-rs", "msg-hs"} {
		token, _ := s.Sign(ctx, profile, []byte("payload"))
		b.Run(profile+"/sign", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = s.Sign(ctx, profile, []byte("payload"))
			}
		})
		b.Run(profile+"/verify", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = s.Verify(ctx, profile, token)
			}
		})
	}
}

func BenchmarkEncryptDecrypt(b *testing.B) {
	s := benchService(b)
	for _, profile := range []string{"box-rsa", "box-dir"} {
		token, _ := s.Encrypt(ctx, profile, []byte("payload"))
		b.Run(profile+"/encrypt", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = s.Encrypt(ctx, profile, []byte("payload"))
			}
		})
		b.Run(profile+"/decrypt", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = s.Decrypt(ctx, profile, token)
			}
		})
	}
}

func BenchmarkJWT(b *testing.B) {
	s := benchService(b)
	for _, profile := range []string{"access", "nested"} {
		token, _ := s.IssueJWT(ctx, profile, RegisteredClaims{Subject: "u"}, nil)
		b.Run(profile+"/issue", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = s.IssueJWT(ctx, profile, RegisteredClaims{Subject: "u"}, nil)
			}
		})
		b.Run(profile+"/validate", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = s.ValidateJWT(ctx, profile, token, nil)
			}
		})
		b.Run(profile+"/validate-parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, _ = s.ValidateJWT(ctx, profile, token, nil)
				}
			})
		})
	}
}
