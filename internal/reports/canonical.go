package reports

import (
	"fmt"
	"math/big"
	"sort"
)

// Invalid says why a signature is not valid, in the old site's words.
type Invalid string

func (i Invalid) Error() string { return string(i) }

// Canonical gives the bytes a machine key signs: the document as JSON with
// keys sorted by their bytes, no whitespace and non-ASCII characters as
// UTF-8, as the CLI writes it (Python's json.dumps with sort_keys, compact
// separators and ensure_ascii off).
func Canonical(value any) ([]byte, error) {
	return canonical(nil, value)
}

func canonical(out []byte, value any) ([]byte, error) {
	var err error
	switch value := value.(type) {
	case *Object:
		keys := append([]string(nil), value.keys...)
		sort.Strings(keys)
		out = append(out, '{')
		for i, key := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(canonicalString(out, key), ':')
			if out, err = canonical(out, value.members[key]); err != nil {
				return nil, err
			}
		}
		return append(out, '}'), nil
	case []any:
		out = append(out, '[')
		for i, item := range value {
			if i > 0 {
				out = append(out, ',')
			}
			if out, err = canonical(out, item); err != nil {
				return nil, err
			}
		}
		return append(out, ']'), nil
	case string:
		return canonicalString(out, value), nil
	case *big.Int:
		return value.Append(out, 10), nil
	case bool:
		if value {
			return append(out, "true"...), nil
		}
		return append(out, "false"...), nil
	case nil:
		return append(out, "null"...), nil
	case Float:
		return nil, Invalid("the report has a value a signed report can't have (Float)")
	}
	return nil, fmt.Errorf("reports: %T is not a JSON value", value)
}

func canonicalString(out []byte, text string) []byte {
	out = append(out, '"')
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '"':
			out = append(out, `\"`...)
		case '\\':
			out = append(out, `\\`...)
		case '\b':
			out = append(out, `\b`...)
		case '\f':
			out = append(out, `\f`...)
		case '\n':
			out = append(out, `\n`...)
		case '\r':
			out = append(out, `\r`...)
		case '\t':
			out = append(out, `\t`...)
		default:
			if c < 0x20 {
				out = fmt.Appendf(out, `\u%04x`, c)
			} else {
				out = append(out, c)
			}
		}
	}
	return append(out, '"')
}
