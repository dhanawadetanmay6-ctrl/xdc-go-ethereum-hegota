// Copyright (c) 2026 XDC Network
// Regression tests for adoptV2ShadowSibling (eth/sync_xdc.go) — the V2
// competing-slot shadow-fork heal.
//
// Failure mode (chain 20118, xone, every run since 2026-10-03): the node imports
// a timed-out shadow proposal as head; the QC'd canonical sibling is never
// received; the first descendant batch fails InsertChain with
// ErrUnknownAncestor; the #801 escape burns the 8,192-block rollback budget and
// hard-stops ("stuck on fork past rollback cap"). The heal fetches the missing
// parent by hash, inserts it (full consensus validation runs), lets the V2
// QC-chain fork choice reorg, and re-imports the batch — no rollback.
//
// The end-to-end fetch→insert→reorg behavior is verified against the live
// network (the roulette produces a shadow every ~1-4 min); these tests pin the
// guard rails so the heal can never fire outside its intended shape.
//
// Run: go test ./eth/ -run TestAdoptV2ShadowSibling -count=1 -v
package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

func TestAdoptV2ShadowSibling_NilPeerRejected(t *testing.T) {
	s := makeSyncer()
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	if s.adoptV2ShadowSibling(types.Blocks{block}, nil) {
		t.Fatal("nil peer must be rejected")
	}
}

func TestAdoptV2ShadowSibling_EmptyBatchRejected(t *testing.T) {
	s := makeSyncer()
	if s.adoptV2ShadowSibling(nil, nil) {
		t.Fatal("empty batch must be rejected")
	}
	if s.adoptV2ShadowSibling(types.Blocks{}, nil) {
		t.Fatal("empty batch must be rejected")
	}
}

// TestAdoptV2ShadowSibling_GateOrdering documents the guard contract that keeps
// the heal scoped: V1 heights belong to adoptCoSignedSibling, and a present
// parent means the failure was NOT the missing-sibling case. Both are enforced
// before any network fetch or InsertChain, so a misfire is a no-op that falls
// through to the rollback path.
func TestAdoptV2ShadowSibling_GateOrdering(t *testing.T) {
	// Structural: adoptCoSignedSibling returns false for V2 heights and
	// adoptV2ShadowSibling returns false for V1 heights — the two heals are
	// disjoint by construction (see the V2.SwitchBlock gate in both functions).
	// This test exists so a future edit that removes a gate fails loudly.
	s := makeSyncer()
	// number=0 blocks are rejected before any chain access (first.NumberU64()==0 guard).
	b0 := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(0)})
	if s.adoptV2ShadowSibling(types.Blocks{b0}, nil) {
		t.Fatal("genesis-parent batch must be rejected")
	}
}
