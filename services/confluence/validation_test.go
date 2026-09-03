package confluence

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateBundleFS_AcceptsGoldenFixtures is the producer half of the E1
// gate. The Docs importer runs the mirrored assertion over the same bytes.
func TestValidateBundleFS_AcceptsGoldenFixtures(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		manifest, lines, err := ValidateBundleFS(os.DirFS(fixtureDir("minimal")))
		require.NoError(t, err)

		require.Equal(t, ManifestVersion, manifest.Version)
		require.Equal(t, 1, manifest.Counts.PagesEmitted)
		require.Equal(t, 0, manifest.Counts.AttachmentsEmitted)
		require.Len(t, lines, 4)
		require.Equal(t, LineTypeVersion, lines[0].Type)
		require.Equal(t, LineTypeResolveSpacePlaceholders, lines[len(lines)-1].Type)
		require.Equal(t, EmptyDocumentJSON, lines[2].Page.Content)
	})

	t.Run("full", func(t *testing.T) {
		manifest, lines, err := ValidateBundleFS(os.DirFS(fixtureDir("full")))
		require.NoError(t, err)

		require.Equal(t, 2, manifest.Counts.PagesEmitted)
		require.Equal(t, 1, manifest.Counts.BlogPostsEmitted)
		require.Equal(t, 3, manifest.Counts.CommentsEmitted)
		require.Equal(t, 2, manifest.Counts.AttachmentsEmitted)
		require.Len(t, manifest.Users, 3)
		require.Equal(t, FidelityRestrictionsUnverified, manifest.Fidelity.PageRestrictions)

		var pages, comments, attachments int
		for _, line := range lines {
			switch line.Type {
			case LineTypePage:
				pages++
				attachments += len(line.Page.Attachments)
			case LineTypePageComment:
				comments++
			}
		}
		require.Equal(t, 3, pages)
		require.Equal(t, 3, comments)
		require.Equal(t, 2, attachments)
	})
}

func TestValidateBundleFS_RejectsInvalidFixtures(t *testing.T) {
	for _, fixture := range contractFixtures(t) {
		if fixture.wantErr == "" {
			continue
		}
		t.Run(fixture.name, func(t *testing.T) {
			_, _, err := ValidateBundleFS(os.DirFS(fixtureDir(fixture.name)))
			require.Error(t, err)
			require.Contains(t, err.Error(), fixture.wantErr)
		})
	}
}

// TestValidateLines covers the contract rules that no committed fixture needs to
// exist for: they are cheap to express in a table and expensive to review as
// separate directories.
func TestValidateLines(t *testing.T) {
	version := BundleVersion
	wrongVersion := 1

	versionLine := func() Line {
		return Line{
			Type:    LineTypeVersion,
			Version: &version,
			Source:  &Source{OrganizationID: fixtureOrganizationID, SpaceID: fixtureSpaceID, SpaceKey: fixtureSpaceKey},
		}
	}
	spaceLine := func() Line { return minimalBundle().lines[1] }
	pageLine := func() Line { return minimalBundle().lines[2] }
	sentinelLine := func() Line {
		return Line{Type: LineTypeResolveSpacePlaceholders, ResolveSpacePlaceholders: &ResolvePlaceholdersData{}}
	}

	tests := []struct {
		name    string
		lines   []Line
		wantErr string
	}{
		{
			name:    "empty stream",
			lines:   nil,
			wantErr: "is empty",
		},
		{
			name:    "space without version",
			lines:   []Line{spaceLine(), sentinelLine()},
			wantErr: `"space" before "version"`,
		},
		{
			name: "unsupported version",
			lines: []Line{
				{Type: LineTypeVersion, Version: &wrongVersion, Source: versionLine().Source},
				spaceLine(), sentinelLine(),
			},
			wantErr: "unsupported bundle version 1",
		},
		{
			name:    "second space line",
			lines:   []Line{versionLine(), spaceLine(), spaceLine(), sentinelLine()},
			wantErr: `duplicate "space" line`,
		},
		{
			name:    "unknown line type",
			lines:   []Line{versionLine(), spaceLine(), {Type: "channel"}, sentinelLine()},
			wantErr: `unknown line type "channel"`,
		},
		{
			name:    "payload does not match type",
			lines:   []Line{versionLine(), spaceLine(), {Type: LineTypePage, PageComment: &PageCommentData{}}, sentinelLine()},
			wantErr: `type "page" carries a "page_comment" payload`,
		},
		{
			name:    "page missing payload",
			lines:   []Line{versionLine(), spaceLine(), {Type: LineTypePage}, sentinelLine()},
			wantErr: `type "page" is missing its payload`,
		},
		{
			// The stream repeats the space key, and the destination scopes by it.
			name: "space props source id disagrees with version line",
			lines: func() []Line {
				lines := []Line{versionLine(), spaceLine(), sentinelLine()}
				lines[1].Space.Props[PropImportSourceID] = "999"
				return lines
			}(),
			wantErr: `want source.space_key "ENG"`,
		},
		{
			name: "page after comment",
			lines: func() []Line {
				comment := fullBundle().lines[5]
				return []Line{versionLine(), spaceLine(), pageLine(), comment, pageLine(), sentinelLine()}
			}(),
			wantErr: "all pages precede all comments",
		},
		{
			name: "page is its own parent",
			lines: func() []Line {
				page := pageLine()
				page.Page.ParentImportSourceID = fixtureHomePageID
				return []Line{versionLine(), spaceLine(), page, sentinelLine()}
			}(),
			wantErr: "is its own parent",
		},
		{
			name: "page content type is not a Confluence content type",
			lines: func() []Line {
				page := pageLine()
				page.Page.Props[PropConfluenceContentType] = "attachment"
				return []Line{versionLine(), spaceLine(), page, sentinelLine()}
			}(),
			wantErr: `confluence_content_type is "attachment"`,
		},
		{
			name: "page space key disagrees with source",
			lines: func() []Line {
				page := pageLine()
				page.Page.Props[PropConfluenceSpaceKey] = "OPS"
				return []Line{versionLine(), spaceLine(), page, sentinelLine()}
			}(),
			wantErr: "want source.space_key",
		},
		{
			name: "page import labels is not an array of strings",
			lines: func() []Line {
				page := pageLine()
				page.Page.Props[PropImportLabels] = []any{"ok", 7}
				return []Line{versionLine(), spaceLine(), page, sentinelLine()}
			}(),
			wantErr: "import_labels[1] must be a string",
		},
		{
			name: "page restrictions is not an object",
			lines: func() []Line {
				page := pageLine()
				page.Page.Props[PropConfluenceRestrictions] = []any{}
				return []Line{versionLine(), spaceLine(), page, sentinelLine()}
			}(),
			wantErr: "confluence_restrictions must be an object",
		},
		{
			name: "page content is empty",
			lines: func() []Line {
				page := pageLine()
				page.Page.Content = ""
				return []Line{versionLine(), spaceLine(), page, sentinelLine()}
			}(),
			wantErr: "page.content is empty",
		},
		{
			name: "page title exceeds the destination limit",
			lines: func() []Line {
				page := pageLine()
				page.Page.Title = strings.Repeat("é", TitleMaxRunes+1)
				return []Line{versionLine(), spaceLine(), page, sentinelLine()}
			}(),
			wantErr: "page.title is 256 runes",
		},
		{
			name: "comment on a page that was not emitted",
			lines: func() []Line {
				comment := fullBundle().lines[5]
				comment.PageComment.PageImportSourceID = "999999"
				return []Line{versionLine(), spaceLine(), pageLine(), comment, sentinelLine()}
			}(),
			wantErr: "is not an emitted page",
		},
		{
			name: "top level comment thread root is not itself",
			lines: func() []Line {
				comment := fullBundle().lines[5]
				comment.PageComment.ThreadRootImportSourceID = "111"
				return []Line{versionLine(), spaceLine(), pageLine(), comment, sentinelLine()}
			}(),
			wantErr: "want its own source ID",
		},
		{
			name: "descendant thread root is not the parent's thread root",
			lines: func() []Line {
				full := fullBundle()
				root, reply := full.lines[5], full.lines[6]
				reply.PageComment.ThreadRootImportSourceID = fixtureReplyCommentID
				return []Line{versionLine(), spaceLine(), pageLine(), root, reply, sentinelLine()}
			}(),
			wantErr: "want the parent's thread root",
		},
		{
			name: "descendant precedes its parent",
			lines: func() []Line {
				full := fullBundle()
				return []Line{versionLine(), spaceLine(), pageLine(), full.lines[6], full.lines[5], sentinelLine()}
			}(),
			wantErr: "is not an earlier comment",
		},
		{
			name: "duplicate attachment source id across pages",
			lines: func() []Line {
				full := fullBundle()
				home, second := full.lines[2], full.lines[3]
				second.Page.Attachments = []AttachmentData{{
					Path: AttachmentBundlePath(fixtureChildPageID, fixtureDiagramID, "diagram.png"),
					Props: map[string]any{
						PropImportSourceID:              fixtureDiagramID,
						PropConfluenceContainerSourceID: fixtureChildPageID,
						PropFilename:                    "diagram.png",
						PropMediaType:                   "image/png",
						PropSize:                        1,
						PropSHA256:                      strings.Repeat("a", 64),
					},
				}}
				return []Line{versionLine(), spaceLine(), home, second, sentinelLine()}
			}(),
			wantErr: "duplicate attachment import_source_id",
		},
		{
			name: "attachment sha256 is not lowercase hex",
			lines: func() []Line {
				home := fullBundle().lines[2]
				home.Page.Attachments[0].Props[PropSHA256] = strings.ToUpper(strings.Repeat("a", 64))
				return []Line{versionLine(), spaceLine(), home, sentinelLine()}
			}(),
			wantErr: "sha256 must be 64 lowercase hex characters",
		},
		{
			name: "attachment size is negative",
			lines: func() []Line {
				home := fullBundle().lines[2]
				home.Page.Attachments[0].Props[PropSize] = -1
				return []Line{versionLine(), spaceLine(), home, sentinelLine()}
			}(),
			wantErr: "size is -1, want >= 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateLines(test.lines)
			require.Error(t, err)
			require.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestCheckAttachmentPath(t *testing.T) {
	const page, attachment = "262145", "393217"

	valid := AttachmentBundlePath(page, attachment, "diagram.png")
	require.Equal(t, "data/262145/393217/diagram.png", valid)
	require.NoError(t, checkAttachmentPath(valid, page, attachment))

	rejected := map[string]string{
		"":                                  "path is empty",
		"/data/262145/393217/x.png":         "is absolute",
		"data/262145/393217/../../../x.png": "not in cleaned form",
		`data\262145\393217\x.png`:          "contains a backslash",
		"data/262145/393217/sub/x.png":      "exactly 4 segments",
		"data/262145/x.png":                 "exactly 4 segments",
		"other/262145/393217/x.png":         `must start with "data"/`,
		"data/999999/393217/x.png":          `must name page "262145" in segment 2`,
		"data/262145/999999/x.png":          `must name attachment "393217" in segment 3`,
		"data/262145/393217/\x00.png":       "contains NUL",
	}
	for badPath, wantErr := range rejected {
		t.Run(fmt.Sprintf("rejects %q", badPath), func(t *testing.T) {
			err := checkAttachmentPath(badPath, page, attachment)
			require.Error(t, err)
			require.Contains(t, err.Error(), wantErr)
		})
	}
}

func TestValidateManifest(t *testing.T) {
	base := func() *Manifest {
		m := fullBundle().manifest
		m.Checksums = ManifestChecksums{
			JSONLSHA256:       strings.Repeat("a", 64),
			AttachmentsSHA256: strings.Repeat("b", 64),
		}
		return &m
	}

	require.NoError(t, ValidateManifest(base()))

	t.Run("rejects a manifest carrying errors", func(t *testing.T) {
		m := base()
		m.Errors = []Issue{{Code: ErrCodeContentDuplicateCanonical, Message: "two canonical objects"}}
		require.ErrorContains(t, ValidateManifest(m), "not importable")
	})

	t.Run("rejects unsorted warnings", func(t *testing.T) {
		m := base()
		m.Warnings[0], m.Warnings[1] = m.Warnings[1], m.Warnings[0]
		require.ErrorContains(t, ValidateManifest(m), "not sorted")
	})

	t.Run("rejects an unknown username proposal source", func(t *testing.T) {
		m := base()
		m.Users[0].UsernameProposalSource = "guessed"
		require.ErrorContains(t, ValidateManifest(m), "unknown username_proposal_source")
	})

	t.Run("rejects duplicate usernames", func(t *testing.T) {
		m := base()
		m.Users[1].MattermostUsername = m.Users[0].MattermostUsername
		require.ErrorContains(t, ValidateManifest(m), "duplicate mattermost_username")
	})

	t.Run("rejects duplicate account ids", func(t *testing.T) {
		m := base()
		m.Users[1].AccountID = m.Users[0].AccountID
		require.ErrorContains(t, ValidateManifest(m), "duplicate account_id")
	})

	t.Run("rejects an uppercase checksum", func(t *testing.T) {
		m := base()
		m.Checksums.JSONLSHA256 = strings.ToUpper(m.Checksums.JSONLSHA256)
		require.ErrorContains(t, ValidateManifest(m), "jsonl_sha256")
	})

	t.Run("rejects a missing fidelity statement", func(t *testing.T) {
		m := base()
		m.Fidelity.PageRestrictions = ""
		require.ErrorContains(t, ValidateManifest(m), "fidelity.page_restrictions is empty")
	})
}

func TestCheckManifestAgainstLines(t *testing.T) {
	t.Run("rejects an author the manifest omits", func(t *testing.T) {
		full := fullBundle()
		full.manifest.Users = full.manifest.Users[:2]
		full.manifest.Counts.UsersEmitted = 2

		err := checkManifestAgainstLines(&full.manifest, full.lines)
		require.ErrorContains(t, err, "not listed in manifest users")
	})

	t.Run("rejects a count that disagrees with the stream", func(t *testing.T) {
		full := fullBundle()
		full.manifest.Counts.CommentsEmitted = 99

		err := checkManifestAgainstLines(&full.manifest, full.lines)
		require.ErrorContains(t, err, "counts.comments_emitted is 99, stream contains 3")
	})
}
