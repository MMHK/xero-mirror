package xero

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testCipher 以固定 32 bytes 金鑰建立 Cipher，供加密測試共用。
func testCipher(t *testing.T, seed byte) *Cipher {
	t.Helper()
	c, err := NewCipher(bytes.Repeat([]byte{seed}, 32))
	require.NoError(t, err)
	return c
}

func TestCipherRoundTrip(t *testing.T) {
	c := testCipher(t, 0x01)
	enc, err := c.Encrypt("access-token-abc")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(enc, "v1:"))

	plain, err := c.Decrypt(enc)
	require.NoError(t, err)
	require.Equal(t, "access-token-abc", plain)

	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, "v1:"))
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0xff
	_, err = c.Decrypt("v1:" + base64.StdEncoding.EncodeToString(raw))
	require.Error(t, err)

	_, err = c.Decrypt("v2:" + base64.StdEncoding.EncodeToString(raw))
	require.Error(t, err)
}

func TestCipherWrongKey(t *testing.T) {
	enc, err := testCipher(t, 0x01).Encrypt("refresh-token-xyz")
	require.NoError(t, err)

	_, err = testCipher(t, 0x02).Decrypt(enc)
	require.Error(t, err)
}
