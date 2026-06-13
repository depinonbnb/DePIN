package scheduler

import (
	"time"

	"github.com/depinonbnb/depin/internal/types"
)

// runUptimeRewardLoop is the goroutine entry point for the uptime ticker.
func (s *Scheduler) runUptimeRewardLoop() {
	s.tickerLoop("uptime", s.cfg.RewardInterval, s.runUptimeRewardOnce)
}

// runUptimeRewardOnce awards uptime points to every active node that produced
// at least one heartbeat in the last reward window. AwardUptimePoints itself
// is a no-op for inactive/flagged/banned nodes — we still pre-filter here so
// the log counters reflect what we actually decided to reward, not what the
// store happened to skip.
//
// The window is RewardInterval + a small buffer. The buffer absorbs jitter
// between the heartbeat ticker and the uptime ticker — they run on
// independent goroutines and a heartbeat that arrived ~1s into the next
// uptime window should still count.
func (s *Scheduler) runUptimeRewardOnce() {
	nodes := s.store.GetAllActiveNodes()
	if len(nodes) == 0 {
		return
	}

	// Buffer of one full heartbeat interval ensures we don't miss a node
	// whose last heartbeat happened just before the uptime tick fires.
	buffer := s.cfg.HeartbeatInterval
	since := time.Now().Add(-(s.cfg.RewardInterval + buffer)).UnixMilli()

	// time.Duration / time.Duration yields a time.Duration whose value is the
	// integer ratio (in unitless form, not nanoseconds) — exactly the number
	// of whole minutes that fit in RewardInterval.
	minutesOnline := uint64(s.cfg.RewardInterval / time.Minute)
	if minutesOnline == 0 {
		// Sub-minute reward intervals (test mode): credit 1 minute per cycle
		// so AwardUptimePoints sees a non-zero increment.
		minutesOnline = 1
	}

	// First pass: decide which nodes are eligible on uptime grounds (active,
	// not flagged/banned, reported synced in the window). We collect them with
	// their wallets so the token gate can resolve balances in one batch before
	// any points are awarded.
	type candidate struct {
		id     string
		wallet string
	}
	candidates := make([]candidate, 0, len(nodes))
	var skipped int

	for _, node := range nodes {
		// Skip nodes that AwardUptimePoints would skip anyway, so the log
		// counters tell the truth instead of the store's view.
		if node.CheatStatus == types.StatusFlagged || node.CheatStatus == types.StatusBanned {
			skipped++
			continue
		}

		hbs := s.store.GetHeartbeats(node.ID, since)
		// Only a node that reported synced in the window earns uptime points.
		// Being online but out of sync does not accrue rewards.
		synced := false
		for _, hb := range hbs {
			if hb.IsSynced {
				synced = true
				break
			}
		}
		if !synced {
			skipped++
			continue
		}

		candidates = append(candidates, candidate{id: node.ID, wallet: node.WalletAddress})
	}

	// Token gate: batch-resolve holder status for the eligible wallets so the
	// per-node Allow checks below hit a warm cache (one RPC pass, not one per
	// node). No-op when gating is disabled.
	if s.holder.Enabled() && len(candidates) > 0 {
		wallets := make([]string, 0, len(candidates))
		for _, c := range candidates {
			wallets = append(wallets, c.wallet)
		}
		s.holder.Refresh(s.ctx, wallets)
	}

	var rewarded, skippedNoToken int
	for _, c := range candidates {
		// Withhold uptime points from wallets below the minimum token balance.
		// Allow returns true when gating is disabled, so this is a no-op then.
		if !s.holder.Allow(c.wallet) {
			skippedNoToken++
			continue
		}
		s.store.AwardUptimePoints(c.id, minutesOnline)
		rewarded++
	}

	if rewarded == 0 && skipped == 0 && skippedNoToken == 0 {
		return
	}
	s.logger.Info("uptime cycle done",
		"rewarded", rewarded,
		"skipped", skipped,
		"skipped_no_token", skippedNoToken,
	)
}
