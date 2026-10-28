// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "../lib/IERC20.sol";
import {SafeTransfer} from "../lib/SafeTransfer.sol";
import {SignatureChecker} from "../lib/SignatureChecker.sol";
import {PolicyEngine} from "../policy/PolicyEngine.sol";

/// @title AgentAccount
/// @notice Holds an org's stablecoins and pays on behalf of its agents.
///
/// Agents never hold the owner key. Each agent signs EIP-712 payment intents
/// with a session key bound to a policy node; anyone may relay the intent.
/// Every payment passes through PolicyEngine.authorize before funds move, so a
/// prompt-injected agent or a compromised relayer cannot exceed policy.
/// Amounts above a node's co-sign threshold also need the owner's signature
/// (EOA or EIP-1271 contract such as a Safe).
contract AgentAccount {
    using SafeTransfer for IERC20;

    struct SessionKey {
        bytes32 node;
        uint64 validUntil;
        bool active;
    }

    struct PaymentIntent {
        address token;
        address to;
        uint256 amount;
        bytes32 ref; // invoice / payment reference for the ledger
        uint256 nonce;
        uint64 deadline;
    }

    bytes32 public constant PAYMENT_INTENT_TYPEHASH = keccak256(
        "PaymentIntent(address token,address to,uint256 amount,bytes32 ref,uint256 nonce,uint64 deadline)"
    );
    bytes32 private constant DOMAIN_TYPEHASH =
        keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");

    PolicyEngine public immutable engine;
    address public owner;

    mapping(address => SessionKey) public sessionKeys;
    mapping(address => uint256) public nonces;

    bool private _entered;

    event OwnerChanged(address indexed previous, address indexed next);
    event SessionKeySet(address indexed key, bytes32 indexed node, uint64 validUntil);
    event SessionKeyRevoked(address indexed key);
    event PaymentExecuted(
        bytes32 indexed node,
        address indexed key,
        address indexed token,
        address to,
        uint256 amount,
        bytes32 ref,
        uint256 nonce,
        bool coSigned
    );

    error NotOwner();
    error ZeroAddress();
    error InvalidSessionSignature();
    error SessionKeyInactive(address key);
    error SessionKeyExpired(address key);
    error BadNonce(uint256 expected, uint256 got);
    error IntentExpired(uint64 deadline);
    error InvalidCoSignature();
    error Reentrancy();
    error CallFailed(bytes reason);

    modifier onlyOwner() {
        if (msg.sender != owner) revert NotOwner();
        _;
    }

    modifier nonReentrant() {
        if (_entered) revert Reentrancy();
        _entered = true;
        _;
        _entered = false;
    }

    constructor(PolicyEngine engine_, address owner_) {
        if (address(engine_) == address(0) || owner_ == address(0)) revert ZeroAddress();
        engine = engine_;
        owner = owner_;
        emit OwnerChanged(address(0), owner_);
    }

    // ------------------------------------------------------------ payments

    /// @notice Execute an agent's signed payment intent. Callable by anyone.
    /// @param sessionSig signature over the intent digest by a session key.
    /// @param ownerSig   optional owner co-signature over the same digest;
    ///                   empty if none.
    function executePayment(PaymentIntent calldata intent, bytes calldata sessionSig, bytes calldata ownerSig)
        external
        nonReentrant
    {
        bytes32 digest = intentDigest(intent);
        (address key, bytes32 node) = _useSessionKey(digest, intent, sessionSig);

        bool coSigned;
        if (ownerSig.length != 0) {
            if (!SignatureChecker.isValid(owner, digest, ownerSig)) revert InvalidCoSignature();
            coSigned = true;
        }
        _pay(node, key, intent, coSigned);
    }

    /// @dev Recovers and validates the session key, then consumes its nonce.
    function _useSessionKey(bytes32 digest, PaymentIntent calldata intent, bytes calldata sessionSig)
        internal
        returns (address key, bytes32 node)
    {
        key = SignatureChecker.recover(digest, sessionSig);
        if (key == address(0)) revert InvalidSessionSignature();
        SessionKey memory sk = sessionKeys[key];
        if (!sk.active) revert SessionKeyInactive(key);
        if (block.timestamp > sk.validUntil) revert SessionKeyExpired(key);
        if (block.timestamp > intent.deadline) revert IntentExpired(intent.deadline);
        uint256 expected = nonces[key];
        if (intent.nonce != expected) revert BadNonce(expected, intent.nonce);
        nonces[key] = expected + 1;
        node = sk.node;
    }

    /// @dev Policy check, then transfer. Checks-effects-interactions: the
    ///      engine records the spend before tokens move.
    function _pay(bytes32 node, address key, PaymentIntent calldata intent, bool coSigned) internal {
        engine.authorize(node, intent.token, intent.to, intent.amount, coSigned);
        IERC20(intent.token).safeTransfer(intent.to, intent.amount);
        emit PaymentExecuted(node, key, intent.token, intent.to, intent.amount, intent.ref, intent.nonce, coSigned);
    }

    /// @notice EIP-712 digest of an intent for this account and chain.
    function intentDigest(PaymentIntent calldata i) public view returns (bytes32) {
        bytes32 structHash =
            keccak256(abi.encode(PAYMENT_INTENT_TYPEHASH, i.token, i.to, i.amount, i.ref, i.nonce, i.deadline));
        return keccak256(abi.encodePacked("\x19\x01", domainSeparator(), structHash));
    }

    function domainSeparator() public view returns (bytes32) {
        return keccak256(
            abi.encode(DOMAIN_TYPEHASH, keccak256("AgentAccount"), keccak256("1"), block.chainid, address(this))
        );
    }

    // --------------------------------------------------------- owner admin

    function setSessionKey(address key, bytes32 node, uint64 validUntil) external onlyOwner {
        if (key == address(0)) revert ZeroAddress();
        // The node must exist and belong to this account.
        if (engine.node(node).account != address(this)) revert PolicyEngine.NotNodeAccount(node);
        sessionKeys[key] = SessionKey({node: node, validUntil: validUntil, active: true});
        emit SessionKeySet(key, node, validUntil);
    }

    function revokeSessionKey(address key) external onlyOwner {
        sessionKeys[key].active = false;
        emit SessionKeyRevoked(key);
    }

    function transferOwnership(address next) external onlyOwner {
        if (next == address(0)) revert ZeroAddress();
        emit OwnerChanged(owner, next);
        owner = next;
    }

    /// @notice Owner escape hatch and policy administration: arbitrary call.
    ///         Policy nodes are owned by this account, so the owner configures
    ///         them by calling the engine through here.
    function ownerCall(address target, uint256 value, bytes calldata data)
        external
        onlyOwner
        nonReentrant
        returns (bytes memory ret)
    {
        bool ok;
        (ok, ret) = target.call{value: value}(data);
        if (!ok) revert CallFailed(ret);
    }

    receive() external payable {}
}
