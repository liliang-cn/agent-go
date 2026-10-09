package agent

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// partialJSONStringField reads the value of a string field out of a JSON
// object that may still be arriving: `{"result": "Disk on orange2 is 9` gives
// "Disk on orange2 is 9". It returns what has been decoded so far, stopping
// before an escape sequence that is not complete yet, and "" when the field
// has not started. It only looks at top-level keys of a flat object, which is
// what a tool call's arguments are.
func partialJSONStringField(raw, field string) string {
	i := 0
	n := len(raw)
	skipSpace := func() {
		for i < n && (raw[i] == ' ' || raw[i] == '\n' || raw[i] == '\t' || raw[i] == '\r') {
			i++
		}
	}
	// readString decodes the string starting at raw[i] (an opening quote);
	// done reports whether its closing quote was reached.
	readString := func() (string, bool) {
		i++ // opening quote
		var b strings.Builder
		for i < n {
			c := raw[i]
			switch {
			case c == '"':
				i++
				return b.String(), true
			case c == '\\':
				if i+1 >= n {
					return b.String(), false
				}
				switch e := raw[i+1]; e {
				case '"', '\\', '/':
					b.WriteByte(e)
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				case 'b':
					b.WriteByte('\b')
				case 'f':
					b.WriteByte('\f')
				case 'u':
					if i+6 > n {
						return b.String(), false
					}
					r, err := strconv.ParseUint(raw[i+2:i+6], 16, 32)
					if err != nil {
						return b.String(), false
					}
					i += 6
					if utf8.ValidRune(rune(r)) && (r < 0xD800 || r > 0xDFFF) {
						b.WriteRune(rune(r))
						continue
					}
					// A surrogate pair: the low half follows as its own escape.
					if r >= 0xD800 && r <= 0xDBFF {
						if i+6 > n {
							return b.String(), false
						}
						if raw[i] == '\\' && raw[i+1] == 'u' {
							lo, err := strconv.ParseUint(raw[i+2:i+6], 16, 32)
							if err == nil && lo >= 0xDC00 && lo <= 0xDFFF {
								b.WriteRune(rune(0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)))
								i += 6
								continue
							}
						}
					}
					b.WriteRune(utf8.RuneError)
					continue
				default:
					b.WriteByte(e)
				}
				i += 2
			default:
				b.WriteByte(c)
				i++
			}
		}
		return b.String(), false
	}
	// skipValue steps over a non-string value; it reports false at the end of
	// the input.
	skipValue := func() bool {
		depth := 0
		for i < n {
			switch raw[i] {
			case '"':
				if _, done := readString(); !done {
					return false
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				if depth == 0 {
					return true
				}
				depth--
			case ',':
				if depth == 0 {
					return true
				}
			}
			i++
		}
		return false
	}

	skipSpace()
	if i >= n || raw[i] != '{' {
		return ""
	}
	i++
	for {
		skipSpace()
		if i >= n || raw[i] != '"' {
			return ""
		}
		key, done := readString()
		if !done {
			return ""
		}
		skipSpace()
		if i >= n || raw[i] != ':' {
			return ""
		}
		i++
		skipSpace()
		if i >= n {
			return ""
		}
		if key == field {
			if raw[i] != '"' {
				return ""
			}
			value, _ := readString()
			return value
		}
		if raw[i] == '"' {
			if _, done := readString(); !done {
				return ""
			}
		} else if !skipValue() {
			return ""
		}
		skipSpace()
		if i >= n || raw[i] != ',' {
			return ""
		}
		i++
	}
}
