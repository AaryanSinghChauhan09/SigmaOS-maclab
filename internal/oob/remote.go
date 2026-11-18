package oob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewRemoteClient is labd's client for a Mac lab controller on another Mac:
// HTTPS to one address, its certificate pinned by its SHA-256 (as the
// controller's install prints it), the token in a file only labd reads.
func NewRemoteClient(addr, tokenFile, fingerprint string) (*Client, error) {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "https" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil || u.Port() == "" {
		return nil, fmt.Errorf("a Mac lab controller's address %q is not https://ADDRESS:PORT", addr)
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsUnspecified() {
		return nil, fmt.Errorf("%q names no one machine", addr)
	}
	pin, err := ParseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	if tokenFile == "" {
		return nil, errors.New("the controller's token has no file")
	}
	return &Client{URL: "https://" + u.Host, TokenFile: tokenFile, pin: pin}, nil
}

// ParseFingerprint reads a certificate's SHA-256 written in hex, with or
// without colons, as `openssl x509 -fingerprint -sha256` prints it.
func ParseFingerprint(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "sha256 Fingerprint=")
	b, err := hex.DecodeString(strings.ReplaceAll(s, ":", ""))
	if err != nil || len(b) != sha256.Size {
		return nil, fmt.Errorf("%q is not a certificate's SHA-256 fingerprint", s)
	}
	return b, nil
}

// Fingerprint is how a certificate's pin is written: hex, lower case.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Pinned is the fingerprint this client takes the controller's certificate
// by, "" for the mini's own.
func (c *Client) Pinned() string {
	if c.pin == nil {
		return ""
	}
	return hex.EncodeToString(c.pin)
}

// pinned is a transport that trusts exactly one certificate, whatever its
// name or issuer: a controller makes its own.
func pinned(pin []byte) *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by VerifyPeerCertificate's pin
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("the controller showed no certificate")
			}
			sum := sha256.Sum256(raw[0])
			if !bytes.Equal(sum[:], pin) {
				return fmt.Errorf("the controller's certificate is %x, not the one pinned", sum)
			}
			return nil
		}}}
}

// RIDs asks the controller which ports it has.
func (c *Client) RIDs(ctx context.Context) ([]int, error) {
	b, err := c.do(ctx, "GET", "/v1/ports", nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var out struct {
		RIDs []int `json:"rids"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("the controller's ports: %w", err)
	}
	for _, r := range out.RIDs {
		if r < 0 || r >= perController {
			return nil, fmt.Errorf("the controller has a port with RID %d, which the lab cannot name", r)
		}
	}
	if len(out.RIDs) == 0 {
		return nil, errors.New("the controller has no port")
	}
	return out.RIDs, nil
}
