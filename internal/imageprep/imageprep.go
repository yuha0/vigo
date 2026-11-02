// Package imageprep turns an uploaded photo into what The Frame expects: a
// 3840x2160 JPEG with EXIF orientation baked into the pixels. The TV crops
// anything else to fill rather than fitting it (docs/tv-api.md §5 "Image
// preparation").
package imageprep

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"

	"github.com/disintegration/imaging"
)

const (
	Width   = 3840
	Height  = 2160
	Quality = 92

	// MaxPixels bounds the decoded size to keep a hostile upload from
	// exhausting memory.
	MaxPixels = 120_000_000
)

// Fit chooses how an image of another aspect ratio is made 16:9.
type Fit int

const (
	// FitPad keeps the whole image and adds black bars.
	FitPad Fit = iota
	// FitCrop fills the frame and cuts the overflowing edges.
	FitCrop
)

var ErrUnsupported = errors.New("unsupported image; only JPEG and PNG are accepted")

// ParseFit parses "pad", "crop" or "" (pad).
func ParseFit(s string) (Fit, error) {
	switch s {
	case "", "pad":
		return FitPad, nil
	case "crop":
		return FitCrop, nil
	}
	return 0, fmt.Errorf("imageprep: unknown fit %q; want pad or crop", s)
}

// Prepare decodes a JPEG or PNG, applies its EXIF orientation, fits it to
// 3840x2160 and encodes it as JPEG.
func Prepare(r io.Reader, fit Fit) ([]byte, error) {
	return PrepareTo(r, fit, Width, Height)
}

// PrepareTo is Prepare with another target size, so tests can exercise the
// same pipeline without 4K image work.
func PrepareTo(r io.Reader, fit Fit, width, height int) ([]byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("imageprep: read: %w", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, ErrUnsupported
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, fmt.Errorf("the image is %dx%d; at most %d pixels are accepted", cfg.Width, cfg.Height, MaxPixels)
	}
	img, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("imageprep: decode: %w", err)
	}

	var fitted image.Image
	switch fit {
	case FitCrop:
		fitted = imaging.Fill(img, width, height, imaging.Center, imaging.Lanczos)
	default:
		fitted = pad(img, width, height)
	}
	// Flatten onto black so transparent PNG pixels match the bars.
	out := imaging.OverlayCenter(imaging.New(width, height, color.Black), fitted, 1)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: Quality}); err != nil {
		return nil, fmt.Errorf("imageprep: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// pad scales img, up or down, to the largest size that fits the frame.
func pad(img image.Image, width, height int) image.Image {
	b := img.Bounds()
	scale := math.Min(float64(width)/float64(b.Dx()), float64(height)/float64(b.Dy()))
	w := max(1, min(width, int(math.Round(float64(b.Dx())*scale))))
	h := max(1, min(height, int(math.Round(float64(b.Dy())*scale))))
	return imaging.Resize(img, w, h, imaging.Lanczos)
}
