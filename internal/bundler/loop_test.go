package bundler

import (
	"context"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"go.uber.org/zap"
)

// --- mock signer ---

type mockSigner struct {
	addr      common.Address
	submitted []common.Hash
	failNext  bool
}

func (s *mockSigner) Address() common.Address { return s.addr }
func (s *mockSigner) CheckBalance(ctx context.Context) error {
	return nil
}
func (s *mockSigner) SignAndSubmit(ctx context.Context, to common.Address, data []byte, gasLimit uint64) (common.Hash, error) {
	if s.failNext {
		s.failNext = false
		return common.Hash{}, fmt.Errorf("submit failed")
	}
	h := common.BytesToHash(core.Keccak256(data))
	s.submitted = append(s.submitted, h)
	return h, nil
}

// --- mock client ---

type mockBundleClient struct {
	gasEstimate uint64
	failGas     bool
	receipts    map[common.Hash]*types.Receipt
}

func (c *mockBundleClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	if c.failGas {
		return 0, fmt.Errorf("estimate failed")
	}
	return c.gasEstimate, nil
}
func (c *mockBundleClient) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	if r, ok := c.receipts[txHash]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("not found")
}

// --- helpers ---

func testSQLiteRepo(t *testing.T) *mempool.SQLiteRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	repo, err := mempool.Open(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func insertTestOp(t *testing.T, repo mempool.Repository, nonce int64) (*mempool.StoredOp, common.Hash) {
	t.Helper()
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	target := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	calldata := core.BuildExecuteCalldata(target, big.NewInt(0), []byte{})
	op := &core.PackedUserOp{
		Sender:             sender,
		Nonce:              big.NewInt(nonce),
		InitCode:           nil,
		CallData:           calldata,
		AccountGasLimits:   core.PackUint128s(31000, 50000),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		PaymasterAndData:   nil,
		Signature:          make([]byte, 65),
	}
	hash := core.ComputeUserOpHash(op, common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032"), 1)
	if err := repo.Insert(op, hash); err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.GetByHash(hash)
	return stored, hash
}

func TestTickSubmitsBundle(t *testing.T) {
	repo := testSQLiteRepo(t)
	signer := &mockSigner{addr: common.HexToAddress("0xbbbb")}
	client := &mockBundleClient{gasEstimate: 100_000}
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	loop := NewLoop(repo, signer, client, ep, 10, time.Second, zap.NewNop())

	_, hash := insertTestOp(t, repo, 0)

	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Op should be submitted.
	op, _ := repo.GetByHash(hash)
	if op.Status != mempool.StatusSubmitted {
		t.Errorf("status = %s, want submitted", op.Status)
	}
	if op.BundleTxHash == nil {
		t.Error("bundle_tx_hash should be set")
	}
	if op.BundleIndex == nil || *op.BundleIndex != 0 {
		t.Error("bundle_index should be 0")
	}
	if len(signer.submitted) != 1 {
		t.Errorf("signer submitted %d txs, want 1", len(signer.submitted))
	}
}

func TestTickNoPendingOps(t *testing.T) {
	repo := testSQLiteRepo(t)
	signer := &mockSigner{addr: common.HexToAddress("0xbbbb")}
	client := &mockBundleClient{gasEstimate: 100_000}
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	loop := NewLoop(repo, signer, client, ep, 10, time.Second, zap.NewNop())

	// No ops — should be a no-op.
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(signer.submitted) != 0 {
		t.Error("should not submit when no pending ops")
	}
}

func TestTickMultipleOps(t *testing.T) {
	repo := testSQLiteRepo(t)
	signer := &mockSigner{addr: common.HexToAddress("0xbbbb")}
	client := &mockBundleClient{gasEstimate: 100_000}
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	loop := NewLoop(repo, signer, client, ep, 10, time.Second, zap.NewNop())

	var hashes []common.Hash
	for i := int64(0); i < 3; i++ {
		_, h := insertTestOp(t, repo, i)
		hashes = append(hashes, h)
		time.Sleep(time.Millisecond)
	}

	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// All 3 ops should be submitted with correct bundle indices.
	for i, h := range hashes {
		op, _ := repo.GetByHash(h)
		if op.Status != mempool.StatusSubmitted {
			t.Errorf("op[%d] status = %s, want submitted", i, op.Status)
		}
		if op.BundleIndex == nil || *op.BundleIndex != i {
			t.Errorf("op[%d] bundle_index = %v, want %d", i, op.BundleIndex, i)
		}
	}
}

func TestTickEstimateGasFailResetsOps(t *testing.T) {
	repo := testSQLiteRepo(t)
	signer := &mockSigner{addr: common.HexToAddress("0xbbbb")}
	client := &mockBundleClient{failGas: true}
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	loop := NewLoop(repo, signer, client, ep, 10, time.Second, zap.NewNop())

	_, hash := insertTestOp(t, repo, 0)

	err := loop.Tick(context.Background())
	if err == nil {
		t.Error("expected error from estimate gas failure")
	}

	// Fix R5: op should be reset to pending, not stuck in bundling.
	op, _ := repo.GetByHash(hash)
	if op.Status != mempool.StatusPending {
		t.Errorf("status = %s, want pending (R5 fix)", op.Status)
	}
}

func TestTickSubmitFailResetsOps(t *testing.T) {
	repo := testSQLiteRepo(t)
	signer := &mockSigner{addr: common.HexToAddress("0xbbbb"), failNext: true}
	client := &mockBundleClient{gasEstimate: 100_000}
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	loop := NewLoop(repo, signer, client, ep, 10, time.Second, zap.NewNop())

	_, hash := insertTestOp(t, repo, 0)

	err := loop.Tick(context.Background())
	if err == nil {
		t.Error("expected error from submit failure")
	}

	// Fix R5: op should be reset to pending, not stuck in bundling.
	op, _ := repo.GetByHash(hash)
	if op.Status != mempool.StatusPending {
		t.Errorf("status = %s, want pending (R5 fix)", op.Status)
	}
}

func TestTickRespectsMaxBundleSize(t *testing.T) {
	repo := testSQLiteRepo(t)
	signer := &mockSigner{addr: common.HexToAddress("0xbbbb")}
	client := &mockBundleClient{gasEstimate: 100_000}
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	maxBundle := 2
	loop := NewLoop(repo, signer, client, ep, maxBundle, time.Second, zap.NewNop())

	for i := int64(0); i < 5; i++ {
		insertTestOp(t, repo, i)
		time.Sleep(time.Millisecond)
	}

	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Only 2 should be submitted, 3 still pending.
	submitted, _ := repo.GetByStatus(mempool.StatusSubmitted)
	pending, _ := repo.GetPending(10)
	if len(submitted) != 2 {
		t.Errorf("submitted = %d, want 2", len(submitted))
	}
	if len(pending) != 3 {
		t.Errorf("pending = %d, want 3", len(pending))
	}
}

func TestBuildHandleOpsCalldata(t *testing.T) {
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	target := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	calldata := core.BuildExecuteCalldata(target, big.NewInt(0), []byte{})
	op := &mempool.StoredOp{}
	op.Sender = sender
	op.Nonce = big.NewInt(0)
	op.CallData = calldata
	op.AccountGasLimits = core.PackUint128s(31000, 50000)
	op.PreVerificationGas = big.NewInt(50000)
	op.GasFees = core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000))
	op.Signature = make([]byte, 65)

	beneficiary := common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	result := BuildHandleOpsCalldata([]*mempool.StoredOp{op}, beneficiary)

	// Should start with handleOps selector.
	if len(result) < 4 {
		t.Fatal("calldata too short")
	}
	for i := 0; i < 4; i++ {
		if result[i] != handleOpsSelector[i] {
			t.Errorf("selector byte %d: %x, want %x", i, result[i], handleOpsSelector[i])
		}
	}

	// Should be substantial (selector + offsets + array + tuple).
	if len(result) < 200 {
		t.Errorf("calldata suspiciously short: %d bytes", len(result))
	}
}
