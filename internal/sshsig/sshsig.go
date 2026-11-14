// Package sshsig checks a file's signature as `ssh-keygen -Y verify` does,
// against one pinned ed25519 key, with no program to run: lab-agent checks a
// new lab-agent with it on a Mac.
package sshsig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
)

const (
	magic   = "SSHSIG"
	keyType = "ssh-ed25519"
	armor   = "SSH SIGNATURE"
)

// Verify reports whether sig is a signature of message under namespace by
// key, an ed25519 public key as "ssh-ed25519 <base64>". Nothing else passes:
// another key, another namespace, another kind of key.
func Verify(key, namespace string, message io.Reader, sig []byte) error {
	pinned, err := publicKey(key)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(sig)
	if block == nil || block.Type != armor {
		return errors.New("not an SSH signature")
	}
	b := block.Bytes
	if !bytes.HasPrefix(b, []byte(magic)) || len(b) < len(magic)+4 || binary.BigEndian.Uint32(b[len(magic):]) != 1 {
		return errors.New("not an SSH signature of version 1")
	}
	b = b[len(magic)+4:]
	var fields [5][]byte // public key, namespace, reserved, hash algorithm, signature
	for i := range fields {
		if fields[i], b, err = str(b); err != nil {
			return errors.New("the SSH signature is cut short")
		}
	}
	signer, err := keyBlob(fields[0])
	if err != nil {
		return err
	}
	if !bytes.Equal(signer, pinned) {
		return errors.New("signed by another key than the pinned one")
	}
	if string(fields[1]) != namespace {
		return fmt.Errorf("signed for %q, not for %q", fields[1], namespace)
	}
	var h hash.Hash
	switch string(fields[3]) {
	case "sha512":
		h = sha512.New()
	case "sha256":
		h = sha256.New()
	default:
		return fmt.Errorf("unknown hash %q", fields[3])
	}
	if _, err := io.Copy(h, message); err != nil {
		return err
	}
	kind, rest, err := str(fields[4])
	if err != nil || string(kind) != keyType {
		return errors.New("not an ed25519 signature")
	}
	raw, _, err := str(rest)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("not an ed25519 signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pinned), Signed(namespace, string(fields[3]), h.Sum(nil)), raw) {
		return errors.New("the signature does not match the file")
	}
	return nil
}

// Signed is what an SSH signature signs: the namespace, the hash's name and
// the file's hash.
func Signed(namespace, hashName string, sum []byte) []byte {
	out := []byte(magic)
	for _, f := range [][]byte{[]byte(namespace), nil, []byte(hashName), sum} {
		out = AppendString(out, f)
	}
	return out
}

// AppendString appends s as SSH writes a string: its length, then its bytes.
func AppendString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

func str(b []byte) (s, rest []byte, err error) {
	if len(b) < 4 {
		return nil, nil, io.ErrUnexpectedEOF
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, io.ErrUnexpectedEOF
	}
	return b[4 : 4+n], b[4+n:], nil
}

// publicKey is the 32 bytes of an ed25519 key written "ssh-ed25519 <base64>".
func publicKey(key string) ([]byte, error) {
	f := strings.Fields(key)
	if len(f) < 2 || f[0] != keyType {
		return nil, errors.New("the pinned key is not an ed25519 public key")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return nil, errors.New("the pinned key is not an ed25519 public key")
	}
	k, err := keyBlob(blob)
	if err != nil {
		return nil, errors.New("the pinned key is not an ed25519 public key")
	}
	return k, nil
}

func keyBlob(blob []byte) ([]byte, error) {
	kind, rest, err := str(blob)
	if err != nil || string(kind) != keyType {
		return nil, errors.New("signed by a key that is not ed25519")
	}
	k, rest, err := str(rest)
	if err != nil || len(k) != ed25519.PublicKeySize || len(rest) != 0 {
		return nil, errors.New("signed by a key that is not ed25519")
	}
	return k, nil
}
