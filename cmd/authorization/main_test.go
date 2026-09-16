package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCertificateBanksAcceptsExactlyOneSource(t *testing.T) {
	directory := t.TempDir()
	mapping := filepath.Join(directory, "mapping.json")
	if err := os.WriteFile(mapping, []byte(`{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":"bank"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTHORIZATION_CLIENT_CERTIFICATES", "")
	t.Setenv("AUTHORIZATION_CLIENT_CERTIFICATES_FILE", mapping)
	banks, err := certificateBanks()
	if err != nil || banks["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"] != "bank" {
		t.Fatalf("file mapping = %#v, %v", banks, err)
	}
	t.Setenv("AUTHORIZATION_CLIENT_CERTIFICATES", `{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb":"other"}`)
	if _, err := certificateBanks(); err == nil {
		t.Fatal("conflicting mapping sources were accepted")
	}
}
