package sshkeys

import (
	"crypto/dsa" //nolint:staticcheck // to make a DSA key to refuse
	"crypto/rand"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Keys made with ssh-keygen; each fingerprint is what `ssh-keygen -l` printed for it, without the "SHA256:".
const (
	ed25519Line = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBhgpejLQl2Ae5qAX7iRAbunnQxYZbuWBxF3zxC/xher test-ed"
	ed25519FP   = "/jWxgXv75WMhCGdCuGjOluuSlDlO1hmb5tYUR/rDQ1U"

	rsaLine = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCaCK5iDdBRbRp42WMOLIjC/Mcho/2GkwVVPUqEslVLyqNd4TtcmyKal4ouWEEFH1svytaXWUfcbQNclijvhdO9OvSgvsx9fFAQ6XmtIaPR5Xp3u0mx7sK0wXx+1iiLwmxjFKrXSIaqb/iyQhiMEXVnJlOS/CKVawzBfCYSPYzlS8E0S01WWGDVel8+jlWLYcdtC4W2fHcGS71pH6ew1kiQq/FrmmIGjUmCvQQATv4NMR+y38e9IZjeHbsZwtCfNQUNwfFcbPPL3zvQCDW1bG4LF1vQxDMHPnZlAlkfPhwueg2e8hob5ArOMIDq1Ti83WliE8n51Q7f9gcFV7SuelJ9 test-rsa2048"
	rsaFP   = "RJbN9rk06qqB60gT3f9ZTC7hjT707jikQcAXc3e5e/M"

	rsa1024Line = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQDlNdofERg8ADqeMuxkd1Jzi0SjXoq5KgR3GKpr2ycnTZOu4vd3XMR4gWbSRptj7VYS+Y9XrGcLYRnnGoPavhQ7G5GE9tEu6whnw5a9u82ESRzeAr8Fq9hgRn9a/i6nR1QxvUnEIxkJcFkFhB5lScWZ2mEFUVDtKdT4Tb99SRtVcQ== test-rsa1024"

	p256Line = "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBAru94FXOnpKbtWTYt39fx+ZCIskuyBCO5fqLZVVTMCTmQCaS9Qz0wUAmA1kWcjoMJsdW7yHcS19jHiRquxCytA= test-p256"
	p256FP   = "71G2esI8je867ZPzZSCfQPoDtBF+amAO34DSO6wBDW4"

	p384Line = "ecdsa-sha2-nistp384 AAAAE2VjZHNhLXNoYTItbmlzdHAzODQAAAAIbmlzdHAzODQAAABhBJD6s0WHSFkM39wChTlt1qqo8CPenUpUIjKj1eVd6JLkZ09SDBQ72yS9L8+Jcp1N0rGOUsHsrLz6YX9/W0kVeumgXRS8Yb3bmp9lZfICLIxHJr2T6PBgaw1nMPfp+SvaUg== test-p384"
	p384FP   = "87MUa7h0yUl8P6YNLhnApz2FxKZ1u5WU3BUb+7tV5HY"

	rsaPKCS8 = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAmgiuYg3QUW0aeNljDiyI
wvzHIaP9hpMFVT1KhLJVS8qjXeE7XJsimpeKLlhBBR9bL8rWl1lH3G0DXJYo74XT
vTr0oL7MfXxQEOl5rSGj0eV6d7tJse7CtMF8ftYoi8JsYxSq10iGqm/4skIYjBF1
ZyZTkvwilWsMwXwmEj2M5UvBNEtNVlhg1XpfPo5Vi2HHbQuFtnx3Bku9aR+nsNZI
kKvxa5piBo1Jgr0EAE7+DTEfst/HvSGY3h27GcLQnzUFDcHxXGzzy9870Ag1tWxu
Cxdb0MQzBz52ZQJZHz4cLnoNnvIaG+QKzjCA6tU4vN1pYhPJ+dUO3/YHBVe0rnpS
fQIDAQAB
-----END PUBLIC KEY-----
`
	rsaPKCS1 = `-----BEGIN RSA PUBLIC KEY-----
MIIBCgKCAQEAmgiuYg3QUW0aeNljDiyIwvzHIaP9hpMFVT1KhLJVS8qjXeE7XJsi
mpeKLlhBBR9bL8rWl1lH3G0DXJYo74XTvTr0oL7MfXxQEOl5rSGj0eV6d7tJse7C
tMF8ftYoi8JsYxSq10iGqm/4skIYjBF1ZyZTkvwilWsMwXwmEj2M5UvBNEtNVlhg
1XpfPo5Vi2HHbQuFtnx3Bku9aR+nsNZIkKvxa5piBo1Jgr0EAE7+DTEfst/HvSGY
3h27GcLQnzUFDcHxXGzzy9870Ag1tWxuCxdb0MQzBz52ZQJZHz4cLnoNnvIaG+QK
zjCA6tU4vN1pYhPJ+dUO3/YHBVe0rnpSfQIDAQAB
-----END RSA PUBLIC KEY-----
`
	p256PKCS8 = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAECu73gVc6ekpu1ZNi3f1/H5kIiyS7
IEI7l+otlVVMwJOZAJpL1DPTBQCYDWRZyOgwmx1bvIdxLX2MeJGq7ELK0A==
-----END PUBLIC KEY-----
`
)

func TestParseOpenSSH(t *testing.T) {
	tests := []struct {
		name string
		in   string
		typ  string
		fp   string
	}{
		{"ed25519", ed25519Line, "ssh-ed25519", ed25519FP},
		{"RSA 2048", rsaLine, "ssh-rsa", rsaFP},
		{"ECDSA P-256", p256Line, "ecdsa-sha2-nistp256", p256FP},
		{"ECDSA P-384", p384Line, "ecdsa-sha2-nistp384", p384FP},
		{"with the newline the node adds", ed25519Line + "\n", "ssh-ed25519", ed25519FP},
		{"with surrounding white space", "  \t" + ed25519Line + " \r\n", "ssh-ed25519", ed25519FP},
		{"without a comment", strings.TrimSuffix(ed25519Line, " test-ed"), "ssh-ed25519", ed25519FP},
		{"a comment with spaces", ed25519Line + " on my laptop", "ssh-ed25519", ed25519FP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := Parse(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if k.Type != tt.typ || k.Fingerprint != tt.fp {
				t.Errorf("Parse = %s %s, want %s %s", k.Type, k.Fingerprint, tt.typ, tt.fp)
			}
		})
	}
}

func TestParsePEM(t *testing.T) {
	tests := []struct {
		name string
		in   string
		typ  string
		fp   string
	}{
		{"RSA as PKIX", rsaPKCS8, "ssh-rsa", rsaFP},
		{"RSA as PKCS#1", rsaPKCS1, "ssh-rsa", rsaFP},
		{"ECDSA as PKIX", p256PKCS8, "ecdsa-sha2-nistp256", p256FP},
		{"with surrounding white space", "\n\n" + rsaPKCS8 + "\n\n", "ssh-rsa", rsaFP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := Parse(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if k.Type != tt.typ || k.Fingerprint != tt.fp {
				t.Errorf("Parse = %s %s, want %s %s", k.Type, k.Fingerprint, tt.typ, tt.fp)
			}
		})
	}
}

func TestParseEd25519PEM(t *testing.T) {
	// ssh-keygen cannot write this form, so it is made here from the same key.
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(ed25519Line))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub.(ssh.CryptoPublicKey).CryptoPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	k, err := Parse(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	if err != nil || k.Fingerprint != ed25519FP || k.Type != "ssh-ed25519" {
		t.Errorf("Parse = %+v, %v", k, err)
	}
}

func TestPEMOutput(t *testing.T) {
	for in, want := range map[string]string{rsaLine: rsaPKCS8, p256Line: p256PKCS8} {
		k, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := k.PEM()
		if err != nil || got != want {
			t.Errorf("PEM() = %q, %v; want %q", got, err, want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	dsaKey := func() string {
		var params dsa.Parameters
		if err := dsa.GenerateParameters(&params, rand.Reader, dsa.L1024N160); err != nil {
			t.Fatal(err)
		}
		priv := &dsa.PrivateKey{PublicKey: dsa.PublicKey{Parameters: params}}
		if err := dsa.GenerateKey(priv, rand.Reader); err != nil {
			t.Fatal(err)
		}
		pub, err := ssh.NewPublicKey(&priv.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return string(ssh.MarshalAuthorizedKey(pub))
	}()
	edBlob := strings.Fields(ed25519Line)[1]

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrInvalid},
		{"white space only", " \n\t", ErrInvalid},
		{"text", "hello world", ErrInvalid},
		{"too long", ed25519Line + strings.Repeat("x", MaxLength), ErrInvalid},
		{"a NUL", ed25519Line + "\x00", ErrInvalid},
		{"options in front of the key", `command="rm -rf /" ` + ed25519Line, ErrInvalid},
		{"from= in front of the key", `from="10.0.0.1" ` + rsaLine, ErrInvalid},
		{"two keys", ed25519Line + "\n" + p256Line, ErrInvalid},
		{"a newline inside", "ssh-ed25519 " + edBlob[:20] + "\n" + edBlob[20:], ErrInvalid},
		{"a truncated key", ed25519Line[:60], ErrInvalid},
		{"broken base64", "ssh-ed25519 !!!!", ErrInvalid},
		{"the name does not match the key", "ssh-rsa " + edBlob + " x", ErrInvalid},
		{"a DSA key in OpenSSH form", dsaKey, ErrInvalid},
		{"a certificate type", "ssh-ed25519-cert-v01@openssh.com " + edBlob, ErrInvalid},
		{"a hardware key type", "sk-ssh-ed25519@openssh.com " + edBlob, ErrInvalid},
		{"a private key", "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----", ErrInvalid},
		{"a PEM block of another type", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----", ErrInvalid},
		{"a PEM block with nothing in it", "-----BEGIN PUBLIC KEY-----\n-----END PUBLIC KEY-----", ErrInvalid},
		{"a PEM block that is not a key", "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----", ErrInvalid},
		{"a PEM block with headers", strings.Replace(rsaPKCS8, "\n", "\nProc-Type: 4,ENCRYPTED\n\n", 1), ErrInvalid},
		{"something after the PEM block", rsaPKCS8 + "\nsomething else", ErrInvalid},
		{"a PKCS#1 block holding a PKIX key", strings.ReplaceAll(strings.ReplaceAll(rsaPKCS8, "PUBLIC KEY", "RSA PUBLIC KEY"), "BEGIN RSA RSA", "BEGIN RSA"), ErrInvalid},
		{"RSA of 1024 bits", rsa1024Line, ErrShortRSA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.in); !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParseRefusesDSAInPEM(t *testing.T) {
	var params dsa.Parameters
	if err := dsa.GenerateParameters(&params, rand.Reader, dsa.L1024N160); err != nil {
		t.Fatal(err)
	}
	priv := &dsa.PrivateKey{PublicKey: dsa.PublicKey{Parameters: params}}
	if err := dsa.GenerateKey(priv, rand.Reader); err != nil {
		t.Fatal(err)
	}

	// Go does not write DSA public keys, so the SubjectPublicKeyInfo is built by hand.
	type dsaParameters struct{ P, Q, G *big.Int }
	type algorithm struct {
		OID        asn1.ObjectIdentifier
		Parameters dsaParameters
	}
	type subjectPublicKeyInfo struct {
		Algorithm algorithm
		PublicKey asn1.BitString
	}
	y, err := asn1.Marshal(priv.Y)
	if err != nil {
		t.Fatal(err)
	}
	der, err := asn1.Marshal(subjectPublicKeyInfo{
		Algorithm: algorithm{OID: asn1.ObjectIdentifier{1, 2, 840, 10040, 4, 1}, Parameters: dsaParameters{params.P, params.Q, params.G}},
		PublicKey: asn1.BitString{Bytes: y, BitLength: len(y) * 8},
	})
	if err != nil {
		t.Fatal(err)
	}

	block := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if _, err := Parse(block); !errors.Is(err, ErrDSA) {
		t.Errorf("error = %v, want ErrDSA", err)
	}
}

func TestFingerprintHasNoPadding(t *testing.T) {
	for _, line := range []string{ed25519Line, rsaLine, p256Line, p384Line} {
		k, err := Parse(line)
		if err != nil {
			t.Fatal(err)
		}
		if len(k.Fingerprint) != 43 || strings.ContainsAny(k.Fingerprint, "=:") {
			t.Errorf("fingerprint %q: want 43 characters without padding or prefix", k.Fingerprint)
		}
	}
}
