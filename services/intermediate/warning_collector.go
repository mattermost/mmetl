package intermediate

import (
	"fmt"
	"sort"
	"sync"

	log "github.com/sirupsen/logrus"
)

// maxNoteExamples caps how many distinct formatted messages are kept per call
// site. Migrations can produce thousands of near-identical warnings (e.g. one
// per user missing an email address); keeping every one would make the Notes
// section unreadable and blow up summary.md's size for no benefit.
const maxNoteExamples = 5

// EntityCountField optionally accompanies EntityKeyField when a single log
// call reports a known batch of dropped entities at once (e.g. "dropping N
// thread replies because their root was skipped", where N is known upfront).
// When absent, a tagged entry counts as 1. Sites that drop entities one at a
// time — the common case — should not set this and just log once per drop.
const EntityCountField = "entity_count"

// NoteEntry summarizes every log entry that fired from a single call site
// (grouped by file:line, which logrus.Entry.Caller already carries once
// SetReportCaller(true) is set — no message parsing required).
type NoteEntry struct {
	Location string   // "file.go:123"
	Examples []string // up to maxNoteExamples distinct fully-formatted messages, first-seen order
	Count    int      // total occurrences at this location, including duplicates of Examples and any beyond it
}

// WarningCollector is a logrus.Hook that captures every Warn/Error-level log
// entry emitted during a transform run, without requiring any change to the
// call sites themselves. Two things fall out of this for free at attach time:
//
//   - Skipped counts per entity: a call site opts in by tagging its entry with
//     WithField(EntityKeyField, EntityX) before logging; Fire tallies those.
//   - Notes: every Warn/Error entry, tagged or not, is grouped by call site and
//     surfaces in the summary's Notes section with a count, so "why wasn't X
//     transformed" is always answered even for warnings that don't map to a
//     specific entity row.
//
// It must be attached to the concrete *logrus.Logger before that logger is
// handed off as the log.FieldLogger interface to a Transformer (AddHook is
// not part of that interface).
type WarningCollector struct {
	mu       sync.Mutex
	skipped  map[string]int
	notes    map[string]*NoteEntry
	noteKeys []string // every location seen; Notes() sorts a copy of this
}

// NewWarningCollector returns a ready-to-attach WarningCollector.
func NewWarningCollector() *WarningCollector {
	return &WarningCollector{
		skipped: make(map[string]int),
		notes:   make(map[string]*NoteEntry),
	}
}

// Levels implements logrus.Hook.
func (w *WarningCollector) Levels() []log.Level {
	return []log.Level{log.WarnLevel, log.ErrorLevel}
}

// Fire implements logrus.Hook.
func (w *WarningCollector) Fire(entry *log.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if entity, ok := entry.Data[EntityKeyField].(string); ok && entity != "" {
		n := 1
		if c, ok := entry.Data[EntityCountField].(int); ok && c > 0 {
			n = c
		}
		w.skipped[entity] += n
	}

	loc := callerLocation(entry)
	note, ok := w.notes[loc]
	if !ok {
		note = &NoteEntry{Location: loc}
		w.notes[loc] = note
		w.noteKeys = append(w.noteKeys, loc)
	}
	note.Count++
	if len(note.Examples) < maxNoteExamples && !containsString(note.Examples, entry.Message) {
		note.Examples = append(note.Examples, entry.Message)
	}

	return nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// callerLocation formats entry.Caller as "file.go:line", falling back to
// "unknown" when caller reporting isn't available (e.g. a test that fires
// entries directly without SetReportCaller).
func callerLocation(entry *log.Entry) string {
	if entry.Caller == nil {
		return "unknown"
	}
	return fmt.Sprintf("%s:%d", baseName(entry.Caller.File), entry.Caller.Line)
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// Skipped returns the accumulated per-entity skip tallies, keyed by the same
// entity keys used in EntityCount, for merging into BuildCounts via
// ApplySkipped.
func (w *WarningCollector) Skipped() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string]int, len(w.skipped))
	for k, v := range w.skipped {
		result[k] = v
	}
	return result
}

// Notes returns every distinct call site that logged a Warn/Error, sorted by
// location (file:line) for stable, reproducible output.
func (w *WarningCollector) Notes() []NoteEntry {
	w.mu.Lock()
	defer w.mu.Unlock()

	keys := append([]string(nil), w.noteKeys...)
	sort.Strings(keys)

	result := make([]NoteEntry, 0, len(keys))
	for _, k := range keys {
		// Copying the struct alone would still share the Examples backing
		// array with the collector's own state — clone it so a caller can't
		// mutate collector-owned storage, or race with a concurrent Fire,
		// after this lock is released.
		note := *w.notes[k]
		note.Examples = append([]string(nil), note.Examples...)
		result = append(result, note)
	}
	return result
}
