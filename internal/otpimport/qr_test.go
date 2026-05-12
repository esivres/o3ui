package otpimport_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	qrenc "github.com/skip2/go-qrcode"
	"github.com/stretchr/testify/require"

	"github.com/esivres/openvpn3ui/internal/otpimport"
)

// TestDecodeQRImage_RoundTrip encodes a known otpauth URI as a QR PNG and
// confirms the decoder yields the same string back. This exercises the full
// PNG → bitmap → QR path without depending on a sample image fixture.
func TestDecodeQRImage_RoundTrip(t *testing.T) {
	const want = "otpauth://totp/Example:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Example"

	q, err := qrenc.New(want, qrenc.Medium)
	require.NoError(t, err)
	pngBytes, err := q.PNG(256)
	require.NoError(t, err)

	got, err := otpimport.DecodeQRImage(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	require.Equal(t, want, got)

	// And the parser is happy with the round-tripped URI.
	accs, err := otpimport.ParseURI(got)
	require.NoError(t, err)
	require.Equal(t, "alice@example.com", accs[0].Name)
	require.Equal(t, "JBSWY3DPEHPK3PXP", accs[0].Secret)
}

func TestDecodeQRImage_NotAnImage(t *testing.T) {
	_, err := otpimport.DecodeQRImage(strings.NewReader("not even close to a png"))
	require.Error(t, err)
}

func TestDecodeQRImage_ImageWithoutQR(t *testing.T) {
	// Plain white PNG — no QR pattern, decoder must fail.
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	_, err := otpimport.DecodeQRImage(&buf)
	require.Error(t, err)
}

// Reference color so the import isn't flagged as unused on minor refactors.
var _ = color.Gray{}
