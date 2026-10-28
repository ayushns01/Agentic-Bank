// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title PolicyEngine
/// @notice On-chain source of truth for agent spending policy (ADR 0001).
///
/// Policies form a delegation tree per account: an org root node, agent
/// nodes under it, sub-agent nodes under those. A spend by node n passes only
/// if it passes the limits of n AND every ancestor, and on success it is
/// charged to all of them. Freezing a node therefore freezes its subtree, and
/// a parent's budget bounds the sum of everything spent beneath it.
///
/// Only the account that owns a node may configure it or spend through it.
contract PolicyEngine {
    uint256 public constant MAX_DEPTH = 8;
    uint128 public constant UNLIMITED = type(uint128).max;

    struct Limits {
        uint128 perTxLimit; // max per payment
        uint128 periodLimit; // max per window (ignored if periodLength == 0)
        uint128 coSignAbove; // amounts above this need an owner co-signature
        uint64 periodLength; // window length in seconds; 0 = no window
        uint64 validUntil; // node expiry (unix seconds); 0 = never
        bool recipientAllowlist; // if true, only allowlisted recipients
    }

    struct Node {
        address account;
        bytes32 parent;
        uint8 depth; // 0 for a root
        bool frozen;
        Limits limits;
        uint64 periodStart;
        uint128 spentInPeriod;
    }

    mapping(bytes32 => Node) internal _nodes;
    mapping(bytes32 => mapping(address => bool)) public tokenAllowed;
    mapping(bytes32 => mapping(address => bool)) public recipientAllowed;

    event NodeCreated(bytes32 indexed node, address indexed account, bytes32 indexed parent);
    event LimitsUpdated(bytes32 indexed node, Limits limits);
    event FrozenSet(bytes32 indexed node, bool frozen);
    event TokenAllowed(bytes32 indexed node, address indexed token, bool allowed);
    event RecipientAllowed(bytes32 indexed node, address indexed recipient, bool allowed);
    event Charged(bytes32 indexed node, address indexed token, address indexed to, uint256 amount);

    error NodeExists(bytes32 node);
    error UnknownNode(bytes32 node);
    error NotNodeAccount(bytes32 node);
    error TooDeep();
    error ZeroNode();
    error NodeFrozen(bytes32 node);
    error NodeExpired(bytes32 node);
    error TokenNotAllowed(bytes32 node, address token);
    error RecipientNotAllowed(bytes32 node, address to);
    error PerTxLimit(bytes32 node, uint256 amount, uint256 limit);
    error PeriodLimit(bytes32 node, uint256 wouldSpend, uint256 limit);
    error CoSignRequired(bytes32 node, uint256 amount, uint256 threshold);

    modifier onlyNodeAccount(bytes32 id) {
        Node storage n = _nodes[id];
        if (n.account == address(0)) revert UnknownNode(id);
        if (n.account != msg.sender) revert NotNodeAccount(id);
        _;
    }

    // ---------------------------------------------------------------- admin

    /// @notice Create a node owned by the caller. `parent` must be zero (root)
    ///         or an existing node owned by the caller.
    function createNode(bytes32 id, bytes32 parent, Limits calldata limits) external {
        if (id == bytes32(0)) revert ZeroNode();
        if (_nodes[id].account != address(0)) revert NodeExists(id);
        uint8 depth;
        if (parent != bytes32(0)) {
            Node storage p = _nodes[parent];
            if (p.account == address(0)) revert UnknownNode(parent);
            if (p.account != msg.sender) revert NotNodeAccount(parent);
            if (p.depth + 1 >= MAX_DEPTH) revert TooDeep();
            depth = p.depth + 1;
        }
        Node storage n = _nodes[id];
        n.account = msg.sender;
        n.parent = parent;
        n.depth = depth;
        n.limits = limits;
        n.periodStart = uint64(block.timestamp);
        emit NodeCreated(id, msg.sender, parent);
        emit LimitsUpdated(id, limits);
    }

    /// @notice Replace a node's limits. Spend already recorded in the current
    ///         window is kept, so lowering a limit takes effect immediately.
    function setLimits(bytes32 id, Limits calldata limits) external onlyNodeAccount(id) {
        Node storage n = _nodes[id];
        if (limits.periodLength != n.limits.periodLength) {
            n.periodStart = uint64(block.timestamp);
            n.spentInPeriod = 0;
        }
        n.limits = limits;
        emit LimitsUpdated(id, limits);
    }

    function setFrozen(bytes32 id, bool frozen) external onlyNodeAccount(id) {
        _nodes[id].frozen = frozen;
        emit FrozenSet(id, frozen);
    }

    function setToken(bytes32 id, address token, bool allowed) external onlyNodeAccount(id) {
        tokenAllowed[id][token] = allowed;
        emit TokenAllowed(id, token, allowed);
    }

    function setRecipient(bytes32 id, address recipient, bool allowed) external onlyNodeAccount(id) {
        recipientAllowed[id][recipient] = allowed;
        emit RecipientAllowed(id, recipient, allowed);
    }

    // ------------------------------------------------------------- enforce

    /// @notice Check a spend against `id` and every ancestor, then charge it
    ///         to all of them. Reverts with the first violated rule.
    /// @param coSigned whether the account owner co-signed this payment.
    function authorize(bytes32 id, address token, address to, uint256 amount, bool coSigned)
        external
        onlyNodeAccount(id)
    {
        bytes32 cur = id;
        for (uint256 i = 0; i <= MAX_DEPTH && cur != bytes32(0); ++i) {
            Node storage n = _nodes[cur];
            _roll(n);
            _check(cur, n, token, to, amount, coSigned);
            n.spentInPeriod += uint128(amount); // amount <= perTxLimit <= uint128 max
            emit Charged(cur, token, to, amount);
            cur = n.parent;
        }
    }

    /// @notice Dry-run of `authorize`: returns true if the spend would pass.
    function wouldAuthorize(bytes32 id, address token, address to, uint256 amount, bool coSigned)
        external
        view
        returns (bool ok, bytes memory reason)
    {
        bytes32 cur = id;
        for (uint256 i = 0; i <= MAX_DEPTH && cur != bytes32(0); ++i) {
            Node storage n = _nodes[cur];
            if (n.account == address(0)) return (false, abi.encodeWithSelector(UnknownNode.selector, cur));
            try this.checkNode(cur, token, to, amount, coSigned) {}
            catch (bytes memory err) {
                return (false, err);
            }
            cur = n.parent;
        }
        return (true, "");
    }

    /// @notice Single-node check exposed for `wouldAuthorize`.
    function checkNode(bytes32 id, address token, address to, uint256 amount, bool coSigned) external view {
        _check(id, _nodes[id], token, to, amount, coSigned);
    }

    function _check(bytes32 id, Node storage n, address token, address to, uint256 amount, bool coSigned)
        internal
        view
    {
        Limits memory l = n.limits;
        if (n.frozen) revert NodeFrozen(id);
        if (l.validUntil != 0 && block.timestamp > l.validUntil) revert NodeExpired(id);
        if (!tokenAllowed[id][token]) revert TokenNotAllowed(id, token);
        if (l.recipientAllowlist && !recipientAllowed[id][to]) revert RecipientNotAllowed(id, to);
        if (amount > l.perTxLimit) revert PerTxLimit(id, amount, l.perTxLimit);
        if (amount > l.coSignAbove && !coSigned) revert CoSignRequired(id, amount, l.coSignAbove);
        if (l.periodLength != 0) {
            uint256 wouldSpend = _currentSpent(n) + amount;
            if (wouldSpend > l.periodLimit) revert PeriodLimit(id, wouldSpend, l.periodLimit);
        }
    }

    // --------------------------------------------------------------- views

    function node(bytes32 id) external view returns (Node memory) {
        return _nodes[id];
    }

    /// @notice Spend in the current window, accounting for an elapsed window.
    function spentInCurrentPeriod(bytes32 id) external view returns (uint256) {
        return _currentSpent(_nodes[id]);
    }

    /// @notice Remaining budget in the current window for this node alone.
    function remainingInPeriod(bytes32 id) external view returns (uint256) {
        Node storage n = _nodes[id];
        if (n.limits.periodLength == 0) return UNLIMITED;
        uint256 spent = _currentSpent(n);
        return spent >= n.limits.periodLimit ? 0 : n.limits.periodLimit - spent;
    }

    function _currentSpent(Node storage n) internal view returns (uint256) {
        if (n.limits.periodLength == 0) return n.spentInPeriod;
        if (block.timestamp >= uint256(n.periodStart) + n.limits.periodLength) return 0;
        return n.spentInPeriod;
    }

    /// @dev Called by `authorize` before checking: rolls the window forward
    ///      in whole periods so that `spentInPeriod` reflects the current one.
    function _roll(Node storage n) internal {
        uint64 len = n.limits.periodLength;
        if (len == 0) return;
        if (block.timestamp >= uint256(n.periodStart) + len) {
            uint256 elapsed = (block.timestamp - n.periodStart) / len;
            n.periodStart += uint64(elapsed * len);
            n.spentInPeriod = 0;
        }
    }
}
