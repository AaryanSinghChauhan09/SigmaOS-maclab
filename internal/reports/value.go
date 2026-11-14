package reports

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// A parsed JSON document, as the old site's parser gave it to its rules: an
// object is an *Object, an array a []any, a string a string, a number
// written without fraction or exponent a *big.Int and any other number a
// Float, then bool and nil.

// Object is a JSON object with its members in document order: the schema's
// refusals name unknown members in that order.
type Object struct {
	keys    []string
	members map[string]any
}

func (o *Object) Keys() []string { return o.keys }

func (o *Object) Get(key string) (any, bool) {
	value, ok := o.members[key]
	return value, ok
}

func (o *Object) without(key string) *Object {
	if _, ok := o.members[key]; !ok {
		return o
	}
	rest := &Object{members: make(map[string]any, len(o.members))}
	for _, k := range o.keys {
		if k != key {
			rest.keys = append(rest.keys, k)
			rest.members[k] = o.members[k]
		}
	}
	return rest
}

// Float is a JSON number written with a fraction or an exponent. A signed
// report can't hold one.
type Float float64

// ErrNotJSON is what Parse gives for anything the old site answered
// "The report is not valid JSON." to.
var ErrNotJSON = errors.New("not valid JSON")

// The old site's parser counted only containers that hold something.
const maxNesting = 100

// Parse reads one JSON document as the old site's parser did: strict JSON,
// no comment, no member named twice, no lone surrogate, at most 100 levels.
// Unlike it, text that is not UTF-8 is refused here, not later.
func Parse(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, ErrNotJSON
	}
	p := &parser{data: data}
	p.space()
	value, ok := p.value()
	if !ok {
		return nil, ErrNotJSON
	}
	p.space()
	if p.at != len(p.data) {
		return nil, ErrNotJSON
	}
	return value, nil
}

type parser struct {
	data  []byte
	at    int
	depth int
}

func (p *parser) peek() byte {
	if p.at < len(p.data) {
		return p.data[p.at]
	}
	return 0
}

func (p *parser) space() {
	for p.at < len(p.data) {
		switch p.data[p.at] {
		case ' ', '\t', '\n', '\r':
			p.at++
		default:
			return
		}
	}
}

func (p *parser) literal(word string) bool {
	if len(p.data)-p.at >= len(word) && string(p.data[p.at:p.at+len(word)]) == word {
		p.at += len(word)
		return true
	}
	return false
}

func (p *parser) value() (any, bool) {
	switch c := p.peek(); {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case c == 't':
		return true, p.literal("true")
	case c == 'f':
		return false, p.literal("false")
	case c == 'n':
		return nil, p.literal("null")
	}
	return nil, false
}

func (p *parser) enter() bool {
	p.depth++
	return p.depth <= maxNesting
}

func (p *parser) object() (any, bool) {
	p.at++
	p.space()
	object := &Object{members: map[string]any{}}
	if p.peek() == '}' {
		p.at++
		return object, true
	}
	if !p.enter() {
		return nil, false
	}
	for {
		p.space()
		if p.peek() != '"' {
			return nil, false
		}
		key, ok := p.str()
		if !ok {
			return nil, false
		}
		if _, twice := object.members[key]; twice {
			return nil, false
		}
		p.space()
		if p.peek() != ':' {
			return nil, false
		}
		p.at++
		p.space()
		value, ok := p.value()
		if !ok {
			return nil, false
		}
		object.keys = append(object.keys, key)
		object.members[key] = value
		p.space()
		switch p.peek() {
		case ',':
			p.at++
		case '}':
			p.at++
			p.depth--
			return object, true
		default:
			return nil, false
		}
	}
}

func (p *parser) array() (any, bool) {
	p.at++
	p.space()
	items := []any{}
	if p.peek() == ']' {
		p.at++
		return items, true
	}
	if !p.enter() {
		return nil, false
	}
	for {
		p.space()
		value, ok := p.value()
		if !ok {
			return nil, false
		}
		items = append(items, value)
		p.space()
		switch p.peek() {
		case ',':
			p.at++
		case ']':
			p.at++
			p.depth--
			return items, true
		default:
			return nil, false
		}
	}
}

func (p *parser) str() (string, bool) {
	p.at++
	start := p.at
	for p.at < len(p.data) {
		switch c := p.data[p.at]; {
		case c == '"':
			p.at++
			return string(p.data[start : p.at-1]), true
		case c == '\\':
			return p.escaped(start)
		case c < 0x20:
			return "", false
		default:
			p.at++
		}
	}
	return "", false
}

func (p *parser) escaped(start int) (string, bool) {
	text := append([]byte(nil), p.data[start:p.at]...)
	for p.at < len(p.data) {
		c := p.data[p.at]
		p.at++
		switch {
		case c == '"':
			return string(text), true
		case c < 0x20:
			return "", false
		case c != '\\':
			text = append(text, c)
			continue
		}
		if p.at >= len(p.data) {
			return "", false
		}
		c = p.data[p.at]
		p.at++
		switch c {
		case '"', '\\', '/':
			text = append(text, c)
		case 'b':
			text = append(text, '\b')
		case 'f':
			text = append(text, '\f')
		case 'n':
			text = append(text, '\n')
		case 'r':
			text = append(text, '\r')
		case 't':
			text = append(text, '\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return "", false
			}
			if utf16.IsSurrogate(r) {
				if !p.literal(`\u`) {
					return "", false
				}
				low, ok := p.hex4()
				if !ok {
					return "", false
				}
				if r = utf16.DecodeRune(r, low); r == utf8.RuneError {
					return "", false
				}
			}
			text = utf8.AppendRune(text, r)
		default:
			return "", false
		}
	}
	return "", false
}

func (p *parser) hex4() (rune, bool) {
	if len(p.data)-p.at < 4 {
		return 0, false
	}
	var r rune
	for _, c := range p.data[p.at : p.at+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	p.at += 4
	return r, true
}

func (p *parser) digits() bool {
	start := p.at
	for p.at < len(p.data) && p.data[p.at] >= '0' && p.data[p.at] <= '9' {
		p.at++
	}
	return p.at > start
}

func (p *parser) number() (any, bool) {
	start := p.at
	if p.peek() == '-' {
		p.at++
	}
	if p.peek() == '0' {
		p.at++
	} else if !p.digits() {
		return nil, false
	}
	whole := true
	if p.peek() == '.' {
		whole = false
		p.at++
		if !p.digits() {
			return nil, false
		}
	}
	if c := p.peek(); c == 'e' || c == 'E' {
		whole = false
		p.at++
		if c := p.peek(); c == '+' || c == '-' {
			p.at++
		}
		if !p.digits() {
			return nil, false
		}
	}
	text := string(p.data[start:p.at])
	if whole {
		n, ok := new(big.Int).SetString(text, 10)
		return n, ok
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !math.IsInf(f, 0) {
		return nil, false
	}
	return Float(f), true
}
