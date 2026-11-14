package web

// The Volunteer's terms and the privacy statement are two text files in
// texts/. They are compiled into labd, which serves them at /terms and
// /privacy, and they ship beside it in a Lab release. A text's version is
// its date, on its second line, and the sha256 of the file.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"regexp"
	"strings"
)

//go:embed texts/terms.txt
var termsText string

//go:embed texts/privacy.txt
var privacyText string

// Text is one of the two texts, as shipped.
type Text struct {
	Body   string
	Date   string // 2026-11-14
	SHA256 string
}

var reTextDate = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)

func textOf(body string) Text {
	sum := sha256.Sum256([]byte(body))
	t := Text{Body: body, SHA256: hex.EncodeToString(sum[:])}
	if lines := strings.SplitN(body, "\n", 3); len(lines) > 1 {
		t.Date = reTextDate.FindString(lines[1])
	}
	return t
}

// Terms are the Volunteer's terms.
func Terms() Text { return textOf(termsText) }

// Privacy is the privacy statement.
func Privacy() Text { return textOf(privacyText) }
