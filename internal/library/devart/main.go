// Command devart seeds a directory with generated sample art for local
// development against a file source. It covers each library case: in-spec
// 16:9 4K images, a non-16:9 image, a low-resolution one, a phone photo
// stored sideways with EXIF orientation 6, and a PNG.
//
// Existing files are left alone, so running it again is harmless.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type sample struct {
	path        string
	w, h        int
	hue         float64
	orientation uint16
	png         bool
}

var samples = []sample{
	{path: "landscapes/aurora.jpg", w: 3840, h: 2160, hue: 0.45},
	{path: "landscapes/dunes.jpg", w: 3840, h: 2160, hue: 0.08},
	{path: "landscapes/2024/harbour.jpg", w: 3840, h: 2160, hue: 0.6},
	{path: "four-by-three/garden.jpg", w: 4032, h: 3024, hue: 0.3},
	{path: "low-res/sketch.jpg", w: 1280, h: 720, hue: 0.8},
	{path: "phone/IMG_0042.jpg", w: 4032, h: 3024, hue: 0.95, orientation: 6},
	{path: "poster.png", w: 1080, h: 1920, hue: 0.15, png: true},
}

func main() {
	out := flag.String("out", ".dev/art", "directory to seed")
	flag.Parse()
	for _, s := range samples {
		p := filepath.Join(*out, s.path)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := write(p, s); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s %dx%d\n", p, s.w, s.h)
	}
}

func write(p string, s sample) error {
	img := render(s)
	var buf bytes.Buffer
	var err error
	if s.png {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88})
	}
	if err != nil {
		return err
	}
	data := buf.Bytes()
	if s.orientation != 0 {
		data = withOrientation(data, s.orientation)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// render draws a sky-to-ground gradient with a sun, so thumbnails are
// distinguishable and the orientation is obvious: the sun is near the top.
func render(s sample) image.Image {
	w, h := s.w, s.h
	if s.orientation >= 5 {
		w, h = h, w
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	sx, sy, sr := float64(w)*0.7, float64(h)*0.25, float64(min(w, h))*0.08
	for y := range h {
		t := float64(y) / float64(h)
		sky := hsv(s.hue, 0.5, 1-0.5*t)
		ground := hsv(math.Mod(s.hue+0.3, 1), 0.6, 0.35)
		for x := range w {
			c := sky
			if t > 0.65+0.05*math.Sin(float64(x)/float64(w)*6) {
				c = ground
			}
			if math.Hypot(float64(x)-sx, float64(y)-sy) < sr {
				c = color.RGBA{255, 230, 140, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	if s.orientation >= 5 {
		return rotateLeft(img)
	}
	return img
}

// rotateLeft stores the upright image sideways, as a phone sensor would;
// EXIF orientation 6 rotates it back.
func rotateLeft(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			dst.SetRGBA(y, b.Dx()-1-x, src.RGBAAt(x, y))
		}
	}
	return dst
}

func hsv(h, s, v float64) color.RGBA {
	i := math.Floor(h * 6)
	f := h*6 - i
	p, q, t := v*(1-s), v*(1-f*s), v*(1-(1-f)*s)
	var r, g, b float64
	switch int(i) % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return color.RGBA{uint8(r * 255), uint8(g * 255), uint8(b * 255), 255}
}

// withOrientation inserts an APP1 Exif segment holding only the
// Orientation tag right after the JPEG SOI marker.
func withOrientation(jpg []byte, orientation uint16) []byte {
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	for _, v := range []any{uint32(8), uint16(1), uint16(0x0112), uint16(3), uint32(1), orientation, uint16(0), uint32(0)} {
		binary.Write(&tiff, binary.BigEndian, v)
	}
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write(jpg[:2])
	out.Write([]byte{0xFF, 0xE1})
	binary.Write(&out, binary.BigEndian, uint16(len(payload)+2))
	out.Write(payload)
	out.Write(jpg[2:])
	return out.Bytes()
}
