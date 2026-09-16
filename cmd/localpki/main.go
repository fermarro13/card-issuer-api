// localpki provisions an ignored, development-only PKI for local mTLS tests.
package main

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const bankID = "10000000-0000-4000-8000-000000000001"

var serverNames = []string{"localhost", "authorization"}

var fileModes = map[string]os.FileMode{
	"server-ca.pem":                0o444,
	"client-ca.pem":                0o444,
	"client-ca-key.pem":            0o400,
	"authorization-server.pem":     0o444,
	"authorization-server-key.pem": 0o400,
	"bank-client.pem":              0o444,
	"bank-client-key.pem":          0o400,
	"client-certificates.json":     0o444,
}

func main() {
	directory := os.Getenv("LOCAL_PKI_DIR")
	if directory == "" {
		directory = "/pki"
	}
	if err := provision(directory); err != nil {
		os.Exit(1)
	}
}

func provision(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("cannot create local PKI directory")
	}
	present, err := expectedFiles(directory)
	if err != nil {
		return err
	}
	if present == len(fileModes) {
		if err := validate(directory); err == nil {
			return nil
		}
		if err := validateWithServerNames(directory, []string{"localhost"}); err != nil {
			return errors.New("local PKI directory is invalid")
		}
		if err := removeKnownFiles(directory); err != nil {
			return errors.New("cannot renew local PKI directory")
		}
		return generate(directory)
	}
	if present != 0 {
		return errors.New("local PKI directory is partial")
	}
	return generate(directory)
}

func expectedFiles(directory string) (int, error) {
	present := 0
	for name := range fileModes {
		info, err := os.Stat(filepath.Join(directory, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return 0, errors.New("local PKI directory is invalid")
		}
		present++
	}
	return present, nil
}

func generate(directory string) error {
	return generateWithServerNames(directory, serverNames)
}

func generateWithServerNames(directory string, names []string) error {
	now := time.Now().UTC()
	serverCA, serverCAKey, err := certificateAuthority("local authorization server CA", now)
	if err != nil {
		return errors.New("cannot generate local server CA")
	}
	clientCA, clientCAKey, err := certificateAuthority("local authorization client CA", now)
	if err != nil {
		return errors.New("cannot generate local client CA")
	}
	server, serverKey, err := leafCertificate(serverCA, serverCAKey, "authorization", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now, names)
	if err != nil {
		return errors.New("cannot generate authorization server certificate")
	}
	client, clientKey, err := leafCertificate(clientCA, clientCAKey, "local test bank", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now, nil)
	if err != nil {
		return errors.New("cannot generate bank client certificate")
	}
	fingerprint := sha256.Sum256(client.Raw)
	mapping, err := json.Marshal(map[string]string{hex.EncodeToString(fingerprint[:]): bankID})
	if err != nil {
		return errors.New("cannot generate certificate mapping")
	}
	files := map[string][]byte{
		"server-ca.pem":                certificatePEM(serverCA),
		"client-ca.pem":                certificatePEM(clientCA),
		"client-ca-key.pem":            privateKeyPEM(clientCAKey),
		"authorization-server.pem":     certificatePEM(server),
		"authorization-server-key.pem": privateKeyPEM(serverKey),
		"bank-client.pem":              certificatePEM(client),
		"bank-client-key.pem":          privateKeyPEM(clientKey),
		"client-certificates.json":     append(mapping, '\n'),
	}
	for name, data := range files {
		if err := writeAtomically(filepath.Join(directory, name), data, fileModes[name]); err != nil {
			return errors.New("cannot write local PKI files")
		}
	}
	return validateWithServerNames(directory, names)
}

func certificateAuthority(commonName string, now time.Time) (*x509.Certificate, ed25519.PrivateKey, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	return certificate, privateKey, err
}

func leafCertificate(issuer *x509.Certificate, issuerKey crypto.PrivateKey, commonName string, usages []x509.ExtKeyUsage, now time.Time, names []string) (*x509.Certificate, ed25519.PrivateKey, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.AddDate(0, 30, 0),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           usages,
		DNSNames:              names,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer, publicKey, issuerKey)
	if err != nil {
		return nil, nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	return certificate, privateKey, err
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func certificatePEM(certificate *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
}

func privateKeyPEM(key ed25519.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func writeAtomically(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func validate(directory string) error {
	return validateWithServerNames(directory, serverNames)
}

func validateWithServerNames(directory string, names []string) error {
	serverCA, err := loadCertificate(filepath.Join(directory, "server-ca.pem"))
	if err != nil {
		return errors.New("local PKI is invalid")
	}
	clientCA, err := loadCertificate(filepath.Join(directory, "client-ca.pem"))
	if err != nil {
		return errors.New("local PKI is invalid")
	}
	server, err := loadCertificate(filepath.Join(directory, "authorization-server.pem"))
	if err != nil {
		return errors.New("local PKI is invalid")
	}
	for _, name := range names {
		if verify(server, serverCA, x509.ExtKeyUsageServerAuth, name) != nil {
			return errors.New("local PKI is invalid")
		}
	}
	if len(names) == 0 {
		return errors.New("local PKI is invalid")
	}
	client, err := loadCertificate(filepath.Join(directory, "bank-client.pem"))
	if err != nil || verify(client, clientCA, x509.ExtKeyUsageClientAuth, "") != nil {
		return errors.New("local PKI is invalid")
	}
	if !matchingKey(server, filepath.Join(directory, "authorization-server-key.pem")) || !matchingKey(client, filepath.Join(directory, "bank-client-key.pem")) || !matchingKey(clientCA, filepath.Join(directory, "client-ca-key.pem")) {
		return errors.New("local PKI is invalid")
	}
	data, err := os.ReadFile(filepath.Join(directory, "client-certificates.json"))
	if err != nil {
		return errors.New("local PKI is invalid")
	}
	mapping := map[string]string{}
	fingerprint := sha256.Sum256(client.Raw)
	if json.Unmarshal(data, &mapping) != nil || len(mapping) != 1 || mapping[hex.EncodeToString(fingerprint[:])] != bankID {
		return errors.New("local PKI is invalid")
	}
	return nil
}

func removeKnownFiles(directory string) error {
	for name := range fileModes {
		path := filepath.Join(directory, name)
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func loadCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func verify(certificate, authority *x509.Certificate, usage x509.ExtKeyUsage, dnsName string) error {
	roots := x509.NewCertPool()
	roots.AddCert(authority)
	_, err := certificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: dnsName, KeyUsages: []x509.ExtKeyUsage{usage}})
	return err
}

func matchingKey(certificate *x509.Certificate, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return false
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return false
	}
	publicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	return ok && privateKey.Public().(ed25519.PublicKey).Equal(publicKey)
}
