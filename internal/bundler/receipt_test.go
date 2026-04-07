package bundler

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"go.uber.org/zap"
)

var testEntryPoint = common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

func makeUserOperationEventLog(opHash common.Hash, sender, paymaster common.Address, success bool, gasCost *big.Int) *types.Log {
	// Data: nonce(32) + success(32) + actualGasCost(32) + actualGasUsed(32)
	data := make([]byte, 128)
	// nonce = 0 (leave zeros)
	if success {
		data[63] = 1 // success bool
	}
	copy(data[64:96], core.PadLeft32(gasCost.Bytes())) // actualGasCost
	// actualGasUsed = 0 (leave zeros)

	return &types.Log{
		Address: testEntryPoint,
		Topics: []common.Hash{
			userOperationEventTopic,
			opHash,
			common.BytesToHash(sender.Bytes()),
			common.BytesToHash(paymaster.Bytes()),
		},
		Data: data,
	}
}

func makeRevertReasonLog(opHash common.Hash, sender common.Address, reason string) *types.Log {
	// Data: nonce(32) + offset(32) + length(32) + reason(padded)
	paddedReasonLen := ((len(reason) + 31) / 32) * 32
	data := make([]byte, 96+paddedReasonLen)
	// nonce = 0
	copy(data[32:64], core.PadLeft32(big.NewInt(64).Bytes())) // offset = 64
	copy(data[64:96], core.PadLeft32(big.NewInt(int64(len(reason))).Bytes()))
	copy(data[96:], []byte(reason))

	return &types.Log{
		Address: testEntryPoint,
		Topics: []common.Hash{
			userOperationRevertReasonTopic,
			opHash,
			common.BytesToHash(sender.Bytes()),
		},
		Data: data,
	}
}

func TestParseHandleOpsReceiptSuccess(t *testing.T) {
	opHash := common.HexToHash("0xaaaa")
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	paymaster := common.Address{}
	gasCost := big.NewInt(42000)

	receipt := &types.Receipt{
		Logs: []*types.Log{
			makeUserOperationEventLog(opHash, sender, paymaster, true, gasCost),
		},
	}

	outcomes := ParseHandleOpsReceipt(receipt, testEntryPoint)
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(outcomes))
	}

	o := outcomes[opHash]
	if !o.Success {
		t.Error("expected success=true")
	}
	if o.ActualGasCost.Cmp(gasCost) != 0 {
		t.Errorf("gasCost = %s, want %s", o.ActualGasCost, gasCost)
	}
	if o.RevertReason != nil {
		t.Error("expected no revert reason")
	}
}

func TestParseHandleOpsReceiptFailed(t *testing.T) {
	opHash := common.HexToHash("0xbbbb")
	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")

	receipt := &types.Receipt{
		Logs: []*types.Log{
			makeUserOperationEventLog(opHash, sender, common.Address{}, false, big.NewInt(10000)),
			makeRevertReasonLog(opHash, sender, "AA23 reverted"),
		},
	}

	outcomes := ParseHandleOpsReceipt(receipt, testEntryPoint)
	o := outcomes[opHash]
	if o.Success {
		t.Error("expected success=false")
	}
	if o.RevertReason == nil || *o.RevertReason != "AA23 reverted" {
		t.Errorf("revert_reason = %v", o.RevertReason)
	}
}

func TestParseHandleOpsReceiptMultipleOps(t *testing.T) {
	hash1 := common.HexToHash("0x1111")
	hash2 := common.HexToHash("0x2222")
	sender := common.HexToAddress("0xaaaa")

	receipt := &types.Receipt{
		Logs: []*types.Log{
			makeUserOperationEventLog(hash1, sender, common.Address{}, true, big.NewInt(100)),
			makeUserOperationEventLog(hash2, sender, common.Address{}, true, big.NewInt(200)),
		},
	}

	outcomes := ParseHandleOpsReceipt(receipt, testEntryPoint)
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	if !outcomes[hash1].Success || !outcomes[hash2].Success {
		t.Error("both should be successful")
	}
}

func TestParseHandleOpsReceiptIgnoresOtherContracts(t *testing.T) {
	otherAddr := common.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
	receipt := &types.Receipt{
		Logs: []*types.Log{
			{
				Address: otherAddr, // not the entry point
				Topics:  []common.Hash{userOperationEventTopic, common.HexToHash("0xaaaa")},
				Data:    make([]byte, 128),
			},
		},
	}

	outcomes := ParseHandleOpsReceipt(receipt, testEntryPoint)
	if len(outcomes) != 0 {
		t.Error("should ignore logs from other contracts")
	}
}

func TestDecodeFailedOp(t *testing.T) {
	// Build FailedOp(uint256 opIndex=2, string reason="AA25 invalid account nonce")
	reason := "AA25 invalid account nonce"

	data := make([]byte, 4+32+32+32+32)
	copy(data[:4], failedOpSelector)
	copy(data[4:36], core.PadLeft32(big.NewInt(2).Bytes()))     // opIndex = 2
	copy(data[36:68], core.PadLeft32(big.NewInt(64).Bytes()))   // offset to string = 64
	copy(data[68:100], core.PadLeft32(big.NewInt(int64(len(reason))).Bytes())) // string length
	data = append(data, make([]byte, 32)...)                     // padded string data
	copy(data[100:], []byte(reason))

	idx, r := DecodeFailedOp(data)
	if idx != 2 {
		t.Errorf("opIndex = %d, want 2", idx)
	}
	if r != reason {
		t.Errorf("reason = %q, want %q", r, reason)
	}
}

func TestDecodeFailedOpShortData(t *testing.T) {
	idx, r := DecodeFailedOp([]byte{0x01, 0x02})
	if idx != 0 || r != "unknown" {
		t.Errorf("short data: idx=%d reason=%q", idx, r)
	}
}

func TestHandleBundleRevertFailedOp(t *testing.T) {
	repo := newMockRepo()

	idx0, idx1 := 0, 1
	ops := []*mempool.StoredOp{
		{Hash: common.HexToHash("0xaa"), BundleIndex: &idx0},
		{Hash: common.HexToHash("0xbb"), BundleIndex: &idx1},
	}
	repo.ops[ops[0].Hash] = ops[0]
	repo.ops[ops[1].Hash] = ops[1]

	// Build FailedOp revert for index 1.
	reason := "AA21 insufficient balance"
	revertData := make([]byte, 4+32+32+32+32)
	copy(revertData[:4], failedOpSelector)
	copy(revertData[4:36], core.PadLeft32(big.NewInt(1).Bytes()))
	copy(revertData[36:68], core.PadLeft32(big.NewInt(64).Bytes()))
	copy(revertData[68:100], core.PadLeft32(big.NewInt(int64(len(reason))).Bytes()))
	revertData = append(revertData, make([]byte, 32)...)
	copy(revertData[100:], []byte(reason))

	HandleBundleRevert(revertData, ops, repo, zap.NewNop())

	// Op at index 1 should be failed.
	if repo.ops[ops[1].Hash].Status != mempool.StatusFailed {
		t.Errorf("op[1] status = %s, want failed", repo.ops[ops[1].Hash].Status)
	}
	// Op at index 0 should be reset to pending.
	if repo.ops[ops[0].Hash].Status != mempool.StatusPending {
		t.Errorf("op[0] status = %s, want pending", repo.ops[ops[0].Hash].Status)
	}
}

func TestHandleBundleRevertUnknown(t *testing.T) {
	repo := newMockRepo()

	idx0 := 0
	ops := []*mempool.StoredOp{
		{Hash: common.HexToHash("0xcc"), BundleIndex: &idx0},
	}
	repo.ops[ops[0].Hash] = ops[0]

	// Unknown revert — not FailedOp.
	HandleBundleRevert([]byte{0xde, 0xad}, ops, repo, zap.NewNop())

	if repo.ops[ops[0].Hash].Status != mempool.StatusPending {
		t.Errorf("status = %s, want pending", repo.ops[ops[0].Hash].Status)
	}
}

// mockRepo for HandleBundleRevert tests — only needs UpdateStatus.
type mockRepo struct {
	ops map[common.Hash]*mempool.StoredOp
}

func newMockRepo() *mockRepo {
	return &mockRepo{ops: map[common.Hash]*mempool.StoredOp{}}
}

func (m *mockRepo) UpdateStatus(hash common.Hash, status mempool.Status, extra *mempool.StatusExtra) error {
	if op, ok := m.ops[hash]; ok {
		op.Status = status
		if extra != nil && extra.RevertReason != nil {
			op.RevertReason = extra.RevertReason
		}
	}
	return nil
}

// Unused interface methods.
func (m *mockRepo) Insert(_ *core.PackedUserOp, _ common.Hash) error                   { return nil }
func (m *mockRepo) Replace(_ common.Hash, _ *core.PackedUserOp, _ common.Hash) error    { return nil }
func (m *mockRepo) GetPending(_ int) ([]*mempool.StoredOp, error)                       { return nil, nil }
func (m *mockRepo) ClaimForBundling(_ []common.Hash) error                              { return nil }
func (m *mockRepo) GetByHash(_ common.Hash) (*mempool.StoredOp, error)                  { return nil, nil }
func (m *mockRepo) GetConfirmedByHash(_ common.Hash) (*mempool.StoredOp, error)         { return nil, nil }
func (m *mockRepo) GetByBundleTx(_ common.Hash) ([]*mempool.StoredOp, error)            { return nil, nil }
func (m *mockRepo) GetByStatus(_ mempool.Status) ([]*mempool.StoredOp, error)           { return nil, nil }
func (m *mockRepo) ResetBundlingToPending() (int, error)                                { return 0, nil }
func (m *mockRepo) MarkTtlExpired(_ int64) (int, error)                                 { return 0, nil }
func (m *mockRepo) PruneHistory(_ int64) (int, error)                                   { return 0, nil }
func (m *mockRepo) Close() error                                                        { return nil }
