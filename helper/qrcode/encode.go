package qrcode

import (
	"errors"
	"fmt"
)

// This file is the QR Code Model 2 encoder (ISO/IEC 18004): payload → data
// codewords → Reed-Solomon → function patterns → masked matrix. Everything
// the renderers need (the matrix and the function-module mask) comes from
// here, so PNG and SVG can never disagree about the symbol.

const (
	minVersion = 1
	maxVersion = 40
)

type segMode uint8

const (
	modeNumeric segMode = iota
	modeAlnum
	modeByte
)

const alnumCharset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:"

// symbol is an encoded QR Code: its modules and which of them are function
// patterns (finders, separators, timing, alignment, format and version
// information, the dark module).
type symbol struct {
	version int
	level   ErrorCorrection
	size    int
	dark    []bool // size*size, row-major
	fn      []bool // function modules, same layout
}

func (s *symbol) at(x, y int) bool { return s.dark[y*s.size+x] }

// pickMode chooses the single most compact mode that represents data exactly.
// Mixed-mode segmentation is not attempted: the symbol is valid but may be
// one version larger than the theoretical minimum for mixed content.
func pickMode(data []byte) segMode {
	numeric, alnum := true, true
	for _, c := range data {
		if c < '0' || c > '9' {
			numeric = false
		}
		if !isAlnum(c) {
			alnum = false
		}
	}
	switch {
	case numeric && len(data) > 0:
		return modeNumeric
	case alnum && len(data) > 0:
		return modeAlnum
	}
	return modeByte
}

func isAlnum(c byte) bool {
	for i := range len(alnumCharset) {
		if alnumCharset[i] == c {
			return true
		}
	}
	return false
}

func alnumIndex(c byte) int {
	for i := range len(alnumCharset) {
		if alnumCharset[i] == c {
			return i
		}
	}
	return -1
}

func countBits(m segMode, version int) int {
	var t [3]int
	switch {
	case version <= 9:
		t = [3]int{10, 9, 8}
	case version <= 26:
		t = [3]int{12, 11, 16}
	default:
		t = [3]int{14, 13, 16}
	}
	return t[m]
}

type bitBuf struct {
	b []byte
	n int // bits used
}

func (w *bitBuf) put(val uint32, nbits int) {
	for i := nbits - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if val>>uint(i)&1 == 1 {
			w.b[w.n/8] |= 0x80 >> uint(w.n%8)
		}
		w.n++
	}
}

// dataBits is the number of bits the payload needs in mode m at a version
// (mode indicator, character count and data), or -1 when the count does not
// fit the count field.
func dataBits(m segMode, n, version int) int {
	cb := countBits(m, version)
	if n >= 1<<uint(cb) {
		return -1
	}
	total := 4 + cb
	switch m {
	case modeNumeric:
		total += n/3*10 + [3]int{0, 4, 7}[n%3]
	case modeAlnum:
		total += n/2*11 + n%2*6
	default:
		total += n * 8
	}
	return total
}

func dataCapacity(version int, level ErrorCorrection) int {
	bi := blockTable[version-1][level]
	return bi.blocks1*bi.data1 + bi.blocks2*bi.data2
}

// minimalVersion returns the smallest version >= from that holds data at
// level, or 0 when none does.
func minimalVersion(data []byte, level ErrorCorrection, from int) (int, segMode) {
	m := pickMode(data)
	for v := max(from, minVersion); v <= maxVersion; v++ {
		if need := dataBits(m, len(data), v); need >= 0 && need <= dataCapacity(v, level)*8 {
			return v, m
		}
	}
	return 0, m
}

func encodeData(data []byte, m segMode, version int, level ErrorCorrection) []byte {
	var w bitBuf
	w.put([3]uint32{1, 2, 4}[m], 4)
	w.put(uint32(len(data)), countBits(m, version)) //nolint:gosec // G115: len(data) is bounded by the symbol capacity, far below 2^32
	switch m {
	case modeNumeric:
		for i := 0; i < len(data); i += 3 {
			end := min(i+3, len(data))
			var v uint32
			for _, c := range data[i:end] {
				v = v*10 + uint32(c-'0')
			}
			w.put(v, [4]int{0, 4, 7, 10}[end-i])
		}
	case modeAlnum:
		for i := 0; i < len(data); i += 2 {
			if i+1 < len(data) {
				w.put(uint32(alnumIndex(data[i])*45+alnumIndex(data[i+1])), 11) //nolint:gosec // G115: alphanumeric pair value is at most 45*45+44
			} else {
				w.put(uint32(alnumIndex(data[i])), 6) //nolint:gosec // G115: alphanumeric index is at most 44
			}
		}
	default:
		for _, c := range data {
			w.put(uint32(c), 8)
		}
	}
	capBits := dataCapacity(version, level) * 8
	w.put(0, min(4, capBits-w.n)) // terminator
	w.put(0, (8-w.n%8)%8)
	for pad := byte(0xEC); len(w.b) < capBits/8; {
		w.b = append(w.b, pad)
		pad ^= 0xEC ^ 0x11
	}
	return w.b
}

// Reed-Solomon over GF(256) with the QR polynomial 0x11D.
var gfExp, gfLog = func() (e [512]byte, l [256]int) {
	x := 1
	for i := range 255 {
		e[i] = byte(x)
		l[x] = i
		x <<= 1
		if x >= 256 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		e[i] = e[i-255]
	}
	return
}()

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[gfLog[a]+gfLog[b]]
}

func rsGenerator(degree int) []byte {
	root := byte(1)
	// Coefficients of the product of (x - α^i), i in 0..degree-1, from the
	// lowest degree up; the leading 1 is implicit.
	res := make([]byte, degree)
	res[degree-1] = 1
	for range degree {
		for j := range degree {
			res[j] = gfMul(res[j], root)
			if j+1 < degree {
				res[j] ^= res[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return res
}

func rsRemainder(data, gen []byte) []byte {
	rem := make([]byte, len(gen))
	for _, b := range data {
		f := b ^ rem[0]
		copy(rem, rem[1:])
		rem[len(rem)-1] = 0
		for i, c := range gen {
			rem[i] ^= gfMul(c, f)
		}
	}
	return rem
}

// interleave splits data into the blocks of (version, level), appends the
// error correction of each and returns the final codeword sequence.
func interleave(data []byte, version int, level ErrorCorrection) []byte {
	bi := blockTable[version-1][level]
	nBlocks := bi.blocks1 + bi.blocks2
	gen := rsGenerator(bi.ecPerBlock)
	dataBlocks := make([][]byte, 0, nBlocks)
	ecBlocks := make([][]byte, 0, nBlocks)
	off := 0
	for i := range nBlocks {
		n := bi.data1
		if i >= bi.blocks1 {
			n = bi.data2
		}
		blk := data[off : off+n]
		off += n
		dataBlocks = append(dataBlocks, blk)
		ecBlocks = append(ecBlocks, rsRemainder(blk, gen))
	}
	out := make([]byte, 0, off+nBlocks*bi.ecPerBlock)
	longest := max(bi.data1, bi.data2)
	for i := range longest {
		for _, blk := range dataBlocks {
			if i < len(blk) {
				out = append(out, blk[i])
			}
		}
	}
	for i := range bi.ecPerBlock {
		for _, blk := range ecBlocks {
			out = append(out, blk[i])
		}
	}
	return out
}

func alignmentCenters(version int) []int {
	if version == 1 {
		return nil
	}
	n := version/7 + 2
	size := 17 + 4*version
	step := 26
	if version != 32 {
		step = (size - 13 + (n-1)*2 - 1) / ((n - 1) * 2) * 2 // ceil((size-13)/(n-1)) up to even
	}
	pos := make([]int, n)
	pos[0] = 6
	for i := n - 1; i >= 1; i-- {
		pos[i] = size - 7 - (n-1-i)*step
	}
	return pos
}

// functionMask returns which modules of a version are function patterns.
func functionMask(version int) []bool {
	s := newSymbol(version, Low)
	s.drawFunctions()
	return s.fn
}

func newSymbol(version int, level ErrorCorrection) *symbol {
	size := 17 + 4*version
	return &symbol{version: version, level: level, size: size, dark: make([]bool, size*size), fn: make([]bool, size*size)}
}

func (s *symbol) setFn(x, y int, dark bool) {
	s.dark[y*s.size+x] = dark
	s.fn[y*s.size+x] = true
}

func (s *symbol) drawFunctions() {
	size := s.size
	for i := range size {
		s.setFn(6, i, i%2 == 0)
		s.setFn(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x < 0 || y < 0 || x >= size || y >= size {
					continue
				}
				d := max(abs(dx), abs(dy))
				s.setFn(x, y, d != 2 && d != 4)
			}
		}
	}
	pos := alignmentCenters(s.version)
	for i, cy := range pos {
		for j, cx := range pos {
			if (i == 0 && j == 0) || (i == 0 && j == len(pos)-1) || (i == len(pos)-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					s.setFn(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	s.drawFormat(0) // reserves the format areas and the dark module
	if s.version >= 7 {
		rem := s.version
		for range 12 {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		v := s.version<<12 | rem
		for i := range 18 {
			bit := v>>uint(i)&1 == 1
			a, b := size-11+i%3, i/3
			s.setFn(a, b, bit)
			s.setFn(b, a, bit)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (s *symbol) drawFormat(mask int) {
	size := s.size
	lv := [4]int{1, 0, 3, 2}[s.level]
	data := lv<<3 | mask
	rem := data
	for range 10 {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	f := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return f>>uint(i)&1 == 1 }
	for i := 0; i <= 5; i++ {
		s.setFn(8, i, bit(i))
	}
	s.setFn(8, 7, bit(6))
	s.setFn(8, 8, bit(7))
	s.setFn(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		s.setFn(14-i, 8, bit(i))
	}
	for i := range 8 {
		s.setFn(size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		s.setFn(8, size-15+i, bit(i))
	}
	s.setFn(8, size-8, true) // the always-dark module
}

func (s *symbol) place(codewords []byte) {
	size := s.size
	i := 0
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range size {
			for j := range 2 {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = size - 1 - vert
				}
				if !s.fn[y*size+x] && i < len(codewords)*8 {
					s.dark[y*size+x] = codewords[i>>3]>>uint(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	}
	return ((x+y)%2+x*y%3)%2 == 0
}

func (s *symbol) applyMask(mask int) {
	for y := range s.size {
		for x := range s.size {
			if !s.fn[y*s.size+x] && maskBit(mask, x, y) {
				s.dark[y*s.size+x] = !s.dark[y*s.size+x]
			}
		}
	}
}

func (s *symbol) penalty() int {
	n := s.size
	total := 0
	for pass := range 2 {
		get := func(i, j int) bool {
			if pass == 0 {
				return s.dark[i*n+j]
			}
			return s.dark[j*n+i]
		}
		for i := range n {
			run := 1
			for j := 1; j <= n; j++ {
				if j < n && get(i, j) == get(i, j-1) {
					run++
					continue
				}
				if run >= 5 {
					total += 3 + run - 5
				}
				run = 1
			}
			// 1:1:3:1:1 finder-like patterns with four light modules on a side.
			for j := 0; j+11 <= n; j++ {
				var w11 int
				for k := range 11 {
					w11 <<= 1
					if get(i, j+k) {
						w11 |= 1
					}
				}
				if w11 == 0b10111010000 || w11 == 0b00001011101 {
					total += 40
				}
			}
		}
	}
	for y := 0; y+1 < n; y++ {
		for x := 0; x+1 < n; x++ {
			c := s.dark[y*n+x]
			if c == s.dark[y*n+x+1] && c == s.dark[(y+1)*n+x] && c == s.dark[(y+1)*n+x+1] {
				total += 3
			}
		}
	}
	darkCount := 0
	for _, d := range s.dark {
		if d {
			darkCount++
		}
	}
	k := (abs(darkCount*20-n*n*10)+n*n-1)/(n*n) - 1
	return total + max(k, 0)*10
}

// encodeSymbol builds the symbol of data at exactly the given version.
func encodeSymbol(data []byte, mode segMode, version int, level ErrorCorrection) (*symbol, error) {
	if version < minVersion || version > maxVersion {
		return nil, fmt.Errorf("%w: version %d", ErrTooLarge, version)
	}
	if need := dataBits(mode, len(data), version); need < 0 || need > dataCapacity(version, level)*8 {
		return nil, errors.New("qrcode: internal error: payload does not fit the chosen version")
	}
	cw := interleave(encodeData(data, mode, version, level), version, level)
	s := newSymbol(version, level)
	s.drawFunctions()
	s.place(cw)
	best, bestScore := 0, int(^uint(0)>>1)
	for m := range 8 {
		s.applyMask(m)
		s.drawFormat(m)
		if p := s.penalty(); p < bestScore {
			best, bestScore = m, p
		}
		s.applyMask(m) // XOR again: undo
	}
	s.applyMask(best)
	s.drawFormat(best)
	return s, nil
}
