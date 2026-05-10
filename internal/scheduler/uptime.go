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

	var rewarded, skipped int

	for _, node := range nodes {
		// Skip nodes that AwardUptimePoints would skip anyway, so the log
		// counters tell the truth instead of the store's view.
		if node.CheatStatus == types.StatusFlagged || node.CheatStatus == types.StatusBanned {
			skipped++
			continue
		}

		hbs := s.store.GetHeartbeats(node.ID, since)
		if len(hbs) == 0 {
			skipped++
			continue
		}

		s.store.AwardUptimePoints(node.ID, minutesOnline)
		rewarded++
	}

	if rewarded == 0 && skipped == 0 {
		return
	}
	s.logger.Info("uptime cycle done",
		"rewarded", rewarded,
		"skipped", skipped,
	)
}
