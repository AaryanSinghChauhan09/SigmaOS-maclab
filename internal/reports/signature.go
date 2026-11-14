package reports

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"strings"
)

// The machine key's signature on an uploaded report, or on a Mac sign-in
// (the "signature" member): what `ssh-keygen -Y sign` makes with an ed25519
// key, an SSHSIG (PROTOCOL.sshsig in OpenSSH) over the document's canonical
// form. Every refusal is worded as the old site worded it.

const (
	// NamespaceReport is the SSHSIG namespace of a report.
	NamespaceReport = "omarchy-m-test-report"
	// NamespaceSignIn is that of a Mac sign-in and of the requests made
	// under one. The wire name stays "tester".
	NamespaceSignIn = "omarchy-m-test-tester"

	keyType = "ssh-ed25519"
)

// MachineKey is a Mac's public Machine key in SSH wire form. It is never
// stored, shown or exported: a Mac is known by its machine id.
type MachineKey []byte

var armorRE = regexp.MustCompile(`\A-----BEGIN SSH SIGNATURE-----\n([A-Za-z0-9+/=\n]+)\n-----END SSH SIGNATURE-----\z`)

const changed = Invalid("it doesn't match the report: the report was changed after it was signed")

// VerifySignature checks the document's "signature" member against the rest
// of the document and gives the key that made it. The error is an Invalid.
func VerifySignature(document *Object, namespace string) (MachineKey, error) {
	message, err := Canonical(document.without("signature"))
	if err != nil {
		return nil, err
	}
	signature, ok := document.members["signature"].(*Object)
	if !ok {
		return nil, Invalid("it isn't a signature object")
	}
	keyBlob, err := parsePublicKey(rubyToS(signature.members["public_key"]))
	if err != nil {
		return nil, err
	}
	blob, err := dearmor(rubyToS(signature.members["signature"]))
	if err != nil {
		return nil, err
	}

	sig := sshReader{bytes: blob}
	if magic, err := sig.raw(6); err != nil {
		return nil, err
	} else if string(magic) != "SSHSIG" {
		return nil, Invalid("it isn't an SSH signature")
	}
	if version, err := sig.uint32(); err != nil {
		return nil, err
	} else if version != 1 {
		return nil, Invalid("it isn't an SSH signature")
	}
	if signer, err := sig.string(); err != nil {
		return nil, err
	} else if !bytes.Equal(signer, keyBlob) {
		return nil, Invalid("it was made with a different key than the report's public_key")
	}
	signedNamespace, err := sig.string()
	if err != nil {
		return nil, err
	}
	if string(signedNamespace) != namespace {
		kind := "sign-in"
		if namespace == NamespaceReport {
			kind = "report"
		}
		return nil, Invalid("it isn't a " + kind + " signature (namespace)")
	}
	reserved, err := sig.string()
	if err != nil {
		return nil, err
	}
	hashName, err := sig.string()
	if err != nil {
		return nil, err
	}
	var digest []byte
	switch string(hashName) {
	case "sha512":
		sum := sha512.Sum512(message)
		digest = sum[:]
	case "sha256":
		sum := sha256.Sum256(message)
		digest = sum[:]
	default:
		return nil, Invalid("it uses an unknown hash (" + strings.ToValidUTF8(string(hashName), "�") + ")")
	}
	innerBytes, err := sig.string()
	if err != nil {
		return nil, err
	}
	if err := sig.finish(); err != nil {
		return nil, err
	}
	inner := sshReader{bytes: innerBytes}
	if kind, err := inner.string(); err != nil {
		return nil, err
	} else if string(kind) != keyType {
		return nil, Invalid("it isn't an ed25519 signature")
	}
	rawSignature, err := inner.string()
	if err != nil {
		return nil, err
	}
	if err := inner.finish(); err != nil {
		return nil, err
	}

	signedData := append([]byte("SSHSIG"), sshPack(signedNamespace, reserved, hashName, digest)...)
	if !ed25519.Verify(ed25519.PublicKey(keyBlob[len(keyBlob)-32:]), signedData, rawSignature) {
		return nil, changed
	}
	return MachineKey(keyBlob), nil
}

// MachineID is what reports group by: "key:" and the first 20 hex digits of
// a keyed digest of the Mac's public key. secret is the machine-id key.
func MachineID(secret []byte, key MachineKey) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(key)
	return "key:" + hex.EncodeToString(mac.Sum(nil))[:20]
}

func asciiSpace(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r')
}

func parsePublicKey(publicKey string) ([]byte, error) {
	const notAKey = Invalid("its public_key isn't an ed25519 key")
	fields := strings.FieldsFunc(publicKey, asciiSpace)
	if len(fields) != 2 || fields[0] != keyType {
		return nil, notAKey
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil {
		return nil, notAKey
	}
	reader := sshReader{bytes: blob}
	kind, err := reader.string()
	if err != nil {
		return nil, err
	}
	if string(kind) != keyType {
		return nil, notAKey
	}
	key, err := reader.string()
	if err != nil {
		return nil, err
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, notAKey
	}
	if err := reader.finish(); err != nil {
		return nil, err
	}
	return blob, nil
}

func dearmor(armored string) ([]byte, error) {
	const notArmored = Invalid("it isn't an armored SSH signature")
	match := armorRE.FindStringSubmatch(armored)
	if match == nil {
		return nil, notArmored
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(strings.ReplaceAll(match[1], "\n", ""))
	if err != nil {
		return nil, notArmored
	}
	return blob, nil
}

func sshPack(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = binary.BigEndian.AppendUint32(out, uint32(len(part)))
		out = append(out, part...)
	}
	return out
}

// sshReader reads the SSH wire format: uint32 and length-prefixed strings.
type sshReader struct {
	bytes []byte
	at    int
}

func (r *sshReader) raw(count int) ([]byte, error) {
	if count > len(r.bytes)-r.at {
		return nil, Invalid("it is cut short")
	}
	r.at += count
	return r.bytes[r.at-count : r.at], nil
}

func (r *sshReader) uint32() (uint32, error) {
	raw, err := r.raw(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(raw), nil
}

func (r *sshReader) string() ([]byte, error) {
	length, err := r.uint32()
	if err != nil {
		return nil, err
	}
	if uint64(length) > uint64(len(r.bytes)) {
		return nil, Invalid("it is cut short")
	}
	return r.raw(int(length))
}

func (r *sshReader) finish() error {
	if r.at != len(r.bytes) {
		return Invalid("it has trailing bytes")
	}
	return nil
}
