package server

import (
	"context"
	"hash/fnv"
	"log"
	"testing"
)

// testAccount is the account id a test's person goes by: one per handle.
func testAccount(handle string) int64 {
	h := fnv.New32a()
	h.Write([]byte(handle))
	return 100000 + int64(h.Sum32())
}

// seedMaintainer makes handle a person of s with the Maintainer role who
// holds token as a Lab token that never expires, as if they had signed in
// and made it on their own page. It changes nothing the second time.
func seedMaintainer(s *Server, handle, token string) error {
	id, now := testAccount(handle), s.now()
	if err := s.store.signedInAs(id, handle, now); err != nil {
		return err
	}
	if err := s.store.setMaintainer(id, true); err != nil {
		return err
	}
	_, err := s.store.db.Exec(`INSERT INTO lab_tokens (hash, account_id, name, period, made, told) VALUES (?, ?, 'test', 0, ?, 1) ON CONFLICT (hash) DO NOTHING`,
		hashSecret(token), id, now.UnixMicro())
	return err
}

// newWith is New for a labd its test reaches as the Maintainer handle, with
// token.
func newWith(handle, token string, ctx context.Context, cfg Config, logger *log.Logger) (*Server, error) {
	s, err := New(ctx, cfg, logger)
	if err == nil {
		err = seedMaintainer(s, handle, token)
	}
	return s, err
}

// seeded is seedMaintainer for a labd already made.
func seeded(tb testing.TB, s *Server, handle, token string) {
	tb.Helper()
	if err := seedMaintainer(s, handle, token); err != nil {
		tb.Fatal(err)
	}
}
