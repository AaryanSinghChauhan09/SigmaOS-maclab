package reports

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema is a JSON Schema of the few keywords the report schema uses. Its
// refusals are worded and ordered as the old site's validator (json_schemer)
// worded and ordered them, so a client sees the same details.
type Schema struct{ root *schemaNode }

type schemaNode struct {
	items         *schemaNode
	properties    []schemaProperty
	hasProperties bool
	closed        bool // additionalProperties: false
	kind          string
	enum          []any
	constant      any
	hasConstant   bool
	maximum       *big.Int
	minimum       *big.Int
	maxLength     *int
	minLength     *int
	pattern       *regexp.Regexp
	patternText   string
	maxItems      *int
	minItems      *int
	required      []string
}

type schemaProperty struct {
	name string
	node *schemaNode
}

// LoadSchema reads a schema. A keyword these rules don't know is an error:
// it would otherwise pass every report.
func LoadSchema(data []byte) (*Schema, error) {
	value, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("reports: the schema is not JSON")
	}
	root, err := compileSchema(value, "")
	if err != nil {
		return nil, err
	}
	return &Schema{root}, nil
}

var (
	schemaTypes = map[string]string{"object": "an object", "array": "an array", "string": "a string", "integer": "an integer", "number": "a number", "boolean": "a boolean", "null": "null"}
	plainEnum   = regexp.MustCompile(`\A[ !$-\[\]-~]*\z`)
)

// Patterns are ECMA-262's. Only what means the same to Go's regexp is taken:
// no group flags, and no escape but a dot, a newline and a backslash.
func plainPattern(pattern string) bool {
	if pattern == "" || strings.Contains(pattern, "(?") {
		return false
	}
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' {
			i++
			if i == len(pattern) || !strings.ContainsRune(`.n\`, rune(pattern[i])) {
				return false
			}
		}
	}
	return true
}

func compileSchema(value any, at string) (*schemaNode, error) {
	object, ok := value.(*Object)
	if !ok {
		return nil, fmt.Errorf("reports: schema%s is not an object", at)
	}
	node := &schemaNode{}
	count := func(value any, keyword string) (*int, error) {
		n, ok := value.(*big.Int)
		if !ok || !n.IsInt64() || n.Sign() < 0 || n.Int64() > math.MaxInt32 {
			return nil, fmt.Errorf("reports: schema%s/%s is not a count", at, keyword)
		}
		c := int(n.Int64())
		return &c, nil
	}
	for _, keyword := range object.keys {
		value := object.members[keyword]
		var err error
		switch keyword {
		case "$schema", "$id", "title", "description":
		case "type":
			kind, _ := value.(string)
			if schemaTypes[kind] == "" {
				err = fmt.Errorf("reports: schema%s/type is not a type these rules know", at)
			}
			node.kind = kind
		case "properties":
			properties, ok := value.(*Object)
			if !ok {
				return nil, fmt.Errorf("reports: schema%s/properties is not an object", at)
			}
			node.hasProperties = true
			for _, name := range properties.keys {
				child, err := compileSchema(properties.members[name], at+"/properties/"+name)
				if err != nil {
					return nil, err
				}
				node.properties = append(node.properties, schemaProperty{name, child})
			}
		case "additionalProperties":
			allowed, ok := value.(bool)
			if !ok {
				err = fmt.Errorf("reports: schema%s/additionalProperties is not true or false", at)
			}
			node.closed = !allowed
		case "items":
			node.items, err = compileSchema(value, at+"/items")
		case "required":
			names, _ := value.([]any)
			for _, name := range names {
				name, ok := name.(string)
				if !ok {
					return nil, fmt.Errorf("reports: schema%s/required is not a list of names", at)
				}
				node.required = append(node.required, name)
			}
		case "enum":
			values, _ := value.([]any)
			for _, value := range values {
				if text, ok := value.(string); !ok || !plainEnum.MatchString(text) {
					return nil, fmt.Errorf("reports: schema%s/enum holds more than plain strings", at)
				}
			}
			node.enum = values
		case "const":
			switch value.(type) {
			case string, bool, *big.Int:
				node.constant, node.hasConstant = value, true
			default:
				err = fmt.Errorf("reports: schema%s/const is not a string, a whole number, true or false", at)
			}
		case "minimum", "maximum":
			n, ok := value.(*big.Int)
			if !ok {
				return nil, fmt.Errorf("reports: schema%s/%s is not a whole number", at, keyword)
			}
			if keyword == "minimum" {
				node.minimum = n
			} else {
				node.maximum = n
			}
		case "pattern":
			text, _ := value.(string)
			if !plainPattern(text) {
				return nil, fmt.Errorf("reports: schema%s/pattern uses more than these rules know", at)
			}
			node.patternText = text
			node.pattern, err = regexp.Compile(text)
		case "maxLength":
			node.maxLength, err = count(value, keyword)
		case "minLength":
			node.minLength, err = count(value, keyword)
		case "maxItems":
			node.maxItems, err = count(value, keyword)
		case "minItems":
			node.minItems, err = count(value, keyword)
		default:
			err = fmt.Errorf("reports: schema%s uses %q, which these rules don't know", at, keyword)
		}
		if err != nil {
			return nil, err
		}
	}
	return node, nil
}

// Errors gives the problems with a document, in words; empty when it is valid.
func (s *Schema) Errors(document any) []string {
	return s.root.errors(nil, document, "")
}

func location(pointer string) string {
	if pointer == "" {
		return "root"
	}
	return "`" + pointer + "`"
}

var pointerEscapes = strings.NewReplacer("~", "~0", "/", "~1")

// The keywords in the order json_schemer applies them, which is the order of
// its errors: items, properties, additionalProperties, type, enum, const,
// maximum, minimum, maxLength, minLength, pattern, maxItems, minItems, required.
func (n *schemaNode) errors(problems []string, value any, pointer string) []string {
	at := location(pointer)
	object, isObject := value.(*Object)
	items, isArray := value.([]any)
	text, isString := value.(string)
	number := numeric(value)

	if n.items != nil && isArray {
		for i, item := range items {
			problems = n.items.errors(problems, item, pointer+"/"+strconv.Itoa(i))
		}
	}
	if isObject {
		known := map[string]bool{}
		for _, property := range n.properties {
			if member, ok := object.members[property.name]; ok {
				known[property.name] = true
				problems = property.node.errors(problems, member, pointer+"/"+pointerEscapes.Replace(property.name))
			}
		}
		if n.closed {
			for _, key := range object.keys {
				if !known[key] {
					problems = append(problems, "object property at "+location(pointer+"/"+pointerEscapes.Replace(key))+" is a disallowed additional property")
				}
			}
		}
	}
	if n.kind != "" && !isType(n.kind, value) {
		problems = append(problems, "value at "+at+" is not "+schemaTypes[n.kind])
	}
	if n.enum != nil {
		found := false
		for _, allowed := range n.enum {
			found = found || rubyEqual(allowed, value)
		}
		if !found {
			quoted := make([]string, len(n.enum))
			for i, allowed := range n.enum {
				quoted[i] = `"` + allowed.(string) + `"`
			}
			problems = append(problems, "value at "+at+" is not one of: ["+strings.Join(quoted, ", ")+"]")
		}
	}
	if n.hasConstant && !rubyEqual(n.constant, value) {
		problems = append(problems, "value at "+at+" is not: "+rubyToS(n.constant))
	}
	if n.maximum != nil && number != nil && number.Cmp(new(big.Float).SetInt(n.maximum)) > 0 {
		problems = append(problems, "number at "+at+" is greater than: "+n.maximum.String())
	}
	if n.minimum != nil && number != nil && number.Cmp(new(big.Float).SetInt(n.minimum)) < 0 {
		problems = append(problems, "number at "+at+" is less than: "+n.minimum.String())
	}
	if n.maxLength != nil && isString && utf8.RuneCountInString(text) > *n.maxLength {
		problems = append(problems, "string length at "+at+" is greater than: "+strconv.Itoa(*n.maxLength))
	}
	if n.minLength != nil && isString && utf8.RuneCountInString(text) < *n.minLength {
		problems = append(problems, "string length at "+at+" is less than: "+strconv.Itoa(*n.minLength))
	}
	if n.pattern != nil && isString && !n.pattern.MatchString(text) {
		problems = append(problems, "string at "+at+" does not match pattern: "+n.patternText)
	}
	if n.maxItems != nil && isArray && len(items) > *n.maxItems {
		problems = append(problems, "array size at "+at+" is greater than: "+strconv.Itoa(*n.maxItems))
	}
	if n.minItems != nil && isArray && len(items) < *n.minItems {
		problems = append(problems, "array size at "+at+" is less than: "+strconv.Itoa(*n.minItems))
	}
	if n.required != nil && isObject {
		var missing []string
		for _, name := range n.required {
			if _, ok := object.members[name]; !ok {
				missing = append(missing, name)
			}
		}
		if missing != nil {
			problems = append(problems, "object at "+at+" is missing required properties: "+strings.Join(missing, ", "))
		}
	}
	return problems
}

// numeric is a JSON number as an exact value, or nil.
func numeric(value any) *big.Float {
	switch value := value.(type) {
	case *big.Int:
		return new(big.Float).SetInt(value)
	case Float:
		return big.NewFloat(float64(value))
	}
	return nil
}

func isType(kind string, value any) bool {
	switch value := value.(type) {
	case *Object:
		return kind == "object"
	case []any:
		return kind == "array"
	case string:
		return kind == "string"
	case *big.Int:
		return kind == "integer" || kind == "number"
	case Float:
		f := float64(value)
		return kind == "number" || (kind == "integer" && !math.IsInf(f, 0) && f == math.Floor(f))
	case bool:
		return kind == "boolean"
	case nil:
		return kind == "null"
	}
	return false
}

// rubyEqual is Ruby's ==, which the old validator compared with: 1 == 1.0.
func rubyEqual(a, b any) bool {
	if x, y := numeric(a), numeric(b); x != nil || y != nil {
		return x != nil && y != nil && x.Cmp(y) == 0
	}
	switch a := a.(type) {
	case string:
		b, ok := b.(string)
		return ok && a == b
	case bool:
		b, ok := b.(bool)
		return ok && a == b
	}
	return false
}

// rubyToS is Ruby's to_s for a string, true, false or a whole number; ""
// stands for anything else.
func rubyToS(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case *big.Int:
		return value.String()
	}
	return ""
}
