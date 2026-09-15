package authorization

import (
	"context"

	"card-issuer-api/internal/vault"
)

// VaultVerifier adapts the PCI boundary without allowing raw credentials into
// the authorization persistence API.
type VaultVerifier struct {
	vault vault.CredentialVault
}

func NewVaultVerifier(credentialVault vault.CredentialVault) VaultVerifier {
	return VaultVerifier{vault: credentialVault}
}

func (v VaultVerifier) Verify(ctx context.Context, request CredentialVerification) (CredentialVerificationResult, error) {
	result, err := v.vault.Verify(ctx, vault.VerificationRequest{PAN: request.PAN, CVV: request.CVV})
	return CredentialVerificationResult{CardID: result.CardID, Valid: result.Valid}, err
}
