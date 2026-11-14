// Package sshsigtest makes throwaway release keys for tests and signs with
// them as `ssh-keygen -Y sign` does.
package sshsigtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/sshsig"
)

// Key is a throwaway signing key.
type Key struct {
	// Public is its public half as "ssh-ed25519 <base64>".
	Public string
	blob   []byte
	priv   ed25519.PrivateKey
}

func NewKey(t testing.TB) *Key {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := sshsig.AppendString(sshsig.AppendString(nil, []byte("ssh-ed25519")), pub)
	return &Key{Public: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob), blob: blob, priv: priv}
}

// Sign is the armored signature of message under namespace.
func (k *Key) Sign(namespace string, message []byte) []byte {
	sum := sha512.Sum512(message)
	raw := ed25519.Sign(k.priv, sshsig.Signed(namespace, "sha512", sum[:]))
	b := append([]byte("SSHSIG"), 0, 0, 0, 1)
	for _, f := range [][]byte{k.blob, []byte(namespace), nil, []byte("sha512"),
		sshsig.AppendString(sshsig.AppendString(nil, []byte("ssh-ed25519")), raw)} {
		b = sshsig.AppendString(b, f)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "SSH SIGNATURE", Bytes: b})
}

// Signers is the key as an allowed_signers file for signer and namespace.
func (k *Key) Signers(signer, namespace string) string {
	return signer + ` namespaces="` + namespace + `" ` + k.Public + "\n"
}
