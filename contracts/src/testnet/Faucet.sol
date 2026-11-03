// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {TestToken} from "./TestToken.sol";

/// @title Faucet
/// @notice Funds testnet accounts with a fixed drip per token, at most once
///         per cooldown per recipient, up to a lifetime cap per recipient.
contract Faucet {
    struct Drip {
        uint128 amount; // per claim
        uint128 lifetimeCap; // per recipient
        uint64 cooldown; // seconds between claims
        bool enabled;
    }

    address public admin;
    mapping(address => Drip) public drips; // token => config
    mapping(address => mapping(address => uint64)) public lastClaim; // token => recipient => time
    mapping(address => mapping(address => uint256)) public claimed; // token => recipient => total

    event DripSet(address indexed token, uint128 amount, uint128 lifetimeCap, uint64 cooldown, bool enabled);
    event Claimed(address indexed token, address indexed to, uint256 amount);

    error NotAdmin();
    error DripDisabled(address token);
    error CoolingDown(uint64 nextAt);
    error CapReached(uint256 claimed, uint256 cap);

    constructor(address admin_) {
        admin = admin_;
    }

    function setDrip(address token, uint128 amount, uint128 lifetimeCap, uint64 cooldown, bool enabled) external {
        if (msg.sender != admin) revert NotAdmin();
        drips[token] = Drip({amount: amount, lifetimeCap: lifetimeCap, cooldown: cooldown, enabled: enabled});
        emit DripSet(token, amount, lifetimeCap, cooldown, enabled);
    }

    /// @notice Send one drip of `token` to `to`. Anyone may claim for any
    ///         address, so an operator can fund freshly opened accounts.
    function claim(address token, address to) external returns (uint256 amount) {
        Drip memory d = drips[token];
        if (!d.enabled) revert DripDisabled(token);
        uint64 last = lastClaim[token][to];
        if (last != 0 && block.timestamp < uint256(last) + d.cooldown) revert CoolingDown(last + d.cooldown);
        uint256 total = claimed[token][to] + d.amount;
        if (total > d.lifetimeCap) revert CapReached(claimed[token][to], d.lifetimeCap);
        lastClaim[token][to] = uint64(block.timestamp);
        claimed[token][to] = total;
        amount = d.amount;
        TestToken(token).mint(to, amount);
        emit Claimed(token, to, amount);
    }
}
