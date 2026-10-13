package branding

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/store"
)

func encode(t *testing.T, w, h int, asJPEG bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.NRGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if asJPEG {
		_ = jpeg.Encode(&buf, img, nil)
	} else {
		_ = png.Encode(&buf, img)
	}
	return buf.Bytes()
}

func TestRender(t *testing.T) {
	imgs, err := Render(encode(t, 400, 200, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		b    []byte
		size int
	}{{imgs.Logo, SizeLogo}, {imgs.Favicon, SizeFavic}, {imgs.Apple, SizeApple}} {
		img, err := png.Decode(bytes.NewReader(c.b))
		if err != nil || img.Bounds().Dx() != c.size || img.Bounds().Dy() != c.size {
			t.Fatalf("rendition %v %v", img.Bounds(), err)
		}
		// A wide logo is centred: the top row is transparent.
		if _, _, _, a := img.At(c.size/2, 0).RGBA(); a != 0 {
			t.Errorf("size %d: top not transparent", c.size)
		}
	}
	if _, err := Render(encode(t, 64, 64, true)); err != nil {
		t.Fatalf("jpeg: %v", err)
	}
	for name, data := range map[string][]byte{
		"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"tiny":  encode(t, 8, 8, false),
		"empty": nil,
		"big":   make([]byte, MaxLogoBytes+1),
	} {
		if _, err := Render(data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestManager(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := New(st)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if b := m.Get(); b.Name != DefaultName || b.Title != DefaultName || b.HasLogo {
		t.Fatalf("defaults %+v", b)
	}
	if b, err := m.SetText(ctx, "  Acme   Cloud ", ""); err != nil || b.Name != "Acme Cloud" || b.Title != "Acme Cloud" {
		t.Fatalf("set name: %+v %v", b, err)
	}
	if _, err := m.SetText(ctx, "<b>x</b>", ""); err == nil {
		t.Fatal("markup accepted")
	}
	if _, err := m.SetLogo(ctx, encode(t, 64, 64, false)); err != nil {
		t.Fatal(err)
	}
	m2 := New(st)
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if b := m2.Get(); b.Name != "Acme Cloud" || !b.HasLogo || b.Version == "default" || m2.Images() == nil {
		t.Fatalf("reloaded %+v", b)
	}
	if b, err := m2.RemoveLogo(ctx); err != nil || b.HasLogo || m2.Images() != nil {
		t.Fatalf("remove: %+v %v", b, err)
	}
}
