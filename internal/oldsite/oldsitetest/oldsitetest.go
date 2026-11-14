// Package oldsitetest is a made-up old site for tests: its three tables
// filled from the golden reports, with what an import has to carry (hidden
// rows, an old deletion link, Mac sign-ins, handles that never signed in,
// a report the old site stored twice, one today's rules refuse). It gives
// the tables as a Source, and as the SQL that fills a Postgres with them.
package oldsitetest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/oldsite"
)

// The public ids and old deletion secrets tests ask for by name.
const (
	// Verified is a Verified run of machine "a", the golden m2-max-image2
	// report as that Mac would sign it; VerifiedToken opens its old
	// deletion link.
	Verified      = "h7Qm2ZpX4cVb9NkT3sWd8RfY"
	VerifiedToken = "Zm9yIHRoZSBvbGQgc2l0ZSdzIGRlbGV0aW9uIGxpbms_-x"
	// Outsider came from a Mac signed in as a handle off the allowlist.
	Outsider = "b2Outsider0000000000000b"
	// Hidden is a hidden report.
	Hidden = "c3Hidden000000000000000c"
	// BeforeKeys has an "ip:" machine id and NoMachine has none.
	BeforeKeys = "d4BeforeKeys00000000000d"
	NoMachine  = "e5NoMachine000000000000e"
	// LeftTheTeam names a handle the allowlist no longer has and no Mac is
	// signed in as.
	LeftTheTeam = "f6LeftTheTeam0000000000f"
	// Twice is Verified stored a second time, as the old site allowed.
	Twice = "g7Twice0000000000000000g"
	// UnknownCheck has a check id the catalogue lacks; GoneCheck is the id.
	UnknownCheck = "h8UnknownCheck000000000h"
	GoneCheck    = "display.gone"
	// Benchmark is the golden benchmark run.
	Benchmark = "j9Benchmark000000000000j"
)

// Corpus is the made-up old site.
type Corpus struct {
	Testers  []oldsite.Tester
	Bindings []oldsite.TesterBinding
	Reports  []oldsite.Report
	// Files is each report's body as the old site was sent it, before
	// Postgres wrote it out its own way.
	Files map[string][]byte
}

// Root is the repository's root, found from the test's own folder.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = filepath.Dir(dir)
	}
}

// Machine is a test Mac's key, made from its name as the report tests make
// theirs.
func Machine(name string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("test machine " + name))
	return ed25519.NewKeyFromSeed(seed[:])
}

func keyBlob(name string) []byte {
	var out []byte
	for _, part := range [][]byte{[]byte("ssh-ed25519"), Machine(name).Public().(ed25519.PublicKey)} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(part)))
		out = append(out, part...)
	}
	return out
}

// PublicKey is the Machine public key of a test Mac, as a Mac writes it.
func PublicKey(name string) string {
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(keyBlob(name))
}

// MachineID is the machine id the old site gave a test Mac, under its
// machine-id key.
func MachineID(machineIDKey []byte, name string) string {
	mac := hmac.New(sha256.New, machineIDKey)
	mac.Write(keyBlob(name))
	return "key:" + hex.EncodeToString(mac.Sum(nil))[:20]
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func stamp(t testing.TB, text string) oldsite.Stamp {
	t.Helper()
	at, err := oldsite.ParseStamp(text)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func decode(t testing.TB, data []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func encode(t testing.TB, doc any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Made is the corpus. machineIDKey is the old site's machine-id key: the
// test Macs' machine ids are made under it.
func Made(t testing.TB, machineIDKey []byte) *Corpus {
	t.Helper()
	golden := filepath.Join(Root(t), "schema", "golden")
	c := &Corpus{Files: map[string][]byte{}}
	id := func(n int64) *int64 { return &n }
	text := func(s string) *string { return &s }
	machine := func(name string) *string { return text(MachineID(machineIDKey, name)) }

	added := stamp(t, "2026-10-28T12:00:00.654321")
	c.Testers = []oldsite.Tester{
		{ID: 1, Login: "maralcbr", GitHubID: id(1001), CreatedAt: stamp(t, "2026-09-27T00:00:00.000000"), UpdatedAt: stamp(t, "2026-09-27T10:11:12.131415")},
		{ID: 2, Login: "scottjones", GitHubID: id(1002), CreatedAt: stamp(t, "2026-10-28T00:00:00.000001"), UpdatedAt: stamp(t, "2026-10-29T07:08:09.101112")},
		{ID: 3, Login: "dhh", CreatedAt: added, UpdatedAt: added},
		{ID: 4, Login: "ryanrhughes", CreatedAt: added, UpdatedAt: added},
		{ID: 5, Login: "gone-handle", CreatedAt: added, UpdatedAt: added},
		{ID: 7, Login: "demo-tester", GitHubID: id(4242), CreatedAt: added, UpdatedAt: added},
	}
	c.Bindings = []oldsite.TesterBinding{
		{ID: 1, MachineID: *machine("a"), GitHubLogin: "maralcbr", GitHubID: 1001, CreatedAt: stamp(t, "2026-09-27T10:11:12.000007"), UpdatedAt: stamp(t, "2026-10-30T23:59:59.999999")},
		{ID: 3, MachineID: *machine("d"), GitHubLogin: "scottjones", GitHubID: 1002, CreatedAt: stamp(t, "2026-10-29T07:08:09.101112"), UpdatedAt: stamp(t, "2026-10-29T07:08:09.101112")},
		{ID: 4, MachineID: *machine("b"), GitHubLogin: "outsider", GitHubID: 5005, CreatedAt: stamp(t, "2026-10-01T01:01:01.010101"), UpdatedAt: stamp(t, "2026-10-01T01:01:01.010101")},
		{ID: 6, MachineID: *machine("e"), GitHubLogin: "maralcbr", GitHubID: 1001, CreatedAt: stamp(t, "2026-11-01T00:00:00.000500"), UpdatedAt: stamp(t, "2026-11-02T00:00:00.000600")},
	}

	add := func(row oldsite.Report, doc map[string]any) {
		delete(doc, "signature")
		file := encode(t, doc)
		row.SchemaVersion, row.Body = 1, JSONB(t, file)
		if row.UpdatedAt == 0 {
			row.UpdatedAt = row.CreatedAt
		}
		if row.DeletionTokenDigest == "" {
			row.DeletionTokenDigest = digest("the old site's token for " + row.PublicID)
		}
		c.Files[row.PublicID] = file
		c.Reports = append(c.Reports, row)
	}

	var export struct {
		Reports []struct {
			ID         string          `json:"id"`
			UploadedAt string          `json:"uploaded_at"`
			Report     json.RawMessage `json:"report"`
		} `json:"reports"`
	}
	data, err := os.ReadFile(filepath.Join(golden, "production", "reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatal(err)
	}
	for i, entry := range export.Reports {
		at := stamp(t, strings.TrimSuffix(entry.UploadedAt, "Z")+fmt.Sprintf(".%06d", i*99999))
		row := oldsite.Report{ID: int64(2*i + 1), PublicID: entry.ID, CreatedAt: at, MachineID: text("key:" + digest(fmt.Sprintf("snapshot-%d", i))[:20])}
		if i%3 == 0 {
			row.TesterLogin = text("demo-tester")
		}
		if i == 4 {
			hidden := stamp(t, "2026-10-20T20:20:20.202020")
			row.HiddenAt, row.UpdatedAt = &hidden, hidden
		}
		add(row, decode(t, entry.Report))
	}

	file := func(name string) map[string]any {
		data, err := os.ReadFile(filepath.Join(golden, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return decode(t, data)
	}
	firstCheck := func(doc map[string]any) map[string]any {
		return doc["checks"].([]any)[0].(map[string]any)
	}
	hidden := stamp(t, "2026-10-03T03:03:03.000003")

	add(oldsite.Report{ID: 40, PublicID: Verified, DeletionTokenDigest: digest(VerifiedToken), CreatedAt: stamp(t, "2026-10-01T03:04:05.123456"),
		MachineID: machine("a"), TesterLogin: text("maralcbr")}, file("m2-max-image2"))
	add(oldsite.Report{ID: 41, PublicID: Outsider, CreatedAt: stamp(t, "2026-10-02T00:00:00.000001"),
		MachineID: machine("b"), TesterLogin: text("outsider")}, file("m1-pro-converged"))
	add(oldsite.Report{ID: 42, PublicID: Hidden, CreatedAt: stamp(t, "2026-10-02T12:00:00.999999"), UpdatedAt: hidden, HiddenAt: &hidden,
		MachineID: machine("c")}, file("m1-pro-mx-mac"))
	add(oldsite.Report{ID: 43, PublicID: BeforeKeys, CreatedAt: stamp(t, "2026-09-26T09:00:00.000100"),
		MachineID: text("ip:0123456789abcdef0123")}, file("m2-max-converged"))
	add(oldsite.Report{ID: 44, PublicID: NoMachine, CreatedAt: stamp(t, "2026-09-26T08:00:00.000000")}, file("m1-pro-converged-fresh"))

	left := file("m2-max-image2")
	check := firstCheck(left)
	check["evidence"] = append(check["evidence"].([]any), "run: 1")
	add(oldsite.Report{ID: 45, PublicID: LeftTheTeam, CreatedAt: stamp(t, "2026-10-04T04:04:04.040404"),
		MachineID: machine("a"), TesterLogin: text("left-the-team")}, left)

	add(oldsite.Report{ID: 46, PublicID: Twice, CreatedAt: stamp(t, "2026-10-01T03:04:06.000000"),
		MachineID: machine("a"), TesterLogin: text("maralcbr")}, file("m2-max-image2"))

	gone := file("m2-max-image2")
	firstCheck(gone)["id"] = GoneCheck
	add(oldsite.Report{ID: 47, PublicID: UnknownCheck, CreatedAt: stamp(t, "2026-10-29T08:00:00.500000"),
		MachineID: machine("d"), TesterLogin: text("scottjones")}, gone)

	add(oldsite.Report{ID: 60, PublicID: Benchmark, CreatedAt: stamp(t, "2026-11-01T11:11:11.111111"),
		MachineID: machine("a"), TesterLogin: text("maralcbr")}, file(filepath.Join("benchmarks", "m2-max-converged")))
	return c
}

// Counts is how many rows each table of the corpus has.
func (c *Corpus) Counts() oldsite.Counts {
	return oldsite.Counts{Reports: int64(len(c.Reports)), TesterBindings: int64(len(c.Bindings)), Testers: int64(len(c.Testers))}
}

// Read makes the corpus a Source.
func (c *Corpus) Read(ctx context.Context, sink oldsite.Sink) (oldsite.Counts, error) {
	for i := range c.Testers {
		if err := sink.Tester(&c.Testers[i]); err != nil {
			return oldsite.Counts{}, err
		}
	}
	for i := range c.Bindings {
		if err := sink.TesterBinding(&c.Bindings[i]); err != nil {
			return oldsite.Counts{}, err
		}
	}
	for i := range c.Reports {
		if err := ctx.Err(); err != nil {
			return oldsite.Counts{}, err
		}
		if err := sink.Report(&c.Reports[i]); err != nil {
			return oldsite.Counts{}, err
		}
	}
	return c.Counts(), nil
}

// Report is the corpus's report with that public id.
func (c *Corpus) Report(t testing.TB, publicID string) *oldsite.Report {
	t.Helper()
	for i := range c.Reports {
		if c.Reports[i].PublicID == publicID {
			return &c.Reports[i]
		}
	}
	t.Fatalf("the corpus has no report %s", publicID)
	return nil
}

// Dump writes the corpus to a dump file in a folder of the test's own.
func (c *Corpus) Dump(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old-site.dump")
	if _, err := oldsite.WriteDump(context.Background(), path, c, stamp(t, "2026-11-15T01:02:03.000000").Time()); err != nil {
		t.Fatal(err)
	}
	return path
}

// JSONB is a JSON text as Postgres writes a jsonb value out: an object's
// keys by length and then by their bytes, each once, with a space after
// every colon and comma.
func JSONB(t testing.TB, text []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return string(jsonb(t, nil, value))
}

func jsonb(t testing.TB, out []byte, value any) []byte {
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		out = append(out, '{')
		for i, key := range keys {
			if i > 0 {
				out = append(out, ", "...)
			}
			out = append(jsonbString(out, key), ": "...)
			out = jsonb(t, out, value[key])
		}
		return append(out, '}')
	case []any:
		out = append(out, '[')
		for i, item := range value {
			if i > 0 {
				out = append(out, ", "...)
			}
			out = jsonb(t, out, item)
		}
		return append(out, ']')
	case string:
		return jsonbString(out, value)
	case json.Number:
		if strings.ContainsAny(string(value), "eE") || strings.HasPrefix(string(value), "-0") {
			t.Fatalf("the corpus has a number Postgres would write another way: %s", value)
		}
		return append(out, value...)
	case bool:
		return fmt.Append(out, value)
	case nil:
		return append(out, "null"...)
	}
	t.Fatalf("%T is not a JSON value", value)
	return nil
}

func jsonbString(out []byte, text string) []byte {
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

// Schema is the old site's three tables as Rails makes them from
// site/db/schema.rb.
const Schema = `CREATE TABLE reports (
  id bigserial PRIMARY KEY,
  public_id character varying NOT NULL,
  deletion_token_digest character varying NOT NULL,
  schema_version integer NOT NULL,
  body jsonb NOT NULL,
  created_at timestamp(6) without time zone NOT NULL,
  updated_at timestamp(6) without time zone NOT NULL,
  hidden_at timestamp(6) without time zone,
  machine_id character varying,
  tester_login character varying
);
CREATE UNIQUE INDEX index_reports_on_public_id ON reports (public_id);
CREATE TABLE tester_bindings (
  id bigserial PRIMARY KEY,
  machine_id character varying NOT NULL,
  github_login character varying NOT NULL,
  github_id bigint NOT NULL,
  created_at timestamp(6) without time zone NOT NULL,
  updated_at timestamp(6) without time zone NOT NULL
);
CREATE INDEX index_tester_bindings_on_github_login ON tester_bindings (github_login);
CREATE UNIQUE INDEX index_tester_bindings_on_machine_id ON tester_bindings (machine_id);
CREATE TABLE testers (
  id bigserial PRIMARY KEY,
  login character varying NOT NULL,
  github_id bigint,
  created_at timestamp(6) without time zone NOT NULL,
  updated_at timestamp(6) without time zone NOT NULL
);
CREATE UNIQUE INDEX index_testers_on_login ON testers (login);
`

func quote(text *string) string {
	if text == nil {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(*text, "'", "''") + "'"
}

func quoteStamp(at *oldsite.Stamp) string {
	if at == nil {
		return "NULL"
	}
	return "'" + strings.Replace(at.String(), "T", " ", 1) + "'"
}

func number(n *int64) string {
	if n == nil {
		return "NULL"
	}
	return fmt.Sprint(*n)
}

// Rows is the SQL that fills the three tables, one statement a line. A
// report's body goes in as the old site was sent it.
func (c *Corpus) Rows() string {
	var b strings.Builder
	for _, row := range c.Testers {
		fmt.Fprintf(&b, "INSERT INTO testers (id, login, github_id, created_at, updated_at) VALUES (%d, %s, %s, %s, %s);\n",
			row.ID, quote(&row.Login), number(row.GitHubID), quoteStamp(&row.CreatedAt), quoteStamp(&row.UpdatedAt))
	}
	for _, row := range c.Bindings {
		fmt.Fprintf(&b, "INSERT INTO tester_bindings (id, machine_id, github_login, github_id, created_at, updated_at) VALUES (%d, %s, %s, %d, %s, %s);\n",
			row.ID, quote(&row.MachineID), quote(&row.GitHubLogin), row.GitHubID, quoteStamp(&row.CreatedAt), quoteStamp(&row.UpdatedAt))
	}
	for _, row := range c.Reports {
		var compact bytes.Buffer
		if err := json.Compact(&compact, c.Files[row.PublicID]); err != nil {
			panic(err)
		}
		body := compact.String()
		fmt.Fprintf(&b, "INSERT INTO reports (id, public_id, deletion_token_digest, schema_version, body, created_at, updated_at, hidden_at, machine_id, tester_login) VALUES (%d, %s, %s, %d, %s::jsonb, %s, %s, %s, %s, %s);\n",
			row.ID, quote(&row.PublicID), quote(&row.DeletionTokenDigest), row.SchemaVersion, quote(&body),
			quoteStamp(&row.CreatedAt), quoteStamp(&row.UpdatedAt), quoteStamp(row.HiddenAt), quote(row.MachineID), quote(row.TesterLogin))
	}
	return b.String()
}
