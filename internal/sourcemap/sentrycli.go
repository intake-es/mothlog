package sourcemap

import (
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// Lo que se anuncia a sentry-cli para la subida por trozos.
const (
	chunkSize        = 8 << 20
	chunksPerRequest = 64
	chunkRequestSize = 32 << 20
	chunkConcurrency = 4
)

// registerCLI añade la API que usa sentry-cli para subir source maps (la
// subida por trozos de artifact bundles de las versiones actuales y la de
// ficheros de release de las antiguas) y para crear, finalizar y desplegar
// releases. La organización se acepta tal cual y
// los proyectos se buscan por nombre.
func (s *Service) registerCLI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/0/organizations/{org}/chunk-upload/{$}", s.cli(s.chunkOptions))
	mux.HandleFunc("POST /api/0/organizations/{org}/chunk-upload/{$}", s.cli(s.chunkUpload))
	mux.HandleFunc("POST /api/0/organizations/{org}/artifactbundle/assemble/{$}", s.cli(s.assemble))

	mux.HandleFunc("POST /api/0/organizations/{org}/releases/{$}", s.cli(s.newRelease))
	mux.HandleFunc("POST /api/0/projects/{org}/{project}/releases/{$}", s.cli(s.newRelease))
	mux.HandleFunc("PUT /api/0/organizations/{org}/releases/{version}/{$}", s.cli(s.finalizeRelease))
	mux.HandleFunc("PUT /api/0/projects/{org}/{project}/releases/{version}/{$}", s.cli(s.finalizeRelease))
	mux.HandleFunc("POST /api/0/organizations/{org}/releases/{version}/deploys/{$}", s.cli(s.newDeploy))
	mux.HandleFunc("GET /api/0/projects/{org}/{project}/releases/{version}/files/{$}", s.cli(s.releaseFiles))
	mux.HandleFunc("POST /api/0/projects/{org}/{project}/releases/{version}/files/{$}", s.cli(s.uploadReleaseFile))
}

// cliError lleva el mensaje en detail, que es lo que enseña sentry-cli.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

func cliErr(code int, format string, a ...any) error {
	return &cliError{code: code, msg: fmt.Sprintf(format, a...)}
}

func (s *Service) cli(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(w, r) {
			return
		}
		err := h(w, r)
		if err == nil {
			return
		}
		var ce *cliError
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &ce):
		case errors.As(err, &tooBig):
			ce = &cliError{http.StatusRequestEntityTooLarge, fmt.Sprintf("la petición pasa de %d MiB", tooBig.Limit>>20)}
		default:
			s.log.ErrorContext(r.Context(), "sentry-cli", "path", r.URL.Path, "err", err)
			ce = &cliError{http.StatusInternalServerError, "error interno"}
		}
		writeJSON(w, ce.code, map[string]string{"detail": ce.msg, "error": ce.msg})
	}
}

func (s *Service) chunkOptions(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, map[string]any{
		"url":              "/api/0/organizations/" + url.PathEscape(r.PathValue("org")) + "/chunk-upload/",
		"chunkSize":        chunkSize,
		"chunksPerRequest": chunksPerRequest,
		"maxFileSize":      s.opts.MaxBundleBytes,
		"maxRequestSize":   chunkRequestSize,
		"concurrency":      chunkConcurrency,
		"hashAlgorithm":    "sha1",
		"compression":      []string{"gzip"},
		"accept":           []string{"artifact_bundles"},
	})
	return nil
}

func isSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (s *Service) chunkUpload(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, chunkRequestSize+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return cliErr(http.StatusBadRequest, "se espera multipart/form-data con campos file o file_gzip")
	}
	chunks := map[string][]byte{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return multipartErr(err)
		}
		form := part.FormName()
		if form != "file" && form != "file_gzip" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				return multipartErr(err)
			}
			continue
		}
		sum := strings.ToLower(part.FileName())
		if !isSHA1(sum) {
			return cliErr(http.StatusBadRequest, "el nombre del trozo %q no es su sha1", part.FileName())
		}
		var body io.Reader = part
		if form == "file_gzip" {
			zr, err := gzip.NewReader(part)
			if err != nil {
				return cliErr(http.StatusBadRequest, "el trozo %s no es gzip: %v", sum, err)
			}
			body = zr
		}
		data, err := io.ReadAll(io.LimitReader(body, chunkSize+1))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				return err
			}
			return cliErr(http.StatusBadRequest, "no se pudo leer el trozo %s: %v", sum, err)
		}
		if len(data) > chunkSize {
			return cliErr(http.StatusRequestEntityTooLarge, "el trozo %s pasa de %d MiB", sum, chunkSize>>20)
		}
		if got := sha1.Sum(data); hex.EncodeToString(got[:]) != sum {
			return cliErr(http.StatusBadRequest, "el trozo %s no coincide con su sha1", sum)
		}
		chunks[sum] = data
		if len(chunks) > chunksPerRequest {
			return cliErr(http.StatusBadRequest, "más de %d trozos en una petición", chunksPerRequest)
		}
	}
	if len(chunks) == 0 {
		return cliErr(http.StatusBadRequest, "ningún trozo")
	}
	if err := s.store.PutUploadChunks(r.Context(), chunks); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func multipartErr(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return err
	}
	return cliErr(http.StatusBadRequest, "multipart no válido: %v", err)
}

type assembleRequest struct {
	Checksum string   `json:"checksum"`
	Chunks   []string `json:"chunks"`
	Projects []string `json:"projects"`
	Version  string   `json:"version"`
	Dist     string   `json:"dist"`
}

type assembleResponse struct {
	State         string   `json:"state"`
	MissingChunks []string `json:"missingChunks"`
	Detail        *string  `json:"detail"`
}

func assembleState(w http.ResponseWriter, state string, missing []string, detail string) {
	res := assembleResponse{State: state, MissingChunks: missing}
	if res.MissingChunks == nil {
		res.MissingChunks = []string{}
	}
	if detail != "" {
		res.Detail = &detail
	}
	writeJSON(w, http.StatusOK, res)
}

// assemble ensambla el artifact bundle con los trozos ya subidos y guarda
// sus mapas. Si faltan trozos responde not_found con cuáles, para que
// sentry-cli los suba y vuelva a llamar, salvo que el bundle ya esté
// guardado; un bundle roto es el estado error. Los trozos no se borran aquí,
// porque otra subida puede estar usándolos: caducan a los chunkTTL.
func (s *Service) assemble(w http.ResponseWriter, r *http.Request) error {
	var req assembleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return cliErr(http.StatusBadRequest, "JSON no válido: %v", err)
	}
	req.Checksum = strings.ToLower(req.Checksum)
	if !isSHA1(req.Checksum) {
		return cliErr(http.StatusBadRequest, "checksum no es un sha1")
	}
	if len(req.Chunks) == 0 {
		return cliErr(http.StatusBadRequest, "faltan los trozos (chunks)")
	}
	for i, c := range req.Chunks {
		req.Chunks[i] = strings.ToLower(c)
		if !isSHA1(req.Chunks[i]) {
			return cliErr(http.StatusBadRequest, "el trozo %q no es un sha1", c)
		}
	}
	ids, err := s.projectIDs(r, req.Projects)
	if err != nil {
		return err
	}
	ctx := r.Context()

	// Con los trozos aún guardados, un bundle ya ensamblado se vuelve a
	// guardar: así repone sus mapas y renueva su fecha para la retención.
	missing, err := s.store.MissingUploadChunks(ctx, req.Chunks)
	if err != nil {
		return err
	}
	var b []byte
	detail := ""
	if len(missing) == 0 {
		b, missing, detail, err = s.readChunks(r, req)
		if err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		done, err := s.store.BundleAssembled(ctx, req.Checksum, ids)
		if err != nil {
			return err
		}
		if done {
			assembleState(w, "ok", nil, "")
			return nil
		}
		assembleState(w, "not_found", missing, fmt.Sprintf("faltan %d de %d trozos", len(missing), len(req.Chunks)))
		return nil
	}
	var (
		m     manifest
		files []store.SourceMapFile
	)
	if detail == "" {
		m, files, err = readBundle(b, s.opts.MaxFileBytes, s.opts.MaxBundleBytes)
		if err != nil {
			detail = err.Error()
		}
	}
	if detail != "" {
		s.log.WarnContext(ctx, "artifact bundle rechazado", "checksum", req.Checksum, "detail", detail)
		assembleState(w, "error", nil, detail)
		return nil
	}

	bundle := store.Bundle{Checksum: req.Checksum, ProjectIDs: ids, Release: req.Version, Dist: req.Dist, Files: files}
	if bundle.Release == "" {
		bundle.Release = m.Release
	}
	if bundle.Dist == "" {
		bundle.Dist = m.Dist
	}
	for i := range bundle.Files {
		bundle.Files[i].Dist = bundle.Dist
	}
	err = s.store.PutBundle(ctx, bundle)
	if errors.Is(err, store.ErrNotFound) {
		return cliErr(http.StatusNotFound, "proyecto no encontrado")
	}
	if err != nil {
		return err
	}
	s.cache.clear()
	withID := 0
	for _, f := range files {
		if f.DebugID != "" {
			withID++
		}
	}
	s.log.InfoContext(ctx, "artifact bundle guardado", "projects", req.Projects, "release", bundle.Release,
		"dist", bundle.Dist, "maps", len(files), "debug_ids", withID)
	assembleState(w, "ok", nil, "")
	return nil
}

// readChunks junta los trozos en orden. Devuelve los que faltan si alguno
// caducó entre medias, y un detail no vacío si el bundle no vale, que se
// responde como estado error.
func (s *Service) readChunks(r *http.Request, req assembleRequest) (b []byte, missing []string, detail string, err error) {
	for _, sum := range req.Chunks {
		c, err := s.store.UploadChunk(r.Context(), sum)
		if errors.Is(err, store.ErrNotFound) {
			return nil, []string{sum}, "", nil
		}
		if err != nil {
			return nil, nil, "", err
		}
		if int64(len(b)+len(c)) > s.opts.MaxBundleBytes {
			return nil, nil, fmt.Sprintf("el bundle pasa de %d MiB", s.opts.MaxBundleBytes>>20), nil
		}
		b = append(b, c...)
	}
	if sum := sha1.Sum(b); hex.EncodeToString(sum[:]) != req.Checksum {
		return nil, nil, "los trozos no forman el bundle del checksum " + req.Checksum, nil
	}
	return b, nil, "", nil
}

func (s *Service) projectIDs(r *http.Request, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, cliErr(http.StatusBadRequest, "falta el proyecto")
	}
	var ids []int64
	for _, name := range names {
		p, err := s.store.ProjectByName(r.Context(), name)
		if errors.Is(err, store.ErrNotFound) {
			return nil, cliErr(http.StatusNotFound, "proyecto %q no encontrado", name)
		}
		if errors.Is(err, store.ErrInvalid) {
			return nil, cliErr(http.StatusBadRequest, "%v", err)
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, p.ID) {
			ids = append(ids, p.ID)
		}
	}
	return ids, nil
}

// releaseFiles lista los ficheros ya subidos al release; siempre vacía, así
// que sentry-cli los vuelve a subir todos.
func (s *Service) releaseFiles(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.projectIDs(r, []string{r.PathValue("project")}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, []any{})
	return nil
}

// uploadReleaseFile recibe un fichero de release (campos file, name, dist y
// header). Solo se guardan los .map; el resto se acepta y se descarta.
func (s *Service) uploadReleaseFile(w http.ResponseWriter, r *http.Request) error {
	ids, err := s.projectIDs(r, []string{r.PathValue("project")})
	if err != nil {
		return err
	}
	release := r.PathValue("version")
	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxFileBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return cliErr(http.StatusBadRequest, "se espera multipart/form-data con un campo file")
	}
	var (
		data          []byte
		gotFile       bool
		filename      string
		name, dist    string
		headers       = map[string]string{}
		readFieldText = func(p io.Reader) (string, error) {
			b, err := io.ReadAll(io.LimitReader(p, 64<<10))
			return string(b), err
		}
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return multipartErr(err)
		}
		switch part.FormName() {
		case "file":
			filename = part.FileName()
			data, err = io.ReadAll(io.LimitReader(part, s.opts.MaxFileBytes+1))
			if err != nil {
				return multipartErr(err)
			}
			if int64(len(data)) > s.opts.MaxFileBytes {
				return cliErr(http.StatusRequestEntityTooLarge, "%s pasa de %d MiB", filename, s.opts.MaxFileBytes>>20)
			}
			gotFile = true
		case "name":
			name, err = readFieldText(part)
		case "dist":
			dist, err = readFieldText(part)
		case "header":
			var h string
			h, err = readFieldText(part)
			if k, v, ok := strings.Cut(h, ":"); ok {
				headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		default:
			_, err = io.Copy(io.Discard, part)
		}
		if err != nil {
			return multipartErr(err)
		}
	}
	if !gotFile {
		return cliErr(http.StatusBadRequest, "falta el campo file")
	}
	if name == "" {
		name = "~/" + path.Base(strings.ReplaceAll(filename, `\`, "/"))
	}
	if jsURL, ok := strings.CutSuffix(name, ".map"); ok {
		if _, err := Parse(data); err != nil {
			return cliErr(http.StatusBadRequest, "%s no es un source map v3 válido: %v", name, err)
		}
		f := store.SourceMapFile{File: path.Base(jsURL), Dist: dist, URL: jsURL, DebugID: mapDebugID(data), Map: data}
		for _, id := range ids {
			err := s.store.PutSourceMaps(r.Context(), id, release, []store.SourceMapFile{f})
			if errors.Is(err, store.ErrNotFound) {
				return cliErr(http.StatusNotFound, "proyecto no encontrado")
			}
			if err != nil {
				return err
			}
		}
		s.cache.clear()
		s.log.InfoContext(r.Context(), "source map subido", "project", r.PathValue("project"), "release", release, "file", name)
	}
	sum := sha1.Sum(data)
	var distJSON any
	if dist != "" {
		distJSON = dist
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": hex.EncodeToString(sum[:]), "name": name, "dist": distJSON, "headers": headers,
		"size": len(data), "sha1": hex.EncodeToString(sum[:]), "dateCreated": time.Now().UTC(),
	})
	return nil
}
