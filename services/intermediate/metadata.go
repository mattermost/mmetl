package intermediate

import "time"

const generatorName = "mmetl"

// Info is mmetl's typed mirror of imports.VersionInfoImportData
type Info struct {
	Generator  string      `json:"generator"` // always "mmetl"
	Version    string      `json:"version"`   // "<version> (<build hash>)"
	Created    string      `json:"created"`   // RFC3339Nano UTC
	Additional *Additional `json:"additional,omitempty"`
}

// Additional is the json.RawMessage object the server accepts under
// info.additional.
//
// Keep it bounded: the version line shares the importer's 16 MB per-line
// scanner budget.
type Additional struct {
	Source SourceInfo `json:"source"`
	Target TargetInfo `json:"target"`
	Run    RunInfo    `json:"run"`
	Counts Counts     `json:"counts"`
}

// SourceInfo describes the export being converted. File is a base name, never
// a path.
//
// There is deliberately no export-format version: neither a Slack export zip nor
// the RocketChat collections mmetl reads carry one anywhere it parses, and a
// version is better omitted than invented.
type SourceInfo struct {
	Platform  string `json:"platform"` // "slack", "rocketchat"
	File      string `json:"file,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// TargetInfo describes where the export is headed. mmetl targets exactly one
// team per run.
type TargetInfo struct {
	Team   string `json:"team,omitempty"`
	Output string `json:"output,omitempty"` // base name of the import file
}

// RunInfo describes the invocation itself.
//
// Finished is zero until the export completes: line 1 is written before any other
// line and rewritten with a finish time once the rest of the file is there (see
// RewriteVersion). An export that aborted before then keeps the zero value, which
// is how its line 1 reports that the run never finished.
type RunInfo struct {
	Version   string    `json:"mmetl_version"`
	BuildHash string    `json:"mmetl_build_hash,omitempty"`
	Command   string    `json:"command,omitempty"` // "mmetl transform slack"
	Flags     string    `json:"flags,omitempty"`   // only the flags actually set
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
}

// Counts is how many lines of each kind the import file ended up with.
// It stays zero until RewriteVersion, which runs only after every other line
// has been written — so a run that aborted mid-export does not claim the file
// contains lines it never produced.
type Counts struct {
	Users int `json:"users"`
	Bots  int `json:"bots"`

	PublicChannels  int `json:"public_channels"`
	PrivateChannels int `json:"private_channels"`
	GroupChannels   int `json:"group_channels"`
	DirectChannels  int `json:"direct_channels"`

	Posts   int `json:"posts"`
	Replies int `json:"replies"`

	Reactions   int `json:"reactions"`
	Attachments int `json:"attachments"`
}

// VersionString renders the two halves of the mmetl version as one string:
// "v1.2.3 (9f2c1ab)", or just the version when there is no build hash.
func (i Info) VersionString() string {
	run := i.details().Run
	switch {
	case run.Version == "":
		return i.Version
	case run.BuildHash == "":
		return run.Version
	default:
		return run.Version + " (" + run.BuildHash + ")"
	}
}

// details returns Additional by value, zero-valued when it is absent, so the
// consumers can read the field paths without a nil check of their own.
func (i Info) details() Additional {
	if i.Additional == nil {
		return Additional{}
	}
	return *i.Additional
}

// Duration is how long the run took, or 0 when it has not finished yet.
func (r RunInfo) Duration() time.Duration {
	if r.Started.IsZero() || r.Finished.IsZero() || r.Finished.Before(r.Started) {
		return 0
	}

	elapsed := r.Finished.Sub(r.Started)
	switch {
	case elapsed >= time.Minute:
		return elapsed.Round(time.Second)
	case elapsed >= time.Second:
		return elapsed.Round(10 * time.Millisecond)
	default:
		return elapsed.Round(time.Millisecond)
	}
}

// stampExportInfo records onto the report when the import file was created.
// Counts wait for RewriteVersion: they describe what was written, and until
// every line is on disk that is still a prediction.
//
// Called from ExportVersion only. RewriteVersion re-renders line 1 and
// must not come through here, or Created moves to the end of the run.
func (e *Exporter) stampExportInfo() {
	if e.Report == nil {
		return
	}

	metadata := &e.Report.Metadata
	metadata.Generator = generatorName
	metadata.Created = NowFunc().UTC().Format(time.RFC3339Nano)
	if metadata.Additional == nil {
		metadata.Additional = &Additional{}
	}
	metadata.Version = metadata.VersionString()
}

// Counts totals the lines the Intermediate would produce. A nil Intermediate
// counts as empty, matching the rest of the report's tolerance for a half-built
// Exporter.
func (i *Intermediate) Counts() Counts {
	counts := Counts{}
	if i == nil {
		return counts
	}

	counts.PublicChannels = len(i.PublicChannels)
	counts.PrivateChannels = len(i.PrivateChannels)
	counts.GroupChannels = len(i.GroupChannels)
	counts.DirectChannels = len(i.DirectChannels)

	for _, user := range i.UsersById {
		if user.IsBot {
			counts.Bots++
			continue
		}
		counts.Users++
	}

	for _, post := range i.Posts {
		counts.Posts++
		counts.Reactions += len(post.Reactions)
		counts.Attachments += len(post.Attachments)

		for _, reply := range post.Replies {
			counts.Replies++
			counts.Reactions += len(reply.Reactions)
			counts.Attachments += len(reply.Attachments)
		}
	}

	return counts
}
