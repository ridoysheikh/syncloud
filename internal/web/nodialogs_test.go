package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoNativeDialogs keeps the browser's confirm(), alert() and prompt()
// out of the dashboard: it uses the app's dialogs (web/src/ui/dialogs.tsx).
func TestNoNativeDialogs(t *testing.T) {
	native := regexp.MustCompile(`(^|[^.\w])(window\.)?(confirm|alert|prompt)\(`)
	root := filepath.Join("..", "..", "web", "src")
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")) {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") || strings.HasPrefix(code, "*") || strings.HasPrefix(code, "/*") {
				continue
			}
			if native.MatchString(code) {
				t.Errorf("%s:%d uses a native dialog; use confirmDialog/alertDialog from @/ui/dialogs: %s", p, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestNoBareChoiceInputs keeps raw checkboxes and radio buttons out of the
// dashboard (Phase 16b): choices are cards, chips, segmented controls or
// toggles from web/src/ui/choice.tsx and controls.tsx.
func TestNoBareChoiceInputs(t *testing.T) {
	bare := regexp.MustCompile(`type=["'{](checkbox|radio)`)
	root := filepath.Join("..", "..", "web", "src")
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".tsx") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d uses a bare checkbox or radio; use ChoiceCards, ChipSelect, Segmented or Toggle: %s", p, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestNoNativeSelects keeps the browser's unthemed <select>, <datalist> and
// <dialog> out of the pages: they use Select, SearchSelect and Combobox
// (web/src/ui/select.tsx) and the one Dialog (web/src/ui/Dialog.tsx).
func TestNoNativeSelects(t *testing.T) {
	native := regexp.MustCompile(`<(select|datalist|dialog)[\s>]|\slist=["{]`)
	root := filepath.Join("..", "..", "web", "src")
	allowed := map[string]bool{"Dialog.tsx": true}
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".tsx") || allowed[filepath.Base(p)] {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") || strings.HasPrefix(code, "*") || strings.HasPrefix(code, "/*") {
				continue
			}
			if native.MatchString(line) {
				t.Errorf("%s:%d uses a native select, datalist or dialog; use Select, SearchSelect, Combobox or Dialog from @/ui: %s", p, i+1, code)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
