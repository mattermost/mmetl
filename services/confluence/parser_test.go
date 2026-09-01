package confluence

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseSourceTime(t *testing.T) {
	vancouver, err := time.LoadLocation("America/Vancouver")
	require.NoError(t, err, "the embedded zone database must be available")

	t.Run("naive timestamps are read in the export's zone", func(t *testing.T) {
		parsed, ok := ParseSourceTime("2025-11-14 16:53:35.571", vancouver)
		require.True(t, ok)
		require.Equal(t, time.Date(2025, time.November, 14, 16, 53, 35, 571000000, vancouver), parsed)

		_, offset := parsed.Zone()
		require.Equal(t, -8*60*60, offset, "November is standard time in Vancouver")
	})

	t.Run("the zone actually changes the instant", func(t *testing.T) {
		inVancouver, ok := ParseSourceTime("2025-11-14 16:53:35.571", vancouver)
		require.True(t, ok)
		inUTC, ok := ParseSourceTime("2025-11-14 16:53:35.571", time.UTC)
		require.True(t, ok)

		require.NotEqual(t, inVancouver.UnixMilli(), inUTC.UnixMilli())
		require.Equal(t, int64(8*60*60*1000), inVancouver.UnixMilli()-inUTC.UnixMilli())
	})

	t.Run("fractional seconds are optional", func(t *testing.T) {
		parsed, ok := ParseSourceTime("2025-11-14 16:53:35", vancouver)
		require.True(t, ok)
		require.Equal(t, 0, parsed.Nanosecond())
	})

	// An explicit offset is authoritative and must not be reinterpreted in the
	// export's zone.
	t.Run("an explicit offset wins over the export zone", func(t *testing.T) {
		parsed, ok := ParseSourceTime("2025-11-14T16:53:35.571Z", vancouver)
		require.True(t, ok)
		require.Equal(t, time.Date(2025, time.November, 14, 16, 53, 35, 571000000, time.UTC), parsed.UTC())

		withOffset, ok := ParseSourceTime("2025-11-14T16:53:35+02:00", vancouver)
		require.True(t, ok)
		require.Equal(t, time.Date(2025, time.November, 14, 14, 53, 35, 0, time.UTC), withOffset.UTC())
	})

	t.Run("a nil location falls back to UTC", func(t *testing.T) {
		parsed, ok := ParseSourceTime("2025-11-14 16:53:35.571", nil)
		require.True(t, ok)
		require.Equal(t, time.UTC, parsed.Location())
	})

	// A zero time.Time is year 1, which converts to a large negative Unix
	// millisecond value. Emitting that would date pages to antiquity instead of
	// leaving them undated.
	t.Run("unparseable values report absence rather than a zero time", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "not a date", "2025-13-45 99:99:99", "0"} {
			parsed, ok := ParseSourceTime(raw, vancouver)
			require.False(t, ok, raw)
			require.True(t, parsed.IsZero(), raw)

			millis, ok := SourceTimeMillis(raw, vancouver)
			require.False(t, ok, raw)
			require.Zero(t, millis, raw)
		}
	})

	t.Run("millis conversion", func(t *testing.T) {
		millis, ok := SourceTimeMillis("2025-11-14T16:53:35.571Z", vancouver)
		require.True(t, ok)
		require.Equal(t, int64(1763139215571), millis)
	})
}

func TestParseSourcePosition(t *testing.T) {
	tests := map[string]struct {
		want int64
		ok   bool
	}{
		"875":  {875, true},
		"0":    {0, true},
		"-1":   {-1, true},
		" 12 ": {12, true},
		"":     {0, false},
		"   ":  {0, false},
		"abc":  {0, false},
		"1.5":  {0, false},
	}

	for raw, expected := range tests {
		t.Run(raw, func(t *testing.T) {
			value, ok := parseSourcePosition(raw)
			require.Equal(t, expected.ok, ok)
			require.Equal(t, expected.want, value)
		})
	}
}

// TestIsAbsentOrZeroID pins the three spellings Confluence uses for "no
// original version". Reading "0" as a real id would exclude every current page
// in an export that spells it that way.
func TestIsAbsentOrZeroID(t *testing.T) {
	for _, raw := range []string{"", "   ", "0", "00", " 0 "} {
		require.Truef(t, isAbsentOrZeroID(raw), "%q means absent", raw)
	}
	for _, raw := range []string{"1", "26542273", "-1", "abc"} {
		require.Falsef(t, isAbsentOrZeroID(raw), "%q names something", raw)
	}
}
