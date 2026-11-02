package imageprep

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"github.com/disintegration/imaging"
)

// Probe returns the upright (EXIF-applied) dimensions and format of a JPEG
// or PNG without decoding the pixels.
func Probe(data []byte) (width, height int, format string, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return 0, 0, "", ErrUnsupported
	}
	width, height = cfg.Width, cfg.Height
	if format == "jpeg" && orientation(data) >= 5 {
		width, height = height, width
	}
	return width, height, format, nil
}

// orientation reads the EXIF Orientation tag (274) of a JPEG, 1 when absent
// or unreadable. Values 5 to 8 rotate by 90 degrees.
func orientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[ifd:]))
	for k := range count {
		e := ifd + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// decodeUpright decodes a JPEG or PNG with its EXIF orientation applied.
func decodeUpright(data []byte) (image.Image, error) {
	w, h, _, err := Probe(data)
	if err != nil {
		return nil, err
	}
	if int64(w)*int64(h) > MaxPixels {
		return nil, fmt.Errorf("the image is %dx%d; at most %d pixels are accepted", w, h, MaxPixels)
	}
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("imageprep: decode: %w", err)
	}
	return img, nil
}

func jpegBytes(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("imageprep: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// Scaled returns an upright JPEG no larger than maxW x maxH, keeping the
// aspect ratio and never upscaling. Used for thumbnails and previews.
func Scaled(data []byte, maxW, maxH, quality int) ([]byte, error) {
	img, err := decodeUpright(data)
	if err != nil {
		return nil, err
	}
	return jpegBytes(imaging.Fit(img, maxW, maxH, imaging.Lanczos), quality)
}

// Crop cuts r, in upright pixels, out of the image and re-encodes it in
// the same format: JPEG quality 92, or PNG for a PNG, so the file's name
// stays correct. Re-encoding bakes the orientation in and drops all
// metadata.
func Crop(data []byte, r image.Rectangle) ([]byte, error) {
	_, _, format, err := Probe(data)
	if err != nil {
		return nil, err
	}
	img, err := decodeUpright(data)
	if err != nil {
		return nil, err
	}
	if r.Empty() || !r.In(img.Bounds()) {
		return nil, fmt.Errorf("the crop %v is not inside the %dx%d image", r, img.Bounds().Dx(), img.Bounds().Dy())
	}
	cropped := imaging.Crop(img, r)
	if format == "png" {
		var buf bytes.Buffer
		if err := png.Encode(&buf, cropped); err != nil {
			return nil, fmt.Errorf("imageprep: encode: %w", err)
		}
		return buf.Bytes(), nil
	}
	return jpegBytes(cropped, Quality)
}
