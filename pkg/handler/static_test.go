package handler

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestStaticHandler_Handle_Root(t *testing.T) {
	testFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>home</html>")},
	}
	h := NewStaticHandler(testFS, false)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "home")
}

func TestStaticHandler_Handle_Setup(t *testing.T) {
	testFS := fstest.MapFS{
		"setup.html": &fstest.MapFile{Data: []byte("<html>setup</html>")},
	}
	h := NewStaticHandler(testFS, false)

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "setup")
}

func TestStaticHandler_Handle_SwaggerEnabled(t *testing.T) {
	testFS := fstest.MapFS{
		"docs.html":    &fstest.MapFile{Data: []byte("<html>swagger</html>")},
		"openapi.json": &fstest.MapFile{Data: []byte(`{"openapi":"3.0.0"}`)},
	}
	h := NewStaticHandler(testFS, true)

	// 測試 /swagger
	req := httptest.NewRequest(http.MethodGet, "/swagger", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "swagger")

	// 測試 /openapi.json
	req = httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec = httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "openapi")
}

func TestStaticHandler_Handle_SwaggerDisabled(t *testing.T) {
	testFS := fstest.MapFS{
		"docs.html":    &fstest.MapFile{Data: []byte("<html>swagger</html>")},
		"openapi.json": &fstest.MapFile{Data: []byte(`{"openapi":"3.0.0"}`)},
	}
	h := NewStaticHandler(testFS, false)

	// 測試 /swagger 應該回 404
	req := httptest.NewRequest(http.MethodGet, "/swagger", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)

	// 測試 /openapi.json 應該回 404
	req = httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec = httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestStaticHandler_Handle_NotFound(t *testing.T) {
	testFS := fstest.MapFS{}
	h := NewStaticHandler(testFS, false)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestStaticHandler_Handle_MethodNotAllowed(t *testing.T) {
	testFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>home</html>")},
	}
	h := NewStaticHandler(testFS, false)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
}

func TestStaticHandler_Handle_FileNotFound(t *testing.T) {
	// 建立一個會回錯誤的 mock FS
	mockFS := &errorFS{}
	h := NewStaticHandler(mockFS, false)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// errorFS 是一個會回錯誤的 mock fs.FS
type errorFS struct{}

func (e *errorFS) Open(name string) (fs.File, error) {
	return nil, fs.ErrInvalid
}
