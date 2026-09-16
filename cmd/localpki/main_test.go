package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProvisionIsIdempotentAndValid(t *testing.T) {
	directory := t.TempDir()
	if err := provision(directory); err != nil {
		t.Fatal(err)
	}
	if err := provision(directory); err != nil {
		t.Fatalf("idempotent provision failed: %v", err)
	}
	for name := range fileModes {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s was not generated: %v", name, err)
		}
	}
}

func TestProvisionRejectsPartialDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "server-ca.pem"), []byte("partial"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := provision(directory); err == nil {
		t.Fatal("partial local PKI directory was accepted")
	}
}

func TestProvisionRenewsCoherentLegacyPKI(t *testing.T) {
	directory := t.TempDir()
	if err := generateWithServerNames(directory, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	if err := provision(directory); err != nil {
		t.Fatalf("legacy PKI renewal failed: %v", err)
	}
	if err := validate(directory); err != nil {
		t.Fatalf("renewed PKI is not valid for Compose DNS: %v", err)
	}
}
