package auth

import "testing"

func TestNewPasswordPolicySupportsUnicodeAndRejectsMissingClasses(t *testing.T) {
	if err := ValidateNewPassword("Åbcdéfghijk1!"); err != nil {
		t.Fatalf("expected valid Unicode password: %v", err)
	}
	for _, password := range []string{"Abcdefghijk12", "abcdefghijk1!", "ABCDEFGHIJKL!", "Abcdefghijk!"} {
		if err := ValidateNewPassword(password); err != ErrPasswordPolicy {
			t.Fatalf("%q unexpectedly passed: %v", password, err)
		}
	}
}

func TestArgon2IDHashesVerifySeedCompatibility(t *testing.T) {
	seedHash := "$argon2id$v=19$m=19456,t=2,p=1$a5cSmLw26Ij1Grx1duyRbg$4TkbMQuIpUQA9uLWupb1Hz87cmt/Na1GIAD9hf97qWk"
	valid, err := VerifyPassword(seedHash, "Test-Issuer-Operator!2026")
	if err != nil || !valid {
		t.Fatalf("seed hash did not verify: valid=%v err=%v", valid, err)
	}
	valid, err = VerifyPassword(seedHash, "wrong-password")
	if err != nil || valid {
		t.Fatalf("incorrect password verified: valid=%v err=%v", valid, err)
	}
	hash, err := HashPassword("Valid Password!2026")
	if err != nil {
		t.Fatal(err)
	}
	valid, err = VerifyPassword(hash, "Valid Password!2026")
	if err != nil || !valid {
		t.Fatalf("generated hash did not verify: valid=%v err=%v", valid, err)
	}
}
