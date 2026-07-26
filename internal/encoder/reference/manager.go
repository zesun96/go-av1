// Package reference manages encoder-side AV1 reference slots.
package reference

// InterPlan describes the seven reference-type mappings and the one slot that
// will be refreshed after the current inter frame is decoded.
type InterPlan struct {
	RefIdx       [7]uint8
	RefreshFlags uint8
	TargetSlot   int
}

// Manager rotates decoded pictures through AV1's eight reference slots while
// retaining newest-to-oldest history for mode decision.
type Manager struct {
	latest  int
	next    int
	history []int
	ready   bool
}

// ResetKey records the key frame that refreshes all eight slots.
func (m *Manager) ResetKey() {
	m.latest = 0
	m.next = 1
	m.history = append(m.history[:0], 0)
	m.ready = true
}

// PlanInter maps LAST to the newest slot, LAST2 to the preceding distinct
// slot when available, and older reference types to progressively older slots.
func (m *Manager) PlanInter() InterPlan {
	if !m.ready {
		m.ResetKey()
	}
	plan := InterPlan{
		RefreshFlags: 1 << uint(m.next),
		TargetSlot:   m.next,
	}
	for i := range plan.RefIdx {
		historyIndex := i
		if historyIndex >= len(m.history) {
			historyIndex = len(m.history) - 1
		}
		plan.RefIdx[i] = uint8(m.history[historyIndex])
	}
	return plan
}

// CommitInter advances the slot history after an inter frame has been encoded.
func (m *Manager) CommitInter(targetSlot int) {
	if targetSlot < 0 || targetSlot >= 8 {
		return
	}
	history := make([]int, 0, 8)
	history = append(history, targetSlot)
	for _, slot := range m.history {
		if slot != targetSlot && len(history) < 8 {
			history = append(history, slot)
		}
	}
	m.history = history
	m.latest = targetSlot
	m.next = (targetSlot + 1) & 7
	m.ready = true
}

// Latest returns the slot containing the most recently reconstructed frame.
func (m *Manager) Latest() int {
	if !m.ready {
		return 0
	}
	return m.latest
}

// History returns a copy of the newest-to-oldest distinct slot list.
func (m *Manager) History() []int {
	return append([]int(nil), m.history...)
}
