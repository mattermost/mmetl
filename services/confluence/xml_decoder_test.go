package confluence

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// decodeAll drains a decoder built over the given document.
func decodeAll(t *testing.T, document string) []*RawObject {
	t.Helper()

	decoder := NewObjectDecoder(strings.NewReader(document))

	var objects []*RawObject
	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return objects
		}
		require.NoError(t, err)
		objects = append(objects, object)
	}
}

func decodeOne(t *testing.T, document string) *RawObject {
	t.Helper()

	objects := decodeAll(t, document)
	require.Len(t, objects, 1)
	return objects[0]
}

func wrapEntities(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<hibernate-generic datetime="2026-09-01 10:52:46.614">` + "\n" +
		body + "\n</hibernate-generic>\n"
}

// samplePage is a Page object copied from the private Confluence Cloud export,
// with its shape preserved exactly: self-closing empty properties, an enum-free
// reference, collections of references, and CDATA scalars.
const samplePage = `<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542273</id>
<property name="hibernateVersion">7</property>
<property name="title"><![CDATA[dkh-space]]></property>
<collection name="bodyContents" class="java.util.Collection"><element class="BodyContent" package="com.atlassian.confluence.core"><id name="id">26542274</id>
</element>
</collection>
<collection name="adfContentBodies" class="java.util.Collection"></collection>
<property name="version">1</property>
<property name="creator" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[5adea1817b296356232d9267]]></id>
</property>
<property name="agentCreatorAaid"/><property name="creationDate">2025-11-14 16:53:35.571</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
<property name="position">875</property>
<collection name="childrens" class="java.util.Collection"><element class="Page" package="com.atlassian.confluence.pages"><id name="id">26542289</id>
</element>
<element class="Page" package="com.atlassian.confluence.pages"><id name="id">26542301</id>
</element>
</collection>
</object>`

func TestObjectDecoder_DecodesRealPageShape(t *testing.T) {
	object := decodeOne(t, wrapEntities(samplePage))

	require.True(t, object.Is(ClassPage))
	require.Equal(t, EntityKey{
		Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: "26542273",
	}, object.Key)
	require.False(t, object.IsComposite())

	t.Run("scalars", func(t *testing.T) {
		require.Equal(t, "dkh-space", object.ScalarValue("title"))
		require.Equal(t, "current", object.ScalarValue("contentStatus"))
		require.Equal(t, "875", object.ScalarValue("position"))
		require.Equal(t, "2025-11-14 16:53:35.571", object.ScalarValue("creationDate"))
	})

	// The canonical-content predicate turns on exactly this distinction: a
	// self-closing originalVersionId means "no original version", while the
	// property being absent means the class never carries one.
	t.Run("present-and-empty is not the same as missing", func(t *testing.T) {
		value, present := object.Scalar("originalVersionId")
		require.True(t, present)
		require.Empty(t, value)

		value, present = object.Scalar("agentCreatorAaid")
		require.True(t, present)
		require.Empty(t, value)

		_, present = object.Scalar("neverWritten")
		require.False(t, present)
		require.False(t, object.HasScalar("neverWritten"))
	})

	t.Run("references", func(t *testing.T) {
		space, ok := object.Reference("space")
		require.True(t, ok)
		require.Equal(t, EntityKey{Package: PkgConfluenceSpaces, Class: "Space", IDName: "id", ID: "26542084"}, space)

		// ConfluenceUserImpl keys on "key", not "id".
		creator, ok := object.Reference("creator")
		require.True(t, ok)
		require.Equal(t, EntityKey{
			Package: PkgConfluenceUser, Class: "ConfluenceUserImpl",
			IDName: "key", ID: "5adea1817b296356232d9267",
		}, creator)
		require.True(t, ClassConfluenceUser.Matches(creator))

		_, ok = object.Reference("absent")
		require.False(t, ok)
	})

	t.Run("collections", func(t *testing.T) {
		require.Equal(t, []EntityKey{
			{Package: PkgConfluenceCore, Class: "BodyContent", IDName: "id", ID: "26542274"},
		}, object.Collection("bodyContents"))

		require.Equal(t, []EntityKey{
			{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: "26542289"},
			{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: "26542301"},
		}, object.Collection("childrens"))

		require.Empty(t, object.Collection("adfContentBodies"))
		require.Contains(t, object.Collections, "adfContentBodies", "an empty collection is still recorded")
		require.Empty(t, object.Collection("neverWritten"))
	})
}

// TestObjectDecoder_EnumPropertyIsAScalar guards the one property shape that a
// package-based reference test would misread. An enum carries package and
// enum-class but no class, and its value is text, not a nested id.
func TestObjectDecoder_EnumPropertyIsAScalar(t *testing.T) {
	object := decodeOne(t, wrapEntities(`<object class="InternalGroup" package="com.atlassian.crowd.model.group">
<id name="id">1</id>
<property name="type" enum-class="GroupType" package="com.atlassian.crowd.model.group">GROUP</property>
<property name="directory" class="DirectoryImpl" package="com.atlassian.crowd.model.directory"><id name="id">1</id>
</property>
</object>`))

	require.Equal(t, "GROUP", object.ScalarValue("type"))
	require.NotContains(t, object.References, "type")

	directory, ok := object.Reference("directory")
	require.True(t, ok)
	require.Equal(t, "1", directory.ID)
}

func TestObjectDecoder_MultipleObjects(t *testing.T) {
	objects := decodeAll(t, wrapEntities(`<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">1</id>
<property name="key"><![CDATA[ENG]]></property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">2</id>
</object>
<object class="BlogPost" package="com.atlassian.confluence.pages">
<id name="id">3</id>
</object>`))

	require.Len(t, objects, 3)
	require.True(t, objects[0].Is(ClassSpace))
	require.True(t, objects[1].Is(ClassPage))
	require.True(t, objects[2].Is(ClassBlogPost))

	require.Less(t, objects[0].Offset, objects[1].Offset, "offsets must locate each object in the file")
	require.Less(t, objects[1].Offset, objects[2].Offset)
}

// TestObjectDecoder_ClassMatchingChecksPackage guards against joining unrelated
// entities that share a class name across packages.
func TestObjectDecoder_ClassMatchingChecksPackage(t *testing.T) {
	object := decodeOne(t, wrapEntities(`<object class="Page" package="com.example.other">
<id name="id">1</id>
</object>`))

	require.False(t, object.Is(ClassPage))
	require.Equal(t, ClassRef{Package: "com.example.other", Class: "Page"}, object.Class())
}

// TestObjectDecoder_CompositeID uses the real shape from the private sample:
// composite keys are a <composite-id> element whose parts are <property>
// children, not repeated <id> elements. A real export is full of these, so
// failing on one would abort the whole stream.
func TestObjectDecoder_CompositeID(t *testing.T) {
	objects := decodeAll(t, wrapEntities(`<object class="BucketPropertySetItem" package="bucket.user.propertyset">
<composite-id><property name="entityName" type="string"><![CDATA[USERPROPS-712020:c531b087]]></property>
<property name="entityId" type="long">0</property>
<property name="key" type="string"><![CDATA[localStorage/atlassian.ghosttext]]></property>
</composite-id>
<property name="type">6</property>
<property name="stringVal"/><property name="textVal"><![CDATA["{}"]]></property>
</object>
<object class="BucketPropertySetItem" package="bucket.user.propertyset">
<composite-id><property name="entityName" type="string"><![CDATA[USERPROPS-712020:747200fb]]></property>
<property name="entityId" type="long">0</property>
<property name="key" type="string"><![CDATA[localStorage/atlassian.have-ended]]></property>
</composite-id>
<property name="type">6</property>
</object>`))

	require.Len(t, objects, 2)

	first := objects[0]
	require.True(t, first.IsComposite())
	require.Equal(t, []RawID{
		{Name: "entityName", Value: "USERPROPS-712020:c531b087"},
		{Name: "entityId", Value: "0"},
		{Name: "key", Value: "localStorage/atlassian.ghosttext"},
	}, first.IDs)
	require.Equal(t, compositeIDName, first.Key.IDName)

	// The composite parts still decode as an object's own scalars.
	require.Equal(t, "6", first.ScalarValue("type"))
	require.Equal(t, `"{}"`, first.ScalarValue("textVal"))

	// Distinct composite rows of one class must not collapse onto one key, or
	// a normal export would look like a duplicate-key error.
	require.NotEqual(t, first.Key, objects[1].Key)
}

// TestObjectDecoder_RepeatedIDElements keeps the defensive path for a shape the
// sample does not contain but Hibernate can emit.
func TestObjectDecoder_RepeatedIDElements(t *testing.T) {
	object := decodeOne(t, wrapEntities(`<object class="SpacePermission" package="com.atlassian.confluence.security">
<id name="spaceId">1</id>
<id name="permission">VIEWSPACE</id>
</object>`))

	require.True(t, object.IsComposite())
	require.Equal(t, compositeIDName, object.Key.IDName)
	require.Equal(t, "spaceId=1\x00permission=VIEWSPACE", object.Key.ID)
}

func TestObjectDecoder_SkipsUnknownStructure(t *testing.T) {
	object := decodeOne(t, wrapEntities(`<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">1</id>
<unexpected><nested attr="x">text</nested></unexpected>
<property name="title">kept</property>
<collection name="things" class="java.util.Collection"><unexpected/><element class="Page" package="com.atlassian.confluence.pages"><id name="id">2</id></element></collection>
</object>`))

	require.Equal(t, "kept", object.ScalarValue("title"))
	require.Len(t, object.Collection("things"), 1)
}

// TestObjectDecoder_ReferenceWithoutID covers a wrapper that points at nothing.
// Recording it as an empty key would make it look like a reference to the
// entity whose ID is the empty string.
func TestObjectDecoder_ReferenceWithoutID(t *testing.T) {
	object := decodeOne(t, wrapEntities(`<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">1</id>
<property name="parent" class="Page" package="com.atlassian.confluence.pages"></property>
</object>`))

	_, ok := object.Reference("parent")
	require.False(t, ok)
}

func TestObjectDecoder_RejectsMalformedDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
		wantErr  string
	}{
		{
			name:     "doctype",
			document: "<?xml version=\"1.0\"?>\n<!DOCTYPE hibernate-generic [<!ENTITY xxe SYSTEM \"file:///etc/passwd\">]>\n<hibernate-generic></hibernate-generic>",
			wantErr:  "XML directives are rejected",
		},
		{
			name:     "undefined entity reference",
			document: wrapEntities(`<object class="Page" package="com.atlassian.confluence.pages"><id name="id">&xxe;</id></object>`),
			wantErr:  "invalid character entity &xxe;",
		},
		{
			name:     "declared non-UTF-8 encoding",
			document: `<?xml version="1.0" encoding="ISO-8859-1"?><hibernate-generic></hibernate-generic>`,
			wantErr:  "encoding",
		},
		{
			name:     "no root element",
			document: `<?xml version="1.0" encoding="UTF-8"?>`,
			wantErr:  "no <hibernate-generic> root element",
		},
		{
			name:     "object without a class",
			document: wrapEntities(`<object package="com.atlassian.confluence.pages"><id name="id">1</id></object>`),
			wantErr:  "has no class attribute",
		},
		{
			name:     "object without a package",
			document: wrapEntities(`<object class="Page"><id name="id">1</id></object>`),
			wantErr:  "has no package attribute",
		},
		{
			name:     "object without an id",
			document: wrapEntities(`<object class="Page" package="com.atlassian.confluence.pages"><property name="title">x</property></object>`),
			wantErr:  "has no <id> or <composite-id> element",
		},
		{
			name: "truncated mid-object",
			document: `<?xml version="1.0" encoding="UTF-8"?>
<hibernate-generic><object class="Page" package="com.atlassian.confluence.pages"><id name="id">1</id>`,
			wantErr: "unexpected EOF",
		},
		{
			name:     "mismatched tags",
			document: wrapEntities(`<object class="Page" package="com.atlassian.confluence.pages"><id name="id">1</close></object>`),
			wantErr:  "element <id> closed by </close>",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder := NewObjectDecoder(strings.NewReader(test.document))
			var err error
			for err == nil {
				_, err = decoder.Next()
			}
			require.Error(t, err)
			require.NotErrorIs(t, err, io.EOF, "a malformed document must not look like a clean end of stream")
			require.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestObjectDecoder_AcceptFilter(t *testing.T) {
	document := wrapEntities(`<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">1</id>
</object>
<object class="AuditRecordEntity" package="com.atlassian.confluence.impl.audit">
<id name="id">2</id>
<property name="payload"><![CDATA[irrelevant]]></property>
</object>
<object class="BlogPost" package="com.atlassian.confluence.pages">
<id name="id">3</id>
</object>`)

	decoder := NewObjectDecoder(strings.NewReader(document))
	decoder.SetAccept(func(class ClassRef) bool {
		return class == ClassPage || class == ClassBlogPost
	})

	var ids []string
	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		ids = append(ids, object.Key.ID)
	}

	require.Equal(t, []string{"1", "3"}, ids)
	require.Equal(t, int64(2), decoder.Decoded())
	require.Equal(t, int64(1), decoder.Skipped())
	require.Equal(t, "2026-09-01 10:52:46.614", decoder.Datetime())
}

// syntheticEntities streams an entities.xml containing objects with large
// bodies, without ever holding the whole document in memory.
func syntheticEntities(objects, bodyBytes int) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		body := strings.Repeat("x", bodyBytes)
		write := func(s string) bool {
			_, err := io.WriteString(pw, s)
			return err == nil
		}

		if !write(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<hibernate-generic>\n") {
			return
		}
		for i := range objects {
			object := fmt.Sprintf(`<object class="BodyContent" package="com.atlassian.confluence.core">`+
				`<id name="id">%d</id>`+
				`<property name="body"><![CDATA[%s]]></property>`+
				`<property name="content" class="Page" package="com.atlassian.confluence.pages"><id name="id">%d</id></property>`+
				`</object>`+"\n", i, body, i)
			if !write(object) {
				return
			}
		}
		_ = write("</hibernate-generic>\n")
		_ = pw.Close()
	}()

	return pr
}

// TestObjectDecoder_MemoryDoesNotGrowWithBodyBytes is the E3 gate expressed as a
// test: the decoder must stream a document far larger than the heap it retains.
//
// It measures retained heap rather than total allocation, because the XML
// reader necessarily allocates and releases buffers proportional to the bytes
// it walks past. What must not happen is those bodies accumulating.
func TestObjectDecoder_MemoryDoesNotGrowWithBodyBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~64 MiB of streamed XML")
	}

	const (
		objects   = 256
		bodyBytes = 256 * 1024 // 64 MiB of body text in total
	)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	decoder := NewObjectDecoder(syntheticEntities(objects, bodyBytes))
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassBodyContent })

	var decoded int
	var lastBodyLen int
	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)

		// Read the body, then drop it: this is exactly what a payload pass does.
		lastBodyLen = len(object.ScalarValue("body"))
		decoded++
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	require.Equal(t, objects, decoded)
	require.Equal(t, bodyBytes, lastBodyLen)

	const totalBodyBytes = uint64(objects) * uint64(bodyBytes)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	require.Less(t, retained, int64(totalBodyBytes/8),
		"retained heap grew with body bytes: %d retained after streaming %d body bytes", retained, totalBodyBytes)
}

// TestObjectDecoder_SkippedObjectsAreNotMaterialized checks the other half of
// the streaming design: a rejected class costs no scalar strings at all.
func TestObjectDecoder_SkippedObjectsAreNotMaterialized(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~64 MiB of streamed XML")
	}

	decoder := NewObjectDecoder(syntheticEntities(256, 256*1024))
	decoder.SetAccept(func(ClassRef) bool { return false })

	_, err := decoder.Next()
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, int64(0), decoder.Decoded())
	require.Equal(t, int64(256), decoder.Skipped())
}

// TestObjectDecoder_PrivateSample is the E3 gate against a real export. It is
// skipped by default: the sample is private and must never be committed.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestObjectDecoder_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	entities, err := archive.OpenEntities()
	require.NoError(t, err)
	defer func() { require.NoError(t, entities.Close()) }()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	counts := map[ClassRef]int{}
	composite := 0

	decoder := NewObjectDecoder(entities)
	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)

		require.NotEmpty(t, object.Key.Class)
		require.NotEmpty(t, object.Key.Package)
		require.NotEmpty(t, object.Key.IDName)
		require.NotEmpty(t, object.Key.ID)

		counts[object.Class()]++
		if object.IsComposite() {
			composite++
		}
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	require.Positive(t, decoder.Decoded())
	require.Positive(t, counts[ClassPage], "a real export must contain pages")
	require.Positive(t, counts[ClassSpace], "a real export must contain spaces")

	classes := make([]ClassRef, 0, len(counts))
	for class := range counts {
		classes = append(classes, class)
	}
	sort.Slice(classes, func(i, j int) bool {
		if counts[classes[i]] != counts[classes[j]] {
			return counts[classes[i]] > counts[classes[j]]
		}
		return classes[i].String() < classes[j].String()
	})

	t.Logf("streamed %d objects (%d composite) across %d classes from %d bytes of entities.xml",
		decoder.Decoded(), composite, len(classes), archive.EntitiesSize())
	for _, class := range classes {
		t.Logf("%8d  %s", counts[class], class)
	}
	t.Logf("retained heap delta: %d bytes", int64(after.HeapAlloc)-int64(before.HeapAlloc))
}
