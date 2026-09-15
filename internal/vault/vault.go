// Package vault defines the PCI-scoped credential boundary used by issuer workflows.
package vault

import "context"

// CredentialVault never returns a stored credential. Provision disclosures are
// one-time values intended only for the initial caller response.
type CredentialVault interface {
	Provision(context.Context, ProvisionRequest) (ProvisionedCredential, error)
	Verify(context.Context, VerificationRequest) (VerificationResult, error)
	Revoke(context.Context, string) error
}

type ProvisionRequest struct {
	CardID string
}

type ProvisionedCredential struct {
	MaskedPAN string
	PAN       string
	CVV       string
}

type VerificationRequest struct {
	PAN string
	CVV string
}

type VerificationResult struct {
	CardID string
	Valid  bool
}
