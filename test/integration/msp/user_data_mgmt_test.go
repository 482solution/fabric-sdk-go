/*
Copyright SecureKey Technologies Inc. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package msp

import (
	"testing"

	"strings"

	"encoding/hex"

	"github.com/hyperledger/fabric-sdk-go/pkg/client/msp"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/context"
	mspctx "github.com/hyperledger/fabric-sdk-go/pkg/common/providers/msp"
	"github.com/hyperledger/fabric-sdk-go/pkg/core/cryptosuite"
	"github.com/hyperledger/fabric-sdk-go/pkg/core/cryptosuite/bccsp/sw"
	"github.com/hyperledger/fabric-sdk-go/pkg/fab"
	"github.com/hyperledger/fabric-sdk-go/pkg/fabsdk"
	mspimpl "github.com/hyperledger/fabric-sdk-go/pkg/msp"
	"github.com/hyperledger/fabric-sdk-go/test/integration"
)

// TestWithCustomStores demonstrates the usage of custom key and cert stores
// to manage user private keys and certificates.
func TestWithCustomStores(t *testing.T) {
	configBackend, err := integration.ConfigBackend()
	if err != nil {
		t.Fatalf("Unexpected error from config backend: %v", err)
	}

	cryptoConfig := cryptosuite.ConfigFromBackend(configBackend...)

	endpointConfig, err := fab.ConfigFromBackend(configBackend...)
	if err != nil {
		t.Fatalf("Unexpected error from config: %v", err)
	}

	identityConfig, err := mspimpl.ConfigFromBackend(configBackend...)
	if err != nil {
		t.Fatalf("Unexpected error from config: %v", err)
	}

	// User private keys are managed by BCCSP. When BCCSP is configured
	// to use HSM, keys are normally not exportable, and client
	// never gets hold of them. When BCCSP is configured to use
	// software crypto provider (SW), keys are by default stored
	// in pem files, in a directory specified by
	// cclient.credentialStore.cryptoStore.path in SDK configuration
	// file.
	//
	// Here we are replacing default key store with a simple
	// in-memory implementation.

	//
	// NOTE: BCCSP SW implementation currently doesn't allow
	// writing private keys out. The file store used internally
	// by BCCSP has access to provate parts that are not available
	// outside of BCCSP at the moment. Fot this reason, our
	// example custom kay store will just hold the keys in memory.
	//

	customKeyStore := mspimpl.NewMemoryKeyStore([]byte("password"))
	customCryptoSuite, err := sw.GetSuite(cryptoConfig.SecurityLevel(), cryptoConfig.SecurityAlgorithm(), customKeyStore)
	if err != nil {
		t.Fatalf("Unexpected error from GetSuiteByConfig: %v", err)
	}
	customCoreSuite := NewCustomCoreFactory(customCryptoSuite)

	// Defaulf user store implementation is a simple file store that
	// stores user enrollment certificate in a pem file, in
	// a directory specified by client.credentialStore.path in
	// SDK configuration file. File naming convention
	// (username@mspid-cert.pem) preserves username and MSP ID
	// and enables lookup.
	//
	// Here we are replacing default user store with a sinple
	// in-memory implementation.

	customUserStore := mspimpl.NewMemoryUserStore()
	customMSPSuite := NewCustomMSPFactory(customUserStore)

	// Let's see if it works:)

	sdk, err := fabsdk.New(nil, fabsdk.WithCryptoSuiteConfig(cryptoConfig), fabsdk.WithEndpointConfig(endpointConfig), fabsdk.WithIdentityConfig(identityConfig), fabsdk.WithCorePkg(customCoreSuite), fabsdk.WithMSPPkg(customMSPSuite))
	if err != nil {
		t.Fatalf("Error initializing SDK: %s", err)
	}

	ctxProvider := sdk.Context()

	// Get the Client.
	// Without WithOrg option, uses default client organization.
	mspClient, err := msp.New(ctxProvider)
	if err != nil {
		t.Fatalf("failed to create Client: %v", err)
	}

	// As this integration test spawns a fresh CA instance,
	// we have to enroll the CA registrar first. Otherwise,
	// CA operations that require the registrar's identity
	// will be rejected by the CA.
	registrarEnrollID, registrarEnrollSecret := getRegistrarEnrollmentCredentials(t, ctxProvider)
	err = mspClient.Enroll(registrarEnrollID, msp.WithSecret(registrarEnrollSecret))
	if err != nil {
		t.Fatalf("Enroll failed: %v", err)
	}

	// Generate a random user name
	username := integration.GenerateRandomID()

	// Register the new user
	enrollmentSecret, err := mspClient.Register(&msp.RegistrationRequest{
		Name: username,
		Type: IdentityTypeUser,
		// Affiliation is mandatory. "org1" and "org2" are hardcoded as CA defaults
		// See https://github.com/hyperledger/fabric-ca/blob/release/cmd/fabric-ca-server/config.go
		Affiliation: "org2",
	})
	if err != nil {
		t.Fatalf("Registration failed: %v", err)
	}

	// Enroll the new user
	err = mspClient.Enroll(username, msp.WithSecret(enrollmentSecret))
	if err != nil {
		t.Fatalf("Enroll failed: %v", err)
	}

	// Let's try to find user's key and cert in our custom stores
	// and compare them to what is returned by msp.GetUser()
	user := checkUserData(mspClient, username, t, customUserStore, ctxProvider)

	checkPrivateKey(customKeyStore, user, t)

}

func checkPrivateKey(customKeyStore *mspimpl.MemoryKeyStore, user mspctx.SigningIdentity, t *testing.T) {
	privateKey, err := customKeyStore.GetKey(user.PrivateKey().SKI())
	if err != nil {
		t.Fatalf("customKeyStore.GetKey failed: %v", err)
	}
	if privateKey == nil {
		t.Fatalf("key from customKeyStore is nil")
	}
	if hex.EncodeToString(privateKey.SKI()) != hex.EncodeToString(user.PrivateKey().SKI()) {
		t.Fatalf("keys don't match")
	}
}

func checkUserData(mspClient *msp.Client, username string, t *testing.T, customUserStore *mspimpl.MemoryUserStore, ctxProvider context.ClientProvider) mspctx.SigningIdentity {
	user, err := mspClient.GetSigningIdentity(username)
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	userDataFromStore, err := customUserStore.Load(mspctx.IdentityIdentifier{MSPID: getMyMSPID(t, ctxProvider), ID: username})
	if err != nil {
		t.Fatalf("Load user failed: %v", err)
	}
	if userDataFromStore.ID != user.Identifier().ID {
		t.Fatalf("username doesn't match")
	}
	if userDataFromStore.MSPID != user.Identifier().MSPID {
		t.Fatalf("username doesn't match")
	}
	if hex.EncodeToString(user.EnrollmentCertificate()) != hex.EncodeToString(userDataFromStore.EnrollmentCertificate) {
		t.Fatalf("cert doesn't match")
	}
	return user
}

func getMyMSPID(t *testing.T, ctxProvider context.ClientProvider) string {

	ctx, err := ctxProvider()
	if err != nil {
		t.Fatalf("failed to get context: %v", err)
	}

	clientConfig, err := ctx.IdentityConfig().Client()
	if err != nil {
		t.Fatalf("config.Client() failed: %v", err)
	}

	netConfig, ok := ctx.EndpointConfig().NetworkConfig()
	if !ok {
		t.Fatal("Failed to get networkConfig ")
	}
	myOrg, ok := netConfig.Organizations[strings.ToLower(clientConfig.Organization)]
	if !ok {
		t.Fatalf("Organization is not configured: %v", clientConfig.Organization)
	}

	return myOrg.MSPID
}
