// SPDX-License-Identifier: AGPL-3.0-only
package agent

import "log"

// noteBlocker records why a still-pending backup operation is not progressing, in the journal
// entry's Error field. The report sends it as the queued outcome's error, so the Controller task
// shows the reason. An empty reason clears it. Settled entries are left alone.
func noteBlocker(s *Store, id, reason string) {
	entry, ok := s.State.Journal[id]
	if !ok || entry.Status != "pending" || entry.Error == reason {
		return
	}
	entry.Error = reason
	s.State.Journal[id] = entry
	if err := s.Save(); err != nil {
		log.Printf("record backup blocker for %s: %v", id, err)
	}
}

// noteQueuedBackupBlockers explains why a pending backup command that was not picked this pass
// is waiting for another run, and clears the note on the one that was picked. A reason a
// dispatch already recorded, such as an unreachable member, is kept.
func noteQueuedBackupBlockers(s *Store, picked, active string) {
	if picked != "" {
		noteBlocker(s, picked, "")
	}
	for id, entry := range s.State.Journal {
		if id == picked || id == active || entry.Status != "pending" || entry.Error != "" || entry.Command == nil || !backupCommand(entry.Command.Kind) {
			continue
		}
		switch {
		case hasEarlierUnsettledBackup(s.State.Journal, id, entry):
			noteBlocker(s, id, "waiting for an earlier backup of this VM to finish")
		case active != "":
			noteBlocker(s, id, "waiting for another backup or snapshot to finish")
		}
	}
}

// noteCleanupBlocker records why a pending or needs-attention test cleanup could not reach its member.
func noteCleanupBlocker(s *Store, id, reason string) {
	entry, ok := s.State.Journal[id]
	if !ok || (entry.Status != "pending" && entry.Status != "needs_attention") || entry.Error == reason {
		return
	}
	entry.Error, entry.Acknowledged = reason, false
	s.State.Journal[id] = entry
	if err := s.Save(); err != nil {
		log.Printf("record cleanup blocker for %s: %v", id, err)
	}
}
