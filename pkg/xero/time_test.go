package xero

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestXeroTimeParse(t *testing.T) {
	t.Parallel()
	var ms, iso, null XeroTime

	require.NoError(t, json.Unmarshal([]byte(`"/Date(1746000000000+0000)/"`), &ms))
	require.NoError(t, json.Unmarshal([]byte(`"2025-04-30T08:00:00Z"`), &iso))

	want := time.UnixMilli(1746000000000).UTC()
	require.Equal(t, want, ms.UTC())
	require.Equal(t, ms.UTC(), iso.UTC())

	require.NoError(t, json.Unmarshal([]byte(`null`), &null))
	require.True(t, null.IsZero())
}

func TestXeroTimeParseNegativeOffset(t *testing.T) {
	t.Parallel()
	var neg XeroTime
	require.NoError(t, json.Unmarshal([]byte(`"/Date(1746000000000-0500)/"`), &neg))
	require.Equal(t, time.UnixMilli(1746000000000).UTC(), neg.UTC())
}
