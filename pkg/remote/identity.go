package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Identity holds a persistent Ed25519 keypair, self-signed certificate, and derived NodeID/Fingerprint.
type Identity struct {
	NodeID      string            `json:"node_id"`
	Fingerprint string            `json:"fingerprint"`
	CertPEM     string            `json:"cert_pem"`
	KeyPEM      string            `json:"key_pem"`
	PubKeyPEM   string            `json:"pub_key_pem"`
	TLSCert     tls.Certificate   `json:"-"`
	PubKey      ed25519.PublicKey `json:"-"`
}

var base32LowerNoPad = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NodeIDFromPubKey deterministically derives nex_<24-char-base32> from an Ed25519 public key.
func NodeIDFromPubKey(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "nex_" + base32LowerNoPad.EncodeToString(sum[:15])
}

// FingerprintFromPubKey computes the 64-char lowercase hex SHA-256 of an Ed25519 public key.
func FingerprintFromPubKey(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// ShortFingerprint formats the first 16 hex characters as xxxx:xxxx:xxxx:xxxx for human verification.
func ShortFingerprint(fp string) string {
	clean := strings.ToLower(strings.TrimSpace(fp))
	if len(clean) < 16 {
		return clean
	}
	return fmt.Sprintf("%s:%s:%s:%s", clean[0:4], clean[4:8], clean[8:12], clean[12:16])
}

// GenerateIdentity creates a new Ed25519 keypair and 10-year self-signed X.509 certificate.
func GenerateIdentity(commonName string) (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("remote: generate ed25519 key: %w", err)
	}
	if strings.TrimSpace(commonName) == "" {
		commonName = NodeIDFromPubKey(pub)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("remote: generate serial: %w", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"Nex Remote Access"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("remote: create certificate: %w", err)
	}
	pkcs8Priv, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("remote: marshal private key: %w", err)
	}
	spkiPub, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("remote: marshal public key: %w", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Priv}))
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spkiPub}))

	id, err := ParseIdentityPEM(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	id.PubKeyPEM = pubPEM
	return id, nil
}

// ParseIdentityPEM reconstructs an Identity from PEM-encoded certificate and private key strings.
func ParseIdentityPEM(certPEM, keyPEM string) (*Identity, error) {
	tlsCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("remote: parse X509 keypair: %w", err)
	}
	if len(tlsCert.Certificate) == 0 {
		return nil, errors.New("remote: empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("remote: parse leaf certificate: %w", err)
	}
	tlsCert.Leaf = leaf
	pub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("remote: certificate public key is not ed25519")
	}
	spkiPub, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spkiPub}))

	return &Identity{
		NodeID:      NodeIDFromPubKey(pub),
		Fingerprint: FingerprintFromPubKey(pub),
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		PubKeyPEM:   pubPEM,
		TLSCert:     tlsCert,
		PubKey:      pub,
	}, nil
}

// LoadOrCreateIdentity loads a persisted identity JSON file or generates & saves a new one (0600).
func LoadOrCreateIdentity(filePath, commonName string) (*Identity, error) {
	if raw, err := os.ReadFile(filePath); err == nil {
		var saved struct {
			CertPEM string `json:"cert_pem"`
			KeyPEM  string `json:"key_pem"`
		}
		if json.Unmarshal(raw, &saved) == nil && saved.CertPEM != "" && saved.KeyPEM != "" {
			if id, pErr := ParseIdentityPEM(saved.CertPEM, saved.KeyPEM); pErr == nil {
				return id, nil
			}
		}
	}

	id, err := GenerateIdentity(commonName)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Dir(filePath), 0o700)
	b, err := json.MarshalIndent(struct {
		NodeID      string `json:"node_id"`
		Fingerprint string `json:"fingerprint"`
		CertPEM     string `json:"cert_pem"`
		KeyPEM      string `json:"key_pem"`
		PubKeyPEM   string `json:"pub_key_pem"`
	}{
		NodeID:      id.NodeID,
		Fingerprint: id.Fingerprint,
		CertPEM:     id.CertPEM,
		KeyPEM:      id.KeyPEM,
		PubKeyPEM:   id.PubKeyPEM,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filePath, b, 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

// FingerprintFromCert extracts and hashes an Ed25519 public key from a raw DER X.509 certificate.
func FingerprintFromCert(rawDER []byte) (string, error) {
	cert, err := x509.ParseCertificate(rawDER)
	if err != nil {
		return "", err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("remote: peer certificate public key is not ed25519")
	}
	return FingerprintFromPubKey(pub), nil
}

// FingerprintFromPubKeyPEM parses a PEM-encoded PKIX public key and returns its SHA-256 hex fingerprint.
func FingerprintFromPubKeyPEM(pubPEM string) (string, error) {
	blk, _ := pem.Decode([]byte(strings.TrimSpace(pubPEM)))
	if blk == nil {
		return "", errors.New("remote: invalid public key PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return "", fmt.Errorf("remote: parse public key PEM: %w", err)
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("remote: public key is not ed25519")
	}
	return FingerprintFromPubKey(pub), nil
}

// ServerTLSConfig returns a TLS 1.3 config enforcing client certificate authentication against verifyClient.
func ServerTLSConfig(id *Identity, verifyClient func(pubkeyFP string) (allowed bool, pairingOnly bool)) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.TLSCert},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{"http/1.1"},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("remote: client certificate required")
			}
			fp, err := FingerprintFromCert(rawCerts[0])
			if err != nil {
				return err
			}
			if verifyClient == nil {
				return errors.New("remote: no client verifier configured")
			}
			allowed, _ := verifyClient(fp)
			if !allowed {
				return errors.New("remote: peer certificate rejected")
			}
			return nil
		},
	}
}

// ClientTLSConfig returns a TLS 1.3 config pinning the server's Ed25519 public key fingerprint.
func ClientTLSConfig(clientID *Identity, expectedServerFP string) *tls.Config {
	want := strings.ToLower(strings.TrimSpace(expectedServerFP))
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{clientID.TLSCert},
		InsecureSkipVerify: true, // Verified via strict public-key fingerprint pinning below
		NextProtos:         []string{"http/1.1"},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("remote: server certificate required")
			}
			got, err := FingerprintFromCert(rawCerts[0])
			if err != nil {
				return err
			}
			if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return fmt.Errorf("remote: server fingerprint mismatch (got=%s want=%s)", ShortFingerprint(got), ShortFingerprint(want))
			}
			return nil
		},
	}
}
