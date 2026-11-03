// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {AgentAccount} from "./AgentAccount.sol";
import {PolicyEngine} from "../policy/PolicyEngine.sol";

/// @title AccountFactory
/// @notice Opens AgentAccounts at deterministic (CREATE2) addresses, so an
///         account's address is known, and can be funded, before it exists.
contract AccountFactory {
    PolicyEngine public immutable engine;

    mapping(address => bool) public isAccount;

    event AccountOpened(address indexed account, address indexed owner, bytes32 salt);

    error AlreadyOpened(address account);

    constructor(PolicyEngine engine_) {
        engine = engine_;
    }

    /// @notice Deploy an account for `owner`. The salt is bound to the owner,
    ///         so nobody can squat another owner's predicted address.
    function openAccount(address owner, bytes32 salt) external returns (AgentAccount account) {
        address predicted = predictAddress(owner, salt);
        if (predicted.code.length != 0) revert AlreadyOpened(predicted);
        account = new AgentAccount{salt: _salt(owner, salt)}(engine, owner);
        isAccount[address(account)] = true;
        emit AccountOpened(address(account), owner, salt);
    }

    function predictAddress(address owner, bytes32 salt) public view returns (address) {
        bytes32 initHash = keccak256(abi.encodePacked(type(AgentAccount).creationCode, abi.encode(engine, owner)));
        bytes32 h = keccak256(abi.encodePacked(bytes1(0xff), address(this), _salt(owner, salt), initHash));
        return address(uint160(uint256(h)));
    }

    function _salt(address owner, bytes32 salt) private pure returns (bytes32) {
        return keccak256(abi.encode(owner, salt));
    }
}
