package sourcemap

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/intake-es/mothlog/internal/store"
)

const UploadPattern = "POST /api/{project_id}/releases/{release}/sourcemaps/"

type savedFile struct {
	Name string `json:"name"`
	File string `json:"file"`
	Size int    `json:"size"`
}

// Register añade al mux la ruta de subida propia y las de sentry-cli.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc(UploadPattern, s.upload)
	s.registerCLI(mux)
}

// authorized acepta SOURCEMAP_UPLOAD_TOKEN o un token activo de la API. Si
// no, ya ha respondido 401 o 500.
func (s *Service) authorized(w http.ResponseWriter, r *http.Request) bool {
	scheme, got, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	got = strings.TrimSpace(got)
	if strings.EqualFold(scheme, "Bearer") && got != "" {
		if s.opts.UploadToken != "" {
			a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(s.opts.UploadToken))
			if subtle.ConstantTimeCompare(a[:], b[:]) == 1 {
				return true
			}
		}
		_, err := s.store.Authenticate(r.Context(), got)
		if err == nil {
			return true
		}
		if !errors.Is(err, store.ErrNotFound) {
			s.log.ErrorContext(r.Context(), "authenticate source map upload", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return false
		}
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "invalid token")
	return false
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	projectID, err := strconv.ParseInt(r.PathValue("project_id"), 10, 64)
	if err != nil || projectID <= 0 {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	release := r.PathValue("release")
	if release == "" {
		writeError(w, http.StatusNotFound, "missing release")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxRequestBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with file fields")
		return
	}
	var (
		files []store.SourceMapFile
		names []string
		seen  = map[string]int{}
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.readError(w, err)
			return
		}
		if part.FormName() != "file" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				s.readError(w, err)
				return
			}
			continue
		}
		name := path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		file, ok := strings.CutSuffix(name, ".map")
		if !ok || file == "" || name == "." || name == "/" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a .map file", part.FileName()))
			return
		}
		b, err := io.ReadAll(io.LimitReader(part, s.opts.MaxFileBytes+1))
		if err != nil {
			s.readError(w, err)
			return
		}
		if int64(len(b)) > s.opts.MaxFileBytes {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("%s is over %d MiB", name, s.opts.MaxFileBytes>>20))
			return
		}
		if i, ok := seen[file]; ok {
			files[i].Map = b
			continue
		}
		seen[file] = len(files)
		files = append(files, store.SourceMapFile{File: file, Map: b})
		names = append(names, name)
	}
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "no file field")
		return
	}
	for i, f := range files {
		m, err := Parse(f.Map)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is not a valid v3 source map: %v", names[i], err))
			return
		}
		if err := s.checkSize(names[i], m); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
	}

	err = s.store.PutSourceMaps(r.Context(), projectID, release, files)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "upload source maps", "project", projectID, "release", release, "err", err)
		writeError(w, http.StatusInternalServerError, "could not save them")
		return
	}
	s.cache.clear()
	saved := make([]savedFile, len(files))
	for i, f := range files {
		saved[i] = savedFile{Name: names[i], File: f.File, Size: len(f.Map)}
	}
	s.log.InfoContext(r.Context(), "source maps uploaded", "project", projectID, "release", release, "n", len(saved))
	writeJSON(w, http.StatusOK, map[string]any{"release": release, "files": saved})
}

func (s *Service) readError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the request is over %d MiB", s.opts.MaxRequestBytes>>20))
		return
	}
	writeError(w, http.StatusBadRequest, "invalid multipart: "+err.Error())
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
