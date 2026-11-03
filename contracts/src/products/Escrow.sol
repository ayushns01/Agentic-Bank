// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "../lib/IERC20.sol";
import {SafeTransfer} from "../lib/SafeTransfer.sol";

/// @title Escrow
/// @notice Trustless holds for agent commerce: hold, capture in parts,
///         refund, and arbiter resolution.
///
/// Lifecycle of a hold:
///   open      payer locks `amount` of `token` for `payee` until `expiry`
///   capture   payer (or arbiter) releases part or all of it to the payee
///   refund    payee gives back the rest at any time; anyone may return the
///             rest to the payer once the hold has expired
///   resolve   arbiter splits what is left between the two parties
/// Funds are only ever paid to the hold's payer or payee.
contract Escrow {
    using SafeTransfer for IERC20;

    struct Hold {
        address payer;
        address payee;
        address arbiter; // zero: no arbiter
        address token;
        uint64 expiry;
        bool closed;
        uint256 amount; // originally held
        uint256 remaining; // still held
        bytes32 ref;
    }

    mapping(uint256 => Hold) public holds;
    uint256 public nextId = 1;
    bool private _entered;

    event Opened(
        uint256 indexed id,
        address indexed payer,
        address indexed payee,
        address token,
        uint256 amount,
        uint64 expiry,
        address arbiter,
        bytes32 ref
    );
    event Captured(uint256 indexed id, uint256 amount, uint256 remaining);
    event Refunded(uint256 indexed id, uint256 amount);
    event Resolved(uint256 indexed id, uint256 toPayee, uint256 toPayer);
    event Closed(uint256 indexed id);

    error ZeroAmount();
    error ZeroAddress();
    error BadExpiry();
    error UnknownHold(uint256 id);
    error HoldClosed(uint256 id);
    error NotAuthorized();
    error ExceedsRemaining(uint256 requested, uint256 remaining);
    error NotExpired(uint64 expiry);
    error Reentrancy();
    error FeeOnTransfer();

    modifier nonReentrant() {
        if (_entered) revert Reentrancy();
        _entered = true;
        _;
        _entered = false;
    }

    /// @notice Lock funds from the caller. The caller must have approved this
    ///         contract for `amount`.
    function open(address payee, address token, uint256 amount, uint64 expiry, address arbiter, bytes32 ref)
        external
        nonReentrant
        returns (uint256 id)
    {
        if (amount == 0) revert ZeroAmount();
        if (payee == address(0) || token == address(0)) revert ZeroAddress();
        if (expiry <= block.timestamp) revert BadExpiry();
        id = nextId++;
        holds[id] = Hold({
            payer: msg.sender,
            payee: payee,
            arbiter: arbiter,
            token: token,
            expiry: expiry,
            closed: false,
            amount: amount,
            remaining: amount,
            ref: ref
        });
        // Measure what actually arrived so fee-on-transfer tokens can't leave
        // the escrow under-collateralised.
        uint256 before = IERC20(token).balanceOf(address(this));
        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        if (IERC20(token).balanceOf(address(this)) - before != amount) revert FeeOnTransfer();
        emit Opened(id, msg.sender, payee, token, amount, expiry, arbiter, ref);
    }

    /// @notice Pay `amount` of the hold to the payee. Payer or arbiter only.
    function capture(uint256 id, uint256 amount) external nonReentrant {
        Hold storage h = _live(id);
        if (msg.sender != h.payer && (h.arbiter == address(0) || msg.sender != h.arbiter)) revert NotAuthorized();
        if (amount == 0) revert ZeroAmount();
        if (amount > h.remaining) revert ExceedsRemaining(amount, h.remaining);
        h.remaining -= amount;
        emit Captured(id, amount, h.remaining);
        _closeIfEmpty(id, h);
        IERC20(h.token).safeTransfer(h.payee, amount);
    }

    /// @notice Return what is left to the payer. The payee may do this at any
    ///         time; anyone may once the hold has expired.
    function refund(uint256 id) external nonReentrant {
        Hold storage h = _live(id);
        if (msg.sender != h.payee && block.timestamp <= h.expiry) revert NotExpired(h.expiry);
        uint256 amount = h.remaining;
        h.remaining = 0;
        emit Refunded(id, amount);
        _closeIfEmpty(id, h);
        IERC20(h.token).safeTransfer(h.payer, amount);
    }

    /// @notice Arbiter settles a dispute by splitting what is left.
    function resolve(uint256 id, uint256 toPayee) external nonReentrant {
        Hold storage h = _live(id);
        if (h.arbiter == address(0) || msg.sender != h.arbiter) revert NotAuthorized();
        if (toPayee > h.remaining) revert ExceedsRemaining(toPayee, h.remaining);
        uint256 toPayer = h.remaining - toPayee;
        h.remaining = 0;
        emit Resolved(id, toPayee, toPayer);
        _closeIfEmpty(id, h);
        if (toPayee != 0) IERC20(h.token).safeTransfer(h.payee, toPayee);
        if (toPayer != 0) IERC20(h.token).safeTransfer(h.payer, toPayer);
    }

    function _live(uint256 id) internal view returns (Hold storage h) {
        h = holds[id];
        if (h.payer == address(0)) revert UnknownHold(id);
        if (h.closed) revert HoldClosed(id);
    }

    function _closeIfEmpty(uint256 id, Hold storage h) internal {
        if (h.remaining == 0) {
            h.closed = true;
            emit Closed(id);
        }
    }
}
