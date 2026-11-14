package reports

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// The exports are written as the old site wrote them: members in the order
// they are put, no whitespace, text escaped as Rails escapes it (which is
// how encoding/json escapes a string), and a number with a fraction as Ruby
// prints one.

// obj is an object of the given members, name then value, in that order.
func obj(pairs ...any) *Object {
	o := &Object{members: make(map[string]any, len(pairs)/2)}
	for i := 0; i+1 < len(pairs); i += 2 {
		o.put(pairs[i].(string), pairs[i+1])
	}
	return o
}

// compact is the object without the members that are nil or "".
func (o *Object) compact() *Object {
	kept := &Object{members: map[string]any{}}
	for _, key := range o.keys {
		switch value := o.members[key].(type) {
		case nil:
			continue
		case string:
			if value == "" {
				continue
			}
		case *Object:
			if value == nil {
				continue
			}
		}
		kept.put(key, o.members[key])
	}
	return kept
}

// orNull is the text, or JSON's null for "".
func orNull(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func texts(list []string) []any {
	out := make([]any, 0, len(list))
	for _, item := range list {
		out = append(out, item)
	}
	return out
}

// rubyFloat prints a number as Ruby's Float#to_s does: "3136.5", "3000.0",
// "1.0e+16".
func rubyFloat(f float64) string {
	if abs := math.Abs(f); abs != 0 && (abs >= 1e16 || abs < 1e-4) {
		mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
		if !strings.Contains(mantissa, ".") {
			mantissa += ".0"
		}
		return mantissa + "e" + exponent
	}
	text := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		text += ".0"
	}
	return text
}

// JSON writes a value of an export.
func JSON(value any) []byte { return appendJSON(nil, value) }

func appendJSON(out []byte, value any) []byte {
	switch value := value.(type) {
	case nil:
		return append(out, "null"...)
	case *Object:
		if value == nil {
			return append(out, "null"...)
		}
		out = append(out, '{')
		for i, key := range value.keys {
			if i > 0 {
				out = append(out, ',')
			}
			out = appendJSON(out, key)
			out = append(out, ':')
			out = appendJSON(out, value.members[key])
		}
		return append(out, '}')
	case []any:
		out = append(out, '[')
		for i, item := range value {
			if i > 0 {
				out = append(out, ',')
			}
			out = appendJSON(out, item)
		}
		return append(out, ']')
	case []string:
		return appendJSON(out, texts(value))
	case string:
		text, _ := json.Marshal(value)
		return append(out, text...)
	case bool:
		return strconv.AppendBool(out, value)
	case int:
		return strconv.AppendInt(out, int64(value), 10)
	case int64:
		return strconv.AppendInt(out, value, 10)
	case *big.Int:
		return append(out, value.String()...)
	case Float:
		return appendJSON(out, float64(value))
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return append(out, "null"...)
		}
		return append(out, rubyFloat(value)...)
	case Tally:
		counts := obj()
		for _, count := range value {
			counts.put(count.State, count.N)
		}
		return appendJSON(out, counts)
	}
	return append(out, "null"...)
}

// rubyText is a value as Ruby's to_s gives it, for a CSV field: nothing for
// nil, digits for a whole number.
func rubyText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case int:
		return strconv.Itoa(value)
	case *big.Int:
		return value.String()
	case Float:
		return rubyFloat(float64(value))
	case float64:
		return rubyFloat(value)
	}
	return ""
}
