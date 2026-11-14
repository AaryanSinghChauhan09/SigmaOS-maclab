package oldsite

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"
)

// A dump is one file of JSON lines:
//
//	{"dump":"omarchy-m-testing.org","version":1,"taken_at":"2026-11-15T01:02:03Z"}
//	{"table":"testers","row":{...}}            every tester, by id
//	{"table":"tester_bindings","row":{...}}    every tester_binding, by id
//	{"table":"reports","row":{...}}            every report, by id
//	{"end":true,"source":{...},"rows":{...},"sha256":"..."}
//
// A row holds its columns under their own names; a report's body is the
// jsonb column's text in a JSON string. The last line counts the rows of
// each table, as the source counted them and as they are written, and its
// sha256 is of every byte before it: a dump that was cut short or changed
// is not read.

const (
	dumpName    = "omarchy-m-testing.org"
	dumpVersion = 1
)

var tableOrder = []string{"testers", "tester_bindings", "reports"}

type dumpHead struct {
	Dump    string    `json:"dump"`
	Version int       `json:"version"`
	TakenAt time.Time `json:"taken_at"`
}

type dumpRow struct {
	Table string `json:"table"`
	Row   any    `json:"row"`
}

type dumpEnd struct {
	End    bool   `json:"end"`
	Source Counts `json:"source"`
	Rows   Counts `json:"rows"`
	SHA256 string `json:"sha256"`
}

type dumpWriter struct {
	out   *bufio.Writer
	sum   hash.Hash
	rows  Counts
	table int
	last  int64
}

func (w *dumpWriter) line(value any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return err
	}
	_, err := io.MultiWriter(w.out, w.sum).Write(buf.Bytes())
	return err
}

// next keeps the tables and their rows in the order a dump has them.
func (w *dumpWriter) next(table int, id int64) error {
	if table < w.table || (table == w.table && id <= w.last) {
		return fmt.Errorf("the source gave %s row %d out of order", tableOrder[table], id)
	}
	w.table, w.last = table, id
	return nil
}

func (w *dumpWriter) Tester(row *Tester) error {
	if err := w.next(0, row.ID); err != nil {
		return err
	}
	w.rows.Testers++
	return w.line(dumpRow{"testers", row})
}

func (w *dumpWriter) TesterBinding(row *TesterBinding) error {
	if err := w.next(1, row.ID); err != nil {
		return err
	}
	w.rows.TesterBindings++
	return w.line(dumpRow{"tester_bindings", row})
}

func (w *dumpWriter) Report(row *Report) error {
	if err := w.next(2, row.ID); err != nil {
		return err
	}
	w.rows.Reports++
	return w.line(dumpRow{"reports", row})
}

// WriteDump reads src and writes its three tables to one file at path, for
// its own account alone. It never replaces a file: a dump is written beside
// its name and takes the name only once it is whole, so a run that was cut
// leaves nothing under the name and can be started again.
func WriteDump(ctx context.Context, path string, src Source, now time.Time) (Counts, error) {
	if _, err := os.Lstat(path); err == nil {
		return Counts{}, fmt.Errorf("%s is already there: a dump is never replaced. Name another file, or remove that one", path)
	} else if !os.IsNotExist(err) {
		return Counts{}, err
	}
	part := path + ".part"
	if err := os.Remove(part); err != nil && !os.IsNotExist(err) {
		return Counts{}, err
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Counts{}, err
	}
	counts, err := writeDump(ctx, f, src, now)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// A link, not a rename: it fails if the name was taken meanwhile.
		err = os.Link(part, path)
	}
	os.Remove(part)
	if err != nil {
		return Counts{}, err
	}
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil {
		dir.Sync()
		dir.Close()
	}
	return counts, nil
}

func writeDump(ctx context.Context, f *os.File, src Source, now time.Time) (Counts, error) {
	w := &dumpWriter{out: bufio.NewWriterSize(f, 1<<20), sum: sha256.New()}
	if err := w.line(dumpHead{dumpName, dumpVersion, now.UTC().Truncate(time.Second)}); err != nil {
		return Counts{}, err
	}
	said, err := src.Read(ctx, w)
	if err != nil {
		return Counts{}, err
	}
	if said != w.rows {
		return Counts{}, fmt.Errorf("the source counts %v and gave %v: nothing was kept", said, w.rows)
	}
	end, err := json.Marshal(dumpEnd{true, said, w.rows, hex.EncodeToString(w.sum.Sum(nil))})
	if err != nil {
		return Counts{}, err
	}
	if _, err := w.out.Write(append(end, '\n')); err != nil {
		return Counts{}, err
	}
	if err := w.out.Flush(); err != nil {
		return Counts{}, err
	}
	return w.rows, f.Sync()
}

// Dump is a dump file that was read whole and found whole.
type Dump struct {
	Path    string
	TakenAt time.Time
	// Source is each table's count as the old site's Postgres gave it, Rows
	// what the file holds. OpenDump refuses a file where they differ.
	Source, Rows Counts

	sum string
}

// OpenDump reads the dump at path from its first byte to its last and
// refuses one that is not whole.
func OpenDump(path string) (*Dump, error) {
	d := &Dump{Path: path}
	if err := d.scan(context.Background(), Funcs{}); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// Read hands sink the dump's rows. It reads the file again and fails if it
// is not the file OpenDump read.
func (d *Dump) Read(ctx context.Context, sink Sink) (Counts, error) {
	again := &Dump{Path: d.Path}
	if err := again.scan(ctx, sink); err != nil {
		return Counts{}, fmt.Errorf("%s: %w", d.Path, err)
	}
	if again.sum != d.sum {
		return Counts{}, fmt.Errorf("%s changed while it was being read", d.Path)
	}
	return d.Source, nil
}

var errNotWhole = errors.New("the dump is not whole: it ends before its last line")

func strict(line []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("more than one value on a line")
	}
	return nil
}

func (d *Dump) scan(ctx context.Context, sink Sink) error {
	f, err := os.Open(d.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	in := bufio.NewReaderSize(f, 1<<20)
	sum := sha256.New()
	next := func() ([]byte, error) {
		line, err := in.ReadBytes('\n')
		if err == io.EOF {
			return nil, errNotWhole
		}
		return line, err
	}
	line, err := next()
	if err != nil {
		return err
	}
	var head dumpHead
	if err := strict(line, &head); err != nil || head.Dump != dumpName {
		return errors.New("not a dump of the old site's tables")
	}
	if head.Version != dumpVersion {
		return fmt.Errorf("a dump of version %d, and this command reads version %d", head.Version, dumpVersion)
	}
	sum.Write(line)
	var (
		rows  Counts
		order dumpWriter
	)
	for n := 2; ; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if line, err = next(); err != nil {
			return err
		}
		var probe struct {
			Table string          `json:"table"`
			Row   json.RawMessage `json:"row"`
			End   bool            `json:"end"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return fmt.Errorf("line %d is not a line of a dump", n)
		}
		if probe.End {
			var end dumpEnd
			if err := strict(line, &end); err != nil {
				return fmt.Errorf("line %d is not a dump's last line", n)
			}
			if _, err := in.ReadByte(); err != io.EOF {
				return errors.New("the dump goes on after its last line")
			}
			d.sum = hex.EncodeToString(sum.Sum(nil))
			switch {
			case end.SHA256 != d.sum:
				return errors.New("the dump was changed after it was written: its digest is another")
			case end.Rows != rows:
				return fmt.Errorf("the dump says it holds %v and holds %v", end.Rows, rows)
			case end.Source != rows:
				return fmt.Errorf("the source counted %v and the dump holds %v", end.Source, rows)
			}
			d.TakenAt, d.Source, d.Rows = head.TakenAt, end.Source, rows
			return nil
		}
		sum.Write(line)
		switch probe.Table {
		case "testers":
			var row Tester
			if err = strict(probe.Row, &row); err == nil {
				if err = order.next(0, row.ID); err == nil {
					rows.Testers++
					err = sink.Tester(&row)
				}
			}
		case "tester_bindings":
			var row TesterBinding
			if err = strict(probe.Row, &row); err == nil {
				if err = order.next(1, row.ID); err == nil {
					rows.TesterBindings++
					err = sink.TesterBinding(&row)
				}
			}
		case "reports":
			var row Report
			if err = strict(probe.Row, &row); err == nil {
				if err = order.next(2, row.ID); err == nil {
					rows.Reports++
					err = sink.Report(&row)
				}
			}
		default:
			err = fmt.Errorf("it names a table %q", probe.Table)
		}
		if err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
	}
}
