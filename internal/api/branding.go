package api

import (
	"bytes"
	"errors"
	"html"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/ridoysheikh/syncloud/internal/branding"
)

func (s *Server) brandingError(w http.ResponseWriter, what string, err error) {
	var inv branding.ErrInvalid
	if errors.As(err, &inv) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
		return
	}
	s.internalError(w, what, err)
}

// handleGetBranding is public: the sign-in page shows the workspace.
func (s *Server) handleGetBranding(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.brand())
}

func (s *Server) brand() branding.Branding {
	if s.branding == nil {
		return branding.Branding{Name: branding.DefaultName, Title: branding.DefaultName, Version: "default"}
	}
	return s.branding.Get()
}

func (s *Server) requireBranding(w http.ResponseWriter) bool {
	if s.branding == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "branding is not available")
		return false
	}
	return true
}

func (s *Server) handleSetBranding(w http.ResponseWriter, r *http.Request) {
	if !s.requireBranding(w) {
		return
	}
	var in struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	b, err := s.branding.SetText(r.Context(), in.Name, in.Title)
	if err != nil {
		s.brandingError(w, "set branding", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "settings:SetBranding", "srn:syncloud:settings/branding", map[string]any{"name": b.Name, "title": b.Title})
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleSetBrandingLogo(w http.ResponseWriter, r *http.Request) {
	if !s.requireBranding(w) {
		return
	}
	var in struct {
		Logo string `json:"logo"` // data URL or base64
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	data, err := branding.DecodeDataURL(in.Logo)
	if err != nil {
		s.brandingError(w, "set logo", err)
		return
	}
	b, err := s.branding.SetLogo(r.Context(), data)
	if err != nil {
		s.brandingError(w, "set logo", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "settings:SetBrandingLogo", "srn:syncloud:settings/branding", nil)
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleDeleteBrandingLogo(w http.ResponseWriter, r *http.Request) {
	if !s.requireBranding(w) {
		return
	}
	b, err := s.branding.RemoveLogo(r.Context())
	if err != nil {
		s.brandingError(w, "remove logo", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "settings:DeleteBrandingLogo", "srn:syncloud:settings/branding", nil)
	writeJSON(w, http.StatusOK, b)
}

// brandingFiles maps the served names to the default files in web/public.
var brandingFiles = map[string]string{"logo.png": "logo-square.png", "favicon.png": "favicon.ico", "apple-touch-icon.png": "apple-touch-icon.png"}

// handleBrandingFile serves the workspace logo, favicon and touch icon,
// or SynCloud's defaults.
func (s *Server) handleBrandingFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	def, ok := brandingFiles[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b := s.brand()
	etag := `"` + b.Version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=300")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	var imgs *branding.Images
	if s.branding != nil {
		imgs = s.branding.Images()
	}
	if imgs == nil {
		data, err := fs.ReadFile(s.web, def)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(data))
		if name == "favicon.png" {
			w.Header().Set("Content-Type", "image/x-icon")
		}
		_, _ = w.Write(data)
		return
	}
	data := map[string][]byte{"logo.png": imgs.Logo, "favicon.png": imgs.Favicon, "apple-touch-icon.png": imgs.Apple}[name]
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}

var (
	titleRE = regexp.MustCompile(`<title>[^<]*</title>`)
	iconRE  = regexp.MustCompile(`<link rel="(icon|apple-touch-icon)"[^>]*>`)
)

// brandIndex puts the workspace title and icons into index.html, so the
// browser tab is right before the dashboard's script runs.
func (s *Server) brandIndex(index []byte) []byte {
	b := s.brand()
	v := html.EscapeString(b.Version)
	index = titleRE.ReplaceAll(index, []byte("<title>"+html.EscapeString(b.Title)+"</title>"))
	first := true
	index = iconRE.ReplaceAllFunc(index, func(m []byte) []byte {
		if !first {
			return nil
		}
		first = false
		return []byte(`<link rel="icon" href="/branding/favicon.png?v=` + v + `" /><link rel="apple-touch-icon" href="/branding/apple-touch-icon.png?v=` + v + `" />`)
	})
	return bytes.TrimSpace(index)
}
