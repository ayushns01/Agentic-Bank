// Package chain is the bank's view of the EVM: contract ABIs, typed event
// decoding, read-only policy checks and the transaction relayer.
package chain

import (
	"embed"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// ABIs are generated from the Foundry build by scripts/gen-abi.sh.
//
//go:embed abi/*.json
var abiFS embed.FS

var (
	AgentAccountABI   = mustABI("AgentAccount")
	PolicyEngineABI   = mustABI("PolicyEngine")
	AccountFactoryABI = mustABI("AccountFactory")
	EscrowABI         = mustABI("Escrow")
	TestTokenABI      = mustABI("TestToken")
	FaucetABI         = mustABI("Faucet")
)

func mustABI(name string) abi.ABI {
	raw, err := abiFS.ReadFile("abi/" + name + ".json")
	if err != nil {
		panic(fmt.Sprintf("chain: missing ABI %s: %v", name, err))
	}
	a, err := abi.JSON(strings.NewReader(string(raw)))
	if err != nil {
		panic(fmt.Sprintf("chain: bad ABI %s: %v", name, err))
	}
	return a
}
