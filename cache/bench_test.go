package cache

import (
	"strconv"
	"testing"
)

// Benchmarks of the memory driver and the codec path. Redis round trips are
// in redis_integration_test.go (they measure the network, not the facade).
//
//	go test ./cache -run '^$' -bench . -benchmem

func BenchmarkMemoryGetBytesHit(b *testing.B) {
	c, _ := openMemory(b, nil)
	_ = c.SetBytes(bg, "k", make([]byte, 256), 0)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.GetBytes(bg, "k"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemorySetBytesWithLRUEviction(b *testing.B) {
	c, _ := openMemory(b, func(cfg *Config) { cfg.Memory.MaxEntries = 1024 })
	keys := make([]string, 4096)
	for i := range keys {
		keys[i] = "k" + strconv.Itoa(i)
	}
	v := make([]byte, 128)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if err := c.SetBytes(bg, keys[i%len(keys)], v, 0); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

func BenchmarkMemoryParallelGet(b *testing.B) {
	c, _ := openMemory(b, nil)
	for i := range 64 {
		_ = c.SetBytes(bg, "k"+strconv.Itoa(i), make([]byte, 64), 0)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = c.GetBytes(bg, "k"+strconv.Itoa(i%64))
			i++
		}
	})
}

func BenchmarkObjects(b *testing.B) {
	for _, format := range []string{CodecJSON, CodecCBOR} {
		c, _ := openMemory(b, func(cfg *Config) { cfg.Codec = format })
		u := User{ID: 42, Name: "Ana", Age: 30}
		_ = c.Set(bg, "u", u, 0)
		b.Run(format+"/set", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := c.Set(bg, "u", u, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(format+"/get", func(b *testing.B) {
			b.ReportAllocs()
			var out User
			for b.Loop() {
				if err := c.Get(bg, "u", &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMemoryIncrementParallel(b *testing.B) {
	c, _ := openMemory(b, nil)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Increment(bg, "n", 1)
		}
	})
}
