// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {AgentAccount} from "../../src/account/AgentAccount.sol";
import {PolicyEngine} from "../../src/policy/PolicyEngine.sol";
import {MockERC20} from "../mocks/MockERC20.sol";

/// Drives two agents (siblings under one org) with random payments,
/// approvals and batches. Some succeed, most hit a limit.
contract Handler is Test {
    AgentAccount public account;
    MockERC20 public usdc;
    uint256[2] internal pks;
    uint256[2] public nonces;
    uint256 public outflow; // value that left or was approved, as observed

    constructor(AgentAccount account_, MockERC20 usdc_, uint256 pkA, uint256 pkB) {
        account = account_;
        usdc = usdc_;
        pks = [pkA, pkB];
    }

    function _sig(uint256 who, bytes32 digest) internal view returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(pks[who], digest);
        return abi.encodePacked(r, s, v);
    }

    function pay(uint256 who, uint256 amount, address to) external {
        who = bound(who, 0, 1);
        amount = bound(amount, 1, 300e6);
        if (to == address(0) || to == address(account)) to = address(0xBEEF);
        AgentAccount.PaymentIntent memory i = AgentAccount.PaymentIntent({
            token: address(usdc), to: to, amount: amount, ref: 0, nonce: nonces[who], deadline: type(uint64).max
        });
        try account.executePayment(i, _sig(who, account.intentDigest(i)), "") {
            ++nonces[who];
            outflow += amount;
        } catch {}
    }

    function approve(uint256 who, uint256 amount) external {
        who = bound(who, 0, 1);
        amount = bound(amount, 1, 300e6);
        AgentAccount.CallIntent memory c = AgentAccount.CallIntent({
            target: address(usdc),
            data: abi.encodeCall(MockERC20.approve, (address(0xCAFE), amount)),
            nonce: nonces[who],
            deadline: type(uint64).max
        });
        try account.executeCall(c, _sig(who, account.callDigest(c)), "") {
            ++nonces[who];
            outflow += amount;
        } catch {}
    }

    function batch(uint256 who, uint256 a, uint256 b) external {
        who = bound(who, 0, 1);
        AgentAccount.Payout[] memory ps = new AgentAccount.Payout[](2);
        ps[0] = AgentAccount.Payout(address(usdc), address(0x1), bound(a, 1, 150e6), 0);
        ps[1] = AgentAccount.Payout(address(usdc), address(0x2), bound(b, 1, 150e6), 0);
        AgentAccount.BatchIntent memory bi = AgentAccount.BatchIntent(ps, nonces[who], type(uint64).max);
        try account.executeBatch(bi, _sig(who, account.batchDigest(bi)), "") {
            ++nonces[who];
            outflow += ps[0].amount + ps[1].amount;
        } catch {}
    }
}

contract BudgetInvariantTest is Test {
    PolicyEngine engine;
    AgentAccount account;
    MockERC20 usdc;
    Handler handler;

    address ownerAddr = address(0x0A);
    bytes32 constant ORG = keccak256("org");
    bytes32 constant A = keccak256("org/a");
    bytes32 constant B = keccak256("org/b");
    uint256 constant ORG_BUDGET = 500e6;
    uint256 constant START = 100_000e6;

    function setUp() public {
        vm.warp(1_800_000_000);
        engine = new PolicyEngine();
        account = new AgentAccount(engine, ownerAddr);
        usdc = new MockERC20("USD Coin", "USDC", 6);
        usdc.mint(address(account), START);

        // Siblings each get 400/day, but the org above them only 500/day.
        _node(ORG, bytes32(0), ORG_BUDGET, type(uint128).max);
        _node(A, ORG, 400e6, 200e6);
        _node(B, ORG, 400e6, 200e6);
        bytes32[3] memory ns = [ORG, A, B];
        for (uint256 k = 0; k < 3; ++k) {
            _admin(abi.encodeCall(PolicyEngine.setToken, (ns[k], address(usdc), true)));
            _admin(abi.encodeCall(PolicyEngine.setCallPermission, (ns[k], address(usdc), MockERC20.approve.selector, true)));
        }

        uint256 pkA = 0xA1;
        uint256 pkB = 0xB1;
        vm.startPrank(ownerAddr);
        account.setSessionKey(vm.addr(pkA), A, type(uint64).max);
        account.setSessionKey(vm.addr(pkB), B, type(uint64).max);
        vm.stopPrank();

        handler = new Handler(account, usdc, pkA, pkB);
        targetContract(address(handler));
    }

    function _admin(bytes memory data) internal {
        vm.prank(ownerAddr);
        account.ownerCall(address(engine), 0, data);
    }

    function _node(bytes32 id, bytes32 parent, uint256 periodLimit, uint256 perTx) internal {
        PolicyEngine.Limits memory l = PolicyEngine.Limits({
            perTxLimit: uint128(perTx),
            periodLimit: uint128(periodLimit),
            coSignAbove: type(uint128).max,
            periodLength: 1 days,
            validUntil: 0,
            recipientAllowlist: false
        });
        _admin(abi.encodeCall(PolicyEngine.createNode, (id, parent, l)));
    }

    /// Whatever the agents try, the org never exposes more than its budget.
    function invariant_OrgBudgetBindsAllAgents() public view {
        assertLe(handler.outflow(), ORG_BUDGET);
        assertEq(handler.outflow(), engine.spentInCurrentPeriod(ORG));
        assertEq(
            engine.spentInCurrentPeriod(A) + engine.spentInCurrentPeriod(B), engine.spentInCurrentPeriod(ORG)
        );
    }

    /// Tokens only leave through charged payments.
    function invariant_BalanceMatchesCharges() public view {
        uint256 sent = START - usdc.balanceOf(address(account));
        assertLe(sent, engine.spentInCurrentPeriod(ORG));
    }
}
