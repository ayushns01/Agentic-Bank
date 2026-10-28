// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {AgentAccount} from "../src/account/AgentAccount.sol";
import {PolicyEngine} from "../src/policy/PolicyEngine.sol";
import {MockERC20, NoReturnERC20} from "./mocks/MockERC20.sol";

contract AgentAccountTest is Test {
    PolicyEngine engine;
    AgentAccount account;
    MockERC20 usdc;
    MockERC20 other;

    uint256 ownerPk = 0xA11CE;
    uint256 agentPk = 0xB0B;
    uint256 subAgentPk = 0xC0FFEE;
    address ownerAddr;
    address agentKey;
    address subAgentKey;

    address merchant = address(0xBEEF);
    address attacker = address(0xBAD);

    bytes32 constant ORG = keccak256("acme-ops");
    bytes32 constant AGENT = keccak256("research-agent");
    bytes32 constant SUB = keccak256("research-agent/scraper");

    uint256 constant USD = 1e6;

    function setUp() public {
        vm.warp(1_800_000_000);
        ownerAddr = vm.addr(ownerPk);
        agentKey = vm.addr(agentPk);
        subAgentKey = vm.addr(subAgentPk);

        engine = new PolicyEngine();
        account = new AgentAccount(engine, ownerAddr);
        usdc = new MockERC20("USD Coin", "USDC", 6);
        other = new MockERC20("Other", "OTH", 6);
        usdc.mint(address(account), 10_000 * USD);
        other.mint(address(account), 10_000 * USD);

        // org: 1000/day; agent: 200/day, 50 per tx, co-sign above 40; sub: 60/day
        _createNode(ORG, bytes32(0), _limits(1_000 * USD, 1_000 * USD, type(uint128).max, 1 days));
        _createNode(AGENT, ORG, _limits(200 * USD, 50 * USD, 40 * USD, 1 days));
        _createNode(SUB, AGENT, _limits(60 * USD, 25 * USD, type(uint128).max, 1 days));
        for (uint256 i = 0; i < 3; ++i) {
            bytes32 n = [ORG, AGENT, SUB][i];
            _owner(abi.encodeCall(PolicyEngine.setToken, (n, address(usdc), true)));
        }

        vm.startPrank(ownerAddr);
        account.setSessionKey(agentKey, AGENT, uint64(block.timestamp + 30 days));
        account.setSessionKey(subAgentKey, SUB, uint64(block.timestamp + 30 days));
        vm.stopPrank();
    }

    // ------------------------------------------------------------ helpers

    function _limits(uint256 periodLimit, uint256 perTx, uint256 coSignAbove, uint64 periodLength)
        internal
        pure
        returns (PolicyEngine.Limits memory)
    {
        return PolicyEngine.Limits({
            perTxLimit: uint128(perTx),
            periodLimit: uint128(periodLimit),
            coSignAbove: uint128(coSignAbove),
            periodLength: periodLength,
            validUntil: 0,
            recipientAllowlist: false
        });
    }

    function _owner(bytes memory data) internal {
        vm.prank(ownerAddr);
        account.ownerCall(address(engine), 0, data);
    }

    function _createNode(bytes32 id, bytes32 parent, PolicyEngine.Limits memory l) internal {
        _owner(abi.encodeCall(PolicyEngine.createNode, (id, parent, l)));
    }

    function _intent(address token, address to, uint256 amount, uint256 nonce)
        internal
        view
        returns (AgentAccount.PaymentIntent memory)
    {
        return AgentAccount.PaymentIntent({
            token: token,
            to: to,
            amount: amount,
            ref: keccak256(abi.encode("invoice", nonce)),
            nonce: nonce,
            deadline: uint64(block.timestamp + 1 hours)
        });
    }

    function _sign(uint256 pk, AgentAccount.PaymentIntent memory i) internal view returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(pk, account.intentDigest(i));
        return abi.encodePacked(r, s, v);
    }

    function _pay(uint256 pk, AgentAccount.PaymentIntent memory i) internal {
        account.executePayment(i, _sign(pk, i), "");
    }

    // -------------------------------------------------------------- tests

    function test_AgentPaysWithinPolicy() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 30 * USD, 0);
        vm.prank(attacker); // relayer identity is irrelevant
        account.executePayment(i, _sign(agentPk, i), "");

        assertEq(usdc.balanceOf(merchant), 30 * USD);
        assertEq(account.nonces(agentKey), 1);
        assertEq(engine.spentInCurrentPeriod(AGENT), 30 * USD);
        assertEq(engine.spentInCurrentPeriod(ORG), 30 * USD, "charged up the tree");
    }

    function test_PerTxLimit() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 51 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        bytes memory ownerSig = _sign(ownerPk, i);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.PerTxLimit.selector, AGENT, 51 * USD, 50 * USD));
        account.executePayment(i, sig, ownerSig);
    }

    function test_CoSignRequiredAboveThreshold() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 45 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.CoSignRequired.selector, AGENT, 45 * USD, 40 * USD));
        account.executePayment(i, sig, "");

        account.executePayment(i, sig, _sign(ownerPk, i));
        assertEq(usdc.balanceOf(merchant), 45 * USD);
    }

    function test_ForgedCoSignatureRejected() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 45 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        bytes memory fake = _sign(agentPk, i); // agent tries to co-sign for itself
        vm.expectRevert(AgentAccount.InvalidCoSignature.selector);
        account.executePayment(i, sig, fake);
    }

    function test_PeriodLimitAndReset() public {
        for (uint256 n = 0; n < 5; ++n) {
            _pay(agentPk, _intent(address(usdc), merchant, 40 * USD, n));
        }
        assertEq(engine.spentInCurrentPeriod(AGENT), 200 * USD);

        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 1 * USD, 5);
        bytes memory sig = _sign(agentPk, i);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.PeriodLimit.selector, AGENT, 201 * USD, 200 * USD));
        account.executePayment(i, sig, "");

        vm.warp(block.timestamp + 1 days);
        account.executePayment(i, sig, "");
        assertEq(engine.spentInCurrentPeriod(AGENT), 1 * USD);
    }

    function test_ParentBudgetBindsChild() public {
        // Sub-agent has 60/day of its own, but the agent above it has spent 190.
        for (uint256 n = 0; n < 4; ++n) {
            _pay(agentPk, _intent(address(usdc), merchant, 40 * USD, n));
        }
        _pay(agentPk, _intent(address(usdc), merchant, 30 * USD, 4));
        assertEq(engine.spentInCurrentPeriod(AGENT), 190 * USD);

        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 20 * USD, 0);
        bytes memory sig = _sign(subAgentPk, i);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.PeriodLimit.selector, AGENT, 210 * USD, 200 * USD));
        account.executePayment(i, sig, "");
    }

    function test_FreezeParentFreezesSubtree() public {
        _owner(abi.encodeCall(PolicyEngine.setFrozen, (ORG, true)));
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 1 * USD, 0);
        bytes memory sig = _sign(subAgentPk, i);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.NodeFrozen.selector, ORG));
        account.executePayment(i, sig, "");
    }

    function test_TokenNotAllowed() public {
        AgentAccount.PaymentIntent memory i = _intent(address(other), merchant, 1 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.TokenNotAllowed.selector, AGENT, address(other)));
        account.executePayment(i, sig, "");
    }

    function test_RecipientAllowlist() public {
        PolicyEngine.Limits memory l = _limits(200 * USD, 50 * USD, 40 * USD, 1 days);
        l.recipientAllowlist = true;
        _owner(abi.encodeCall(PolicyEngine.setLimits, (AGENT, l)));
        _owner(abi.encodeCall(PolicyEngine.setRecipient, (AGENT, merchant, true)));

        _pay(agentPk, _intent(address(usdc), merchant, 10 * USD, 0));

        AgentAccount.PaymentIntent memory i = _intent(address(usdc), attacker, 10 * USD, 1);
        bytes memory sig = _sign(agentPk, i);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.RecipientNotAllowed.selector, AGENT, attacker));
        account.executePayment(i, sig, "");
    }

    /// A prompt-injected agent signs whatever it is told. Policy still holds:
    /// the drain attempt reverts on-chain and the balance is untouched.
    function test_PromptInjectedDrainBlocked() public {
        uint256 before = usdc.balanceOf(address(account));
        AgentAccount.PaymentIntent memory drain = _intent(address(usdc), attacker, before, 0);
        bytes memory sig = _sign(agentPk, drain);
        vm.expectRevert();
        account.executePayment(drain, sig, "");
        assertEq(usdc.balanceOf(address(account)), before);
        assertEq(usdc.balanceOf(attacker), 0);
    }

    function test_ReplayRejected() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 10 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        account.executePayment(i, sig, "");
        vm.expectRevert(abi.encodeWithSelector(AgentAccount.BadNonce.selector, 1, 0));
        account.executePayment(i, sig, "");
    }

    function test_SignatureBoundToAccount() public {
        AgentAccount other_ = new AgentAccount(engine, ownerAddr);
        usdc.mint(address(other_), 1_000 * USD);
        vm.prank(ownerAddr);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.NotNodeAccount.selector, AGENT));
        other_.setSessionKey(agentKey, AGENT, uint64(block.timestamp + 1 days));

        // A signature made for `account` recovers to a random key on other_.
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 10 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        vm.expectRevert();
        other_.executePayment(i, sig, "");
    }

    function test_ExpiredIntent() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 10 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        vm.warp(i.deadline + 1);
        vm.expectRevert(abi.encodeWithSelector(AgentAccount.IntentExpired.selector, i.deadline));
        account.executePayment(i, sig, "");
    }

    function test_RevokedAndExpiredKeys() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 10 * USD, 0);
        bytes memory sig = _sign(agentPk, i);
        vm.prank(ownerAddr);
        account.revokeSessionKey(agentKey);
        vm.expectRevert(abi.encodeWithSelector(AgentAccount.SessionKeyInactive.selector, agentKey));
        account.executePayment(i, sig, "");

        vm.prank(ownerAddr);
        account.setSessionKey(agentKey, AGENT, uint64(block.timestamp + 1));
        vm.warp(block.timestamp + 2);
        i.deadline = uint64(block.timestamp + 1 hours);
        sig = _sign(agentPk, i);
        vm.expectRevert(abi.encodeWithSelector(AgentAccount.SessionKeyExpired.selector, agentKey));
        account.executePayment(i, sig, "");
    }

    function test_MalformedSignature() public {
        AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, 10 * USD, 0);
        vm.expectRevert(AgentAccount.InvalidSessionSignature.selector);
        account.executePayment(i, hex"1234", "");
    }

    function test_OnlyOwnerAdmin() public {
        vm.startPrank(attacker);
        vm.expectRevert(AgentAccount.NotOwner.selector);
        account.setSessionKey(attacker, AGENT, type(uint64).max);
        vm.expectRevert(AgentAccount.NotOwner.selector);
        account.ownerCall(address(usdc), 0, abi.encodeCall(MockERC20.transfer, (attacker, 1)));
        vm.stopPrank();

        // Nodes can only be configured by the account that owns them.
        vm.prank(attacker);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.NotNodeAccount.selector, AGENT));
        engine.setFrozen(AGENT, false);
        vm.prank(attacker);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.NotNodeAccount.selector, ORG));
        engine.createNode(keccak256("evil"), ORG, _limits(1, 1, 1, 0));
    }

    function test_NoReturnTokenSupported() public {
        NoReturnERC20 usdt = new NoReturnERC20();
        usdt.mint(address(account), 100 * USD);
        for (uint256 k = 0; k < 2; ++k) {
            _owner(abi.encodeCall(PolicyEngine.setToken, ([ORG, AGENT][k], address(usdt), true)));
        }
        _pay(agentPk, _intent(address(usdt), merchant, 10 * USD, 0));
        assertEq(usdt.balanceOf(merchant), 10 * USD);
    }

    function test_WouldAuthorizeMatchesAuthorize() public {
        (bool ok,) = engine.wouldAuthorize(AGENT, address(usdc), merchant, 30 * USD, false);
        assertTrue(ok);
        (ok,) = engine.wouldAuthorize(AGENT, address(usdc), merchant, 45 * USD, false);
        assertFalse(ok);
    }

    /// Fuzz: whatever sequence of amounts the agent tries in one window, the
    /// total that actually left the account never exceeds the agent's budget.
    function testFuzz_NeverExceedsPeriodBudget(uint64[12] memory raw) public {
        uint256 nonce;
        for (uint256 k = 0; k < raw.length; ++k) {
            uint256 amount = bound(uint256(raw[k]), 1, 60 * USD);
            AgentAccount.PaymentIntent memory i = _intent(address(usdc), merchant, amount, nonce);
            bytes memory sig = _sign(agentPk, i);
            try account.executePayment(i, sig, _sign(ownerPk, i)) {
                ++nonce;
            } catch {}
        }
        assertLe(usdc.balanceOf(merchant), 200 * USD);
        assertEq(usdc.balanceOf(merchant), engine.spentInCurrentPeriod(AGENT));
    }
}
