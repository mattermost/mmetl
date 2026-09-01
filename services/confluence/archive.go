package confluence

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	// The exporter resolves Confluence's naive timestamps against the timezone
	// named in exportDescriptor.properties. Embedding the zone database keeps
	// that working on hosts that ship no system zoneinfo, where LoadLocation
	// would otherwise fail and silently shift every source timestamp.
	_ "time/tzdata"
)

// Required and recognized entries in a Confluence Cloud XML backup.
const (
	EntitiesEntryName   = "entities.xml"
	DescriptorEntryName = "exportDescriptor.properties"

	sourceAttachmentsPrefix = "attachments/"
	sourcePluginDataPrefix  = "plugin-data/"
)

// attachmentPathRe matches the only attachment layout the contract accepts:
// attachments/<container-id>/<attachment-id>/<version>, all numeric.
var attachmentPathRe = regexp.MustCompile(`^attachments/(\d+)/(\d+)/(\d+)$`)

// windowsDrivePrefixRe matches a leading drive letter such as "C:".
var windowsDrivePrefixRe = regexp.MustCompile(`^[A-Za-z]:`)

// AttachmentRef identifies one attachment blob inside the source archive.
type AttachmentRef struct {
	ContainerID  string
	AttachmentID string
	Version      int
}

func (r AttachmentRef) String() string {
	return fmt.Sprintf("%s%s/%s/%d", sourceAttachmentsPrefix, r.ContainerID, r.AttachmentID, r.Version)
}

// AttachmentEntry is one indexed attachment blob. Bytes are never read at index
// time; callers stream them through Open when they need them.
type AttachmentEntry struct {
	Ref   AttachmentRef
	Path  string
	Size  int64
	CRC32 uint32

	file *zip.File
}

// Open streams the attachment's decompressed bytes. The caller closes the
// reader and is responsible for bounding how much it reads.
func (e AttachmentEntry) Open() (io.ReadCloser, error) {
	rc, err := e.file.Open()
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", e.Path, err)
	}
	return rc, nil
}

// SourceArchive is a validated, indexed Confluence Cloud XML backup. Opening it
// reads the central directory and the descriptor only: entities.xml and
// attachment bodies stay on disk until they are streamed.
type SourceArchive struct {
	path       string
	reader     *zip.ReadCloser
	descriptor Descriptor

	entities    *zip.File
	attachments map[AttachmentRef]AttachmentEntry
	warnings    []Warning
}

// OpenSourceArchive opens and validates a Confluence Cloud XML backup ZIP.
//
// It rejects the archive outright when a required entry is missing or when any
// entry is unsafe: an absolute path, a traversal, a backslash or drive prefix, a
// NUL, a symlink or device, an encrypted entry, an unsupported compression
// method, or a duplicate raw or normalized path. Unsafe entries are rejected
// rather than skipped, because an archive containing one is not a backup this
// tool produced a safe reading of.
//
// Safe but unrecognized root entries are indexed nowhere, never extracted, and
// reported as source_archive_unknown_entry warnings. plugin-data is ignored
// silently: it is expected, and it is never part of an import.
//
// The caller must Close the returned archive.
func OpenSourceArchive(path string) (*SourceArchive, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening source archive %q: %w", path, err)
	}

	archive := &SourceArchive{
		path:        path,
		reader:      reader,
		attachments: map[AttachmentRef]AttachmentEntry{},
	}

	if err := archive.index(); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return archive, nil
}

// Close releases the underlying archive.
func (a *SourceArchive) Close() error {
	return a.reader.Close()
}

// Path is the archive's filesystem path, used for manifest source.export_file.
func (a *SourceArchive) Path() string { return a.path }

// Descriptor is the parsed exportDescriptor.properties.
func (a *SourceArchive) Descriptor() Descriptor { return a.descriptor }

// Warnings returns the indexing warnings, deterministically sorted.
func (a *SourceArchive) Warnings() []Warning {
	out := make([]Warning, len(a.warnings))
	copy(out, a.warnings)
	return out
}

// OpenEntities streams entities.xml. It is never read into memory whole.
func (a *SourceArchive) OpenEntities() (io.ReadCloser, error) {
	rc, err := a.entities.Open()
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", EntitiesEntryName, err)
	}
	return rc, nil
}

// EntitiesSize is the declared decompressed size of entities.xml.
func (a *SourceArchive) EntitiesSize() int64 { return int64(a.entities.UncompressedSize64) }

// Attachment looks up one exact attachment version.
func (a *SourceArchive) Attachment(ref AttachmentRef) (AttachmentEntry, bool) {
	entry, ok := a.attachments[ref]
	return entry, ok
}

// AttachmentCount is the number of indexed attachment blobs across all versions.
func (a *SourceArchive) AttachmentCount() int { return len(a.attachments) }

// AttachmentRefs lists every indexed attachment, ordered by container, then
// attachment, then version, so diagnostics are reproducible.
func (a *SourceArchive) AttachmentRefs() []AttachmentRef {
	refs := make([]AttachmentRef, 0, len(a.attachments))
	for ref := range a.attachments {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ContainerID != refs[j].ContainerID {
			return lessNumericThenLexical(refs[i].ContainerID, refs[j].ContainerID)
		}
		if refs[i].AttachmentID != refs[j].AttachmentID {
			return lessNumericThenLexical(refs[i].AttachmentID, refs[j].AttachmentID)
		}
		return refs[i].Version < refs[j].Version
	})
	return refs
}

func (a *SourceArchive) index() error {
	var descriptorFile *zip.File

	rawSeen := map[string]bool{}
	normalizedSeen := map[string]string{}

	for _, file := range a.reader.File {
		if rawSeen[file.Name] {
			return fmt.Errorf("source archive %q: duplicate entry %q", a.path, file.Name)
		}
		rawSeen[file.Name] = true

		if err := checkEntrySafety(file); err != nil {
			return fmt.Errorf("source archive %q: %w", a.path, err)
		}

		name, isDir, err := normalizeEntryName(file.Name)
		if err != nil {
			return fmt.Errorf("source archive %q: %w", a.path, err)
		}
		if isDir {
			continue
		}
		if previous, exists := normalizedSeen[name]; exists {
			return fmt.Errorf("source archive %q: entries %q and %q normalize to the same path %q",
				a.path, previous, file.Name, name)
		}
		normalizedSeen[name] = file.Name

		switch {
		case name == EntitiesEntryName:
			a.entities = file
		case name == DescriptorEntryName:
			descriptorFile = file
		case strings.HasPrefix(name, sourcePluginDataPrefix):
			// Expected, never imported, and deliberately not warned about.
		case strings.HasPrefix(name, sourceAttachmentsPrefix):
			if err := a.indexAttachment(name, file); err != nil {
				return err
			}
		default:
			a.warnUnknownEntry(name)
		}
	}

	if a.entities == nil {
		return fmt.Errorf("source archive %q: missing required entry %q", a.path, EntitiesEntryName)
	}
	if descriptorFile == nil {
		return fmt.Errorf("source archive %q: missing required entry %q", a.path, DescriptorEntryName)
	}

	descriptor, err := readDescriptor(descriptorFile)
	if err != nil {
		return fmt.Errorf("source archive %q: %w", a.path, err)
	}
	a.descriptor = descriptor

	SortWarnings(a.warnings)
	return nil
}

// indexAttachment records one attachment blob. A path under attachments/ that
// does not match the expected layout is unknown data rather than a corrupt
// archive, so it warns instead of failing the whole export.
func (a *SourceArchive) indexAttachment(name string, file *zip.File) error {
	match := attachmentPathRe.FindStringSubmatch(name)
	if match == nil {
		a.warnUnknownEntry(name)
		return nil
	}

	version, err := strconv.Atoi(match[3])
	if err != nil {
		a.warnUnknownEntry(name)
		return nil
	}

	ref := AttachmentRef{ContainerID: match[1], AttachmentID: match[2], Version: version}
	a.attachments[ref] = AttachmentEntry{
		Ref:   ref,
		Path:  name,
		Size:  int64(file.UncompressedSize64),
		CRC32: file.CRC32,
		file:  file,
	}
	return nil
}

func (a *SourceArchive) warnUnknownEntry(name string) {
	a.warnings = append(a.warnings, Warning{
		Code:       WarnSourceArchiveUnknownEntry,
		EntityType: "archive_entry",
		SourceID:   name,
		Message:    TruncateMessage(fmt.Sprintf("unrecognized source archive entry %q was ignored and never extracted", name)),
	})
}

// checkEntrySafety rejects entries that must never be read, independent of
// where their path points.
func checkEntrySafety(file *zip.File) error {
	// Bit 0 of the general purpose flags marks an encrypted entry.
	if file.Flags&0x1 != 0 {
		return fmt.Errorf("entry %q is encrypted", file.Name)
	}
	if file.Method != zip.Store && file.Method != zip.Deflate {
		return fmt.Errorf("entry %q uses unsupported compression method %d", file.Name, file.Method)
	}

	mode := file.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return fmt.Errorf("entry %q is a symlink", file.Name)
	case mode&fs.ModeDevice != 0, mode&fs.ModeCharDevice != 0:
		return fmt.Errorf("entry %q is a device", file.Name)
	case mode&fs.ModeNamedPipe != 0:
		return fmt.Errorf("entry %q is a named pipe", file.Name)
	case mode&fs.ModeSocket != 0:
		return fmt.Errorf("entry %q is a socket", file.Name)
	case mode&fs.ModeIrregular != 0:
		return fmt.Errorf("entry %q is an irregular file", file.Name)
	}
	return nil
}

// normalizeEntryName applies the section 4 normalization: strip any number of
// leading "./" segments, then reject anything that is not a plain relative
// forward-slash path. Confluence Cloud writes some entries with a leading "./",
// so tolerating exactly that prefix is required; nothing else is.
func normalizeEntryName(raw string) (name string, isDir bool, err error) {
	if raw == "" {
		return "", false, errors.New("entry has an empty path")
	}
	if strings.ContainsRune(raw, 0) {
		return "", false, fmt.Errorf("entry %q contains NUL", raw)
	}
	if strings.Contains(raw, `\`) {
		return "", false, fmt.Errorf("entry %q contains a backslash", raw)
	}
	if windowsDrivePrefixRe.MatchString(raw) {
		return "", false, fmt.Errorf("entry %q has a drive prefix", raw)
	}
	if strings.HasPrefix(raw, "/") {
		return "", false, fmt.Errorf("entry %q is an absolute path", raw)
	}

	name = raw
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}

	isDir = strings.HasSuffix(name, "/")
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" {
		if isDir {
			// A bare "./" or "/" directory entry carries nothing.
			return "", true, nil
		}
		return "", false, fmt.Errorf("entry %q normalizes to an empty path", raw)
	}

	for _, segment := range strings.Split(trimmed, "/") {
		switch segment {
		case "":
			return "", false, fmt.Errorf("entry %q has an empty path segment", raw)
		case ".", "..":
			return "", false, fmt.Errorf("entry %q has an unsafe path segment %q", raw, segment)
		}
	}
	return trimmed, isDir, nil
}

// lessNumericThenLexical orders Confluence IDs numerically when both are
// numeric and lexically otherwise, matching the sibling ordering rule.
func lessNumericThenLexical(a, b string) bool {
	an, aErr := strconv.ParseInt(a, 10, 64)
	bn, bErr := strconv.ParseInt(b, 10, 64)
	if aErr == nil && bErr == nil {
		return an < bn
	}
	return a < b
}

// Descriptor is the parsed exportDescriptor.properties.
type Descriptor struct {
	// TimezoneID is the zone Confluence wrote naive timestamps in. Timestamps
	// without an explicit offset are meaningless without it.
	TimezoneID string

	// Location is TimezoneID resolved. It is time.UTC when TimezoneID is absent
	// or unknown, and TimezoneResolved says which happened.
	Location          *time.Location
	TimezoneResolved  bool
	ExportType        string
	Source            string
	BuildNumber       string
	BackupAttachments bool

	// Properties is every key/value pair, so later passes can read a field this
	// struct does not name without reparsing.
	Properties map[string]string
}

// Descriptor property keys.
const (
	descriptorKeyTimezoneID        = "timezoneId"
	descriptorKeyExportType        = "exportType"
	descriptorKeySource            = "source"
	descriptorKeyBuildNumber       = "buildNumber"
	descriptorKeyBackupAttachments = "backupAttachments"
)

// descriptorMaxBytes bounds the descriptor read. The real file is well under a
// kilobyte; anything approaching this is not a descriptor.
const descriptorMaxBytes = 1 << 20 // 1 MiB

func readDescriptor(file *zip.File) (Descriptor, error) {
	rc, err := file.Open()
	if err != nil {
		return Descriptor{}, fmt.Errorf("opening %s: %w", DescriptorEntryName, err)
	}
	defer func() { _ = rc.Close() }()

	return ParseDescriptor(rc)
}

// ParseDescriptor reads a Java properties file. It supports the subset
// Confluence emits: comment lines, the three key/value separators, backslash
// escapes including \uXXXX, and logical lines continued with a trailing
// backslash.
func ParseDescriptor(r io.Reader) (Descriptor, error) {
	// Read under an explicit cap and fail on overflow rather than truncating.
	// A silently truncated descriptor could drop timezoneId, which would shift
	// every naive source timestamp without anything to notice it.
	raw, err := io.ReadAll(io.LimitReader(r, descriptorMaxBytes+1))
	if err != nil {
		return Descriptor{}, fmt.Errorf("reading %s: %w", DescriptorEntryName, err)
	}
	if len(raw) > descriptorMaxBytes {
		return Descriptor{}, fmt.Errorf("%s exceeds %d bytes", DescriptorEntryName, descriptorMaxBytes)
	}

	properties, err := parseJavaProperties(bytes.NewReader(raw))
	if err != nil {
		return Descriptor{}, err
	}

	descriptor := Descriptor{
		TimezoneID:  properties[descriptorKeyTimezoneID],
		Location:    time.UTC,
		ExportType:  properties[descriptorKeyExportType],
		Source:      properties[descriptorKeySource],
		BuildNumber: properties[descriptorKeyBuildNumber],
		Properties:  properties,
	}
	// Java writes "true"/"false"; anything else is treated as absent.
	descriptor.BackupAttachments = properties[descriptorKeyBackupAttachments] == "true"

	if descriptor.TimezoneID != "" {
		if location, err := time.LoadLocation(descriptor.TimezoneID); err == nil {
			descriptor.Location = location
			descriptor.TimezoneResolved = true
		}
	}
	return descriptor, nil
}

func parseJavaProperties(r io.Reader) (map[string]string, error) {
	properties := map[string]string{}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var logical strings.Builder
	for scanner.Scan() {
		line := scanner.Text()

		if logical.Len() == 0 {
			trimmed := strings.TrimLeft(line, " \t\f")
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
				continue
			}
			line = trimmed
		}

		if hasContinuation(line) {
			logical.WriteString(strings.TrimSuffix(line, `\`))
			continue
		}

		logical.WriteString(line)
		key, value := splitProperty(logical.String())
		logical.Reset()
		if key != "" {
			properties[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", DescriptorEntryName, err)
	}

	// A trailing continuation with no following line still carries a pair.
	if logical.Len() > 0 {
		if key, value := splitProperty(logical.String()); key != "" {
			properties[key] = value
		}
	}
	return properties, nil
}

// hasContinuation reports whether the line ends in an odd number of
// backslashes, which is what continues a logical line. An even number is an
// escaped backslash that happens to sit at the end.
func hasContinuation(line string) bool {
	count := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}

// splitProperty splits a logical line on the first unescaped '=', ':', or run of
// whitespace, then unescapes both halves.
func splitProperty(line string) (key, value string) {
	var keyEnd, valueStart int
	escaped := false

	keyEnd, valueStart = len(line), len(line)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '=', ':':
			keyEnd = i
			valueStart = i + 1
			i = len(line)
		case ' ', '\t', '\f':
			keyEnd = i
			valueStart = i + 1
			// Whitespace may still be followed by an explicit separator.
			for valueStart < len(line) && (line[valueStart] == ' ' || line[valueStart] == '\t' || line[valueStart] == '\f') {
				valueStart++
			}
			if valueStart < len(line) && (line[valueStart] == '=' || line[valueStart] == ':') {
				valueStart++
			}
			i = len(line)
		}
	}

	key = unescapeJavaProperty(line[:keyEnd])
	if valueStart < len(line) {
		value = unescapeJavaProperty(strings.TrimLeft(line[valueStart:], " \t\f"))
	}
	return key, value
}

func unescapeJavaProperty(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}

	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out.WriteByte(s[i])
			continue
		}

		i++
		switch s[i] {
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'f':
			out.WriteByte('\f')
		case 'u':
			if i+4 < len(s) {
				if code, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					out.WriteRune(rune(code))
					i += 4
					continue
				}
			}
			out.WriteByte('u')
		default:
			out.WriteByte(s[i])
		}
	}
	return out.String()
}
