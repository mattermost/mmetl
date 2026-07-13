// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// ErrUnsupportedExportFormat is returned when the ZIP is not a recognized
// Confluence Cloud CSV space export.
var ErrUnsupportedExportFormat = errors.New("unsupported export: expected a Confluence Cloud CSV export (content.csv or exportDescriptor.properties with exportFormat=csv)")

// CSV export file names (Confluence Cloud space export in CSV form). Files may be
// suffixed with ".gz"; in observed exports they are frequently plain text despite
// the suffix, so readers sniff the gzip magic rather than trust the extension.
const (
	fileDescriptor        = "exportDescriptor.properties"
	fileContent           = "content.csv"
	fileBodyContent       = "bodycontent.csv"
	fileSpaces            = "spaces.csv"
	fileUserMapping       = "user_mapping.csv"
	fileContentProperties = "contentproperties.csv"
	fileLabel             = "label.csv"
	fileContentLabel      = "content_label.csv"
	fileContentPermSet    = "content_perm_set.csv"
)

// Confluence content types (content.csv "contenttype" column).
const (
	contentTypePage             = "PAGE"
	contentTypeComment          = "COMMENT"
	contentTypeAttachment       = "ATTACHMENT"
	contentTypeSpaceDescription = "SPACEDESCRIPTION"
	contentTypeCustom           = "CUSTOM"
)

// csvTable is a parsed CSV file with a header→index map for column lookup by
// name, so the parser is resilient to column reordering across Cloud versions.
type csvTable struct {
	header map[string]int
	rows   [][]string
}

// col returns the trimmed value of the named column for the given row, or "" if
// the column or value is absent.
func (tb *csvTable) col(row []string, name string) string {
	i, ok := tb.header[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// firstCol returns the first of the candidate names present in the header, or ""
// if none are present. Used to tolerate schema variation across Cloud versions.
func (tb *csvTable) firstCol(names ...string) string {
	for _, n := range names {
		if _, ok := tb.header[n]; ok {
			return n
		}
	}
	return ""
}

// findFile returns the zip entry for base, trying both the plain name and the
// ".gz" suffix.
func findFile(fileIndex map[string]*zip.File, base string) *zip.File {
	if f, ok := fileIndex[base]; ok {
		return f
	}
	if f, ok := fileIndex[base+".gz"]; ok {
		return f
	}
	return nil
}

// isCSVExport reports whether the ZIP looks like a Confluence Cloud CSV export.
func isCSVExport(fileIndex map[string]*zip.File) bool {
	if findFile(fileIndex, fileContent) != nil {
		return true
	}
	if f := findFile(fileIndex, fileDescriptor); f != nil {
		if props, err := readProperties(f); err == nil && props["exportFormat"] == "csv" {
			return true
		}
	}
	return false
}

// readRawBytes reads a zip entry fully, transparently gunzipping when the content
// starts with the gzip magic bytes (0x1f 0x8b) regardless of the file extension.
func readRawBytes(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gr, gzErr := gzip.NewReader(bytes.NewReader(data))
		if gzErr != nil {
			return nil, gzErr
		}
		defer gr.Close()
		return io.ReadAll(gr)
	}

	return data, nil
}

// readProperties parses a Java .properties file (k=v lines, # comments) from a
// zip entry.
func readProperties(f *zip.File) (map[string]string, error) {
	data, err := readRawBytes(f)
	if err != nil {
		return nil, err
	}
	props := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			props[key] = val
		}
	}
	return props, nil
}

// readCSV parses a (possibly gzipped) CSV zip entry into a csvTable. The first
// row is treated as the header. Returns nil (no error) when the file is absent.
func readCSV(fileIndex map[string]*zip.File, base string) (*csvTable, error) {
	f := findFile(fileIndex, base)
	if f == nil {
		return nil, nil
	}

	data, err := readRawBytes(f)
	if err != nil {
		return nil, err
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1 // rows may have trailing empties
	reader.LazyQuotes = true    // tolerate stray quotes inside large HTML bodies

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return &csvTable{header: map[string]int{}}, nil
	}

	header := make(map[string]int, len(records[0]))
	for i, name := range records[0] {
		header[strings.TrimSpace(name)] = i
	}
	return &csvTable{header: header, rows: records[1:]}, nil
}

// parseCSVTimestamp parses Confluence CSV timestamps such as
// "2024-05-31 10:03:18.78". Confluence emits these in the export's timezone
// (UTC per exportDescriptor); a zero time is returned for empty/unparseable input.
func parseCSVTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if ts, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// parseCSVExport populates export from a Confluence Cloud CSV export. It emits
// the same ConfluenceExport shape as the (removed) XML parser so all downstream
// transform/export code is reused unchanged.
func (t *Transformer) parseCSVExport(fileIndex map[string]*zip.File, export *ConfluenceExport) error {
	// Descriptor (optional) — space key, org id, and export metadata. The org id
	// namespaces source IDs across Confluence instances.
	if f := findFile(fileIndex, fileDescriptor); f != nil {
		if props, err := readProperties(f); err == nil {
			if key := props["spaceKey"]; key != "" && export.SpaceKey == "" {
				export.SpaceKey = key
			}
			if org := props["organizationId"]; org != "" {
				export.OrganizationID = org
			}
		}
	}

	if err := t.loadSpaces(fileIndex, export); err != nil {
		return err
	}

	userKeyToAaid, err := t.loadUsers(fileIndex, export)
	if err != nil {
		return err
	}

	if err := t.loadContent(fileIndex, export, userKeyToAaid); err != nil {
		return err
	}

	if err := t.loadBodies(fileIndex, export); err != nil {
		return err
	}

	if err := t.loadContentProperties(fileIndex, export); err != nil {
		return err
	}

	if err := t.loadLabels(fileIndex, export); err != nil {
		return err
	}

	if err := t.loadRestrictions(fileIndex, export); err != nil {
		return err
	}

	return nil
}

// loadSpaces reads spaces.csv into export.Spaces (and the legacy single-space
// fields). The homepage column gives the authoritative root page id.
func (t *Transformer) loadSpaces(fileIndex map[string]*zip.File, export *ConfluenceExport) error {
	tb, err := readCSV(fileIndex, fileSpaces)
	if err != nil {
		return err
	}
	if tb == nil {
		return nil
	}
	for _, row := range tb.rows {
		key := tb.col(row, "spacekey")
		if key == "" {
			continue
		}
		info := &SpaceInfo{
			Key:        key,
			Name:       tb.col(row, "spacename"),
			HomePageID: tb.col(row, "homepage"),
		}
		export.Spaces[key] = info
		// Populate legacy single-space fields from the first (or descriptor) space.
		if export.SpaceKey == "" || export.SpaceKey == key {
			export.SpaceKey = key
			export.SpaceName = info.Name
		}
	}
	return nil
}

// loadUsers reads user_mapping.csv and returns a user_key→aaid map. It also
// populates export.Users keyed by BOTH the aaid and the user_key (aliased to the
// same record) so that (a) creator/lastmodifier references translated to aaid
// resolve, and (b) body @-mentions — which embed either ri:account-id (=aaid) or
// ri:userkey — both resolve via ResolveUserMentions.
func (t *Transformer) loadUsers(fileIndex map[string]*zip.File, export *ConfluenceExport) (map[string]string, error) {
	userKeyToAaid := make(map[string]string)

	tb, err := readCSV(fileIndex, fileUserMapping)
	if err != nil {
		return nil, err
	}
	if tb == nil {
		return userKeyToAaid, nil
	}

	for _, row := range tb.rows {
		userKey := tb.col(row, "user_key")
		if userKey == "" {
			continue
		}
		aaid := tb.col(row, "aaid")
		username := tb.col(row, "username")
		if aaid == "" {
			aaid = userKey
		}
		userKeyToAaid[userKey] = aaid

		user := &ConfluenceUser{
			AccountID: aaid,
			Username:  username,
		}
		export.Users[aaid] = user
		// Alias by user_key so ri:userkey mentions resolve without a links.go change.
		if userKey != aaid {
			export.Users[userKey] = user
		}
	}
	return userKeyToAaid, nil
}

// resolveActor translates a content.csv creator/lastmodifier user_key to its
// aaid (falling back to the raw value when unmapped) so downstream username
// resolution and the account-ID-keyed --user-mapping CSV work unchanged.
func resolveActor(userKey string, userKeyToAaid map[string]string) string {
	if userKey == "" {
		return ""
	}
	if aaid, ok := userKeyToAaid[userKey]; ok {
		return aaid
	}
	return userKey
}

// loadContent reads content.csv and builds pages and comments. Historical/draft
// discrimination follows the CSV rules: a PAGE row is a historical version when
// content_status=current but spaceid is blank (its prevver points at the
// canonical page); such ids are recorded in HistoricalPageIDs and filtered by
// TransformPages. CUSTOM and SPACEDESCRIPTION rows are dropped.
func (t *Transformer) loadContent(fileIndex map[string]*zip.File, export *ConfluenceExport, userKeyToAaid map[string]string) error {
	tb, err := readCSV(fileIndex, fileContent)
	if err != nil {
		return err
	}
	if tb == nil {
		return ErrUnsupportedExportFormat
	}

	for _, row := range tb.rows {
		id := tb.col(row, "contentid")
		if id == "" {
			continue
		}
		switch tb.col(row, "contenttype") {
		case contentTypePage:
			page := &ConfluencePage{
				ID:            id,
				Title:         tb.col(row, "title"),
				ParentID:      tb.col(row, "parentid"),
				SpaceKey:      export.SpaceKey,
				CreatedBy:     resolveActor(tb.col(row, "creator"), userKeyToAaid),
				CreatedAt:     parseCSVTimestamp(tb.col(row, "creationdate")),
				UpdatedBy:     resolveActor(tb.col(row, "lastmodifier"), userKeyToAaid),
				UpdatedAt:     parseCSVTimestamp(tb.col(row, "lastmoddate")),
				Version:       parsePositionValue(tb.col(row, "version")),
				Position:      parsePositionValue(tb.col(row, "child_position")),
				ContentStatus: tb.col(row, "content_status"),
			}
			// Historical version: current-status PAGE with no space linkage.
			if page.ContentStatus == "current" && tb.col(row, "spaceid") == "" {
				export.HistoricalPageIDs[id] = true
				page.OriginalVersionID = tb.col(row, "prevver")
			}
			export.Pages = append(export.Pages, page)

		case contentTypeComment:
			comment := &ConfluenceComment{
				ID:        id,
				PageID:    tb.col(row, "pageid"),
				ParentID:  tb.col(row, "parentcommentid"),
				CreatedBy: resolveActor(tb.col(row, "creator"), userKeyToAaid),
				CreatedAt: parseCSVTimestamp(tb.col(row, "creationdate")),
				UpdatedBy: resolveActor(tb.col(row, "lastmodifier"), userKeyToAaid),
				UpdatedAt: parseCSVTimestamp(tb.col(row, "lastmoddate")),
			}
			// Version rows (prevver set) are historical edits of a live comment.
			if prevver := tb.col(row, "prevver"); prevver != "" {
				export.HistoricalCommentIDs[id] = true
			}
			export.Comments = append(export.Comments, comment)

		case contentTypeAttachment:
			// ATTACHMENT rows carry attachment metadata; the file bytes live under
			// attachments/ in the ZIP (see attachments.go). MediaType/FileSize are
			// filled best-effort from contentproperties.csv when present.
			pageID := tb.col(row, "pageid")
			if pageID == "" {
				pageID = tb.col(row, "parentid")
			}
			att := &ConfluenceAttachment{
				ID:        id,
				PageID:    pageID,
				FileName:  tb.col(row, "title"),
				CreatedBy: resolveActor(tb.col(row, "creator"), userKeyToAaid),
				CreatedAt: parseCSVTimestamp(tb.col(row, "creationdate")),
			}
			if pageID != "" {
				export.Attachments[pageID] = append(export.Attachments[pageID], att)
			}

		default:
			// SPACEDESCRIPTION and CUSTOM (plugin content-property storage) are not imported.
		}
	}

	return nil
}

// loadBodies reads bodycontent.csv and attaches each storage-format body to its
// page/comment by contentid. Bodies are indexed once to avoid an O(n²) join.
func (t *Transformer) loadBodies(fileIndex map[string]*zip.File, export *ConfluenceExport) error {
	tb, err := readCSV(fileIndex, fileBodyContent)
	if err != nil {
		return err
	}
	if tb == nil {
		return nil
	}

	bodyByContentID := make(map[string]string, len(tb.rows))
	for _, row := range tb.rows {
		contentID := tb.col(row, "contentid")
		if contentID == "" {
			continue
		}
		bodyByContentID[contentID] = tb.col(row, "body")
	}
	export.BodyContents = bodyByContentID

	for _, page := range export.Pages {
		if body, ok := bodyByContentID[page.ID]; ok {
			page.Content = body
		}
	}
	for _, comment := range export.Comments {
		if body, ok := bodyByContentID[comment.ID]; ok {
			comment.Content = body
		}
	}
	return nil
}

// loadContentProperties reads contentproperties.csv and, for each comment,
// attaches inline-comment anchor and resolved-status data directly from the
// comment's own property rows (Cloud CSV exports carry these as first-class
// properties, so no body HTML scraping is required).
func (t *Transformer) loadContentProperties(fileIndex map[string]*zip.File, export *ConfluenceExport) error {
	tb, err := readCSV(fileIndex, fileContentProperties)
	if err != nil {
		return err
	}
	if tb == nil {
		return nil
	}

	type propSet struct {
		markerRef string
		selection string
		resolved  bool
	}
	byContentID := make(map[string]*propSet)
	get := func(id string) *propSet {
		if p, ok := byContentID[id]; ok {
			return p
		}
		p := &propSet{}
		byContentID[id] = p
		return p
	}

	for _, row := range tb.rows {
		contentID := tb.col(row, "contentid")
		if contentID == "" {
			continue
		}
		name := tb.col(row, "propertyname")
		val := tb.col(row, "stringval")
		switch name {
		case "inline-marker-ref":
			get(contentID).markerRef = val
		case "inline-original-selection":
			get(contentID).selection = val
		case "status":
			if val == "resolved" {
				get(contentID).resolved = true
			}
		}
	}

	for _, comment := range export.Comments {
		p, ok := byContentID[comment.ID]
		if !ok {
			continue
		}
		comment.IsResolved = p.resolved
		if p.markerRef != "" || p.selection != "" {
			comment.InlineAnchor = &InlineAnchor{
				AnchorID:          p.markerRef,
				OriginalSelection: p.selection,
			}
			if p.markerRef != "" {
				export.InlineCommentAnchors[p.markerRef] = p.selection
			}
		}
	}

	// Best-effort: fill attachment media-type/file-size from content properties.
	// The observed sample carries none, so absence is expected and harmless.
	attByID := make(map[string]*ConfluenceAttachment)
	for _, atts := range export.Attachments {
		for _, att := range atts {
			attByID[att.ID] = att
		}
	}
	if len(attByID) > 0 {
		for _, row := range tb.rows {
			att, ok := attByID[tb.col(row, "contentid")]
			if !ok {
				continue
			}
			switch tb.col(row, "propertyname") {
			case "media-type", "mediaType":
				att.MediaType = tb.col(row, "stringval")
			case "file-size", "fileSize":
				if n, err := strconv.ParseInt(tb.col(row, "longval"), 10, 64); err == nil {
					att.FileSize = n
				}
			}
		}
	}
	return nil
}

// loadRestrictions detects pages carrying a View restriction from
// content_perm_set.csv. It records content IDs only (principals are not
// resolved); the transform surfaces these as warnings and manifest entries so a
// view-restricted page is not silently widened on import. Column names are
// probed defensively because the CSV schema is unverified for restrictions.
func (t *Transformer) loadRestrictions(fileIndex map[string]*zip.File, export *ConfluenceExport) error {
	tb, err := readCSV(fileIndex, fileContentPermSet)
	if err != nil {
		return err
	}
	if tb == nil {
		return nil
	}

	typeCol := tb.firstCol("cps_type", "type", "permtype", "permission_type")
	idCol := tb.firstCol("cont_id", "content_id", "contentid", "contid")
	if idCol == "" {
		t.Logger.Warn("content_perm_set.csv present but no recognizable content-id column; skipping restriction detection")
		return nil
	}

	for _, row := range tb.rows {
		cid := tb.col(row, idCol)
		if cid == "" {
			continue
		}
		// When the type column is absent, treat any permission set as a possible
		// view restriction rather than miss one (conservative — only a warning).
		if typeCol == "" || strings.EqualFold(tb.col(row, typeCol), "VIEW") {
			export.RestrictedPageIDs[cid] = true
		}
	}
	return nil
}

// loadLabels reads label.csv + content_label.csv and attaches label names to
// their pages.
func (t *Transformer) loadLabels(fileIndex map[string]*zip.File, export *ConfluenceExport) error {
	labelTable, err := readCSV(fileIndex, fileLabel)
	if err != nil {
		return err
	}
	contentLabelTable, err := readCSV(fileIndex, fileContentLabel)
	if err != nil {
		return err
	}
	if labelTable == nil || contentLabelTable == nil {
		return nil
	}

	labelName := make(map[string]string, len(labelTable.rows))
	for _, row := range labelTable.rows {
		if id := labelTable.col(row, "labelid"); id != "" {
			labelName[id] = labelTable.col(row, "name")
		}
	}

	labelsByContentID := make(map[string][]string)
	for _, row := range contentLabelTable.rows {
		if contentLabelTable.col(row, "labelabletype") != "CONTENT" {
			continue
		}
		contentID := contentLabelTable.col(row, "contentid")
		if contentID == "" {
			contentID = contentLabelTable.col(row, "labelableid")
		}
		labelID := contentLabelTable.col(row, "labelid")
		if name, ok := labelName[labelID]; ok && contentID != "" {
			labelsByContentID[contentID] = append(labelsByContentID[contentID], name)
		}
	}

	for _, page := range export.Pages {
		if labels, ok := labelsByContentID[page.ID]; ok {
			page.Labels = append(page.Labels, labels...)
		}
	}
	return nil
}
