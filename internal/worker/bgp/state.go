package bgp

import (
	"sort"
	"sync"
	"time"
)

// PrefixState tracks the most recently seen origin AS for each prefix, used
// to detect origin-change events (hijack heuristic) and to decide whether a
// withdrawal refers to a prefix we knew about. Bounded in memory: when the
// size exceeds maxEntries, the oldest entries (by lastSeen) are pruned down
// to pruneTarget.
type PrefixState struct {
	mu          sync.Mutex
	entries     map[string]prefixEntry
	maxEntries  int
	pruneTarget int
}

type prefixEntry struct {
	originAS uint32
	lastSeen time.Time
}

func NewPrefixState(maxEntries int) *PrefixState {
	if maxEntries <= 0 {
		maxEntries = 500_000
	}
	return &PrefixState{
		entries:     make(map[string]prefixEntry, maxEntries/2),
		maxEntries:  maxEntries,
		pruneTarget: maxEntries * 3 / 4,
	}
}

// Observe records that we just saw `origin` announce `prefix`, and returns
// the previous origin AS if one was stored (0 if this is a first sighting).
func (s *PrefixState) Observe(prefix string, origin uint32) (prev uint32, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[prefix]; ok {
		prev, known = e.originAS, true
	}
	s.entries[prefix] = prefixEntry{originAS: origin, lastSeen: time.Now()}

	if len(s.entries) > s.maxEntries {
		s.pruneLocked()
	}
	return prev, known
}

// Known reports whether `prefix` has been seen recently.
func (s *PrefixState) Known(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[prefix]
	return ok
}

// Forget removes a prefix from the state — called on withdrawal so a later
// re-announcement doesn't look like a hijack against a stale entry.
func (s *PrefixState) Forget(prefix string) {
	s.mu.Lock()
	delete(s.entries, prefix)
	s.mu.Unlock()
}

// pruneLocked drops the oldest entries until len(entries) <= pruneTarget.
// Called under lock. Pruning is rare (amortized once per maxEntries/4
// observations), so the O(n log n) sort is fine.
func (s *PrefixState) pruneLocked() {
	type kv struct {
		key string
		ts  time.Time
	}
	all := make([]kv, 0, len(s.entries))
	for k, v := range s.entries {
		all = append(all, kv{k, v.lastSeen})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ts.Before(all[j].ts) })
	drop := len(all) - s.pruneTarget
	for i := 0; i < drop; i++ {
		delete(s.entries, all[i].key)
	}
}
