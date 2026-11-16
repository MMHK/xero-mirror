package xero

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

// cipherVersion 是密文格式版本前綴；版本不符代表資料不是本版產生。
const cipherVersion = "v1:"

// Cipher 以 AES-256-GCM 加解密 token；key 必須是 32 bytes。
type Cipher struct {
	key []byte
}

// NewCipher 由 32 bytes 金鑰建立 Cipher；長度不符即回錯誤。金鑰由 pkg/config 解 base64 後傳入。
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("token 金鑰必須是 32 bytes（目前 %d bytes）", len(key))
	}
	return &Cipher{key: key}, nil
}

// Encrypt 以隨機 nonce 加密明文，輸出 "v1:" + base64(nonce || ciphertext)。
// 空字串直接回空字串，代表該欄位沒有值。
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("產生 cipher nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return cipherVersion + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 驗證版本前綴後解密；空字串直接回空字串。
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if !strings.HasPrefix(encoded, cipherVersion) {
		return "", fmt.Errorf("token 密文版本不支援（需要 %s）", cipherVersion)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, cipherVersion))
	if err != nil {
		return "", fmt.Errorf("token 密文不是合法 base64: %w", err)
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("token 密文長度不足")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("解密 token 失敗: %w", err)
	}
	return string(plain), nil
}

// gcm 建立 AES-GCM 實例；金鑰在 NewCipher 已驗證，此處只回報極端錯誤。
func (c *Cipher) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, fmt.Errorf("建立 AES cipher: %w", err)
	}
	return cipher.NewGCM(block)
}
