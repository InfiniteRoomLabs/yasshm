package mcpserver

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// jsonCost is an upper bound on the bytes rune r takes inside a JSON string
// as encoding/json writes it (with HTML escaping, the larger of the two
// forms the SDK may use): quote and backslash and the short control escapes
// take 2, other control characters, <, >, &, U+2028, U+2029 and the
// replacement character take 6, and every other rune its UTF-8 length.
func jsonCost(r rune) int {
	switch {
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
		return 2
	case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029' || r == utf8.RuneError:
		return 6
	default:
		return utf8.RuneLen(r)
	}
}

// jsonStringCost is an upper bound on the encoded size of s as a JSON
// string, quotes included.
func jsonStringCost(s string) int {
	n := 2
	for _, r := range s {
		n += jsonCost(r)
	}
	return n
}

// textPage is target output made safe for a JSON string.
type textPage struct {
	// text is the output with every byte that is not part of valid UTF-8
	// replaced by U+FFFD.
	text string
	// used is how many input bytes text stands for.
	used int
	// lossy is set when a byte was replaced.
	lossy bool
}

// fitText turns the longest prefix of data whose JSON encoding costs at most
// budget bytes into text. When more data may follow (final is false), an
// incomplete UTF-8 sequence at the end is left for the next page instead of
// being replaced, so a character split by a page boundary survives -- unless
// it is all the page holds, which is then replaced so the reader moves on.
func fitText(data []byte, budget int, final bool) textPage {
	var b strings.Builder
	var p textPage
	cost := 0
	for p.used < len(data) {
		rest := data[p.used:]
		if !final && !utf8.FullRune(rest) && p.used > 0 {
			break
		}
		r, size := utf8.DecodeRune(rest)
		c := jsonCost(r)
		if cost+c > budget {
			break
		}
		cost += c
		if r == utf8.RuneError && size == 1 {
			p.lossy = true
		}
		b.WriteRune(r)
		p.used += size
	}
	p.text = b.String()
	return p
}

// fitBase64 returns how many bytes of data fit in budget bytes once base64
// encoded (a multiple of 3, so pages join without padding in between, unless
// it is the whole rest) and their encoding.
func fitBase64(data []byte, budget int) (encoded string, n int) {
	n = budget / 4 * 3
	if n >= len(data) {
		n = len(data)
	}
	if n < 0 {
		n = 0
	}
	encoded = base64.StdEncoding.EncodeToString(data[:n])
	return encoded, n
}

// cutRunes returns the first n runes of s and whether it cut anything.
func cutRunes(s string, n int) (string, bool) {
	i := 0
	for j := range s {
		if i == n {
			return s[:j], true
		}
		i++
	}
	return s, false
}
