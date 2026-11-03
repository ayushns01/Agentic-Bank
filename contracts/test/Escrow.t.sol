// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {Escrow} from "../src/products/Escrow.sol";
import {MockERC20} from "./mocks/MockERC20.sol";

contract EscrowTest is Test {
    Escrow escrow;
    MockERC20 usdc;

    address payer = address(0xA);
    address payee = address(0xB);
    address arbiter = address(0xC);
    address stranger = address(0xD);

    uint256 constant USD = 1e6;

    function setUp() public {
        vm.warp(1_800_000_000);
        escrow = new Escrow();
        usdc = new MockERC20("USD Coin", "USDC", 6);
        usdc.mint(payer, 1_000 * USD);
        vm.prank(payer);
        usdc.approve(address(escrow), type(uint256).max);
    }

    function _open(uint256 amount, address arb) internal returns (uint256 id) {
        vm.prank(payer);
        id = escrow.open(payee, address(usdc), amount, uint64(block.timestamp + 7 days), arb, bytes32("inv-1"));
    }

    function _remaining(uint256 id) internal view returns (uint256 r) {
        (,,,,,,, r,) = escrow.holds(id);
    }

    function _closed(uint256 id) internal view returns (bool c) {
        (,,,,, c,,,) = escrow.holds(id);
    }

    function test_OpenLocksFunds() public {
        uint256 id = _open(300 * USD, address(0));
        assertEq(id, 1);
        assertEq(usdc.balanceOf(address(escrow)), 300 * USD);
        assertEq(usdc.balanceOf(payer), 700 * USD);
        assertEq(_remaining(id), 300 * USD);
    }

    function test_PartialCapturesThenRefund() public {
        uint256 id = _open(300 * USD, address(0));
        vm.startPrank(payer);
        escrow.capture(id, 100 * USD);
        escrow.capture(id, 50 * USD);
        vm.stopPrank();
        assertEq(usdc.balanceOf(payee), 150 * USD);

        vm.prank(payee); // payee hands back the rest early
        escrow.refund(id);
        assertEq(usdc.balanceOf(payer), 850 * USD);
        assertTrue(_closed(id));
        assertEq(usdc.balanceOf(address(escrow)), 0);
    }

    function test_FullCaptureCloses() public {
        uint256 id = _open(300 * USD, address(0));
        vm.prank(payer);
        escrow.capture(id, 300 * USD);
        assertTrue(_closed(id));
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(Escrow.HoldClosed.selector, id));
        escrow.capture(id, 1);
    }

    function test_CaptureAuth() public {
        uint256 id = _open(300 * USD, address(0));
        vm.prank(payee);
        vm.expectRevert(Escrow.NotAuthorized.selector);
        escrow.capture(id, 1);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(Escrow.ExceedsRemaining.selector, 301 * USD, 300 * USD));
        escrow.capture(id, 301 * USD);
    }

    function test_RefundOnlyAfterExpiryForOthers() public {
        uint256 id = _open(300 * USD, address(0));
        vm.prank(stranger);
        vm.expectRevert(abi.encodeWithSelector(Escrow.NotExpired.selector, uint64(block.timestamp + 7 days)));
        escrow.refund(id);
        vm.prank(payer); // the payer can't claw back before expiry either
        vm.expectRevert(abi.encodeWithSelector(Escrow.NotExpired.selector, uint64(block.timestamp + 7 days)));
        escrow.refund(id);

        vm.warp(block.timestamp + 7 days + 1);
        vm.prank(stranger);
        escrow.refund(id);
        assertEq(usdc.balanceOf(payer), 1_000 * USD);
    }

    function test_ArbiterResolves() public {
        uint256 id = _open(300 * USD, arbiter);
        vm.prank(payer);
        escrow.capture(id, 100 * USD);
        vm.prank(arbiter);
        escrow.resolve(id, 120 * USD);
        assertEq(usdc.balanceOf(payee), 220 * USD);
        assertEq(usdc.balanceOf(payer), 780 * USD);
        assertTrue(_closed(id));
    }

    function test_ArbiterCanCapture_StrangerCannotResolve() public {
        uint256 id = _open(300 * USD, arbiter);
        vm.prank(arbiter);
        escrow.capture(id, 10 * USD);
        vm.prank(stranger);
        vm.expectRevert(Escrow.NotAuthorized.selector);
        escrow.resolve(id, 0);

        uint256 noArb = _open(10 * USD, address(0));
        vm.prank(stranger);
        vm.expectRevert(Escrow.NotAuthorized.selector);
        escrow.resolve(noArb, 0);
    }

    function test_OpenValidation() public {
        vm.startPrank(payer);
        vm.expectRevert(Escrow.ZeroAmount.selector);
        escrow.open(payee, address(usdc), 0, uint64(block.timestamp + 1), address(0), 0);
        vm.expectRevert(Escrow.BadExpiry.selector);
        escrow.open(payee, address(usdc), 1, uint64(block.timestamp), address(0), 0);
        vm.expectRevert(Escrow.ZeroAddress.selector);
        escrow.open(address(0), address(usdc), 1, uint64(block.timestamp + 1), address(0), 0);
        vm.stopPrank();
        vm.expectRevert(abi.encodeWithSelector(Escrow.UnknownHold.selector, 99));
        escrow.capture(99, 1);
    }

    /// Fuzz: any sequence of captures, then a refund, pays out exactly what
    /// was held, split between payee and payer.
    function testFuzz_Conservation(uint96 amount, uint96[4] memory caps) public {
        uint256 held = bound(uint256(amount), 1, 1_000 * USD);
        uint256 id = _open(held, address(0));
        uint256 captured;
        for (uint256 k = 0; k < caps.length; ++k) {
            uint256 left = held - captured;
            if (left == 0) break;
            uint256 c = bound(uint256(caps[k]), 1, left);
            vm.prank(payer);
            escrow.capture(id, c);
            captured += c;
        }
        if (captured < held) {
            vm.prank(payee);
            escrow.refund(id);
        }
        assertEq(usdc.balanceOf(payee), captured);
        assertEq(usdc.balanceOf(payer), 1_000 * USD - captured);
        assertEq(usdc.balanceOf(address(escrow)), 0);
    }
}
