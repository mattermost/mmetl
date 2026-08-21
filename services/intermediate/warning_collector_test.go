package intermediate

import (
	"runtime"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fireEntry builds a logrus.Entry the way the real logger would (with Caller
// populated, since production always runs with SetReportCaller(true)) and
// fires it through the collector, mimicking what logrus does internally when
// an entry is logged through a hook-bearing logger.
func fireEntry(t *testing.T, w *WarningCollector, fields log.Fields, msg string) {
	t.Helper()
	_, file, line, ok := runtime.Caller(1)
	require.True(t, ok)

	entry := log.NewEntry(log.New())
	entry.Data = fields
	entry.Message = msg
	entry.Caller = &runtime.Frame{File: file, Line: line}

	require.NoError(t, w.Fire(entry))
}

func TestWarningCollector_SkippedTallies(t *testing.T) {
	w := NewWarningCollector()

	fireEntry(t, w, log.Fields{EntityKeyField: EntityPost}, "dropped post 1")
	fireEntry(t, w, log.Fields{EntityKeyField: EntityPost}, "dropped post 2")
	fireEntry(t, w, log.Fields{EntityKeyField: EntityUser}, "dropped user 1")
	fireEntry(t, w, log.Fields{}, "untagged warning, notes-only")

	skipped := w.Skipped()
	assert.Equal(t, 2, skipped[EntityPost])
	assert.Equal(t, 1, skipped[EntityUser])
	assert.Zero(t, skipped[EntityReply])
}

func TestWarningCollector_EntityCountField(t *testing.T) {
	w := NewWarningCollector()

	fireEntry(t, w, log.Fields{EntityKeyField: EntityReply, EntityCountField: 7}, "dropping 7 replies at once")

	assert.Equal(t, 7, w.Skipped()[EntityReply])
}

func TestWarningCollector_NotesGroupByCallSite(t *testing.T) {
	w := NewWarningCollector()

	// Two entries from what fireEntry reports as two distinct call sites
	// (different lines within this function) group separately...
	fireEntry(t, w, nil, "first warning")
	fireEntry(t, w, nil, "second warning")

	notes := w.Notes()
	require.Len(t, notes, 2)
	for _, n := range notes {
		assert.Equal(t, 1, n.Count)
		assert.Len(t, n.Examples, 1)
	}
}

func TestWarningCollector_NotesDedupAndCapExamples(t *testing.T) {
	w := NewWarningCollector()

	entry := log.NewEntry(log.New())
	entry.Caller = &runtime.Frame{File: "services/slack/intermediate.go", Line: 42}

	// Same message repeated many times — collapses into one example with an
	// accurate total count, not maxNoteExamples duplicate lines.
	for i := 0; i < 20; i++ {
		e := *entry
		e.Message = "identical message"
		require.NoError(t, w.Fire(&e))
	}

	notes := w.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, 20, notes[0].Count)
	assert.Equal(t, []string{"identical message"}, notes[0].Examples)
}

func TestWarningCollector_NotesCapsDistinctExamples(t *testing.T) {
	w := NewWarningCollector()

	entry := log.NewEntry(log.New())
	entry.Caller = &runtime.Frame{File: "services/slack/intermediate.go", Line: 42}

	// More distinct messages than maxNoteExamples at the same call site (e.g.
	// a Warnf interpolating a different username each time).
	for i := 0; i < maxNoteExamples+3; i++ {
		e := *entry
		e.Message = "message #" + string(rune('a'+i))
		require.NoError(t, w.Fire(&e))
	}

	notes := w.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, maxNoteExamples+3, notes[0].Count)
	assert.Len(t, notes[0].Examples, maxNoteExamples)
}

func TestWarningCollector_NotesSortedByLocation(t *testing.T) {
	w := NewWarningCollector()

	mk := func(file string, line int, msg string) *log.Entry {
		e := log.NewEntry(log.New())
		e.Caller = &runtime.Frame{File: file, Line: line}
		e.Message = msg
		return e
	}

	require.NoError(t, w.Fire(mk("z.go", 1, "z")))
	require.NoError(t, w.Fire(mk("a.go", 1, "a")))
	require.NoError(t, w.Fire(mk("m.go", 1, "m")))

	notes := w.Notes()
	require.Len(t, notes, 3)
	assert.Equal(t, "a.go:1", notes[0].Location)
	assert.Equal(t, "m.go:1", notes[1].Location)
	assert.Equal(t, "z.go:1", notes[2].Location)
}

func TestWarningCollector_NoCallerFallsBackToUnknown(t *testing.T) {
	w := NewWarningCollector()

	entry := log.NewEntry(log.New())
	entry.Message = "no caller info"
	require.NoError(t, w.Fire(entry))

	notes := w.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, "unknown", notes[0].Location)
}

func TestWarningCollector_Levels(t *testing.T) {
	w := NewWarningCollector()
	assert.ElementsMatch(t, []log.Level{log.WarnLevel, log.ErrorLevel}, w.Levels())
}
