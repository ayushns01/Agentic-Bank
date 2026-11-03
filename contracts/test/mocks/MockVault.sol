// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "../../src/lib/IERC20.sol";

/// @notice Stand-in for a DeFi venue: pulls approved tokens on deposit.
contract MockVault {
    IERC20 public immutable asset;
    mapping(address => uint256) public shares;

    constructor(IERC20 asset_) {
        asset = asset_;
    }

    function deposit(uint256 amount) external {
        asset.transferFrom(msg.sender, address(this), amount);
        shares[msg.sender] += amount;
    }

    function withdraw(uint256 amount) external {
        shares[msg.sender] -= amount;
        asset.transfer(msg.sender, amount);
    }
}
