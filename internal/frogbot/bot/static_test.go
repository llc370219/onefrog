package bot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedFrontendServesNestedAssets(t *testing.T) {
	handler := spa(fstest.MapFS{
		"index.html":            {Data: []byte("<html>Frog Bot</html>")},
		"assets/app.js":         {Data: []byte("console.log('frog');")},
		"assets/bots/frog.webp": {Data: []byte("image payload")},
	})
	for _, tc := range []struct {
		path, body, mime string
	}{
		{"/", "<html>Frog Bot</html>", "text/html"},
		{"/assets/app.js", "console.log('frog');", "javascript"},
		{"/assets/bots/frog.webp", "image payload", "image/webp"},
		{"/some/client/route", "<html>Frog Bot</html>", "text/html"},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != http.StatusOK || w.Body.String() != tc.body || !strings.Contains(w.Header().Get("Content-Type"), tc.mime) {
			t.Errorf("GET %s: status %d, content type %q, body %q", tc.path, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
	}
}
