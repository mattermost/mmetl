package confluence

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sampleDescriptor is the descriptor from the private Confluence Cloud export
// used for discovery, reproduced verbatim. It carries no private data.
const sampleDescriptor = `#Tue Sep 01 10:52:44 UTC 2026
ao.data.list=com.atlassian.confluence.plugins.confluence-space-ia, com.atlassian.mywork.mywork-confluence-host-plugin
ao.data.version.com.atlassian.confluence.plugins.confluence-space-ia=1000.0.0-a1340c230
ao.data.version.min.com.atlassian.confluence.plugins.confluence-space-ia=5.0
backupAttachments=true
buildNumber=6169
createdByBuildNumber=8703
createdByVersionNumber=1000.0.0-a1340c230
defaultUsersGroup=confluence-users
exportType=all
source=cloud
timezoneId=America/Vancouver
`

// testEntry describes one entry to write into a synthetic source archive.
type testEntry struct {
	name   string
	body   string
	mode   fs.FileMode
	method uint16
	flags  uint16
	// rawMethod registers a compressor for method so the writer accepts a
	// method the reader will later reject.
	rawMethod bool
}

// nopWriteCloser lets a test register a compressor for an unsupported method.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// buildArchive writes a synthetic source archive and returns its path.
func buildArchive(t *testing.T, entries ...testEntry) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "source.zip")
	f, err := os.Create(path) //nolint:gosec // path is inside the test's own TempDir
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	zw := zip.NewWriter(f)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: entry.method, Flags: entry.flags}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		if entry.rawMethod {
			zw.RegisterCompressor(entry.method, func(w io.Writer) (io.WriteCloser, error) {
				return nopWriteCloser{w}, nil
			})
		}

		w, err := zw.CreateHeader(header)
		require.NoError(t, err)
		_, err = io.WriteString(w, entry.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	return path
}

// validEntries is a minimal well-formed archive: the two required entries plus
// one attachment.
func validEntries() []testEntry {
	return []testEntry{
		{name: DescriptorEntryName, body: sampleDescriptor, method: zip.Deflate},
		{name: EntitiesEntryName, body: `<hibernate-generic/>`, method: zip.Deflate},
		{name: "attachments/26542083/26542119/1", body: "attachment bytes", method: zip.Deflate},
	}
}

func TestOpenSourceArchive(t *testing.T) {
	archive, err := OpenSourceArchive(buildArchive(t, validEntries()...))
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	require.Empty(t, archive.Warnings())
	require.Equal(t, "America/Vancouver", archive.Descriptor().TimezoneID)

	t.Run("streams entities without extracting the archive", func(t *testing.T) {
		rc, err := archive.OpenEntities()
		require.NoError(t, err)
		defer func() { require.NoError(t, rc.Close()) }()

		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.Equal(t, `<hibernate-generic/>`, string(body))
		require.Equal(t, int64(len(body)), archive.EntitiesSize())
	})

	t.Run("indexes attachments by exact version", func(t *testing.T) {
		require.Equal(t, 1, archive.AttachmentCount())

		ref := AttachmentRef{ContainerID: "26542083", AttachmentID: "26542119", Version: 1}
		require.Equal(t, []AttachmentRef{ref}, archive.AttachmentRefs())
		require.Equal(t, "attachments/26542083/26542119/1", ref.String())

		entry, ok := archive.Attachment(ref)
		require.True(t, ok)
		require.Equal(t, int64(len("attachment bytes")), entry.Size)
		require.NotZero(t, entry.CRC32)

		rc, err := entry.Open()
		require.NoError(t, err)
		defer func() { require.NoError(t, rc.Close()) }()
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.Equal(t, "attachment bytes", string(body))
	})

	t.Run("a version that was never exported is absent, not an error", func(t *testing.T) {
		_, ok := archive.Attachment(AttachmentRef{ContainerID: "26542083", AttachmentID: "26542119", Version: 2})
		require.False(t, ok)
	})
}

// TestOpenSourceArchive_ToleratesSampleLeadingDotSlash pins the one prefix the
// real Confluence Cloud export uses. Rejecting it would reject every real
// backup; accepting anything more would widen the traversal surface.
func TestOpenSourceArchive_ToleratesSampleLeadingDotSlash(t *testing.T) {
	entries := append(validEntries(), testEntry{
		name:   "./plugin-data/com.atlassian.activeobjects.confluence.spi/activeObjectsBackupRestoreProvider.pdata",
		body:   "plugin state",
		method: zip.Deflate,
	})

	archive, err := OpenSourceArchive(buildArchive(t, entries...))
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	require.Empty(t, archive.Warnings(), "plugin-data is expected and never imported, so it must not warn")
	require.Equal(t, 1, archive.AttachmentCount())
}

func TestOpenSourceArchive_NormalizesRepeatedDotSlash(t *testing.T) {
	archive, err := OpenSourceArchive(buildArchive(t,
		testEntry{name: "././" + DescriptorEntryName, body: sampleDescriptor, method: zip.Deflate},
		testEntry{name: "./" + EntitiesEntryName, body: `<hibernate-generic/>`, method: zip.Deflate},
		testEntry{name: "././attachments/1/2/3", body: "bytes", method: zip.Deflate},
	))
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	require.Equal(t, 1, archive.AttachmentCount())
	_, ok := archive.Attachment(AttachmentRef{ContainerID: "1", AttachmentID: "2", Version: 3})
	require.True(t, ok)
}

func TestOpenSourceArchive_RejectsUnsafeArchives(t *testing.T) {
	tests := []struct {
		name    string
		entries []testEntry
		wantErr string
	}{
		{
			name:    "traversal",
			entries: append(validEntries(), testEntry{name: "../escape.txt", body: "x", method: zip.Deflate}),
			wantErr: `unsafe path segment ".."`,
		},
		{
			name:    "traversal hidden mid-path",
			entries: append(validEntries(), testEntry{name: "attachments/1/../../escape.txt", body: "x", method: zip.Deflate}),
			wantErr: `unsafe path segment ".."`,
		},
		{
			name:    "single dot segment",
			entries: append(validEntries(), testEntry{name: "attachments/./1", body: "x", method: zip.Deflate}),
			wantErr: `unsafe path segment "."`,
		},
		{
			name:    "absolute path",
			entries: append(validEntries(), testEntry{name: "/etc/passwd", body: "x", method: zip.Deflate}),
			wantErr: "is an absolute path",
		},
		{
			name:    "backslash",
			entries: append(validEntries(), testEntry{name: `attachments\1\2\3`, body: "x", method: zip.Deflate}),
			wantErr: "contains a backslash",
		},
		{
			name:    "drive prefix",
			entries: append(validEntries(), testEntry{name: "C:/attachments/1/2/3", body: "x", method: zip.Deflate}),
			wantErr: "has a drive prefix",
		},
		{
			name:    "NUL in path",
			entries: append(validEntries(), testEntry{name: "attach\x00ments", body: "x", method: zip.Deflate}),
			wantErr: "contains NUL",
		},
		{
			name:    "empty path segment",
			entries: append(validEntries(), testEntry{name: "attachments//1", body: "x", method: zip.Deflate}),
			wantErr: "has an empty path segment",
		},
		{
			name: "symlink",
			entries: append(validEntries(), testEntry{
				name: "attachments/1/2/3", body: "../../../etc/passwd", method: zip.Deflate, mode: fs.ModeSymlink | 0o777,
			}),
			wantErr: "is a symlink",
		},
		{
			name: "device",
			entries: append(validEntries(), testEntry{
				name: "attachments/1/2/4", body: "", method: zip.Deflate, mode: fs.ModeDevice | 0o600,
			}),
			wantErr: "is a device",
		},
		{
			name: "named pipe",
			entries: append(validEntries(), testEntry{
				name: "attachments/1/2/5", body: "", method: zip.Deflate, mode: fs.ModeNamedPipe | 0o600,
			}),
			wantErr: "is a named pipe",
		},
		{
			name: "encrypted entry",
			entries: append(validEntries(), testEntry{
				name: "attachments/1/2/6", body: "x", method: zip.Deflate, flags: 0x1,
			}),
			wantErr: "is encrypted",
		},
		{
			name: "unsupported compression method",
			entries: append(validEntries(), testEntry{
				name: "attachments/1/2/7", body: "x", method: 99, rawMethod: true,
			}),
			wantErr: "unsupported compression method 99",
		},
		{
			name: "duplicate raw path",
			entries: append(validEntries(), testEntry{
				name: EntitiesEntryName, body: `<hibernate-generic/>`, method: zip.Deflate,
			}),
			wantErr: "duplicate entry",
		},
		{
			name: "duplicate normalized path",
			entries: append(validEntries(), testEntry{
				name: "./" + EntitiesEntryName, body: `<hibernate-generic/>`, method: zip.Deflate,
			}),
			wantErr: "normalize to the same path",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive, err := OpenSourceArchive(buildArchive(t, test.entries...))
			if archive != nil {
				require.NoError(t, archive.Close())
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestOpenSourceArchive_RequiresBothRootEntries(t *testing.T) {
	t.Run("missing entities.xml", func(t *testing.T) {
		_, err := OpenSourceArchive(buildArchive(t,
			testEntry{name: DescriptorEntryName, body: sampleDescriptor, method: zip.Deflate},
		))
		require.ErrorContains(t, err, `missing required entry "entities.xml"`)
	})

	t.Run("missing exportDescriptor.properties", func(t *testing.T) {
		_, err := OpenSourceArchive(buildArchive(t,
			testEntry{name: EntitiesEntryName, body: `<hibernate-generic/>`, method: zip.Deflate},
		))
		require.ErrorContains(t, err, `missing required entry "exportDescriptor.properties"`)
	})

	t.Run("a directory named entities.xml does not satisfy the requirement", func(t *testing.T) {
		_, err := OpenSourceArchive(buildArchive(t,
			testEntry{name: DescriptorEntryName, body: sampleDescriptor, method: zip.Deflate},
			testEntry{name: EntitiesEntryName + "/", method: zip.Store},
		))
		require.ErrorContains(t, err, `missing required entry "entities.xml"`)
	})

	t.Run("not a zip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not.zip")
		require.NoError(t, os.WriteFile(path, []byte("this is not a zip"), 0o600))

		_, err := OpenSourceArchive(path)
		require.ErrorContains(t, err, "opening source archive")
	})
}

// TestOpenSourceArchive_WarnsOnUnknownEntries covers the entries that are safe
// but meaningless to this importer. They are reported and never extracted,
// rather than failing an otherwise valid export.
func TestOpenSourceArchive_WarnsOnUnknownEntries(t *testing.T) {
	entries := append(validEntries(),
		testEntry{name: "unexpected.txt", body: "x", method: zip.Deflate},
		testEntry{name: "attachments/not-numeric/2/3", body: "x", method: zip.Deflate},
		testEntry{name: "attachments/1/2", body: "x", method: zip.Deflate},
		testEntry{name: "attachments/1/2/3/4", body: "x", method: zip.Deflate},
	)

	archive, err := OpenSourceArchive(buildArchive(t, entries...))
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	require.Equal(t, 1, archive.AttachmentCount(), "malformed attachment paths must not be indexed")

	warnings := archive.Warnings()
	require.Len(t, warnings, 4)
	for _, warning := range warnings {
		require.Equal(t, WarnSourceArchiveUnknownEntry, warning.Code)
		require.Equal(t, "archive_entry", warning.EntityType)
	}
	require.Equal(t, []string{
		"attachments/1/2",
		"attachments/1/2/3/4",
		"attachments/not-numeric/2/3",
		"unexpected.txt",
	}, warningSourceIDs(warnings), "warnings must be deterministically ordered")
}

func warningSourceIDs(warnings []Warning) []string {
	ids := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		ids = append(ids, warning.SourceID)
	}
	return ids
}

func TestAttachmentRefsAreOrderedNumerically(t *testing.T) {
	entries := append(validEntries(),
		testEntry{name: "attachments/2/1/1", body: "x", method: zip.Deflate},
		testEntry{name: "attachments/10/1/1", body: "x", method: zip.Deflate},
		testEntry{name: "attachments/2/10/1", body: "x", method: zip.Deflate},
		testEntry{name: "attachments/2/1/10", body: "x", method: zip.Deflate},
	)

	archive, err := OpenSourceArchive(buildArchive(t, entries...))
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	require.Equal(t, []string{
		"attachments/2/1/1",
		"attachments/2/1/10",
		"attachments/2/10/1",
		"attachments/10/1/1",
		"attachments/26542083/26542119/1",
	}, refStrings(archive.AttachmentRefs()))
}

func refStrings(refs []AttachmentRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.String())
	}
	return out
}

func TestParseDescriptor(t *testing.T) {
	t.Run("real sample", func(t *testing.T) {
		descriptor, err := ParseDescriptor(strings.NewReader(sampleDescriptor))
		require.NoError(t, err)

		require.Equal(t, "America/Vancouver", descriptor.TimezoneID)
		require.True(t, descriptor.TimezoneResolved)
		require.Equal(t, "America/Vancouver", descriptor.Location.String())
		require.Equal(t, "all", descriptor.ExportType)
		require.Equal(t, "cloud", descriptor.Source)
		require.Equal(t, "6169", descriptor.BuildNumber)
		require.True(t, descriptor.BackupAttachments)
		require.Equal(t, "confluence-users", descriptor.Properties["defaultUsersGroup"])
		require.NotContains(t, descriptor.Properties, "Tue Sep 01 10:52:44 UTC 2026",
			"the leading timestamp line is a comment, not a property")
	})

	// A naive Confluence timestamp is meaningless without its zone, so the
	// resolved location has to be the real one, not a same-named placeholder.
	t.Run("resolved location actually shifts naive timestamps", func(t *testing.T) {
		descriptor, err := ParseDescriptor(strings.NewReader("timezoneId=America/Vancouver\n"))
		require.NoError(t, err)

		naive := time.Date(2026, time.January, 15, 12, 0, 0, 0, descriptor.Location)
		_, offset := naive.Zone()
		require.Equal(t, -8*60*60, offset)
	})

	t.Run("unknown timezone falls back to UTC and says so", func(t *testing.T) {
		descriptor, err := ParseDescriptor(strings.NewReader("timezoneId=Mars/Olympus_Mons\n"))
		require.NoError(t, err)

		require.Equal(t, "Mars/Olympus_Mons", descriptor.TimezoneID)
		require.False(t, descriptor.TimezoneResolved)
		require.Equal(t, time.UTC, descriptor.Location)
	})

	t.Run("absent timezone falls back to UTC", func(t *testing.T) {
		descriptor, err := ParseDescriptor(strings.NewReader("exportType=all\n"))
		require.NoError(t, err)

		require.Empty(t, descriptor.TimezoneID)
		require.False(t, descriptor.TimezoneResolved)
		require.Equal(t, time.UTC, descriptor.Location)
	})

	t.Run("properties syntax", func(t *testing.T) {
		input := strings.Join([]string{
			"# a comment",
			"! another comment",
			"",
			"   ",
			"colon:value",
			"space value",
			"padded = spaced value ",
			`escaped\:key=value`,
			`continued=one\`,
			"  two",
			`unicode=caf\u00e9`,
			`tab=a\tb`,
			`trailing.backslash=a\\`,
		}, "\n")

		descriptor, err := ParseDescriptor(strings.NewReader(input))
		require.NoError(t, err)

		require.Equal(t, map[string]string{
			"colon":              "value",
			"space":              "value",
			"padded":             "spaced value ",
			"escaped:key":        "value",
			"continued":          "one  two",
			"unicode":            "café",
			"tab":                "a\tb",
			"trailing.backslash": `a\`,
		}, descriptor.Properties)
	})

	t.Run("rejects an oversized descriptor rather than truncating it", func(t *testing.T) {
		_, err := ParseDescriptor(strings.NewReader(strings.Repeat("k=v\n", descriptorMaxBytes)))
		require.ErrorContains(t, err, "exceeds")
	})
}

// TestOpenSourceArchive_PrivateSample runs against the real Confluence Cloud
// export when one is available. It is skipped by default: the sample is
// private and must never be committed.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestOpenSourceArchive_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	require.NotEmpty(t, archive.Descriptor().TimezoneID)
	require.True(t, archive.Descriptor().TimezoneResolved)
	require.Positive(t, archive.EntitiesSize())

	for _, warning := range archive.Warnings() {
		t.Logf("warning %s: %s", warning.Code, warning.SourceID)
	}
	t.Logf("indexed %d attachment blobs", archive.AttachmentCount())
}
