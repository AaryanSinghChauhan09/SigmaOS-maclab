package web

// The Volunteer's terms and the privacy statement are two text files in
// texts/. They are compiled into labd, which serves them at /terms and
// /privacy, and they ship beside it in a Lab release. A text's version is
// its date and the sha256 of the file. The privacy statement carries its
// date on its second line; the terms are one line, dated by termsDate.

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
	Line   string // the terms' one line, without its newline
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

// termsDate is the date of texts/terms.txt: change both together.
const termsDate = "2026-11-17"

// TermsLine is the Volunteer's terms, the one line of texts/terms.txt.
var TermsLine = strings.TrimRight(termsText, "\n")

// Terms are the Volunteer's terms.
func Terms() Text {
	t := textOf(termsText)
	t.Date, t.Line = termsDate, TermsLine
	return t
}

// Privacy is the privacy statement.
func Privacy() Text { return textOf(privacyText) }
