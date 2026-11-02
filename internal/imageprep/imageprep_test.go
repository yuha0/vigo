package imageprep

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

var (
	red  = color.RGBA{255, 0, 0, 255}
	blue = color.RGBA{0, 0, 255, 255}
)

// twoTone is landscape with a red top half and a blue bottom half.
func twoTone(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := red
			if y >= h/2 {
				c = blue
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withOrientation inserts an APP1 Exif segment carrying only the
// Orientation tag (274) right after the JPEG SOI marker.
func withOrientation(t *testing.T, jpg []byte, orientation uint16) []byte {
	t.Helper()
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	binary.Write(&tiff, binary.BigEndian, uint32(8))      // IFD0 offset
	binary.Write(&tiff, binary.BigEndian, uint16(1))      // entries
	binary.Write(&tiff, binary.BigEndian, uint16(0x0112)) // Orientation
	binary.Write(&tiff, binary.BigEndian, uint16(3))      // SHORT
	binary.Write(&tiff, binary.BigEndian, uint32(1))      // count
	binary.Write(&tiff, binary.BigEndian, orientation)    // value
	binary.Write(&tiff, binary.BigEndian, uint16(0))      // padding
	binary.Write(&tiff, binary.BigEndian, uint32(0))      // next IFD
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)

	var out bytes.Buffer
	out.Write(jpg[:2])
	out.Write([]byte{0xFF, 0xE1})
	binary.Write(&out, binary.BigEndian, uint16(len(payload)+2))
	out.Write(payload)
	out.Write(jpg[2:])
	return out.Bytes()
}

func near(c color.Color, want color.RGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, b uint8) bool {
		v := int(a>>8) - int(b)
		return v > -40 && v < 40
	}
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

var black = color.RGBA{0, 0, 0, 255}

func TestPrepare(t *testing.T) {
	plain := encodeJPEG(t, twoTone(300, 200))
	rotated := withOrientation(t, plain, 6)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, twoTone(1000, 500)); err != nil {
		t.Fatal(err)
	}

	type probe struct {
		x, y int
		want color.RGBA
	}
	tests := []struct {
		name   string
		input  []byte
		fit    Fit
		probes []probe
	}{
		{
			// 300x200 raw, displayed 200x300 portrait: rotating 90° clockwise
			// moves the red top to the right. Padded to 1440x2160 centred.
			name:  "exif orientation 6 padded",
			input: rotated,
			fit:   FitPad,
			probes: []probe{
				{100, 1080, black},
				{3740, 1080, black},
				{1920 - 360, 1080, blue},
				{1920 + 360, 1080, red},
			},
		},
		{
			// Crop to fill: 3840 wide, the middle band of the portrait image,
			// still blue on the left and red on the right.
			name:  "exif orientation 6 cropped",
			input: rotated,
			fit:   FitCrop,
			probes: []probe{
				{100, 1080, blue},
				{3740, 1080, red},
				{100, 100, blue},
			},
		},
		{
			// Without the tag the image stays landscape: 3240x2160 padded.
			name:  "no orientation padded",
			input: plain,
			fit:   FitPad,
			probes: []probe{
				{100, 1080, black},
				{1920, 500, red},
				{1920, 1700, blue},
			},
		},
		{
			// 2:1 PNG is wider than 16:9: letterboxed top and bottom.
			name:  "png letterboxed",
			input: pngBuf.Bytes(),
			fit:   FitPad,
			probes: []probe{
				{1920, 50, black},
				{1920, 2110, black},
				{1920, 700, red},
				{1920, 1400, blue},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Prepare(bytes.NewReader(tt.input), tt.fit)
			if err != nil {
				t.Fatal(err)
			}
			img, format, err := image.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if format != "jpeg" {
				t.Fatalf("format = %s", format)
			}
			if b := img.Bounds(); b.Dx() != Width || b.Dy() != Height {
				t.Fatalf("size = %dx%d", b.Dx(), b.Dy())
			}
			for _, p := range tt.probes {
				if got := img.At(p.x, p.y); !near(got, p.want) {
					t.Errorf("pixel (%d,%d) = %v, want ~%v", p.x, p.y, got, p.want)
				}
			}
		})
	}
}

func TestPrepareRejects(t *testing.T) {
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 4, 4), []color.Color{black}), nil); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string][]byte{
		"gif":     gifBuf.Bytes(),
		"garbage": []byte("not an image"),
		"empty":   nil,
	} {
		if _, err := Prepare(bytes.NewReader(in), FitPad); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: err = %v, want ErrUnsupported", name, err)
		}
	}
}

func TestParseFit(t *testing.T) {
	for in, want := range map[string]Fit{"": FitPad, "pad": FitPad, "crop": FitCrop} {
		got, err := ParseFit(in)
		if err != nil || got != want {
			t.Errorf("ParseFit(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseFit("stretch"); err == nil {
		t.Error("ParseFit(stretch) succeeded")
	}
}

func TestProbe(t *testing.T) {
	plain := encodeJPEG(t, twoTone(300, 200))
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, twoTone(64, 48))
	tests := []struct {
		name    string
		data    []byte
		w, h    int
		format  string
		wantErr bool
	}{
		{"jpeg", plain, 300, 200, "jpeg", false},
		{"orientation 6 swaps", withOrientation(t, plain, 6), 200, 300, "jpeg", false},
		{"orientation 8 swaps", withOrientation(t, plain, 8), 200, 300, "jpeg", false},
		{"orientation 3 keeps", withOrientation(t, plain, 3), 300, 200, "jpeg", false},
		{"png", pngBuf.Bytes(), 64, 48, "png", false},
		{"garbage", []byte("nope"), 0, 0, "", true},
	}
	for _, tt := range tests {
		w, h, f, err := Probe(tt.data)
		if (err != nil) != tt.wantErr || w != tt.w || h != tt.h || f != tt.format {
			t.Errorf("%s: %dx%d %s %v", tt.name, w, h, f, err)
		}
	}
}

func TestCropAndScale(t *testing.T) {
	rotated := withOrientation(t, encodeJPEG(t, twoTone(300, 200)), 6)
	// Upright the image is 200x300: blue left half, red right half.
	out, err := Crop(rotated, image.Rect(100, 0, 200, 300))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 300 {
		t.Errorf("crop size = %v", b)
	}
	if !near(img.At(50, 150), red) {
		t.Errorf("crop pixel = %v, want red", img.At(50, 150))
	}
	if orientation(out) != 1 {
		t.Error("orientation tag survived the crop")
	}
	if _, err := Crop(rotated, image.Rect(0, 0, 300, 300)); err == nil {
		t.Error("crop outside the upright bounds accepted")
	}

	s, err := Scaled(rotated, 100, 100, 80)
	if err != nil {
		t.Fatal(err)
	}
	w, h, _, _ := Probe(s)
	if (w != 66 && w != 67) || h != 100 {
		t.Errorf("scaled to %dx%d", w, h)
	}
}

func TestCropKeepsFormat(t *testing.T) {
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, twoTone(160, 120))
	out, err := Crop(pngBuf.Bytes(), image.Rect(0, 15, 160, 105))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("\x89PNG")) {
		t.Fatalf("PNG cropped to %x", out[:4])
	}
	if w, h, f, _ := Probe(out); w != 160 || h != 90 || f != "png" {
		t.Errorf("cropped PNG = %dx%d %s", w, h, f)
	}
	jpg, err := Crop(encodeJPEG(t, twoTone(160, 120)), image.Rect(0, 15, 160, 105))
	if err != nil || !bytes.HasPrefix(jpg, []byte{0xFF, 0xD8}) {
		t.Errorf("JPEG crop: %v", err)
	}
}
