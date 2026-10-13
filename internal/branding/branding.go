// Package branding keeps the workspace's name, browser title and logo
// (Phase 21). Unset values fall back to SynCloud's own name and the logos
// in web/public.
package branding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // decoders for uploaded logos
	"image/png"
	"strings"
	"sync"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/ridoysheikh/syncloud/internal/store"
)

const (
	// DefaultName is the workspace name until one is set.
	DefaultName = "SynCloud"
	// MaxLogoBytes is the largest logo upload.
	MaxLogoBytes = 512 << 10

	settingText = "branding.text"
	settingLogo = "branding.logo" // JSON: the three PNG sizes, base64
)

// Image sizes made from the uploaded logo.
const (
	SizeLogo  = 256
	SizeFavic = 32
	SizeApple = 180
)

// ErrInvalid wraps validation errors.
type ErrInvalid struct{ Err error }

func (e ErrInvalid) Error() string { return e.Err.Error() }

// Branding is what the API shows (public).
type Branding struct {
	Name    string `json:"name"`
	Title   string `json:"title"` // the browser tab; defaults to Name
	HasLogo bool   `json:"hasLogo"`
	// Version changes with every logo upload (cache busting).
	Version string `json:"version"`
}

type text struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Images are the PNG renditions of the logo.
type Images struct {
	Logo, Favicon, Apple []byte
}

type Manager struct {
	st *store.Store

	mu     sync.RWMutex
	text   text
	images *Images
	ver    string
}

func New(st *store.Store) *Manager { return &Manager{st: st} }

// Load reads the stored branding.
func (m *Manager) Load(ctx context.Context) error {
	var t text
	if v, ok, err := m.st.GetSetting(ctx, settingText); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal([]byte(v), &t); err != nil {
			return fmt.Errorf("branding: %w", err)
		}
	}
	var imgs *Images
	if v, ok, err := m.st.GetSetting(ctx, settingLogo); err != nil {
		return err
	} else if ok {
		var raw struct{ Logo, Favicon, Apple []byte }
		if err := json.Unmarshal([]byte(v), &raw); err != nil {
			return fmt.Errorf("branding logo: %w", err)
		}
		imgs = &Images{Logo: raw.Logo, Favicon: raw.Favicon, Apple: raw.Apple}
	}
	m.mu.Lock()
	m.text, m.images, m.ver = t, imgs, version(imgs)
	m.mu.Unlock()
	return nil
}

func version(imgs *Images) string {
	if imgs == nil {
		return "default"
	}
	s := sha256.Sum256(imgs.Logo)
	return hex.EncodeToString(s[:6])
}

// Get returns the branding with defaults applied.
func (m *Manager) Get() Branding {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b := Branding{Name: m.text.Name, Title: m.text.Title, HasLogo: m.images != nil, Version: m.ver}
	if b.Name == "" {
		b.Name = DefaultName
	}
	if b.Title == "" {
		b.Title = b.Name
	}
	return b
}

// Images returns the logo renditions (nil = the defaults).
func (m *Manager) Images() *Images {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.images
}

func clean(s string, max int, what string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		return "", ErrInvalid{fmt.Errorf("the %s may have at most %d characters", what, max)}
	}
	if strings.ContainsAny(s, "<>") {
		return "", ErrInvalid{fmt.Errorf("the %s may not contain < or >", what)}
	}
	return s, nil
}

// SetText sets the name and title ("" = the default).
func (m *Manager) SetText(ctx context.Context, name, title string) (Branding, error) {
	name, err := clean(name, 40, "name")
	if err != nil {
		return Branding{}, err
	}
	if title, err = clean(title, 60, "title"); err != nil {
		return Branding{}, err
	}
	t := text{Name: name, Title: title}
	b, _ := json.Marshal(t)
	if err := m.st.SetSetting(ctx, settingText, string(b)); err != nil {
		return Branding{}, err
	}
	m.mu.Lock()
	m.text = t
	m.mu.Unlock()
	return m.Get(), nil
}

// SetLogo validates an uploaded image (PNG, JPEG or WebP; no SVG) and
// stores square PNG renditions of it.
func (m *Manager) SetLogo(ctx context.Context, data []byte) (Branding, error) {
	imgs, err := Render(data)
	if err != nil {
		return Branding{}, err
	}
	raw, _ := json.Marshal(struct{ Logo, Favicon, Apple []byte }{imgs.Logo, imgs.Favicon, imgs.Apple})
	if err := m.st.SetSetting(ctx, settingLogo, string(raw)); err != nil {
		return Branding{}, err
	}
	m.mu.Lock()
	m.images, m.ver = imgs, version(imgs)
	m.mu.Unlock()
	return m.Get(), nil
}

// RemoveLogo goes back to the default logo.
func (m *Manager) RemoveLogo(ctx context.Context) (Branding, error) {
	if err := m.st.DeleteSetting(ctx, settingLogo); err != nil {
		return Branding{}, err
	}
	m.mu.Lock()
	m.images, m.ver = nil, version(nil)
	m.mu.Unlock()
	return m.Get(), nil
}

// DecodeDataURL accepts "data:image/png;base64,…" or bare base64.
func DecodeDataURL(s string) ([]byte, error) {
	if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i > 0 {
		s = s[i+1:]
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, ErrInvalid{errors.New("the logo must be base64 image data")}
	}
	return b, nil
}

// Render decodes an image and makes the logo, favicon and touch icon.
// Non-square images are centred on a transparent square.
func Render(data []byte) (*Images, error) {
	if len(data) == 0 {
		return nil, ErrInvalid{errors.New("the logo is empty")}
	}
	if len(data) > MaxLogoBytes {
		return nil, ErrInvalid{fmt.Errorf("the logo may be at most %d KB", MaxLogoBytes>>10)}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrInvalid{errors.New("the logo must be a PNG, JPEG or WebP image")}
	}
	if cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 4096 || cfg.Height > 4096 {
		return nil, ErrInvalid{errors.New("the logo must be 16–4096 pixels on each side")}
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrInvalid{fmt.Errorf("decode %s: %w", format, err)}
	}
	out := &Images{}
	for _, r := range []struct {
		size int
		dst  *[]byte
	}{{SizeLogo, &out.Logo}, {SizeFavic, &out.Favicon}, {SizeApple, &out.Apple}} {
		b, err := square(src, r.size)
		if err != nil {
			return nil, err
		}
		*r.dst = b
	}
	return out, nil
}

func square(src image.Image, size int) ([]byte, error) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	// Fit inside size×size, keeping the aspect ratio.
	tw, th := size, size
	if w > h {
		th = max(1, h*size/w)
	} else if h > w {
		tw = max(1, w*size/h)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.Draw(dst, dst.Bounds(), image.Transparent, image.Point{}, draw.Src)
	off := image.Pt((size-tw)/2, (size-th)/2)
	xdraw.CatmullRom.Scale(dst, image.Rectangle{Min: off, Max: off.Add(image.Pt(tw, th))}, src, b, xdraw.Over, nil)
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
