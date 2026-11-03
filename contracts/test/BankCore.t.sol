// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {AgentAccount} from "../src/account/AgentAccount.sol";
import {AccountFactory} from "../src/account/AccountFactory.sol";
import {PolicyEngine} from "../src/policy/PolicyEngine.sol";
import {TestToken} from "../src/testnet/TestToken.sol";
import {Faucet} from "../src/testnet/Faucet.sol";
import {IERC20} from "../src/lib/IERC20.sol";
import {MockVault} from "./mocks/MockVault.sol";

/// Account opening, faucet funding, venue calls and batch payouts.
contract BankCoreTest is Test {
    PolicyEngine engine;
    AccountFactory factory;
    TestToken usdc;
    Faucet faucet;
    MockVault vault;
    AgentAccount account;

    uint256 ownerPk = 0xA11CE;
    uint256 agentPk = 0xB0B;
    address ownerAddr;
    address agentKey;
    address admin = address(0xAD);
    address merchant = address(0xBEEF);

    bytes32 constant ORG = keccak256("acme");
    bytes32 constant AGENT = keccak256("acme/treasury-agent");
    uint256 constant USD = 1e6;

    function setUp() public {
        vm.warp(1_800_000_000);
        ownerAddr = vm.addr(ownerPk);
        agentKey = vm.addr(agentPk);

        engine = new PolicyEngine();
        factory = new AccountFactory(engine);
        usdc = new TestToken("Test USD Coin", "tUSDC", 6, admin);
        faucet = new Faucet(admin);
        vault = new MockVault(IERC20(address(usdc)));

        vm.startPrank(admin);
        usdc.setMinter(address(faucet), true);
        faucet.setDrip(address(usdc), uint128(1_000 * USD), uint128(3_000 * USD), 1 days, true);
        vm.stopPrank();

        account = factory.openAccount(ownerAddr, bytes32("main"));
        faucet.claim(address(usdc), address(account));

        _createNode(ORG, bytes32(0), _limits(500 * USD, 300 * USD, type(uint128).max, 1 days));
        _createNode(AGENT, ORG, _limits(400 * USD, 200 * USD, 150 * USD, 1 days));
        _owner(abi.encodeCall(PolicyEngine.setToken, (ORG, address(usdc), true)));
        _owner(abi.encodeCall(PolicyEngine.setToken, (AGENT, address(usdc), true)));
        _allowCall(address(usdc), IERC20.approve.selector);
        _allowCall(address(vault), MockVault.deposit.selector);

        vm.prank(ownerAddr);
        account.setSessionKey(agentKey, AGENT, uint64(block.timestamp + 30 days));
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

    function _allowCall(address target, bytes4 selector) internal {
        _owner(abi.encodeCall(PolicyEngine.setCallPermission, (ORG, target, selector, true)));
        _owner(abi.encodeCall(PolicyEngine.setCallPermission, (AGENT, target, selector, true)));
    }

    function _call(address target, bytes memory data, uint256 nonce)
        internal
        view
        returns (AgentAccount.CallIntent memory c, bytes memory sig)
    {
        c = AgentAccount.CallIntent({target: target, data: data, nonce: nonce, deadline: uint64(block.timestamp + 1 hours)});
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(agentPk, account.callDigest(c));
        sig = abi.encodePacked(r, s, v);
    }

    function _batch(AgentAccount.Payout[] memory ps, uint256 nonce)
        internal
        view
        returns (AgentAccount.BatchIntent memory b, bytes memory sig)
    {
        b = AgentAccount.BatchIntent({payouts: ps, nonce: nonce, deadline: uint64(block.timestamp + 1 hours)});
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(agentPk, account.batchDigest(b));
        sig = abi.encodePacked(r, s, v);
    }

    function _payout(address to, uint256 amount, uint256 tag) internal view returns (AgentAccount.Payout memory) {
        return AgentAccount.Payout({token: address(usdc), to: to, amount: amount, ref: bytes32(tag)});
    }

    // ------------------------------------------------------ account opening

    function test_FactoryPredictsAndOpens() public {
        address predicted = factory.predictAddress(ownerAddr, bytes32("second"));
        AgentAccount a = factory.openAccount(ownerAddr, bytes32("second"));
        assertEq(address(a), predicted);
        assertTrue(factory.isAccount(address(a)));
        assertEq(a.owner(), ownerAddr);
        assertEq(address(a.engine()), address(engine));

        vm.expectRevert(abi.encodeWithSelector(AccountFactory.AlreadyOpened.selector, predicted));
        factory.openAccount(ownerAddr, bytes32("second"));

        // Same salt, different owner: different address, so no squatting.
        assertTrue(factory.predictAddress(merchant, bytes32("second")) != predicted);
    }

    function test_FundedBeforeOpening() public {
        address predicted = factory.predictAddress(merchant, bytes32("x"));
        faucet.claim(address(usdc), predicted);
        AgentAccount a = factory.openAccount(merchant, bytes32("x"));
        assertEq(usdc.balanceOf(address(a)), 1_000 * USD);
    }

    // --------------------------------------------------------------- faucet

    function test_FaucetCooldownAndCap() public {
        vm.expectRevert(abi.encodeWithSelector(Faucet.CoolingDown.selector, uint64(block.timestamp + 1 days)));
        faucet.claim(address(usdc), address(account));

        vm.warp(block.timestamp + 1 days);
        faucet.claim(address(usdc), address(account));
        vm.warp(block.timestamp + 1 days);
        faucet.claim(address(usdc), address(account));
        assertEq(usdc.balanceOf(address(account)), 3_000 * USD);

        vm.warp(block.timestamp + 1 days);
        vm.expectRevert(abi.encodeWithSelector(Faucet.CapReached.selector, 3_000 * USD, 3_000 * USD));
        faucet.claim(address(usdc), address(account));
    }

    function test_OnlyMinterMints() public {
        vm.expectRevert(TestToken.NotMinter.selector);
        usdc.mint(address(this), 1);
        vm.expectRevert(Faucet.NotAdmin.selector);
        faucet.setDrip(address(usdc), 1, 1, 0, true);
    }

    // ---------------------------------------------------------- venue calls

    function test_ApproveAndDepositIntoVenue() public {
        (AgentAccount.CallIntent memory c, bytes memory sig) =
            _call(address(usdc), abi.encodeCall(IERC20.approve, (address(vault), 120 * USD)), 0);
        account.executeCall(c, sig, "");
        // Approving charges the budget up front.
        assertEq(engine.spentInCurrentPeriod(AGENT), 120 * USD);
        assertEq(engine.spentInCurrentPeriod(ORG), 120 * USD);

        (c, sig) = _call(address(vault), abi.encodeCall(MockVault.deposit, (120 * USD)), 1);
        account.executeCall(c, sig, "");
        assertEq(vault.shares(address(account)), 120 * USD);
        assertEq(usdc.balanceOf(address(account)), 880 * USD);
        // Depositing does not double-charge.
        assertEq(engine.spentInCurrentPeriod(AGENT), 120 * USD);
    }

    function test_CallNotAllowed() public {
        (AgentAccount.CallIntent memory c, bytes memory sig) =
            _call(address(vault), abi.encodeCall(MockVault.withdraw, (1)), 0);
        vm.expectRevert(
            abi.encodeWithSelector(PolicyEngine.CallNotAllowed.selector, AGENT, address(vault), MockVault.withdraw.selector)
        );
        account.executeCall(c, sig, "");
    }

    function test_ParentMustAlsoAllowCall() public {
        _owner(abi.encodeCall(PolicyEngine.setCallPermission, (ORG, address(vault), MockVault.deposit.selector, false)));
        (AgentAccount.CallIntent memory c, bytes memory sig) =
            _call(address(vault), abi.encodeCall(MockVault.deposit, (1)), 0);
        vm.expectRevert(
            abi.encodeWithSelector(PolicyEngine.CallNotAllowed.selector, ORG, address(vault), MockVault.deposit.selector)
        );
        account.executeCall(c, sig, "");
    }

    function test_AgentCannotCallEngineOrSelf() public {
        // Even if the owner mistakenly allowlisted it, the agent must never be
        // able to rewrite its own limits through the account.
        bytes memory raise = abi.encodeCall(PolicyEngine.setFrozen, (AGENT, false));
        (AgentAccount.CallIntent memory c, bytes memory sig) = _call(address(engine), raise, 0);
        vm.expectRevert(AgentAccount.SelfCall.selector);
        account.executeCall(c, sig, "");

        (c, sig) = _call(address(account), abi.encodeCall(AgentAccount.revokeSessionKey, (agentKey)), 0);
        vm.expectRevert(AgentAccount.SelfCall.selector);
        account.executeCall(c, sig, "");
    }

    function test_ApproveBeyondLimitsBlocked() public {
        (AgentAccount.CallIntent memory c, bytes memory sig) =
            _call(address(usdc), abi.encodeCall(IERC20.approve, (address(vault), type(uint256).max)), 0);
        vm.expectRevert(
            abi.encodeWithSelector(PolicyEngine.PerTxLimit.selector, AGENT, type(uint256).max, 200 * USD)
        );
        account.executeCall(c, sig, "");
    }

    function test_RevokeApprovalIsFree() public {
        (AgentAccount.CallIntent memory c, bytes memory sig) =
            _call(address(usdc), abi.encodeCall(IERC20.approve, (address(vault), 0)), 0);
        account.executeCall(c, sig, "");
        assertEq(engine.spentInCurrentPeriod(AGENT), 0);
    }

    function test_TokenTransferViaCallIsCharged() public {
        _allowCall(address(usdc), IERC20.transfer.selector);
        (AgentAccount.CallIntent memory c, bytes memory sig) =
            _call(address(usdc), abi.encodeCall(IERC20.transfer, (merchant, 250 * USD)), 0);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.PerTxLimit.selector, AGENT, 250 * USD, 200 * USD));
        account.executeCall(c, sig, "");

        (c, sig) = _call(address(usdc), abi.encodeCall(IERC20.transfer, (merchant, 100 * USD)), 0);
        account.executeCall(c, sig, "");
        assertEq(engine.spentInCurrentPeriod(AGENT), 100 * USD);
        assertEq(usdc.balanceOf(merchant), 100 * USD);
    }

    function test_CallShortCalldata() public {
        (AgentAccount.CallIntent memory c, bytes memory sig) = _call(address(vault), hex"1234", 0);
        vm.expectRevert(AgentAccount.ShortCalldata.selector);
        account.executeCall(c, sig, "");
    }

    function test_FrozenNodeBlocksCalls() public {
        _owner(abi.encodeCall(PolicyEngine.setFrozen, (ORG, true)));
        (AgentAccount.CallIntent memory c, bytes memory sig) =
            _call(address(vault), abi.encodeCall(MockVault.deposit, (1)), 0);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.NodeFrozen.selector, ORG));
        account.executeCall(c, sig, "");
    }

    // ----------------------------------------------------------- batch pay

    function test_BatchPayouts() public {
        AgentAccount.Payout[] memory ps = new AgentAccount.Payout[](3);
        ps[0] = _payout(address(0x1), 50 * USD, 1);
        ps[1] = _payout(address(0x2), 60 * USD, 2);
        ps[2] = _payout(address(0x3), 70 * USD, 3);
        (AgentAccount.BatchIntent memory b, bytes memory sig) = _batch(ps, 0);
        account.executeBatch(b, sig, "");
        assertEq(usdc.balanceOf(address(0x3)), 70 * USD);
        assertEq(engine.spentInCurrentPeriod(AGENT), 180 * USD);
        assertEq(account.nonces(agentKey), 1);
    }

    function test_BatchIsAtomic() public {
        // 3 x 150 = 450 > agent's 400/day: the third payout fails, so none land.
        AgentAccount.Payout[] memory ps = new AgentAccount.Payout[](3);
        for (uint256 k = 0; k < 3; ++k) {
            ps[k] = _payout(address(uint160(k + 1)), 150 * USD, k);
        }
        (AgentAccount.BatchIntent memory b, bytes memory sig) = _batch(ps, 0);
        vm.expectRevert(abi.encodeWithSelector(PolicyEngine.PeriodLimit.selector, AGENT, 450 * USD, 400 * USD));
        account.executeBatch(b, sig, "");
        assertEq(usdc.balanceOf(address(0x1)), 0);
        assertEq(account.nonces(agentKey), 0);
    }

    function test_BatchTamperedPayoutRejected() public {
        AgentAccount.Payout[] memory ps = new AgentAccount.Payout[](1);
        ps[0] = _payout(merchant, 10 * USD, 1);
        (AgentAccount.BatchIntent memory b, bytes memory sig) = _batch(ps, 0);
        b.payouts[0].to = address(0xBAD); // relayer swaps the recipient
        vm.expectRevert(); // signature now recovers to an unknown key
        account.executeBatch(b, sig, "");
    }

    function test_EmptyBatch() public {
        AgentAccount.Payout[] memory ps = new AgentAccount.Payout[](0);
        (AgentAccount.BatchIntent memory b, bytes memory sig) = _batch(ps, 0);
        vm.expectRevert(AgentAccount.EmptyBatch.selector);
        account.executeBatch(b, sig, "");
    }

    /// Fuzz: whatever mix of approvals and direct transfers the agent signs
    /// in one window, value exposed (approved + sent) never exceeds the
    /// agent's daily budget.
    function testFuzz_CallsNeverExceedBudget(uint64[8] memory raw, bool[8] memory useApprove) public {
        _allowCall(address(usdc), IERC20.transfer.selector);
        uint256 nonce;
        uint256 exposed;
        for (uint256 k = 0; k < raw.length; ++k) {
            uint256 amount = bound(uint256(raw[k]), 1, 220 * USD);
            bytes memory data = useApprove[k]
                ? abi.encodeCall(IERC20.approve, (address(vault), amount))
                : abi.encodeCall(IERC20.transfer, (merchant, amount));
            (AgentAccount.CallIntent memory c, bytes memory sig) = _call(address(usdc), data, nonce);
            try account.executeCall(c, sig, "") {
                ++nonce;
                exposed += amount;
            } catch {}
        }
        assertLe(exposed, 400 * USD);
        assertEq(exposed, engine.spentInCurrentPeriod(AGENT));
    }
}
