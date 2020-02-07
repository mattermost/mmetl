package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlackConvertTimeStamp(t *testing.T) {
	testCases := []struct {
		Name           string
		SlackTimeStamp string
		ExpectedResult int64
	}{
		{
			Name:           "Converting an invalid timestamp",
			SlackTimeStamp: "asd",
			ExpectedResult: 1,
		},
		{
			Name:           "Converting a valid timestamp, rounding down",
			SlackTimeStamp: "1549307811.074100",
			ExpectedResult: 1549307811074,
		},
		{
			Name:           "Converting a valid timestamp, rounding up",
			SlackTimeStamp: "1549307811.074500",
			ExpectedResult: 1549307811075,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			res := SlackConvertTimeStamp(tc.SlackTimeStamp)
			require.Equal(t, tc.ExpectedResult, res)
		})
	}
}
