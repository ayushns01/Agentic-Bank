// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "../lib/IERC20.sol";
import {SafeTransfer} from "../lib/SafeTransfer.sol";
import {SignatureChecker} from "../lib/SignatureChecker.sol";
import {PolicyEngine} from "../policy/PolicyEngine.sol";

/// @title AgentAccount
/// @notice Holds an org's stablecoins and pays on behalf of its agents.
///
/// Agents never hold the owner key. Each agent signs EIP-712 intents with a
/// session key bound to a policy node; anyone may relay the intent. Three
/// intent kinds exist:
///   - PaymentIntent: one token transfer;
///   - BatchIntent:   many transfers (payouts, payroll) under one signature;
///   - CallIntent:    a call to an approved venue function (deposit, swap...).
/// Every token outflow passes through PolicyEngine.authorize before funds
/// move, and every call through PolicyEngine.authorizeCall, so a
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

    struct Payout {
        address token;
        address to;
        uint256 amount;
        bytes32 ref;
    }

    struct BatchIntent {
        Payout[] payouts;
        uint256 nonce;
        uint64 deadline;
    }

    /// @dev Who is spending under which intent; keeps internal calls shallow.
    struct Ctx {
        bytes32 node;
        address key;
        uint256 nonce;
        bool coSigned;
    }

    struct CallIntent {
        address target;
        bytes data;
        uint256 nonce;
        uint64 deadline;
    }

    bytes32 public constant PAYMENT_INTENT_TYPEHASH = keccak256(
        "PaymentIntent(address token,address to,uint256 amount,bytes32 ref,uint256 nonce,uint64 deadline)"
    );
    bytes32 public constant PAYOUT_TYPEHASH = keccak256("Payout(address token,address to,uint256 amount,bytes32 ref)");
    bytes32 public constant BATCH_INTENT_TYPEHASH = keccak256(
        "BatchIntent(Payout[] payouts,uint256 nonce,uint64 deadline)Payout(address token,address to,uint256 amount,bytes32 ref)"
    );
    bytes32 public constant CALL_INTENT_TYPEHASH =
        keccak256("CallIntent(address target,bytes data,uint256 nonce,uint64 deadline)");
    bytes32 private constant DOMAIN_TYPEHASH =
        keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");

    // ERC-20 functions that move or expose the account's tokens. A CallIntent
    // using one of them is charged against the spend policy, not just the
    // call allowlist.
    bytes4 private constant TRANSFER = IERC20.transfer.selector;
    bytes4 private constant TRANSFER_FROM = IERC20.transferFrom.selector;
    bytes4 private constant APPROVE = IERC20.approve.selector;
    bytes4 private constant INCREASE_ALLOWANCE = bytes4(keccak256("increaseAllowance(address,uint256)"));

    uint256 public constant MAX_BATCH = 64;

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
    event CallExecuted(
        bytes32 indexed node, address indexed key, address indexed target, bytes4 selector, uint256 nonce, bool coSigned
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
    error EmptyBatch();
    error BatchTooLarge(uint256 size);
    error ShortCalldata();
    error SelfCall();

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
        Ctx memory c = _begin(intentDigest(intent), intent.nonce, intent.deadline, sessionSig, ownerSig);
        _pay(c, intent.token, intent.to, intent.amount, intent.ref);
    }

    /// @notice Execute many transfers under one signature (payouts, payroll).
    ///         Each payout is checked and charged separately, in order; if any
    ///         one fails the whole batch reverts.
    function executeBatch(BatchIntent calldata intent, bytes calldata sessionSig, bytes calldata ownerSig)
        external
        nonReentrant
    {
        uint256 n = intent.payouts.length;
        if (n == 0) revert EmptyBatch();
        if (n > MAX_BATCH) revert BatchTooLarge(n);
        Ctx memory c = _begin(batchDigest(intent), intent.nonce, intent.deadline, sessionSig, ownerSig);
        for (uint256 i = 0; i < n; ++i) {
            Payout calldata p = intent.payouts[i];
            _pay(c, p.token, p.to, p.amount, p.ref);
        }
    }

    /// @notice Call an approved venue function, e.g. deposit into a vault.
    ///         ERC-20 transfer/approve calls are also charged against the
    ///         node's spend limits, so a call can never move more value than
    ///         a direct payment could. Native value is not supported here;
    ///         budgets are denominated in the account's stablecoin units.
    function executeCall(CallIntent calldata intent, bytes calldata sessionSig, bytes calldata ownerSig)
        external
        nonReentrant
        returns (bytes memory ret)
    {
        if (intent.data.length < 4) revert ShortCalldata();
        // The account administers its own policy nodes, so letting an agent
        // call itself or the engine would let it rewrite its own limits.
        if (intent.target == address(this) || intent.target == address(engine)) revert SelfCall();
        Ctx memory c = _begin(callDigest(intent), intent.nonce, intent.deadline, sessionSig, ownerSig);

        bytes4 selector = bytes4(intent.data[:4]);
        engine.authorizeCall(c.node, intent.target, selector);
        _chargeTokenCall(c, intent.target, selector, intent.data);

        bool ok;
        (ok, ret) = intent.target.call(intent.data);
        if (!ok) revert CallFailed(ret);
        emit CallExecuted(c.node, c.key, intent.target, selector, c.nonce, c.coSigned);
    }

    // ------------------------------------------------------------ internals

    /// @dev Validates the session key (consuming its nonce) and the optional
    ///      owner co-signature.
    function _begin(
        bytes32 digest,
        uint256 nonce,
        uint64 deadline,
        bytes calldata sessionSig,
        bytes calldata ownerSig
    ) internal returns (Ctx memory c) {
        (c.key, c.node) = _useSessionKey(digest, nonce, deadline, sessionSig);
        c.nonce = nonce;
        c.coSigned = _coSigned(digest, ownerSig);
    }

    /// @dev Recovers and validates the session key, then consumes its nonce.
    function _useSessionKey(bytes32 digest, uint256 nonce, uint64 deadline, bytes calldata sessionSig)
        internal
        returns (address key, bytes32 node)
    {
        key = SignatureChecker.recover(digest, sessionSig);
        if (key == address(0)) revert InvalidSessionSignature();
        SessionKey memory sk = sessionKeys[key];
        if (!sk.active) revert SessionKeyInactive(key);
        if (block.timestamp > sk.validUntil) revert SessionKeyExpired(key);
        if (block.timestamp > deadline) revert IntentExpired(deadline);
        uint256 expected = nonces[key];
        if (nonce != expected) revert BadNonce(expected, nonce);
        nonces[key] = expected + 1;
        node = sk.node;
    }

    function _coSigned(bytes32 digest, bytes calldata ownerSig) internal view returns (bool) {
        if (ownerSig.length == 0) return false;
        if (!SignatureChecker.isValid(owner, digest, ownerSig)) revert InvalidCoSignature();
        return true;
    }

    /// @dev Policy check, then transfer. Checks-effects-interactions: the
    ///      engine records the spend before tokens move.
    function _pay(Ctx memory c, address token, address to, uint256 amount, bytes32 ref) internal {
        engine.authorize(c.node, token, to, amount, c.coSigned);
        IERC20(token).safeTransfer(to, amount);
        emit PaymentExecuted(c.node, c.key, token, to, amount, ref, c.nonce, c.coSigned);
    }

    /// @dev Token-moving ERC-20 calls are charged like payments. Approving a
    ///      venue charges the approved amount up front, because the venue may
    ///      pull it later. Revoking (amount 0) is always allowed.
    function _chargeTokenCall(Ctx memory c, address token, bytes4 selector, bytes calldata data) internal {
        if (selector == TRANSFER || selector == APPROVE || selector == INCREASE_ALLOWANCE) {
            (address to, uint256 amount) = abi.decode(data[4:], (address, uint256));
            if (amount != 0) engine.authorize(c.node, token, to, amount, c.coSigned);
        } else if (selector == TRANSFER_FROM) {
            (address from, address to, uint256 amount) = abi.decode(data[4:], (address, address, uint256));
            if (from == address(this) && amount != 0) engine.authorize(c.node, token, to, amount, c.coSigned);
        }
    }

    // --------------------------------------------------------------- digests

    /// @notice EIP-712 digest of a payment intent for this account and chain.
    function intentDigest(PaymentIntent calldata i) public view returns (bytes32) {
        bytes32 structHash =
            keccak256(abi.encode(PAYMENT_INTENT_TYPEHASH, i.token, i.to, i.amount, i.ref, i.nonce, i.deadline));
        return _hashTyped(structHash);
    }

    function batchDigest(BatchIntent calldata b) public view returns (bytes32) {
        uint256 n = b.payouts.length;
        bytes32[] memory hashes = new bytes32[](n);
        for (uint256 i = 0; i < n; ++i) {
            Payout calldata p = b.payouts[i];
            hashes[i] = keccak256(abi.encode(PAYOUT_TYPEHASH, p.token, p.to, p.amount, p.ref));
        }
        bytes32 structHash =
            keccak256(abi.encode(BATCH_INTENT_TYPEHASH, keccak256(abi.encodePacked(hashes)), b.nonce, b.deadline));
        return _hashTyped(structHash);
    }

    function callDigest(CallIntent calldata c) public view returns (bytes32) {
        bytes32 structHash =
            keccak256(abi.encode(CALL_INTENT_TYPEHASH, c.target, keccak256(c.data), c.nonce, c.deadline));
        return _hashTyped(structHash);
    }

    function domainSeparator() public view returns (bytes32) {
        return keccak256(
            abi.encode(DOMAIN_TYPEHASH, keccak256("AgentAccount"), keccak256("1"), block.chainid, address(this))
        );
    }

    function _hashTyped(bytes32 structHash) internal view returns (bytes32) {
        return keccak256(abi.encodePacked("\x19\x01", domainSeparator(), structHash));
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
