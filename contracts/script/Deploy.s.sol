// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console2} from "forge-std/Script.sol";
import {PolicyEngine} from "../src/policy/PolicyEngine.sol";
import {AccountFactory} from "../src/account/AccountFactory.sol";
import {Escrow} from "../src/products/Escrow.sol";
import {TestToken} from "../src/testnet/TestToken.sol";
import {Faucet} from "../src/testnet/Faucet.sol";

/// Deploys the bank core and a faucet-backed test stablecoin.
///
///   forge script script/Deploy.s.sol --rpc-url $RPC_URL --private-key $PK --broadcast
///
/// Prints the BANK_* environment for bankd.
contract Deploy is Script {
    uint128 constant DRIP = 1_000e6; // 1,000 tUSDC per claim
    uint128 constant CAP = 10_000e6; // per recipient
    uint64 constant COOLDOWN = 1 days;

    function run() external {
        vm.startBroadcast();
        address admin = msg.sender;

        PolicyEngine engine = new PolicyEngine();
        AccountFactory factory = new AccountFactory(engine);
        Escrow escrow = new Escrow();
        TestToken usdc = new TestToken("Test USD Coin", "tUSDC", 6, admin);
        Faucet faucet = new Faucet(admin);

        usdc.setMinter(address(faucet), true);
        faucet.setDrip(address(usdc), DRIP, CAP, COOLDOWN, true);
        vm.stopBroadcast();

        console2.log("BANK_ENGINE=%s", address(engine));
        console2.log("BANK_FACTORY=%s", address(factory));
        console2.log("BANK_ESCROW=%s", address(escrow));
        console2.log("BANK_TOKENS=%s", address(usdc));
        console2.log("FAUCET=%s", address(faucet));
    }
}
