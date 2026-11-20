package scheduleproof

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
)

type Status struct {
	State       string           `json:"state"`
	Engine      string           `json:"engine"`
	Source      string           `json:"source,omitempty"`
	GeneratedAt time.Time        `json:"generatedAt"`
	ValidUntil  time.Time        `json:"validUntil"`
	Frontier    types.HashHeight `json:"frontier"`
	Remote      types.HashHeight `json:"remote"`
	Error       string           `json:"error,omitempty"`
	Schedules   []*Schedule      `json:"schedules,omitempty"`
	Applied     uint64           `json:"appliedThisRun"`
	Compared    uint64           `json:"preApplySchedulesCompared"`
	Matched     uint64           `json:"preApplySchedulesMatched"`
	Forks       uint64           `json:"forksThisRun"`
	Rewound     uint64           `json:"rewoundMomentums"`
}
type Syncer struct {
	Replica        *Replica
	Source         Source
	Batch          uint64
	MaxReorg       uint64
	MaxAge         time.Duration
	Now            func() time.Time
	Status         Status
	genesisChecked bool
}

func NewSyncer(replica *Replica, source Source) *Syncer {
	return &Syncer{Replica: replica, Source: source, Batch: 50, MaxReorg: 120, MaxAge: 45 * time.Second, Now: time.Now, Status: Status{State: "starting", Engine: EngineVersion, Source: source.Name()}}
}
func (s *Syncer) save(state string, err error) error {
	s.Status.State = state
	s.Status.GeneratedAt = s.Now().UTC()
	s.Status.Error = ""
	if err != nil {
		s.Status.Error = err.Error()
	}
	if state != "ready" {
		s.Status.Schedules = nil
		s.Status.ValidUntil = time.Time{}
	}
	if s.Replica.Mirror != nil {
		s.Status.Frontier = s.Replica.Mirror.Frontier()
	}
	if writeErr := s.Replica.SaveStatus(s.Status); writeErr != nil {
		return fmt.Errorf("cannot save status: %w", writeErr)
	}
	return err
}
func (s *Syncer) Stop() error { return s.save("stopped", nil) }
func (s *Syncer) validHeader(m *nom.Momentum) bool {
	return m != nil && m.Height > 0 && m.ChainIdentifier == s.Replica.genesis.ChainIdentifier() && m.ComputeHash() == m.Hash
}
func (s *Syncer) Step(ctx context.Context) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("sync failed: %v", v)
		}
		if err != nil {
			err = s.save("unavailable", err)
		}
	}()
	if s.Batch == 0 || s.Batch > 100 || s.MaxReorg > 10000 || s.MaxAge <= 0 {
		return fmt.Errorf("invalid sync limits")
	}
	if err = s.save("checking", nil); err != nil {
		return err
	}
	if s.Replica.Mirror == nil || s.Replica.Mirror.failed {
		if err = s.Replica.Recover(); err != nil {
			return err
		}
	}
	mirror := s.Replica.Mirror
	if !s.genesisChecked {
		genesis, e := s.Source.Header(ctx, 1)
		if e != nil {
			return e
		}
		if !s.validHeader(genesis) || genesis.Identifier() != s.Replica.genesis.GetGenesisMomentum().Identifier() {
			return fmt.Errorf("source genesis mismatch")
		}
		s.genesisChecked = true
	}
	remote, err := s.Source.Frontier(ctx)
	if err != nil {
		return err
	}
	if !s.validHeader(remote) {
		return fmt.Errorf("invalid source frontier")
	}
	s.Status.Remote = remote.Identifier()
	tip := mirror.Frontier()
	if remote.Height < tip.Height {
		return s.save("source-behind", nil)
	}
	common, err := s.Source.Header(ctx, tip.Height)
	if err != nil {
		return err
	}
	if !s.validHeader(common) {
		return fmt.Errorf("invalid source header")
	}
	if common.Hash != tip.Hash {
		anchor := types.ZeroHashHeight
		for depth := uint64(1); depth <= s.MaxReorg && depth < tip.Height; depth++ {
			h := tip.Height - depth
			theirs, e := s.Source.Header(ctx, h)
			if e != nil {
				return e
			}
			if !s.validHeader(theirs) {
				return fmt.Errorf("invalid fork header")
			}
			ours, e := mirror.chain.GetFrontierMomentumStore().GetMomentumByHeight(h)
			if e != nil {
				return e
			}
			if ours != nil && ours.Hash == theirs.Hash {
				anchor = ours.Identifier()
				break
			}
		}
		if anchor.IsZero() {
			return fmt.Errorf("no common ancestor within reorg limit")
		}
		confirm, e := s.Source.Header(ctx, anchor.Height)
		if e != nil {
			return e
		}
		if !s.validHeader(confirm) || confirm.Identifier() != anchor {
			return fmt.Errorf("source changed during ancestor search")
		}
		if err = s.save("reorganizing", nil); err != nil {
			return err
		}
		if err = mirror.Rewind(anchor); err != nil {
			return err
		}
		s.Status.Forks++
		s.Status.Rewound += tip.Height - anchor.Height
		// Drop all pool/cache references to the removed branch before replay.
		if err = s.Replica.Recover(); err != nil {
			return err
		}
		mirror = s.Replica.Mirror
		tip = mirror.Frontier()
	}
	count := remote.Height - tip.Height
	if count > s.Batch {
		count = s.Batch
	}
	if count > 0 {
		raw, err := s.Source.Details(ctx, tip.Height+1, count)
		if err != nil {
			return err
		}
		if uint64(len(raw)) != count {
			return fmt.Errorf("incomplete detailed momentum batch")
		}
		for _, item := range raw {
			if err = ctx.Err(); err != nil {
				return err
			}
			compared, match := s.compareBeforeApply(item)
			if compared && !match {
				return fmt.Errorf("pre-apply schedule differs from supplied producer")
			}
			if err = mirror.ApplyJSON(item); err != nil {
				return fmt.Errorf("replay at height %d: %w", mirror.Frontier().Height+1, err)
			}
			s.Status.Applied++
			if compared {
				s.Status.Compared++
				s.Status.Matched++
			}
		}
	}
	tip = mirror.Frontier()
	confirm, err := s.Source.Header(ctx, tip.Height)
	if err != nil {
		return err
	}
	if !s.validHeader(confirm) || confirm.Identifier() != tip {
		return fmt.Errorf("source changed during replay")
	}
	latest, err := s.Source.Frontier(ctx)
	if err != nil {
		return err
	}
	if !s.validHeader(latest) {
		return fmt.Errorf("invalid final frontier")
	}
	s.Status.Remote = latest.Identifier()
	if latest.Identifier() != tip {
		return s.save("catching-up", nil)
	}
	now := s.Now()
	if now.Sub(*latest.Timestamp) > s.MaxAge || latest.Timestamp.After(now.Add(10*time.Second)) {
		return s.save("stale", nil)
	}
	genesisTime := s.Replica.genesis.GetGenesisMomentum().Timestamp.Unix()
	if now.Unix() < genesisTime {
		return fmt.Errorf("clock before genesis")
	}
	round := uint64((now.Unix() - genesisTime) / 300)
	current, err := mirror.ScheduleAt(round)
	if err != nil {
		return s.save("waiting-for-proof", nil)
	}
	s.Status.Schedules = []*Schedule{current}
	if next, e := mirror.ScheduleAt(round + 1); e == nil {
		s.Status.Schedules = append(s.Status.Schedules, next)
	}
	s.Status.ValidUntil = now.Add(15 * time.Second).UTC()
	freshUntil := latest.Timestamp.Add(s.MaxAge)
	if freshUntil.Before(s.Status.ValidUntil) {
		s.Status.ValidUntil = freshUntil.UTC()
	}
	return s.save("ready", nil)
}
func (s *Syncer) compareBeforeApply(raw json.RawMessage) (bool, bool) {
	var item api.DetailedMomentum
	if json.Unmarshal(raw, &item) != nil || item.Momentum == nil || item.Momentum.Momentum == nil {
		return false, false
	}
	m := item.Momentum.Momentum
	m.EnsureCache()
	genesis := s.Replica.genesis.GetGenesisMomentum().Timestamp.Unix()
	if m.Timestamp.Unix() < genesis {
		return false, false
	}
	round := uint64((m.Timestamp.Unix() - genesis) / 300)
	schedule, err := s.Replica.Mirror.ScheduleAt(round)
	if err != nil {
		return false, false
	}
	for _, slot := range schedule.Slots {
		if slot.StartsAt.Equal(*m.Timestamp) {
			return true, slot.Producer == m.Producer()
		}
	}
	return true, false
}
