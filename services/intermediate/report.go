package intermediate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Report file names, written into the directory that holds the bulk import file.
const (
	ReportMarkdownFilename = "transform-report.md"
	ReportJSONFilename     = "transform-report.json"
)

// EntityKind identifies a class of source entity in the report. Rendering order
// in the summary table is the declaration order of entityOrder, not map order.
type EntityKind string

const (
	EntityUser              EntityKind = "user"
	EntityBot               EntityKind = "bot"
	EntityPublicChannel     EntityKind = "public_channel"
	EntityPrivateChannel    EntityKind = "private_channel"
	EntityGroupChannel      EntityKind = "group_channel"
	EntityDirectChannel     EntityKind = "direct_channel"
	EntityChannelMembership EntityKind = "channel_membership"
	EntityPost              EntityKind = "post"
	EntityThread            EntityKind = "thread"
	EntityReaction          EntityKind = "reaction"
	EntityFile              EntityKind = "file"
	EntityEmoji             EntityKind = "emoji"
	EntitySubscription      EntityKind = "subscription" // RocketChat
	EntityUpload            EntityKind = "upload"       // RocketChat
)

// entityOrder is the order entity kinds appear in the summary table and the
// details sections. Kinds not listed here sort after these, by kind name.
var entityOrder = []EntityKind{
	EntityUser,
	EntityBot,
	EntityPublicChannel,
	EntityPrivateChannel,
	EntityGroupChannel,
	EntityDirectChannel,
	EntityChannelMembership,
	EntitySubscription,
	EntityPost,
	EntityThread,
	EntityReaction,
	EntityFile,
	EntityUpload,
	EntityEmoji,
}

// entityTitles maps a kind to its Markdown section title. The GitHub-flavored
// anchor used by the summary table is derived from the title.
var entityTitles = map[EntityKind]string{
	EntityUser:              "Users",
	EntityBot:               "Bots",
	EntityPublicChannel:     "Public channels",
	EntityPrivateChannel:    "Private channels",
	EntityGroupChannel:      "Group channels",
	EntityDirectChannel:     "Direct channels",
	EntityChannelMembership: "Channel memberships",
	EntitySubscription:      "Subscriptions",
	EntityPost:              "Posts",
	EntityThread:            "Threads",
	EntityReaction:          "Reactions",
	EntityFile:              "Files",
	EntityUpload:            "Uploads",
	EntityEmoji:             "Emoji",
}

// EntityTitle returns the human-readable section title for a kind.
func EntityTitle(kind EntityKind) string {
	if title, ok := entityTitles[kind]; ok {
		return title
	}
	return string(kind)
}

// RunMetadata describes the transform run a report covers. Every field is
// filled by the command layer before the transform starts, except Finished,
// which Finish sets.
type RunMetadata struct {
	Provider string    `json:"provider"`
	Version  string    `json:"version"`
	Input    string    `json:"input"`
	Team     string    `json:"team"`
	Output   string    `json:"output"`
	Flags    string    `json:"flags"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
}

// Duration is how long the run took, or 0 when it has not finished yet. It is
// rounded to a precision that suits its own magnitude, so a long migration
// reads as "2m38s" while a run over a small export still reports a real number
// rather than "0s".
func (m RunMetadata) Duration() time.Duration {
	if m.Started.IsZero() || m.Finished.IsZero() || m.Finished.Before(m.Started) {
		return 0
	}

	elapsed := m.Finished.Sub(m.Started)
	switch {
	case elapsed >= time.Minute:
		return elapsed.Round(time.Second)
	case elapsed >= time.Second:
		return elapsed.Round(10 * time.Millisecond)
	default:
		return elapsed.Round(time.Millisecond)
	}
}

// Report accumulates, for one transform run, which source entities reached the
// Mattermost import file and why the rest did not.
//
// A nil *Report is a valid, inert report: every method is a no-op, so an
// Exporter built as a struct literal (as several tests do) needs no report.
//
// A Report is NOT safe for concurrent use. The transform pipeline is
// single-goroutine by design and paying for a mutex here would be waste.
type Report struct {
	Metadata RunMetadata                  `json:"metadata"`
	Error    string                       `json:"error,omitempty"`
	Entities map[EntityKind]*EntityReport `json:"entities"`
	// Reasons is the dictionary of every reason referenced by a note in this
	// run, emitted once so notes can refer to it by code.
	Reasons map[string]*Reason `json:"reasons"`

	logger log.FieldLogger

	// quietCounts totals the notes recorded for reasons marked Quiet, so the
	// run can log one aggregate line per reason instead of one line per entity.
	quietCounts map[string]int

	// finished guards Finish so a deferred write and an explicit call cannot
	// derive the counters twice.
	finished bool
}

// NewReport creates an empty report that logs through logger. logger may be nil,
// in which case recording a note produces no log output.
func NewReport(logger log.FieldLogger) *Report {
	return &Report{
		Entities:    map[EntityKind]*EntityReport{},
		Reasons:     map[string]*Reason{},
		logger:      logger,
		quietCounts: map[string]int{},
	}
}

// Logger returns the logger the report writes its notes through, or nil.
func (r *Report) Logger() log.FieldLogger {
	if r == nil {
		return nil
	}
	return r.logger
}

// For returns the EntityReport for a kind, creating it on first use.
func (r *Report) For(kind EntityKind) *EntityReport {
	if r == nil {
		return nil
	}
	if r.Entities == nil {
		r.Entities = map[EntityKind]*EntityReport{}
	}
	entity, ok := r.Entities[kind]
	if !ok {
		entity = &EntityReport{kind: kind, report: r}
		r.Entities[kind] = entity
	}
	return entity
}

// Named helpers over For, for the hot paths.
func (r *Report) Users() *EntityReport              { return r.For(EntityUser) }
func (r *Report) Bots() *EntityReport               { return r.For(EntityBot) }
func (r *Report) PublicChannels() *EntityReport     { return r.For(EntityPublicChannel) }
func (r *Report) PrivateChannels() *EntityReport    { return r.For(EntityPrivateChannel) }
func (r *Report) GroupChannels() *EntityReport      { return r.For(EntityGroupChannel) }
func (r *Report) DirectChannels() *EntityReport     { return r.For(EntityDirectChannel) }
func (r *Report) ChannelMemberships() *EntityReport { return r.For(EntityChannelMembership) }
func (r *Report) Subscriptions() *EntityReport      { return r.For(EntitySubscription) }
func (r *Report) Posts() *EntityReport              { return r.For(EntityPost) }
func (r *Report) Threads() *EntityReport            { return r.For(EntityThread) }
func (r *Report) Reactions() *EntityReport          { return r.For(EntityReaction) }
func (r *Report) Files() *EntityReport              { return r.For(EntityFile) }
func (r *Report) Uploads() *EntityReport            { return r.For(EntityUpload) }
func (r *Report) Emoji() *EntityReport              { return r.For(EntityEmoji) }

// The identity helpers below build the composite keys the report names entities
// by. Neither half of any of them locates the entity in the source export on its
// own — a post timestamp repeats across channels, a user ID says nothing about
// which membership is meant — so every provider composes them the same way.

// MembershipID names one user's membership of one channel.
func MembershipID(channel, user string) string {
	return channel + "/" + user
}

// PostID names one message within its channel. It is also how a thread is named,
// by the message that roots it.
func PostID(channel, message string) string {
	return channel + "/" + message
}

// ReactionID names one reaction: the message it is on, who left it, and which
// emoji it was.
func ReactionID(channel, message, user, emoji string) string {
	return PostID(channel, message) + "/" + user + "/" + emoji
}

// EntityReport holds the outcome of every source entity of one kind.
type EntityReport struct {
	Transformed int                `json:"transformed"`
	Skipped     int                `json:"skipped"`
	Notes       []ReportEntityNote `json:"notes"`

	// sourceTotal is the number of source entities seen for this kind; it is
	// the basis for the derived Transformed count. Not serialized.
	sourceTotal int

	kind   EntityKind
	report *Report
}

// ReportEntityNote records one thing that happened to one named entity.
type ReportEntityNote struct {
	EntityID   string   `json:"entity_id"`             // user_id, channel_id, post ts, ...
	EntityName string   `json:"entity_name,omitempty"` // username, channel_name, ... (if available)
	ReasonCode string   `json:"reason_code"`
	Args       []string `json:"args,omitempty"` // formatted into Reason.Specifics

	// reason points at the single registry instance for ReasonCode. The prose
	// is never copied per note.
	reason *Reason
}

// Seen records that n source entities of this kind exist. Call it once per
// source collection, or once per entity for kinds that are discovered as the
// transform walks the export; it is what Transformed is derived from.
func (e *EntityReport) Seen(n int) {
	if e == nil || n <= 0 {
		return
	}
	e.sourceTotal += n
}

// Skip records that one entity was not transformed, and why. There is no
// aggregate variant: every skipped entity is named individually, so id must
// always identify the entity in the source export.
func (e *EntityReport) Skip(id, name string, reason *Reason, args ...string) {
	e.record(id, name, reason, args)
}

// Note records something that happened to an entity that WAS transformed
// (split, merged, renamed, truncated). Does not touch the counters.
func (e *EntityReport) Note(id, name string, reason *Reason, args ...string) {
	e.record(id, name, reason, args)
}

// record appends the note and, when the reason says the entity did not reach
// the import file, counts it as skipped. Whether a note is a skip is decided by
// Reason.Skip alone, so Skip and Note are readable aliases that cannot disagree
// with the reason they reference.
func (e *EntityReport) record(id, name string, reason *Reason, args []string) {
	if e == nil || reason == nil {
		return
	}
	note := ReportEntityNote{
		EntityID:   id,
		EntityName: name,
		ReasonCode: reason.Code,
		Args:       args,
		reason:     reason,
	}
	e.Notes = append(e.Notes, note)
	if reason.Skip {
		e.Skipped++
	}
	e.report.useReason(reason)
	e.report.logNote(e.kind, note, reason)
}

// Logger returns the report's logger, or nil. Recording a note already logs, so
// this exists only for the few call sites that log something the report does
// not model.
func (e *EntityReport) Logger() log.FieldLogger {
	if e == nil || e.report == nil {
		return nil
	}
	return e.report.logger
}

// useReason adds a reason to the run's dictionary the first time it is used.
func (r *Report) useReason(reason *Reason) {
	if r == nil {
		return
	}
	if r.Reasons == nil {
		r.Reasons = map[string]*Reason{}
	}
	if _, ok := r.Reasons[reason.Code]; !ok {
		r.Reasons[reason.Code] = reason
	}
}

// logNote emits the log line for a note. Recording is the only place these
// messages are produced, so a call site never has to keep a Warnf in sync with
// the reason text.
func (r *Report) logNote(kind EntityKind, note ReportEntityNote, reason *Reason) {
	if r == nil || r.logger == nil {
		return
	}
	if reason.Quiet {
		if r.quietCounts == nil {
			r.quietCounts = map[string]int{}
		}
		r.quietCounts[reason.Code]++
		return
	}

	entry := r.logger.WithFields(log.Fields{
		"entity_kind": string(kind),
		"entity_id":   note.EntityID,
		"reason_code": reason.Code,
	})
	message := note.logMessage()
	if reason.Skip {
		entry.Warn(message)
	} else {
		entry.Info(message)
	}
}

// logMessage builds the human-readable half of a note's log line from the
// reason's Short, its per-entity Specifics, and the long Detail.
func (n ReportEntityNote) logMessage() string {
	label := n.EntityID
	if n.EntityName != "" {
		label = fmt.Sprintf("%s (%s)", n.EntityID, n.EntityName)
	}

	parts := []string{}
	if label != "" {
		parts = append(parts, label+":")
	}
	parts = append(parts, n.reason.Short)
	if specifics := n.reason.specifics(n.Args, nil); specifics != "" {
		parts = append(parts, "— "+specifics)
	}
	if n.reason.Detail != "" {
		parts = append(parts, n.reason.Detail)
	}
	return strings.Join(parts, " ")
}

// Reason returns the registered reason behind a note, resolving it from the
// registry when the note came back from JSON rather than from a live run.
func (n ReportEntityNote) Reason() *Reason {
	if n.reason != nil {
		return n.reason
	}
	return LookupReason(n.ReasonCode)
}

// Finish closes the report: it stamps the finish time, records the error that
// aborted the run (if any), derives the Transformed counters, and logs the
// aggregate line for each quiet reason. It is safe to call more than once.
func (r *Report) Finish(runErr error) {
	if r == nil || r.finished {
		return
	}
	r.finished = true

	if r.Metadata.Finished.IsZero() {
		r.Metadata.Finished = NowFunc().UTC()
	}
	if runErr != nil && r.Error == "" {
		r.Error = runErr.Error()
	}

	for _, entity := range r.Entities {
		entity.Transformed = max(entity.sourceTotal-entity.Skipped, 0)
	}

	r.logQuietSummary()
}

// logQuietSummary emits one aggregate line per quiet reason, which is what
// those reasons trade their per-entity log lines for.
func (r *Report) logQuietSummary() {
	if r.logger == nil || len(r.quietCounts) == 0 {
		return
	}
	codes := make([]string, 0, len(r.quietCounts))
	for code := range r.quietCounts {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, reasonSummaryLine(code, r.quietCounts[code]))
	}
	r.logger.Infof("Entities affected by high-volume reasons (named individually in the transform report): %s", strings.Join(parts, ", "))
}

// orderedKinds returns the kinds present in the report, in rendering order.
func (r *Report) orderedKinds() []EntityKind {
	seen := make(map[EntityKind]bool, len(r.Entities))
	ordered := make([]EntityKind, 0, len(r.Entities))
	for _, kind := range entityOrder {
		if _, ok := r.Entities[kind]; ok {
			ordered = append(ordered, kind)
			seen[kind] = true
		}
	}
	// Any kind a provider added without listing it in entityOrder still has to
	// appear, deterministically, rather than be silently dropped.
	extra := make([]EntityKind, 0)
	for kind := range r.Entities {
		if !seen[kind] {
			extra = append(extra, kind)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return append(ordered, extra...)
}

// hasContent reports whether a kind is worth rendering at all.
func (e *EntityReport) hasContent() bool {
	return e != nil && (e.Transformed > 0 || e.Skipped > 0 || len(e.Notes) > 0)
}

// sortedNotes returns the entity's notes ordered by (ReasonCode, EntityID), the
// ordering both the Markdown and the JSON use. Source collections are often Go
// maps, whose iteration order is randomized per run, so without this two
// transforms of the same export would produce reports that cannot be diffed.
func (e *EntityReport) sortedNotes() []ReportEntityNote {
	notes := make([]ReportEntityNote, len(e.Notes))
	copy(notes, e.Notes)
	sort.SliceStable(notes, func(i, j int) bool {
		if notes[i].ReasonCode != notes[j].ReasonCode {
			return notes[i].ReasonCode < notes[j].ReasonCode
		}
		return notes[i].EntityID < notes[j].EntityID
	})
	return notes
}

// sortNotes puts every entity's notes into the canonical order, so a marshalled
// report matches the Markdown one.
func (r *Report) sortNotes() {
	for _, entity := range r.Entities {
		entity.Notes = entity.sortedNotes()
	}
}

// JSON renders the report as indented JSON.
func (r *Report) JSON() ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	r.sortNotes()
	return json.MarshalIndent(r, "", "  ")
}

// Write writes the Markdown and JSON reports into dir, creating it if needed,
// and returns the path of each. A nil report writes nothing.
func (r *Report) Write(dir string) (string, string, error) {
	if r == nil {
		return "", "", nil
	}
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating report directory %s: %w", dir, err)
	}

	markdownPath := filepath.Join(dir, ReportMarkdownFilename)
	jsonPath := filepath.Join(dir, ReportJSONFilename)

	if err := os.WriteFile(markdownPath, []byte(r.Markdown()), 0644); err != nil {
		return "", "", fmt.Errorf("writing %s: %w", markdownPath, err)
	}

	encoded, err := r.JSON()
	if err != nil {
		return "", "", fmt.Errorf("encoding %s: %w", jsonPath, err)
	}
	if err := os.WriteFile(jsonPath, append(encoded, '\n'), 0644); err != nil {
		return "", "", fmt.Errorf("writing %s: %w", jsonPath, err)
	}

	return markdownPath, jsonPath, nil
}
