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
