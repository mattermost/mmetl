package confluence

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// updateFixtures regenerates services/confluence/testdata/contract from the
// builders below. The fixtures are committed so they can be reviewed as plain
// files and copied verbatim into mattermost-plugin-docs, which must accept the
// exact same bytes.
//
//	go test ./services/confluence/... -run TestContractFixtures -update
var updateFixtures = flag.Bool("update", false, "regenerate the contract golden fixtures")

// fixtureClock is the fixed manifest timestamp. created_at is the only
// intentionally variable manifest field, so goldens pin it.
var fixtureClock = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

const (
	fixtureGeneratorVersion = "0.0.0-fixture"
	fixtureOrganizationID   = "https://confluence.example.com"
	fixtureSpaceID          = "196611"
	fixtureSpaceKey         = "ENG"
	fixtureSpaceName        = "Engineering"
	fixtureTeam             = "engineering"
	fixtureExportFile       = "Confluence-export.zip"
	fixtureTimezoneID       = "Etc/UTC"

	fixtureHomePageID  = "262145"
	fixtureChildPageID = "262146"
	fixtureBlogPostID  = "262147"

	fixtureRootCommentID  = "327681"
	fixtureReplyCommentID = "327682"
	fixtureBlogCommentID  = "327683"

	fixtureDiagramID = "393217"
	fixtureLogoID    = "393218"

	// fixtureSpaceDescriptionID is the SpaceDescription container the logo
	// attachment originally hung from. It is remapped onto the source home page.
	fixtureSpaceDescriptionID = "196612"

	fixtureAuthorAccount = "557058:11111111-1111-4111-8111-111111111111"
	fixtureEditorAccount = "557058:22222222-2222-4222-8222-222222222222"
	fixtureGhostAccount  = "confluence-user-key-legacy-3"
)

// fixtureBundle is an in-memory bundle before it is rendered to files.
type fixtureBundle struct {
	lines    []Line
	manifest Manifest
	blobs    map[string][]byte
}

// render produces the exact on-disk bundle files, computing both contract
// checksums from the rendered bytes so a mutated fixture still carries a
// self-consistent manifest and fails only on the rule it is meant to exercise.
func (b *fixtureBundle) render(t *testing.T) map[string][]byte {
	t.Helper()

	var jsonl bytes.Buffer
	require.NoError(t, EncodeJSONL(&jsonl, b.lines))

	blobPaths := make([]string, 0, len(b.blobs))
	for p := range b.blobs {
		blobPaths = append(blobPaths, p)
	}
	sort.Strings(blobPaths)

	attachmentBlobs := make([]AttachmentBlob, 0, len(blobPaths))
	for _, p := range blobPaths {
		body := b.blobs[p]
		attachmentBlobs = append(attachmentBlobs, AttachmentBlob{
			Path: p,
			Size: int64(len(body)),
			Open: func() (io.ReadCloser, error) { return readCloser{bytes.NewReader(body)}, nil },
		})
	}
	attachmentsSum, err := AttachmentsSHA256(attachmentBlobs)
	require.NoError(t, err)

	manifest := b.manifest
	manifest.Checksums = ManifestChecksums{
		JSONLSHA256:       SHA256Hex(jsonl.Bytes()),
		AttachmentsSHA256: attachmentsSum,
	}
	manifestBytes, err := MarshalManifest(&manifest)
	require.NoError(t, err)

	files := map[string][]byte{
		JSONLFilename:    jsonl.Bytes(),
		ManifestFilename: manifestBytes,
	}
	maps.Copy(files, b.blobs)
	return files
}

// readCloser adapts a bytes.Reader for AttachmentBlob.Open.
type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

func minimalBundle() *fixtureBundle {
	version := BundleVersion
	return &fixtureBundle{
		blobs: map[string][]byte{},
		lines: []Line{
			{
				Type:    LineTypeVersion,
				Version: &version,
				Source:  &Source{OrganizationID: fixtureOrganizationID, SpaceID: fixtureSpaceID, SpaceKey: fixtureSpaceKey},
			},
			{
				Type: LineTypeSpace,
				Space: &SpaceData{
					Team:  fixtureTeam,
					Title: fixtureSpaceName,
					Props: map[string]any{
						PropImportSourceID:     fixtureSpaceID,
						PropImportSource:       SourceType,
						PropConfluenceSpaceKey: fixtureSpaceKey,
					},
				},
			},
			{
				Type: LineTypePage,
				Page: &PageData{
					Team:                fixtureTeam,
					SpaceImportSourceID: fixtureSpaceID,
					User:                "aline.turner",
					Title:               "Engineering Home",
					Content:             EmptyDocumentJSON,
					CreateAt:            1735689600000,
					UpdateAt:            1735689600000,
					Props: map[string]any{
						PropImportSourceID:            fixtureHomePageID,
						PropImportSource:              SourceType,
						PropConfluenceSpaceKey:        fixtureSpaceKey,
						PropConfluenceContentType:     ContentTypePage,
						PropConfluenceAuthorAccountID: fixtureAuthorAccount,
						PropImportLabels:              []any{},
						PropConfluenceLabels:          []any{},
						PropConfluenceRestrictions:    map[string]any{},
					},
				},
			},
			{Type: LineTypeResolveSpacePlaceholders, ResolveSpacePlaceholders: &ResolvePlaceholdersData{}},
		},
		manifest: Manifest{
			Version:          ManifestVersion,
			Generator:        Generator,
			GeneratorVersion: fixtureGeneratorVersion,
			CreatedAt:        fixtureClock,
			Source: ManifestSource{
				Type:           SourceType,
				OrganizationID: fixtureOrganizationID,
				SpaceID:        fixtureSpaceID,
				SpaceKey:       fixtureSpaceKey,
				SpaceName:      fixtureSpaceName,
				ExportFile:     fixtureExportFile,
				TimezoneID:     fixtureTimezoneID,
			},
			Target: ManifestTarget{Team: fixtureTeam},
			Counts: ManifestCounts{
				SpacesEmitted:   1,
				PagesDiscovered: 1,
				PagesEmitted:    1,
				UsersEmitted:    1,
			},
			Users: []ManifestUser{{
				AccountID:              fixtureAuthorAccount,
				ConfluenceUsername:     "aline.turner",
				DisplayName:            "Aline Turner",
				Email:                  "aline.turner@example.com",
				Active:                 true,
				MattermostUsername:     "aline.turner",
				UsernameProposalSource: UsernameProposalSourceUsername,
			}},
			Fidelity: fixtureFidelity(),
			Warnings: []Warning{},
			Errors:   []Issue{},
		},
	}
}

func fixtureFidelity() ManifestFidelity {
	return ManifestFidelity{
		Pages:            FidelityPagesImported,
		BlogPosts:        FidelityBlogPostsImportedAsPages,
		Comments:         FidelityCommentsImportedAsPosts,
		Attachments:      FidelityAttachmentsImported,
		Labels:           FidelityLabelsPreservedNotApplied,
		PageRestrictions: FidelityRestrictionsUnverified,
		SpacePermissions: FidelitySpacePermissionsNotImport,
		ExternalAuth:     FidelityExternalAuthPreserved,
	}
}

// fullBundle exercises every field in the contract: both content types, a page
// hierarchy, all three placeholder kinds, both attachment container shapes, a
// comment thread with a descendant, a resolved comment, labels, restrictions,
// all three user shapes, and a sorted warning list.
func fullBundle() *fixtureBundle {
	version := BundleVersion
	diagramBody := []byte("confluence fixture diagram bytes\n")
	logoBody := []byte("confluence fixture space logo bytes\n")

	diagramPath := AttachmentBundlePath(fixtureHomePageID, fixtureDiagramID, "diagram.png")
	logoPath := AttachmentBundlePath(fixtureHomePageID, fixtureLogoID, "space_logo.png")

	homeContent := fmt.Sprintf(`{"type":"doc","content":[`+
		`{"type":"paragraph","content":[{"type":"text","text":"See "},`+
		`{"type":"text","marks":[{"type":"link","attrs":{"href":"{{CONF_PAGE_ID:%s}}"}}],"text":"the child page"},`+
		`{"type":"text","text":" and ask "},`+
		`{"type":"mention","attrs":{"id":"{{CONF_USER_ID:%s}}","label":"Bram Okonkwo"}}]},`+
		`{"type":"image","attrs":{"src":"{{CONF_ATTACHMENT_ID:%s}}","alt":"diagram.png"}}]}`,
		fixtureChildPageID, fixtureEditorAccount, fixtureDiagramID)

	childContent := `{"type":"doc","content":[{"type":"heading","attrs":{"level":2},` +
		`"content":[{"type":"text","text":"Runbook"}]},` +
		`{"type":"paragraph","content":[{"type":"text","marks":[{"type":"bold"}],"text":"Restricted"},` +
		`{"type":"text","text":" page body."}]}]}`

	blogContent := `{"type":"doc","content":[{"type":"paragraph","content":` +
		`[{"type":"text","text":"Quarterly engineering update."}]}]}`

	return &fixtureBundle{
		blobs: map[string][]byte{
			diagramPath: diagramBody,
			logoPath:    logoBody,
		},
		lines: []Line{
			{
				Type:    LineTypeVersion,
				Version: &version,
				Source:  &Source{OrganizationID: fixtureOrganizationID, SpaceID: fixtureSpaceID, SpaceKey: fixtureSpaceKey},
			},
			{
				Type: LineTypeSpace,
				Space: &SpaceData{
					Team:        fixtureTeam,
					Title:       fixtureSpaceName,
					Description: "Engineering handbooks, runbooks, and release notes.",
					Props: map[string]any{
						PropImportSourceID:      fixtureSpaceID,
						PropImportSource:        SourceType,
						PropConfluenceSpaceKey:  fixtureSpaceKey,
						"confluence_space_name": fixtureSpaceName,
					},
				},
			},
			{
				Type: LineTypePage,
				Page: &PageData{
					Team:                fixtureTeam,
					SpaceImportSourceID: fixtureSpaceID,
					User:                "aline.turner",
					Title:               "Engineering Home",
					Content:             homeContent,
					CreateAt:            1735689600000,
					UpdateAt:            1738368000000,
					Props: map[string]any{
						PropImportSourceID:            fixtureHomePageID,
						PropImportSource:              SourceType,
						PropConfluenceSpaceKey:        fixtureSpaceKey,
						PropConfluenceContentType:     ContentTypePage,
						PropConfluenceAuthorAccountID: fixtureAuthorAccount,
						PropImportLabels:              []any{"handbook"},
						PropConfluenceLabels: []any{
							map[string]any{"name": "handbook", "namespace": "global"},
						},
						PropConfluenceRestrictions: map[string]any{},
					},
					Attachments: []AttachmentData{
						{
							Path: diagramPath,
							Props: map[string]any{
								PropImportSourceID:              fixtureDiagramID,
								PropConfluenceContainerSourceID: fixtureHomePageID,
								PropFilename:                    "diagram.png",
								PropMediaType:                   "image/png",
								PropSize:                        len(diagramBody),
								PropSHA256:                      SHA256Hex(diagramBody),
							},
						},
						{
							// Originally attached to the space description and
							// remapped onto the source home page.
							Path: logoPath,
							Props: map[string]any{
								PropImportSourceID:              fixtureLogoID,
								PropConfluenceContainerSourceID: fixtureSpaceDescriptionID,
								PropFilename:                    "space logo.png",
								PropMediaType:                   "image/png",
								PropSize:                        len(logoBody),
								PropSHA256:                      SHA256Hex(logoBody),
							},
						},
					},
				},
			},
			{
				Type: LineTypePage,
				Page: &PageData{
					Team:                 fixtureTeam,
					SpaceImportSourceID:  fixtureSpaceID,
					User:                 "bram.okonkwo",
					Title:                "Incident Runbook",
					Content:              childContent,
					ParentImportSourceID: fixtureHomePageID,
					CreateAt:             1735776000000,
					UpdateAt:             1735776000000,
					Props: map[string]any{
						PropImportSourceID:            fixtureChildPageID,
						PropImportSource:              SourceType,
						PropConfluenceSpaceKey:        fixtureSpaceKey,
						PropConfluenceContentType:     ContentTypePage,
						PropConfluenceAuthorAccountID: fixtureEditorAccount,
						PropImportLabels:              []any{"runbook", "oncall"},
						PropConfluenceLabels: []any{
							map[string]any{"name": "oncall", "namespace": "team"},
							map[string]any{"name": "runbook", "namespace": "global"},
						},
						// Synthetic: no real restricted-page XML fixture exists yet,
						// so extraction stays unverified while storage is exercised.
						PropConfluenceRestrictions: map[string]any{
							"view_users":  []any{fixtureAuthorAccount, fixtureEditorAccount},
							"view_groups": []any{"confluence-engineering"},
							"edit_users":  []any{fixtureEditorAccount},
							"edit_groups": []any{},
						},
					},
				},
			},
			{
				Type: LineTypePage,
				Page: &PageData{
					Team:                fixtureTeam,
					SpaceImportSourceID: fixtureSpaceID,
					User:                "confluence_user_9f86d081884c",
					Title:               "Q1 Engineering Update",
					Content:             blogContent,
					CreateAt:            1736380800000,
					UpdateAt:            1736380800000,
					Props: map[string]any{
						PropImportSourceID:            fixtureBlogPostID,
						PropImportSource:              SourceType,
						PropConfluenceSpaceKey:        fixtureSpaceKey,
						PropConfluenceContentType:     ContentTypeBlogPost,
						PropConfluenceAuthorAccountID: fixtureGhostAccount,
						PropImportLabels:              []any{},
						PropConfluenceLabels:          []any{},
						PropConfluenceRestrictions:    map[string]any{},
					},
				},
			},
			{
				Type: LineTypePageComment,
				PageComment: &PageCommentData{
					PageImportSourceID:       fixtureHomePageID,
					ThreadRootImportSourceID: fixtureRootCommentID,
					User:                     "bram.okonkwo",
					Content:                  "Should we split the runbook out of this page?",
					CreateAt:                 1735862400000,
					UpdateAt:                 1735862400000,
					Props: map[string]any{
						PropImportSourceID:            fixtureRootCommentID,
						PropImportSource:              SourceType,
						PropConfluenceAuthorAccountID: fixtureEditorAccount,
					},
				},
			},
			{
				Type: LineTypePageComment,
				PageComment: &PageCommentData{
					PageImportSourceID:          fixtureHomePageID,
					ParentCommentImportSourceID: fixtureRootCommentID,
					ThreadRootImportSourceID:    fixtureRootCommentID,
					User:                        "aline.turner",
					Content:                     "Done, it lives at Incident Runbook now.",
					CreateAt:                    1735866000000,
					UpdateAt:                    1735866000000,
					Props: map[string]any{
						PropImportSourceID:            fixtureReplyCommentID,
						PropImportSource:              SourceType,
						PropConfluenceAuthorAccountID: fixtureAuthorAccount,
					},
				},
			},
			{
				Type: LineTypePageComment,
				PageComment: &PageCommentData{
					PageImportSourceID:       fixtureBlogPostID,
					ThreadRootImportSourceID: fixtureBlogCommentID,
					User:                     "aline.turner",
					Content:                  "Nice write-up.",
					CreateAt:                 1736467200000,
					UpdateAt:                 1736467200000,
					IsResolved:               true,
					Props: map[string]any{
						PropImportSourceID:            fixtureBlogCommentID,
						PropImportSource:              SourceType,
						PropConfluenceAuthorAccountID: fixtureAuthorAccount,
					},
				},
			},
			{Type: LineTypeResolveSpacePlaceholders, ResolveSpacePlaceholders: &ResolvePlaceholdersData{}},
		},
		manifest: Manifest{
			Version:          ManifestVersion,
			Generator:        Generator,
			GeneratorVersion: fixtureGeneratorVersion,
			CreatedAt:        fixtureClock,
			Source: ManifestSource{
				Type:           SourceType,
				OrganizationID: fixtureOrganizationID,
				SpaceID:        fixtureSpaceID,
				SpaceKey:       fixtureSpaceKey,
				SpaceName:      fixtureSpaceName,
				ExportFile:     fixtureExportFile,
				TimezoneID:     fixtureTimezoneID,
			},
			Target: ManifestTarget{Team: fixtureTeam},
			Counts: ManifestCounts{
				SpacesEmitted:            1,
				PagesDiscovered:          3,
				PagesEmitted:             2,
				PagesSkipped:             1,
				BlogPostsEmitted:         1,
				CommentsDiscovered:       5,
				CommentsEmitted:          3,
				CommentsSkipped:          2,
				AttachmentsDiscovered:    3,
				AttachmentsEmitted:       2,
				AttachmentsSkipped:       1,
				UsersEmitted:             3,
				UsersInactive:            1,
				UsersPlaceholderEmail:    1,
				LabelsPreserved:          3,
				RestrictedPagesPreserved: 1,
				PagesFlattened:           1,
				MissingBodies:            1,
			},
			Users: []ManifestUser{
				{
					AccountID:              fixtureAuthorAccount,
					ConfluenceUserKey:      "8a7f808e6d1c1a02016d1c1a0a2b0001",
					ConfluenceUsername:     "aline.turner",
					DisplayName:            "Aline Turner",
					Email:                  "aline.turner@example.com",
					ExternalID:             "ldap://example/uid=aline",
					Active:                 true,
					MattermostUsername:     "aline.turner",
					EmailIsPlaceholder:     false,
					UsernameProposalSource: UsernameProposalSourceUsername,
				},
				{
					AccountID:              fixtureEditorAccount,
					ConfluenceUserKey:      "8a7f808e6d1c1a02016d1c1a0a2b0002",
					ConfluenceUsername:     "bokonkwo",
					DisplayName:            "Bram Okonkwo",
					Email:                  "bram.okonkwo@example.com",
					Active:                 true,
					MattermostUsername:     "bram.okonkwo",
					EmailIsPlaceholder:     false,
					UsernameProposalSource: UsernameProposalExplicitMapping,
				},
				{
					AccountID:              fixtureGhostAccount,
					ConfluenceUserKey:      fixtureGhostAccount,
					DisplayName:            "Former Employee",
					Email:                  "confluence-9f86d081884c7d659a2f@users.invalid",
					Active:                 false,
					MattermostUsername:     "confluence_user_9f86d081884c",
					EmailIsPlaceholder:     true,
					UsernameProposalSource: UsernameProposalFallback,
				},
			},
			Fidelity: fixtureFidelity(),
			Warnings: []Warning{
				{
					Code:       WarnAttachmentBlobMissing,
					EntityType: "attachment",
					SourceID:   "393219",
					Message:    "attachment blob is absent from the source archive; attachment skipped",
				},
				{
					Code:       WarnCommentParentMissingSkip,
					EntityType: "comment",
					SourceID:   "327690",
					Message:    "parent comment was not emitted; comment and its descendants skipped",
				},
				{
					Code:       WarnPageContentTooLarge,
					EntityType: "page",
					SourceID:   "262148",
					Message:    "converted body exceeds the destination page body limit; page skipped",
				},
				{
					Code:       WarnPageDepthFlattened,
					EntityType: "page",
					SourceID:   fixtureChildPageID,
					Message:    "source depth exceeded the destination maximum; page reparented to the nearest emitted ancestor",
				},
				{
					Code:       WarnPageMissingBody,
					EntityType: "page",
					SourceID:   fixtureBlogPostID,
					Message:    "no body content found; emitted an empty page",
				},
				{
					Code:       WarnRestrictionUnverified,
					EntityType: "page",
					SourceID:   fixtureChildPageID,
					Message:    "page restrictions are preserved as metadata and are not enforced; extraction is unverified",
				},
				{
					Code:       WarnUserPlaceholderEmail,
					EntityType: "user",
					SourceID:   fixtureGhostAccount,
					Message:    "source user has no email; a deterministic placeholder was generated",
				},
			},
			Errors: []Issue{},
		},
	}
}

// contractFixtures is the complete fixture set. Both repositories must agree on
// every entry: valid ones are accepted, invalid ones are rejected for the named
// reason.
func contractFixtures(t *testing.T) []contractFixture {
	t.Helper()

	unsafePath := fullBundle()
	// Rewrite only the declared path; the blob stays at its safe location so
	// path validation, not a missing file, is what rejects the bundle.
	unsafePath.lines[2].Page.Attachments[0].Path = "data/" + fixtureHomePageID + "/../../etc/passwd"

	badParent := fullBundle()
	badParent.lines[3].Page.ParentImportSourceID = "999999"

	duplicateIDs := fullBundle()
	duplicateIDs.lines[3].Page.Props[PropImportSourceID] = fixtureHomePageID

	sequence := fullBundle()
	// Move the sentinel ahead of the comments.
	sequence.lines = append(sequence.lines[:5:5], append([]Line{sequence.lines[len(sequence.lines)-1]}, sequence.lines[5:len(sequence.lines)-1]...)...)

	missingSentinel := fullBundle()
	missingSentinel.lines = missingSentinel.lines[:len(missingSentinel.lines)-1]

	duplicateSentinel := fullBundle()
	duplicateSentinel.lines = append(duplicateSentinel.lines, Line{
		Type: LineTypeResolveSpacePlaceholders, ResolveSpacePlaceholders: &ResolvePlaceholdersData{},
	})

	missingAttachment := fullBundle()
	delete(missingAttachment.blobs, AttachmentBundlePath(fixtureHomePageID, fixtureDiagramID, "diagram.png"))

	return []contractFixture{
		{name: "minimal", bundle: minimalBundle()},
		{name: "full", bundle: fullBundle()},
		{name: "invalid-sequence", bundle: sequence, wantErr: "must be the final line"},
		{name: "invalid-missing-sentinel", bundle: missingSentinel, wantErr: "missing trailing"},
		{name: "invalid-duplicate-sentinel", bundle: duplicateSentinel, wantErr: "must be the final line"},
		{name: "invalid-duplicate-ids", bundle: duplicateIDs, wantErr: "duplicate page import_source_id"},
		{name: "invalid-bad-parent", bundle: badParent, wantErr: "which is not an earlier page"},
		{name: "invalid-unsafe-path", bundle: unsafePath, wantErr: "not in cleaned form"},
		{name: "invalid-missing-attachment", bundle: missingAttachment, wantErr: "missing declared attachment blob"},
		{name: "invalid-bad-checksum", bundle: fullBundle(), corruptChecksum: true, wantErr: "checksum is"},
	}
}

type contractFixture struct {
	name   string
	bundle *fixtureBundle
	// corruptChecksum rewrites the rendered manifest checksum after rendering,
	// which no line-level mutation can express.
	corruptChecksum bool
	wantErr         string
}

func (f contractFixture) files(t *testing.T) map[string][]byte {
	t.Helper()

	files := f.bundle.render(t)
	if f.corruptChecksum {
		manifest, err := DecodeManifest(bytes.NewReader(files[ManifestFilename]))
		require.NoError(t, err)
		manifest.Checksums.JSONLSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		manifestBytes, err := MarshalManifest(manifest)
		require.NoError(t, err)
		files[ManifestFilename] = manifestBytes
	}
	return files
}

func fixtureDir(name string) string {
	return filepath.Join("testdata", "contract", name)
}

// TestContractFixtures keeps the committed fixtures byte-identical to the
// builders above. Run with -update to regenerate them.
func TestContractFixtures(t *testing.T) {
	for _, fixture := range contractFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			dir := fixtureDir(fixture.name)
			files := fixture.files(t)

			if *updateFixtures {
				require.NoError(t, os.RemoveAll(dir))
				for name, body := range files {
					target := filepath.Join(dir, filepath.FromSlash(name))
					require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o750))
					require.NoError(t, os.WriteFile(target, body, 0o600))
				}
				return
			}

			for name, want := range files {
				// The path is built from this test's own fixture names.
				got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))) //nolint:gosec
				require.NoErrorf(t, err, "fixture %s/%s is missing; rerun with -update", fixture.name, name)
				require.Equalf(t, string(want), string(got),
					"fixture %s/%s drifted from the builder; rerun with -update", fixture.name, name)
			}
			require.ElementsMatch(t, sortedKeys(files), walkFixtureFiles(t, dir),
				"fixture %s has files the builder does not produce", fixture.name)
		})
	}
}

func sortedKeys(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func walkFixtureFiles(t *testing.T, dir string) []string {
	t.Helper()

	var names []string
	require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	}))
	sort.Strings(names)
	return names
}

func TestJSONLRoundTripIsByteStable(t *testing.T) {
	original := fullBundle().lines

	var first bytes.Buffer
	require.NoError(t, EncodeJSONL(&first, original))

	decoded, err := DecodeJSONL(bytes.NewReader(first.Bytes()))
	require.NoError(t, err)
	require.Len(t, decoded, len(original))

	var second bytes.Buffer
	require.NoError(t, EncodeJSONL(&second, decoded))
	require.Equal(t, first.String(), second.String())

	require.NoError(t, ValidateLines(decoded))
}

func TestDecodeJSONLRejectsUnknownFields(t *testing.T) {
	_, err := DecodeJSONL(strings.NewReader(`{"type":"version","version":2,"tenant":"x"}` + "\n"))
	require.ErrorContains(t, err, "tenant")
}

// TestCanonicalEncodingDoesNotEscapeHTML pins the canonical encoding. Go's
// default encoder escapes &, < and >, which would change the bundle bytes and
// therefore every checksum, so the difference is asserted explicitly.
func TestCanonicalEncodingDoesNotEscapeHTML(t *testing.T) {
	value := map[string]any{"title": `a & b <c>`}

	defaultEncoded, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, `{"title":"a \u0026 b \u003cc\u003e"}`, string(defaultEncoded),
		"sanity check: this is the escaping the contract must not use")

	encoded, err := MarshalCanonical(value)
	require.NoError(t, err)
	require.Equal(t, `{"title":"a & b <c>"}`, string(encoded))
}

// TestCanonicalEncodingSortsObjectKeys pins the other half of canonical
// encoding: every dynamic object in the contract is a Go map, so key order is
// deterministic without a custom encoder.
func TestCanonicalEncodingSortsObjectKeys(t *testing.T) {
	encoded, err := MarshalCanonical(map[string]any{"zeta": 1, "alpha": 2, "mu": 3})
	require.NoError(t, err)
	require.Equal(t, `{"alpha":2,"mu":3,"zeta":1}`, string(encoded))
}

func TestAttachmentsSHA256Framing(t *testing.T) {
	blob := func(path, body string) AttachmentBlob {
		return AttachmentBlob{
			Path: path,
			Size: int64(len(body)),
			Open: func() (io.ReadCloser, error) { return readCloser{bytes.NewReader([]byte(body))}, nil },
		}
	}

	t.Run("empty attachment set hashes the empty stream", func(t *testing.T) {
		got, err := AttachmentsSHA256(nil)
		require.NoError(t, err)
		require.Equal(t, SHA256Hex(nil), got)
	})

	t.Run("order of the input slice does not matter", func(t *testing.T) {
		a := blob("data/1/2/a.txt", "alpha")
		b := blob("data/1/3/b.txt", "beta")

		forward, err := AttachmentsSHA256([]AttachmentBlob{a, b})
		require.NoError(t, err)
		reverse, err := AttachmentsSHA256([]AttachmentBlob{b, a})
		require.NoError(t, err)
		require.Equal(t, forward, reverse)
	})

	// Without the length prefixes required by section 12.6, these two inputs
	// would concatenate to the same byte stream and collide.
	t.Run("length framing separates equal concatenations", func(t *testing.T) {
		first, err := AttachmentsSHA256([]AttachmentBlob{blob("data/1/2/ab.txt", "")})
		require.NoError(t, err)
		second, err := AttachmentsSHA256([]AttachmentBlob{blob("data/1/2/a", "b.txt")})
		require.NoError(t, err)
		require.NotEqual(t, first, second)
	})

	t.Run("declared size must match the bytes read", func(t *testing.T) {
		short := blob("data/1/2/a.txt", "alpha")
		short.Size = 3
		_, err := AttachmentsSHA256([]AttachmentBlob{short})
		require.ErrorContains(t, err, "declared 3 bytes, read 4")
	})
}

func TestSortWarningsIsTotalAndStable(t *testing.T) {
	warnings := []Warning{
		{Code: WarnPageMissingBody, EntityType: "page", SourceID: "2", Message: "b"},
		{Code: WarnPageMissingBody, EntityType: "page", SourceID: "1", Message: "a"},
		{Code: WarnAttachmentBlobMissing, EntityType: "attachment", SourceID: "9", Message: "z"},
		{Code: WarnPageMissingBody, EntityType: "comment", SourceID: "1", Message: "a"},
	}
	SortWarnings(warnings)

	require.Equal(t, []Warning{
		{Code: WarnAttachmentBlobMissing, EntityType: "attachment", SourceID: "9", Message: "z"},
		{Code: WarnPageMissingBody, EntityType: "comment", SourceID: "1", Message: "a"},
		{Code: WarnPageMissingBody, EntityType: "page", SourceID: "1", Message: "a"},
		{Code: WarnPageMissingBody, EntityType: "page", SourceID: "2", Message: "b"},
	}, warnings)
}

func TestTruncateMessageDoesNotSplitRunes(t *testing.T) {
	require.Equal(t, "short", TruncateMessage("short"))

	got := TruncateMessage(strings.Repeat("é", MessageMaxBytes))
	require.LessOrEqual(t, len(got), MessageMaxBytes)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, strings.Repeat("é", MessageMaxBytes/2), got)
}
