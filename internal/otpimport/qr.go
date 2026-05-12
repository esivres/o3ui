package otpimport

import (
	"fmt"
	"image"
	_ "image/jpeg" // register decoders for png/jpeg
	_ "image/png"
	"io"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// DecodeQRImage reads an image from r (PNG or JPEG), scans it for a single
// QR code, and returns the embedded text. Use ParseURI on the result to
// turn an otpauth:// or otpauth-migration:// payload into accounts.
func DecodeQRImage(r io.Reader) (string, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", fmt.Errorf("bitmap: %w", err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return "", fmt.Errorf("scan qr: %w", err)
	}
	return res.GetText(), nil
}
