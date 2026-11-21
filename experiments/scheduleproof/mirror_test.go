package scheduleproof

import (
	"encoding/json"
	"fmt"
	log15 "github.com/inconshreveable/log15"
	"github.com/zenon-network/go-zenon/consensus"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/wallet"
	"math/big"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/chain/genesis"
	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

func newReplica(t *testing.T) *Mirror {
	t.Helper()
	m, err := NewMirror(t.TempDir(), genesis.NewGenesis(g.EmbeddedGenesis))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
func source(t *testing.T) mock.MockZenon {
	t.Helper()
	oldClock := common.Clock
	z := mock.NewMockZenon(t)
	for _, logger := range []common.Logger{common.RPCLogger, common.ZenonLogger, common.EmbeddedLogger} {
		logger.SetHandler(log15.DiscardHandler())
	}
	t.Cleanup(func() { z.StopPanic(); common.Clock = oldClock })
	return z
}
func replay(t *testing.T, z mock.MockZenon, m *Mirror) {
	t.Helper()
	ledger := api.NewLedgerApi(z)
	target := z.Chain().GetFrontierMomentumStore().Identifier().Height
	for m.Frontier().Height < target {
		count := target - m.Frontier().Height
		if count > 100 {
			count = 100
		}
		response, err := ledger.GetDetailedMomentumsByHeight(m.Frontier().Height+1, count)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.List) == 0 {
			t.Fatal("empty replay batch")
		}
		for _, item := range response.List {
			raw, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ApplyJSON(raw); err != nil {
				t.Fatalf("replay height %d: %v", item.Momentum.Height, err)
			}
		}
	}
}
func checkRound(t *testing.T, z mock.MockZenon, m *Mirror, round uint64) *Schedule {
	t.Helper()
	result, err := m.ScheduleAt(round)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Slots) != 30 {
		t.Fatalf("slots: %d", len(result.Slots))
	}
	expectedState := z.Chain().GetMomentumStore(result.Proof)
	expectedInputs, err := expectedState.ComputePillarDelegations()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Inputs, types.ToPillarDelegation(expectedInputs)) {
		t.Fatal("historical election inputs differ")
	}
	for _, slot := range result.Slots {
		expected, err := z.Consensus().GetMomentumProducer(slot.StartsAt)
		if err != nil {
			t.Fatal(err)
		}
		if expected == nil || *expected != slot.Producer {
			t.Fatalf("slot %s: expected %v, got %v", slot.StartsAt, expected, slot.Producer)
		}
	}
	return result
}
func settle(t *testing.T, z mock.MockZenon, block *nom.AccountBlock, want error) {
	t.Helper()
	result := z.CallContract(block)
	for i := 0; i < 4; i++ {
		z.InsertNewMomentum()
	}
	result.Error(t, want)
}
func call(address types.Address, method string, args ...interface{}) *nom.AccountBlock {
	return &nom.AccountBlock{Address: address, ToAddress: types.PillarContract, Data: definition.ABIPillars.PackMethodPanic(method, args...), TokenStandard: types.ZnnTokenStandard, Amount: big.NewInt(0)}
}

func TestHistoricalInputsAcrossTransitions(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*testing.T, mock.MockZenon)
		changed bool
	}{
		{"producer_rotation", func(t *testing.T, z mock.MockZenon) {
			settle(t, z, call(g.Pillar1.Address, definition.UpdatePillarMethodName, g.Pillar1Name, g.Pillar4.Address, g.Pillar1.Address, uint8(0), uint8(100)), nil)
		}, true},
		{"delegation_ranking", func(t *testing.T, z mock.MockZenon) {
			settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
			settle(t, z, call(g.User2.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
		}, true},
		{"undelegation", func(t *testing.T, z mock.MockZenon) {
			settle(t, z, call(g.User1.Address, definition.UndelegateMethodName), nil)
		}, true},
		{"balance_send_receive", func(t *testing.T, z mock.MockZenon) {
			send := z.InsertSendBlock(&nom.AccountBlock{Address: g.User1.Address, ToAddress: g.User3.Address, TokenStandard: types.ZnnTokenStandard, Amount: big.NewInt(100 * g.Zexp)}, nil, mock.SkipVmChanges)
			z.InsertNewMomentum()
			z.InsertReceiveBlock(send.Header(), nil, nil, mock.SkipVmChanges)
			z.InsertNewMomentum()
		}, true},
		{"registration", func(t *testing.T, z mock.MockZenon) {
			deposit := call(g.Pillar4.Address, definition.DepositQsrMethodName)
			deposit.TokenStandard = types.QsrTokenStandard
			deposit.Amount = big.NewInt(150000 * g.Zexp)
			settle(t, z, deposit, nil)
			register := call(g.Pillar4.Address, definition.RegisterMethodName, g.Pillar4Name, g.Pillar4.Address, g.Pillar4.Address, uint8(0), uint8(100))
			register.Amount = new(big.Int).Set(constants.PillarStakeAmount)
			settle(t, z, register, nil)
		}, true},
		{"revocation", func(t *testing.T, z mock.MockZenon) {
			// Shorten the test network's lock interval; keep it identical for source and replica.
			oldLock, oldRevoke := constants.PillarEpochLockTime, constants.PillarEpochRevokeTime
			constants.PillarEpochLockTime, constants.PillarEpochRevokeTime = 1, 100000
			t.Cleanup(func() { constants.PillarEpochLockTime, constants.PillarEpochRevokeTime = oldLock, oldRevoke })
			settle(t, z, call(g.Pillar1.Address, definition.RevokeMethodName, g.Pillar1Name), nil)
		}, true},
		{"failed_registration", func(t *testing.T, z mock.MockZenon) {
			register := call(g.Pillar4.Address, definition.RegisterMethodName, g.Pillar4Name, g.Pillar4.Address, g.Pillar4.Address, uint8(0), uint8(100))
			register.Amount = new(big.Int).Set(constants.PillarStakeAmount)
			settle(t, z, register, constants.ErrNotEnoughDepositedQsr)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			z := source(t)
			z.InsertMomentumsTo(45)
			m := newReplica(t)
			replay(t, z, m)
			before := checkRound(t, z, m, 2)
			tc.change(t, z)
			replay(t, z, m)
			after := checkRound(t, z, m, 2)
			if !reflect.DeepEqual(before.Slots, after.Slots) || !reflect.DeepEqual(before.Inputs, after.Inputs) || before.Proof != after.Proof {
				t.Fatal("later state changed an established election")
			}
			z.InsertMomentumsTo(61)
			replay(t, z, m)
			next := checkRound(t, z, m, 3)
			same := reflect.DeepEqual(before.Inputs, next.Inputs)
			if same == tc.changed {
				t.Fatalf("unexpected transition input result: same=%v", same)
			}
			if next.Proof.Height != 60 {
				t.Fatalf("exclusive cutoff: expected proof 60, got %d", next.Proof.Height)
			}
			if _, err := m.ScheduleAt(4); err == nil {
				t.Fatal("served uncovered future round")
			}
			t.Logf("90 slots and exact proof-state inputs match validator; before proof=%d after proof=%d", before.Proof.Height, next.Proof.Height)
		})
	}
}

func TestProofReplacement(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(45)
	m := newReplica(t)
	replay(t, z, m)
	anchor := m.Frontier()
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	z.InsertMomentumsTo(61)
	replay(t, z, m)
	branchA := checkRound(t, z, m, 3)
	lock := z.Chain().AcquireInsert("test alternative branch")
	err := z.Chain().RollbackTo(lock, anchor)
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Rewind(anchor); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ScheduleAt(3); err == nil {
		t.Fatal("served old branch beyond coverage")
	}
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar3Name), nil)
	z.InsertMomentumsTo(61)
	replay(t, z, m)
	branchB := checkRound(t, z, m, 3)
	if branchA.Proof.Hash == branchB.Proof.Hash || branchA.Proof.Height != branchB.Proof.Height {
		t.Fatal("test did not replace proof at equal height")
	}
	if reflect.DeepEqual(branchA.Inputs, branchB.Inputs) {
		t.Fatal("reused old branch inputs")
	}
	t.Log("equal-height proof replacement rebuilt inputs and matched all 30 replacement slots")
}

func TestRejectsCorruptOrIncompleteHistory(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(4)
	list, err := api.NewLedgerApi(z).GetDetailedMomentumsByHeight(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		index  int
		mutate func(map[string]interface{})
	}{
		{"missing_height", 2, nil},
		{"wrong_chain", 1, func(v map[string]interface{}) { v["momentum"].(map[string]interface{})["chainIdentifier"] = 999 }},
		{"invalid_signature", 1, func(v map[string]interface{}) { v["momentum"].(map[string]interface{})["signature"] = "AA==" }},
		{"invalid_state_hash", 1, func(v map[string]interface{}) {
			v["momentum"].(map[string]interface{})["changesHash"] = types.ZeroHash.String()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newReplica(t)
			good, err := json.Marshal(list.List[0])
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ApplyJSON(good); err != nil {
				t.Fatal(err)
			}
			if _, err = m.ScheduleAt(0); err != nil {
				t.Fatalf("schedule must be usable before corruption: %v", err)
			}
			raw, _ := json.Marshal(list.List[tc.index])
			if tc.mutate != nil {
				var v map[string]interface{}
				json.Unmarshal(raw, &v)
				tc.mutate(v)
				raw, _ = json.Marshal(v)
			}
			if err := m.ApplyJSON(raw); err == nil {
				t.Fatal("accepted invalid history")
			}
			if _, err := m.ScheduleAt(0); err == nil {
				t.Fatal("served schedule after failed replay")
			}
		})
	}
}

func TestCurrentPillarRPCIsNotProofState(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(45)
	m := newReplica(t)
	replay(t, z, m)
	old := checkRound(t, z, m, 2)
	settle(t, z, call(g.Pillar1.Address, definition.UpdatePillarMethodName, g.Pillar1Name, g.Pillar4.Address, g.Pillar1.Address, uint8(0), uint8(100)), nil)
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	settle(t, z, call(g.User2.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	z.InsertMomentumsTo(61)
	replay(t, z, m)
	future := checkRound(t, z, m, 3)
	// testing=true refreshes the RPC cache synchronously. Even this fresh cache
	// reports prior-period weights, combined with the current registry.
	pillars, err := embedded.NewPillarApi(z, true).GetAll(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	rpcInputs := make([]*types.PillarDelegation, 0, len(pillars.List))
	for _, p := range pillars.List {
		rpcInputs = append(rpcInputs, &types.PillarDelegation{Name: p.Name, Producing: p.BlockProducingAddress, Weight: new(big.Int).Set(p.Weight)})
	}
	ctx := consensus.NewConsensusContext(*z.Chain().GetGenesisMomentum().Timestamp)
	for _, s := range []*Schedule{old, future} {
		projection := consensus.NewElectionAlgorithm(ctx).SelectProducers(consensus.NewAlgorithmContext(append([]*types.PillarDelegation(nil), rpcInputs...), &s.Proof))
		mismatches := 0
		for i, p := range projection {
			if p.Producing != s.Slots[i].Producer {
				mismatches++
			}
		}
		if mismatches == 0 {
			t.Fatal("fixture did not expose mixed/current input error")
		}
		t.Logf("round %d: fresh pillar RPC projection differs in %d/30 slots; replay matches 30/30", s.Round, mismatches)
	}
}

func TestMoreThanThirtyCandidates(t *testing.T) {
	oldGenesis, oldKeys := g.EmbeddedGenesis, g.PillarKeys
	data, err := json.Marshal(oldGenesis)
	if err != nil {
		t.Fatal(err)
	}
	var cfg genesis.GenesisConfig
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	g.EmbeddedGenesis = &cfg
	g.PillarKeys = append([]*wallet.KeyPair(nil), oldKeys...)
	t.Cleanup(func() { g.EmbeddedGenesis = oldGenesis; g.PillarKeys = oldKeys })
	for i := 0; i < 32; i++ {
		key, err := wallet.DeriveWithIndex(uint32(i), []byte("public-schedule-validation-fixture"))
		if err != nil {
			t.Fatal(err)
		}
		g.PillarKeys = append(g.PillarKeys, key)
		cfg.PillarConfig.Pillars = append(cfg.PillarConfig.Pillars, &definition.PillarInfo{Name: fmt.Sprintf("candidate-%02d", i), BlockProducingAddress: key.Address, StakeAddress: key.Address, RewardWithdrawAddress: key.Address, Amount: new(big.Int).Set(constants.PillarStakeAmount), RegistrationTime: cfg.GenesisTimestampSec, PillarType: definition.LegacyPillarType})
	}
	z := source(t)
	z.InsertMomentumsTo(45)
	m := newReplica(t)
	replay(t, z, m)
	old := checkRound(t, z, m, 2)
	if len(old.Inputs) != 35 {
		t.Fatal("expected 35 candidates")
	}
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, "candidate-31"), nil)
	settle(t, z, call(g.User2.Address, definition.DelegateMethodName, "candidate-31"), nil)
	z.InsertMomentumsTo(61)
	replay(t, z, m)
	next := checkRound(t, z, m, 3)
	if next.Inputs[0].Name != "candidate-31" {
		t.Fatal("candidate failed to cross ranking boundary")
	}
	t.Log("35 candidates: top-30 selection, tied weights, random selection, and rank crossing match all slots")
}

func TestRPCWeightCacheRefresh(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(45)
	cache := embedded.NewConsensusCache(z, false)
	waitWeights := func(changed bool, original string) string {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			weights, _ := cache.Get()
			if weights != nil {
				value := weights[g.Pillar1Name].String()
				if !changed || value != original {
					return value
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("cache did not finish refresh")
		return ""
	}
	original := waitWeights(false, "")
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	settle(t, z, call(g.User2.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	z.InsertMomentumsTo(61)
	weights, _ := cache.Get()
	if weights[g.Pillar1Name].String() != original {
		t.Fatal("cache unexpectedly refreshed inside five-minute window")
	}
	z.InsertMomentumsTo(121)
	refreshed := waitWeights(true, original)
	t.Logf("cached pillar weight stayed %s across transition, then refreshed to %s", original, refreshed)
}

func TestMainnetRPCFixture(t *testing.T) {
	path := os.Getenv("SCHEDULEPROOF_MAINNET_FIXTURE")
	if path == "" {
		path = "testdata/mainnet-2-61.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Recent struct {
			List []json.RawMessage `json:"list"`
		} `json:"recent"`
	}
	if err = json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	genesis, err := genesis.MakeEmbeddedGenesisConfig()
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMirror(t.TempDir(), genesis)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, item := range captured.Recent.List {
		if err = m.ApplyJSON(item); err != nil {
			t.Fatalf("height %d: %v", m.Frontier().Height+1, err)
		}
	}
	result, err := m.ScheduleAt(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Slots) != 30 {
		t.Fatal("incomplete round")
	}
	checked := 0
	for _, slot := range result.Slots {
		for _, item := range captured.Recent.List {
			var entry api.DetailedMomentum
			json.Unmarshal(item, &entry)
			if entry.Momentum.TimestampUnix == uint64(slot.StartsAt.Unix()) {
				if entry.Momentum.Momentum.Producer() != slot.Producer {
					t.Fatal("mainnet producer differs")
				}
				checked++
			}
		}
	}
	if checked == 0 || (path == "testdata/mainnet-2-61.json" && checked != 13) {
		t.Fatalf("insufficient observed producer comparisons: %d", checked)
	}
	t.Logf("verified %d mainnet momentums from pinned genesis; reconstructed %d election inputs; %d observed slot matches", len(captured.Recent.List), len(result.Inputs), checked)
}

func TestTransitionAtExclusiveProofCutoff(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(59)
	m := newReplica(t)
	replay(t, z, m)
	before := checkRound(t, z, m, 2)
	// The send is confirmed at 60, but its successful embedded receive at 61
	// has exactly the round-3 proof cutoff timestamp. Round 3 must exclude it.
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	replay(t, z, m)
	atCutoff := checkRound(t, z, m, 3)
	if atCutoff.Proof.Height != 60 || !reflect.DeepEqual(before.Inputs, atCutoff.Inputs) {
		t.Fatal("included transition exactly at exclusive cutoff")
	}
	z.InsertMomentumsTo(91)
	replay(t, z, m)
	later := checkRound(t, z, m, 4)
	if reflect.DeepEqual(atCutoff.Inputs, later.Inputs) {
		t.Fatal("transition never entered later proof state")
	}
	t.Log("send confirmation alone did not change delegation; receive at cutoff affects the following election")
}

func TestMissingConfirmedAccountBlockFailsClosed(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(45)
	m := newReplica(t)
	replay(t, z, m)
	settle(t, z, call(g.User1.Address, definition.DelegateMethodName, g.Pillar2Name), nil)
	items, err := api.NewLedgerApi(z).GetDetailedMomentumsByHeight(46, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.List[0].AccountBlocks) == 0 {
		t.Fatal("fixture has no confirmed blocks")
	}
	items.List[0].AccountBlocks = nil
	raw, err := json.Marshal(items.List[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ApplyJSON(raw); err == nil {
		t.Fatal("accepted missing confirmed block")
	}
	if _, err = m.ScheduleAt(2); err == nil {
		t.Fatal("served schedule after partial response")
	}
}

func TestRestartReusesVerifiedPrivateHistory(t *testing.T) {
	z := source(t)
	z.InsertMomentumsTo(61)
	path := t.TempDir()
	m, err := NewMirror(path, genesis.NewGenesis(g.EmbeddedGenesis))
	if err != nil {
		t.Fatal(err)
	}
	replay(t, z, m)
	before := checkRound(t, z, m, 3)
	m.Close()
	m, err = NewMirror(path, genesis.NewGenesis(g.EmbeddedGenesis))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	after := checkRound(t, z, m, 3)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed schedule or historical inputs")
	}
}
