// Package otpimport reads OTP credentials from the two formats that
// authenticator apps export today:
//
//   - otpauth://totp/Issuer:account?secret=BASE32&issuer=...&digits=...&period=...
//   - otpauth-migration://offline?data=BASE64(MigrationPayload)
//
// The second is what Google Authenticator's "Export accounts" QR contains:
// a protobuf wrapping one or more accounts. We parse the wire format by
// hand to avoid pulling in the full protobuf runtime.
package otpimport

import (
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/esivres/openvpn3ui/internal/otp"
)

// Account is a single OTP credential, normalised across both source formats.
// Secret is always returned in unpadded base32 (the form users paste into
// authenticator apps and the form our SetOTP expects).
type Account struct {
	Issuer    string
	Name      string // typically the user/account label, e.g. "alice@example.com"
	Secret    string // base32, unpadded
	Digits    int    // 6 or 8 (defaults to 6)
	Period    int    // seconds (defaults to 30)
	Algorithm otp.Algorithm
	IsHOTP    bool // false == TOTP
}

// Label returns "Issuer: Name" or one of them if the other is empty —
// suitable for picker UI when multiple accounts are imported. Value
// receiver kept so test literals (`Account{...}.Label()`) stay
// addressable.
//
//nolint:gocritic // hugeParam: addressability matters more than the copy
func (a Account) Label() string {
	switch {
	case a.Issuer != "" && a.Name != "":
		return a.Issuer + ": " + a.Name
	case a.Issuer != "":
		return a.Issuer
	default:
		return a.Name
	}
}

// ParseURI dispatches on scheme. Returns a slice because the migration
// format can pack many accounts in one URI.
func ParseURI(raw string) ([]Account, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "otpauth://"):
		a, err := parseOTPAuth(raw)
		if err != nil {
			return nil, err
		}
		return []Account{a}, nil
	case strings.HasPrefix(raw, "otpauth-migration://"):
		return parseMigration(raw)
	default:
		return nil, fmt.Errorf("unrecognised OTP URI scheme")
	}
}

// parseOTPAuth handles the standard single-account form.
func parseOTPAuth(raw string) (Account, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Account{}, fmt.Errorf("parse uri: %w", err)
	}
	a := Account{Digits: 6, Period: 30, Algorithm: otp.SHA1}

	switch u.Host {
	case "totp":
		a.IsHOTP = false
	case "hotp":
		a.IsHOTP = true
	default:
		return Account{}, fmt.Errorf("unknown otpauth type %q", u.Host)
	}

	// Path is "/Issuer:Name" or "/Name". URL-decoded automatically by net/url.
	label := strings.TrimPrefix(u.Path, "/")
	if i := strings.Index(label, ":"); i >= 0 {
		a.Issuer = label[:i]
		a.Name = strings.TrimSpace(label[i+1:])
	} else {
		a.Name = label
	}

	q := u.Query()
	a.Secret = strings.ToUpper(strings.ReplaceAll(q.Get("secret"), "=", ""))
	if a.Secret == "" {
		return Account{}, errors.New("otpauth uri missing secret")
	}
	if iss := q.Get("issuer"); iss != "" {
		a.Issuer = iss // query param wins over the label prefix
	}
	if d := q.Get("digits"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && (n == 6 || n == 8) {
			a.Digits = n
		}
	}
	if p := q.Get("period"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			a.Period = n
		}
	}
	switch strings.ToUpper(q.Get("algorithm")) {
	case "SHA256":
		a.Algorithm = otp.SHA256
	case "SHA512":
		a.Algorithm = otp.SHA512
	}
	return a, nil
}

// parseMigration handles the Google Authenticator export wire format.
//
// URI shape: otpauth-migration://offline?data=<base64>
// Payload (protobuf):
//
//	message MigrationPayload {
//	  message OtpParameters {
//	    bytes  secret    = 1;
//	    string name      = 2;
//	    string issuer    = 3;
//	    Algorithm alg    = 4;  // 1=SHA1 2=SHA256 3=SHA512 4=MD5
//	    DigitCount dig   = 5;  // 1=six   2=eight
//	    OtpType  type    = 6;  // 1=HOTP  2=TOTP
//	    int64    counter = 7;
//	  }
//	  repeated OtpParameters otp_parameters = 1;
//	  ...
//	}
func parseMigration(raw string) ([]Account, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	dataParam := u.Query().Get("data")
	if dataParam == "" {
		return nil, errors.New("migration uri missing data parameter")
	}
	// Google encodes with standard base64. When the URI was assembled
	// without proper percent-encoding (a common mistake when copying by
	// hand), `+` characters end up decoded as spaces by Query().Get; undo
	// that before decoding. Pad to a multiple of 4 if the source dropped
	// the trailing '='.
	dataParam = strings.ReplaceAll(dataParam, " ", "+")
	if pad := len(dataParam) % 4; pad != 0 {
		dataParam += strings.Repeat("=", 4-pad)
	}
	payload, err := base64.StdEncoding.DecodeString(dataParam)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	var accounts []Account
	r := pbReader(payload)
	for !r.empty() {
		fieldNum, wireType, err := r.tag()
		if err != nil {
			return nil, err
		}
		if fieldNum == 1 && wireType == 2 {
			body, err := r.lenDelimited()
			if err != nil {
				return nil, err
			}
			a, err := parseMigrationAccount(body)
			if err != nil {
				return nil, err
			}
			accounts = append(accounts, a)
		} else {
			if err := r.skip(wireType); err != nil {
				return nil, err
			}
		}
	}
	if len(accounts) == 0 {
		return nil, errors.New("migration payload contained no accounts")
	}
	return accounts, nil
}

func parseMigrationAccount(body []byte) (Account, error) {
	a := Account{Digits: 6, Period: 30, Algorithm: otp.SHA1}
	r := pbReader(body)
	for !r.empty() {
		fieldNum, wireType, err := r.tag()
		if err != nil {
			return Account{}, err
		}
		switch {
		case fieldNum == 1 && wireType == 2: // secret bytes
			b, err := r.lenDelimited()
			if err != nil {
				return Account{}, err
			}
			a.Secret = strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
		case fieldNum == 2 && wireType == 2: // name
			b, err := r.lenDelimited()
			if err != nil {
				return Account{}, err
			}
			a.Name = string(b)
		case fieldNum == 3 && wireType == 2: // issuer
			b, err := r.lenDelimited()
			if err != nil {
				return Account{}, err
			}
			a.Issuer = string(b)
		case fieldNum == 4 && wireType == 0: // algorithm
			v, err := r.varint()
			if err != nil {
				return Account{}, err
			}
			switch v {
			case 2:
				a.Algorithm = otp.SHA256
			case 3:
				a.Algorithm = otp.SHA512
			default:
				a.Algorithm = otp.SHA1
			}
		case fieldNum == 5 && wireType == 0: // digits
			v, err := r.varint()
			if err != nil {
				return Account{}, err
			}
			if v == 2 {
				a.Digits = 8
			} else {
				a.Digits = 6
			}
		case fieldNum == 6 && wireType == 0: // type
			v, err := r.varint()
			if err != nil {
				return Account{}, err
			}
			a.IsHOTP = v == 1
		default:
			if err := r.skip(wireType); err != nil {
				return Account{}, err
			}
		}
	}
	if a.Secret == "" {
		return Account{}, errors.New("migration account missing secret")
	}
	return a, nil
}

// --- minimal protobuf wire reader ---------------------------------------

type pbReader []byte

func (r *pbReader) empty() bool { return len(*r) == 0 }

func (r *pbReader) tag() (fieldNum, wireType int, err error) {
	v, err := r.varint()
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 0x07), nil
}

func (r *pbReader) varint() (uint64, error) {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		if len(*r) == 0 {
			return 0, errors.New("truncated varint")
		}
		b := (*r)[0]
		*r = (*r)[1:]
		v |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return v, nil
		}
		if shift > 63 {
			return 0, errors.New("varint too long")
		}
	}
}

func (r *pbReader) lenDelimited() ([]byte, error) {
	n, err := r.varint()
	if err != nil {
		return nil, err
	}
	if uint64(len(*r)) < n {
		return nil, errors.New("truncated length-delimited")
	}
	out := (*r)[:n]
	*r = (*r)[n:]
	return out, nil
}

func (r *pbReader) skip(wireType int) error {
	switch wireType {
	case 0: // varint
		_, err := r.varint()
		return err
	case 1: // 64-bit
		if len(*r) < 8 {
			return errors.New("truncated 64-bit")
		}
		*r = (*r)[8:]
		return nil
	case 2: // length-delimited
		_, err := r.lenDelimited()
		return err
	case 5: // 32-bit
		if len(*r) < 4 {
			return errors.New("truncated 32-bit")
		}
		*r = (*r)[4:]
		return nil
	default:
		return fmt.Errorf("unsupported wire type %d", wireType)
	}
}
