package js

import (
	"unicode/utf16"
	"unicode/utf8"
)

// JavaScript strings are sequences of UTF-16 code units; this engine stores
// them as Go strings (UTF-8). Everything that counts or addresses characters
// (length, indexing, slice, charAt, indexOf, padding, ...) works in UTF-16
// code units, as JavaScript does. Pure-ASCII strings take a byte-indexed fast
// path. A lone surrogate (for example half of an emoji after slice) is kept
// as its 3-byte WTF-8 form so it survives round trips; concatenating a high
// and a low surrogate rejoins them (see concatStr).

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// decodeWTF8 decodes the rune at s[i:]; lone surrogates (WTF-8) decode to
// their surrogate code point with size 3.
func decodeWTF8(s string, i int) (rune, int) {
	if s[i] == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2]&0xC0 == 0x80 {
		return 0xD000 | rune(s[i+1]&0x3F)<<6 | rune(s[i+2]&0x3F), 3
	}
	return utf8.DecodeRuneInString(s[i:])
}

// strUnits returns the UTF-16 code units of s.
func strUnits(s string) []uint16 {
	u := make([]uint16, 0, len(s))
	for i := 0; i < len(s); {
		r, n := decodeWTF8(s, i)
		i += n
		if r >= 0x10000 {
			r1, r2 := utf16.EncodeRune(r)
			u = append(u, uint16(r1), uint16(r2))
		} else {
			u = append(u, uint16(r))
		}
	}
	return u
}

// unitsStr builds a string from UTF-16 code units, keeping lone surrogates.
func unitsStr(u []uint16) string {
	b := make([]byte, 0, len(u))
	for i := 0; i < len(u); i++ {
		c := rune(u[i])
		if c >= 0xD800 && c < 0xDC00 && i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] < 0xE000 {
			c = utf16.DecodeRune(c, rune(u[i+1]))
			i++
		}
		if c >= 0xD800 && c < 0xE000 { // lone surrogate: WTF-8
			b = append(b, 0xED, byte(0x80|(c>>6)&0x3F), byte(0x80|c&0x3F))
			continue
		}
		b = utf8.AppendRune(b, c)
	}
	return string(b)
}

// strLen is the JavaScript length of s (UTF-16 code units).
func strLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			i++
			n++
			continue
		}
		r, w := decodeWTF8(s, i)
		i += w
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// strSlice returns the code units [st, en) of s. Callers clamp 0<=st<=en<=strLen(s).
func strSlice(s string, st, en int) string {
	if isASCII(s) {
		return s[st:en]
	}
	return unitsStr(strUnits(s)[st:en])
}

// unitIndexOfByte converts a byte offset in s to a UTF-16 unit offset.
func unitIndexOfByte(s string, off int) int {
	return strLen(s[:off])
}

// concatStr is JS string concatenation: a trailing high surrogate on the left
// and a leading low surrogate on the right join into one code point.
func concatStr(a, b string) string {
	if len(a) >= 3 && len(b) >= 3 && b[0] == 0xED && b[1] >= 0xB0 && a[len(a)-3] == 0xED && a[len(a)-2] >= 0xA0 && a[len(a)-2] <= 0xAF {
		lo, _ := decodeWTF8(b, 0)
		hi, _ := decodeWTF8(a, len(a)-3)
		if hi >= 0xD800 && hi < 0xDC00 && lo >= 0xDC00 && lo < 0xE000 {
			return a[:len(a)-3] + string(utf16.DecodeRune(hi, lo)) + b[3:]
		}
	}
	return a + b
}
