package totp2fa

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"image/png"
	"math/big"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const issuer = "Clumoove"

// validateOpts pins the TOTP security parameters independently of library
// defaults. Skew 1 permits one 30-second period of clock tolerance each side.
var validateOpts = totp.ValidateOpts{
	Digits:    otp.DigitsSix,
	Algorithm: otp.AlgorithmSHA1,
	Skew:      1,
	Period:    30,
}

// GenerateProvisioning creates a new TOTP secret for the given user email,
// returns the base32 secret, the otpauth URI, and a base64 PNG data URL of the
// QR code encoding that URI.
func GenerateProvisioning(userEmail string) (secretBase32, otpauthURI, qrPNGDataURL string, err error) {
	if strings.TrimSpace(userEmail) == "" {
		return "", "", "", errors.New("user email is required")
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: userEmail,
	})
	if err != nil {
		return "", "", "", err
	}

	qrImage, err := qr.Encode(key.URL(), qr.H, qr.Auto)
	if err != nil {
		return "", "", "", err
	}

	scaledQRImage, err := barcode.Scale(qrImage, 512, 512)
	if err != nil {
		return "", "", "", err
	}

	var pngBuffer bytes.Buffer
	if err := png.Encode(&pngBuffer, scaledQRImage); err != nil {
		return "", "", "", err
	}

	return key.Secret(), key.URL(), "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBuffer.Bytes()), nil
}

// Validate checks a user-submitted TOTP code against the base32 secret.
func Validate(secretBase32, code string) bool {
	ok, err := totp.ValidateCustom(code, secretBase32, time.Now().UTC(), validateOpts)
	return err == nil && ok
}

// backupCodeLen is the number of characters in a generated backup code.
// 10 characters over a 32-symbol alphabet yields ~50 bits of entropy, which is
// infeasible to brute-force even offline.
const backupCodeLen = 10

// backupCodeCount is how many backup codes are generated on enable.
const backupCodeCount = 10

// backupCodeAlphabet is a Crockford-style base32 alphabet that excludes
// ambiguous characters (0/O, 1/I/L) to reduce transcription errors.
const backupCodeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// GenerateBackupCodes produces backupCodeCount cryptographically random codes
// and returns both the plaintext codes (shown once to the user) and their
// bcrypt hashes (stored in the database). bcrypt is a slow, salted KDF so a
// leaked hash column cannot be brute-forced offline.
func GenerateBackupCodes() (plain []string, hashes []string, err error) {
	plain = make([]string, 0, backupCodeCount)
	hashes = make([]string, 0, backupCodeCount)
	seen := make(map[string]struct{}, backupCodeCount)
	for len(plain) < backupCodeCount {
		code, err := randomCode(backupCodeLen)
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := seen[code]; duplicate {
			continue
		}
		seen[code] = struct{}{}
		hash, err := HashBackupCode(code)
		if err != nil {
			return nil, nil, err
		}
		plain = append(plain, code)
		hashes = append(hashes, hash)
	}
	return plain, hashes, nil
}

// HashBackupCode returns a bcrypt hash of a backup code.
func HashBackupCode(code string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyBackupCode checks the supplied code against the list of stored bcrypt
// hashes. It returns the index of the matching hash, or -1 if none matched.
// bcrypt.CompareHashAndPassword is itself constant-time for a given hash.
// The loop short-circuits on a match, leaking its index via timing; the index
// is not security-sensitive, and checking every hash would enable a mild
// self-DoS. The caller rate-limits failed attempts.
func VerifyBackupCode(hashes []string, code string) int {
	for i, h := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(code)) == nil {
			return i
		}
	}
	return -1
}

// randomCode returns a cryptographically random string of the given length over
// backupCodeAlphabet using rejection-free uniform sampling (crypto/rand + big.Int),
// avoiding the modulo bias of naive byte-mod-len selection.
func randomCode(length int) (string, error) {
	max := big.NewInt(int64(len(backupCodeAlphabet)))
	code := make([]byte, length)
	for index := range code {
		alphabetIndex, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		code[index] = backupCodeAlphabet[alphabetIndex.Int64()]
	}
	return string(code), nil
}
