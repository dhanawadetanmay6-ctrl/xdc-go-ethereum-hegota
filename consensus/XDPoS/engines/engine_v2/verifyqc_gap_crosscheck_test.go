// Copyright (c) 2026 XDC Network
// Regression tests for the #45 verifyQC gap-number cross-check (utils.go:278-313).
//
// Failure mode reproduced (chain 20118, xone build v-xdc-xone-mint-20260725):
// during bulk sync the verify goroutine runs ahead of InsertChain's DB commit, so
// the parent chain is absent and getEpochSwitchInfo falls back to the synthetic
// path, returning the proposed block's OWN number as EpochSwitchBlockInfo.Number.
// The floor-derived gap (N - N%Epoch - Gap) then differs from the QC's
// round-derived GapNumber and every batch is rejected:
//
//	[verifyQC] Gap number mismatch expected=76950 got=76050
//	[verifyHeader] fail to verify QC QCNumber=77850 QCsigLength=3
//	xdcSyncer: InsertChain failed from=77851 attempted=192 inserted=0
//
// The #45 cross-check accepts the mismatch ONLY when the QC's GapNumber indexes a
// stored snapshot whose active signer set equals the set that just verified the
// QC's signatures (safe: VoteForSign commits to GapNumber; a forged gap selects a
// different/absent snapshot and is still rejected).
//
// Run: go test ./consensus/XDPoS/engines/engine_v2/ -run TestVerifyQC_GapCrosscheck -count=1 -v
package engine_v2

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// gapMockChain is a consensus.ChainReader that serves only the headers placed in
// its byNumber map — everything else is absent, forcing the synthetic
// epoch-switch path in getEpochSwitchInfo (the bulk-sync race shape).
type gapMockChain struct {
	cfg      *params.ChainConfig
	byNumber map[uint64]*types.Header
}

func (m *gapMockChain) Config() *params.ChainConfig                     { return m.cfg }
func (m *gapMockChain) CurrentHeader() *types.Header                    { return nil }
func (m *gapMockChain) GetHeader(h common.Hash, n uint64) *types.Header { return nil }
func (m *gapMockChain) GetHeaderByHash(h common.Hash) *types.Header     { return nil }
func (m *gapMockChain) GetTd(h common.Hash, n uint64) *big.Int          { return nil }
func (m *gapMockChain) GetBlock(h common.Hash, n uint64) *types.Block   { return nil }
func (m *gapMockChain) GetHeaderByNumber(n uint64) *types.Header        { return m.byNumber[n] }

// gapTestEngine mirrors newA975Engine but also returns the ChainConfig so the
// mock chain can serve it (epoch 900 / gap 450 / pure-V2, chainID 20118).
func gapTestEngine() (*XDPoS_v2, *params.ChainConfig) {
	cfg := &params.ChainConfig{
		ChainID: big.NewInt(20118),
		XDPoS: &params.XDPoSConfig{
			Epoch: 900,
			Gap:   450,
			V2: &params.V2{
				SwitchBlock: big.NewInt(0),
				CurrentConfig: &params.V2Config{
					MinePeriod:    2,
					TimeoutPeriod: 10,
					ExpTimeoutConfig: params.ExpTimeoutConfig{
						Base:        2.0,
						MaxExponent: 6,
					},
					CertThreshold: 2.0 / 3.0,
				},
				AllConfigs: map[uint64]*params.V2Config{},
			},
		},
	}
	return createEngine(cfg, nil, nil, nil), cfg
}

// makeV2Header builds a synthetic non-epoch-switch V2 header at (num, round)
// whose embedded QC carries the given gapNumber (used by the synthetic
// epoch-info path's snapshot lookup).
func makeV2Header(t *testing.T, num uint64, round types.Round, gapNumber uint64) *types.Header {
	t.Helper()
	extra := &types.ExtraFields_v2{
		Round: round,
		QuorumCert: &types.QuorumCert{
			ProposedBlockInfo: makeBlockInfo(round-1, num-1, common.HexToHash("0xbeef")),
			Signatures:        []types.Signature{},
			GapNumber:         gapNumber,
		},
	}
	enc, err := extra.EncodeToBytes()
	if err != nil {
		t.Fatalf("EncodeToBytes: %v", err)
	}
	return &types.Header{
		Number:     big.NewInt(int64(num)),
		Extra:      enc,
		Difficulty: big.NewInt(1),
	}
}

// seedGapSnapshot stores a snapshot at gapHeader's hash with the given candidate
// pool, all active (no penalties), version 6 (state-certified).
func seedGapSnapshot(t *testing.T, eng *XDPoS_v2, gapNum uint64, gapHeader *types.Header, pool []common.Address) {
	t.Helper()
	snap := newSnapshot(gapNum, gapHeader.Hash(), pool)
	snap.Version = 6
	snap.ActiveMasternodesLen = len(pool)
	if err := storeSnapshot(snap, eng.db); err != nil {
		t.Fatalf("storeSnapshot: %v", err)
	}
	eng.snapshots.Add(snap.Hash, snap)
}

// TestVerifyQC_GapCrosscheck_AcceptCanonicalGap reproduces the xone mode-1 stall:
// the locally-computed gap (floor formula on the synthetic epoch switch) is wrong,
// the QC's gap is right, and the snapshot at the QC's gap matches the signer set
// that verified the QC. Pre-#45 this rejected; post-#45 it must accept.
func TestVerifyQC_GapCrosscheck_AcceptCanonicalGap(t *testing.T) {
	eng, chainCfg := gapTestEngine()
	eng.db = rawdb.NewMemoryDatabase()

	k1, k2, k3 := genKey(t), genKey(t), genKey(t)
	masternodes := []common.Address{addrOf(k1), addrOf(k2), addrOf(k3)}

	// Gap block 76,050 (a normal non-epoch-switch V2 header; round arbitrary).
	gapHeader := makeV2Header(t, 76050, 75601, 0)
	chain := &gapMockChain{cfg: chainCfg, byNumber: map[uint64]*types.Header{76050: gapHeader}}
	seedGapSnapshot(t, eng, 76050, gapHeader, masternodes)

	// The block being verified's parent = proposed block 77,850 (round 77,400).
	// Its embedded QC points at 77,849 (round 77,409) with the CORRECT round-based gap 76,050.
	parentHeader := makeV2Header(t, 77850, 77410, 76050)

	// QC under test: proposed = 77,850 (round 77,410), gap = 76,050 — the
	// canonical round-based value. Locally, with the parent chain absent, the
	// synthetic path returns epochSwitchNumber=77,850 → floor gap 76,950 ≠ 76,050.
	qc := &types.QuorumCert{
		ProposedBlockInfo: makeBlockInfo(77410, 77850, parentHeader.Hash()),
		GapNumber:         76050,
		Signatures: []types.Signature{
			types.Signature(makeVote(t, k1, 77410, 77850, parentHeader.Hash(), 76050).Signature),
			types.Signature(makeVote(t, k2, 77410, 77850, parentHeader.Hash(), 76050).Signature),
			types.Signature(makeVote(t, k3, 77410, 77850, parentHeader.Hash(), 76050).Signature),
		},
	}

	if err := eng.verifyQC(chain, qc, parentHeader, nil); err != nil {
		t.Fatalf("verifyQC rejected a canonical QC whose gap indexes the verified signer set: %v", err)
	}
	t.Log("PASS: canonical QC accepted via #45 snapshot cross-check despite wrong local floor gap")
}

// TestVerifyQC_GapCrosscheck_RejectForgedGap: same shape but the QC's GapNumber is
// one off — no snapshot exists there, so the cross-check must NOT save it.
func TestVerifyQC_GapCrosscheck_RejectForgedGap(t *testing.T) {
	eng, chainCfg := gapTestEngine()
	eng.db = rawdb.NewMemoryDatabase()

	k1, k2, k3 := genKey(t), genKey(t), genKey(t)
	masternodes := []common.Address{addrOf(k1), addrOf(k2), addrOf(k3)}

	gapHeader := makeV2Header(t, 76050, 75601, 0)
	chain := &gapMockChain{cfg: chainCfg, byNumber: map[uint64]*types.Header{76050: gapHeader}}
	seedGapSnapshot(t, eng, 76050, gapHeader, masternodes)

	parentHeader := makeV2Header(t, 77850, 77410, 76050)

	// Forged gap: 76,049 has no snapshot → cross-check must fail → reject.
	qc := &types.QuorumCert{
		ProposedBlockInfo: makeBlockInfo(77410, 77850, parentHeader.Hash()),
		GapNumber:         76049,
		Signatures: []types.Signature{
			types.Signature(makeVote(t, k1, 77410, 77850, parentHeader.Hash(), 76049).Signature),
			types.Signature(makeVote(t, k2, 77410, 77850, parentHeader.Hash(), 76049).Signature),
			types.Signature(makeVote(t, k3, 77410, 77850, parentHeader.Hash(), 76049).Signature),
		},
	}
	if err := eng.verifyQC(chain, qc, parentHeader, nil); err == nil {
		t.Fatal("verifyQC accepted a QC whose forged gap indexes no snapshot — #45 accept path is too permissive")
	}
	t.Log("PASS: forged gap (no snapshot) still rejected")
}

// TestVerifyQC_GapCrosscheck_RejectWrongSignerSet: the QC's gap is canonical-shaped
// and HAS a snapshot, but the snapshot's signer set differs from the QC's actual
// signers — the cross-check must reject (attacker cannot borrow a foreign gap).
func TestVerifyQC_GapCrosscheck_RejectWrongSignerSet(t *testing.T) {
	eng, chainCfg := gapTestEngine()
	eng.db = rawdb.NewMemoryDatabase()

	k1, k2, k3 := genKey(t), genKey(t), genKey(t)
	// Snapshot holds a DIFFERENT set than the QC signers.
	other := []common.Address{addrOf(genKey(t)), addrOf(genKey(t)), addrOf(genKey(t))}

	gapHeader := makeV2Header(t, 76050, 75601, 0)
	chain := &gapMockChain{cfg: chainCfg, byNumber: map[uint64]*types.Header{76050: gapHeader}}
	seedGapSnapshot(t, eng, 76050, gapHeader, other)

	parentHeader := makeV2Header(t, 77850, 77410, 76050)

	qc := &types.QuorumCert{
		ProposedBlockInfo: makeBlockInfo(77410, 77850, parentHeader.Hash()),
		GapNumber:         76050,
		Signatures: []types.Signature{
			types.Signature(makeVote(t, k1, 77410, 77850, parentHeader.Hash(), 76050).Signature),
			types.Signature(makeVote(t, k2, 77410, 77850, parentHeader.Hash(), 76050).Signature),
			types.Signature(makeVote(t, k3, 77410, 77850, parentHeader.Hash(), 76050).Signature),
		},
	}
	if err := eng.verifyQC(chain, qc, parentHeader, nil); err == nil {
		t.Fatal("verifyQC accepted a QC whose gap snapshot signer set differs from the QC signers")
	}
	t.Log("PASS: gap snapshot with mismatched signer set still rejected")
}
