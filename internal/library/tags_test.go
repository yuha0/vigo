package library

import (
	"slices"
	"testing"
)

func TestSystemTags(t *testing.T) {
	tests := []struct {
		name string
		f    facts
		want []string
	}{
		{"art store", facts{tvOnly: true, contentType: "server", onTV: true, categories: []string{"MY-C0004"}},
			[]string{"source/art-store", "status/on-tv", "tv-category/MY-C0004"}},
		{"outside upload", facts{tvOnly: true, contentType: "mobile", onTV: true},
			[]string{"source/upload", "status/on-tv"}},
		{"synced 4K file", facts{refs: []ref{{"nas", "2024/snow.jpg"}}, onTV: true, width: 3840, height: 2160},
			[]string{"folder/2024", "source/nas", "status/on-tv"}},
		{"pending low-res portrait at the root", facts{refs: []ref{{"nas", "p.jpg"}}, width: 1080, height: 1920},
			[]string{"issue/aspect-ratio", "issue/low-resolution", "source/nas"}},
		{"within 1% of 16:9", facts{refs: []ref{{"nas", "a.jpg"}}, width: 3860, height: 2160},
			[]string{"source/nas"}},
		{"4:3", facts{refs: []ref{{"nas", "a.jpg"}}, width: 4032, height: 3024},
			[]string{"issue/aspect-ratio", "source/nas"}},
		{"two sources", facts{refs: []ref{{"a", "x/1.jpg"}, {"b", "y/1.jpg"}}, width: 3840, height: 2160},
			[]string{"folder/x", "folder/y", "source/a", "source/b"}},
	}
	for _, tt := range tests {
		if got := systemTags(tt.f); !slices.Equal(got, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFileName(t *testing.T) {
	tests := []struct{ name, format, want string }{
		{"IMG_0001.JPG", "jpeg", "IMG_0001.JPG"},
		{"photo.jpeg", "jpeg", "photo.jpeg"},
		{"converted.heic", "jpeg", "converted.jpg"},
		{"a/b/../c.png", "png", "c.png"},
		{`C:\Users\me\pic.png`, "png", "pic.png"},
		{".bashrc", "jpeg", "bashrc.jpg"},
		{"...", "jpeg", "upload.jpg"},
		{"", "png", "upload.png"},
		{"screenshot.png", "jpeg", "screenshot.jpg"},
	}
	for _, tt := range tests {
		if got := fileName(tt.name, tt.format); got != tt.want {
			t.Errorf("fileName(%q, %s) = %q, want %q", tt.name, tt.format, got, tt.want)
		}
	}
}

func TestCheckUserTag(t *testing.T) {
	for _, ok := range []string{"favourite", "Winter 2024", "家族"} {
		if _, err := checkUserTag(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", "folder/x", "tab\tbed", string(make([]rune, 65))} {
		if _, err := checkUserTag(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
