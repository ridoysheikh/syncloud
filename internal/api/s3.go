package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"

	"syncloud/internal/s3"
	"syncloud/internal/store"
)

// S3 endpoints, bindings and the bucket browser (§16).

func (s *Server) requireS3(w http.ResponseWriter) bool {
	if s.s3 == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "S3 storage is not available")
		return false
	}
	return true
}

func (s *Server) s3Error(w http.ResponseWriter, what string, err error) {
	var inv s3.ErrInvalid
	var me minio.ErrorResponse
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, CodeBadRequest, inv.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such S3 endpoint")
	case errors.Is(err, store.ErrNameTaken):
		writeError(w, http.StatusConflict, CodeConflict, "an S3 endpoint with that name exists")
	case errors.Is(err, store.ErrInUse):
		writeError(w, http.StatusConflict, CodeConflict, "services are bound to this endpoint; remove their bindings first")
	case errors.As(err, &me) && me.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, CodeNotFound, me.Message)
	case errors.As(err, &me) && me.StatusCode > 0:
		// The S3 provider refused (access denied, bucket exists, …): say what it said.
		writeError(w, http.StatusBadGateway, "s3_error", fmt.Sprintf("%s: %s", me.Code, me.Message))
	default:
		s.internalError(w, what, err)
	}
}

func (s *Server) handleListS3Endpoints(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	es, err := s.s3.List(r.Context())
	if err != nil {
		s.s3Error(w, "list S3 endpoints", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": es})
}

func (s *Server) handleGetS3Endpoint(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	e, err := s.s3.Get(r.Context(), r.PathValue("endpoint"))
	if err != nil {
		s.s3Error(w, "get S3 endpoint", err)
		return
	}
	bs, err := s.store.EndpointS3Bindings(r.Context(), e.ID)
	if err != nil {
		s.s3Error(w, "bindings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoint": e, "bindings": bs})
}

func (s *Server) putS3Endpoint(w http.ResponseWriter, r *http.Request, ref string) {
	if !s.requireS3(w) {
		return
	}
	var in s3.EndpointInput
	if !decodeJSON(w, r, &in) {
		return
	}
	e, err := s.s3.Put(r.Context(), ref, in)
	if err != nil {
		s.s3Error(w, "save S3 endpoint", err)
		return
	}
	u, _ := currentUser(r.Context())
	status := http.StatusOK
	if ref == "" {
		status = http.StatusCreated
	}
	s.audit(r, u.ID, "s3:PutEndpoint", "srn:syncloud:s3/"+e.Name, map[string]any{"url": e.URL})
	writeJSON(w, status, e)
}

func (s *Server) handleCreateS3Endpoint(w http.ResponseWriter, r *http.Request) { s.putS3Endpoint(w, r, "") }
func (s *Server) handleUpdateS3Endpoint(w http.ResponseWriter, r *http.Request) {
	s.putS3Endpoint(w, r, r.PathValue("endpoint"))
}

func (s *Server) handleDeleteS3Endpoint(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	if err := s.s3.Delete(r.Context(), r.PathValue("endpoint")); err != nil {
		s.s3Error(w, "delete S3 endpoint", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "s3:DeleteEndpoint", "srn:syncloud:s3/"+r.PathValue("endpoint"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListS3Buckets(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	bs, err := s.s3.Buckets(r.Context(), r.PathValue("endpoint"))
	if err != nil {
		s.s3Error(w, "list buckets", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": bs})
}

func (s *Server) handleCreateS3Bucket(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.s3.CreateBucket(r.Context(), r.PathValue("endpoint"), req.Name); err != nil {
		s.s3Error(w, "create bucket", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "s3:CreateBucket", "srn:syncloud:s3/"+r.PathValue("endpoint")+"/bucket/"+req.Name, nil)
	writeJSON(w, http.StatusCreated, map[string]string{"name": req.Name})
}

func (s *Server) handleListS3Objects(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	q := r.URL.Query()
	l, err := s.s3.Objects(r.Context(), r.PathValue("endpoint"), r.PathValue("bucket"), q.Get("prefix"), q.Get("startAfter"))
	if err != nil {
		s.s3Error(w, "list objects", err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleGetS3Usage(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	u, err := s.s3.Usage(r.Context(), r.PathValue("endpoint"), r.PathValue("bucket"), r.URL.Query().Get("prefix"))
	if err != nil {
		s.s3Error(w, "bucket usage", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func objectKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "key is required")
		return "", false
	}
	return key, true
}

func (s *Server) handleDownloadS3Object(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	key, ok := objectKey(w, r)
	if !ok {
		return
	}
	body, info, err := s.s3.Open(r.Context(), r.PathValue("endpoint"), r.PathValue("bucket"), key)
	if err != nil {
		s.s3Error(w, "download object", err)
		return
	}
	defer body.Close()
	ct := info.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size))
	// Always a download: objects are user content and must not render on the dashboard's origin.
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(key)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, body)
}

// maxUpload bounds dashboard uploads; larger objects belong in an S3 client.
const maxUpload = 5 << 30

func (s *Server) handleUploadS3Object(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	key, ok := objectKey(w, r)
	if !ok {
		return
	}
	if r.ContentLength < 0 || r.ContentLength > maxUpload {
		writeError(w, http.StatusRequestEntityTooLarge, CodeBadRequest, "uploads need a Content-Length of at most 5 GiB")
		return
	}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		ct = "application/octet-stream" // the dashboard's API client default, not the file's type
	}
	if err := s.s3.Upload(r.Context(), r.PathValue("endpoint"), r.PathValue("bucket"), key, http.MaxBytesReader(w, r.Body, maxUpload), r.ContentLength, ct); err != nil {
		s.s3Error(w, "upload object", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "s3:UploadObject", "srn:syncloud:s3/"+r.PathValue("endpoint")+"/bucket/"+r.PathValue("bucket"), map[string]any{"key": key, "bytes": r.ContentLength})
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "size": r.ContentLength})
}

func (s *Server) handleDeleteS3Object(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) {
		return
	}
	key, ok := objectKey(w, r)
	if !ok {
		return
	}
	n, err := s.s3.DeleteObject(r.Context(), r.PathValue("endpoint"), r.PathValue("bucket"), key)
	if err != nil {
		s.s3Error(w, "delete object", err)
		return
	}
	u, _ := currentUser(r.Context())
	s.audit(r, u.ID, "s3:DeleteObject", "srn:syncloud:s3/"+r.PathValue("endpoint")+"/bucket/"+r.PathValue("bucket"), map[string]any{"key": key, "objects": n})
	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// ── service bindings ─────────────────────────────────────────────────────────

func (s *Server) handleGetServiceS3(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) || !s.requireWorkloads(w) {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	bs, err := s.store.ServiceS3Bindings(r.Context(), sv.ID)
	if err != nil {
		s.internalError(w, "S3 bindings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": bs})
}

// handleSetServiceS3 replaces a service's bindings and rolls them out as a
// new revision.
func (s *Server) handleSetServiceS3(w http.ResponseWriter, r *http.Request) {
	if !s.requireS3(w) || !s.requireWorkloads(w) {
		return
	}
	e, ok := s.environment(w, r)
	if !ok {
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		Bindings []s3.BindingInput `json:"bindings"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	bs, err := s.s3.Resolve(r.Context(), sv.ID, req.Bindings)
	if err != nil {
		s.s3Error(w, "S3 bindings", err)
		return
	}
	if err := s.store.SetServiceS3Bindings(r.Context(), sv.ID, bs); err != nil {
		s.s3Error(w, "S3 bindings", err)
		return
	}
	spec, err := s.workloads.SpecFor(r.Context(), sv.ID, sv.Revision)
	if err != nil {
		s.internalError(w, "service spec", err)
		return
	}
	u, _ := currentUser(r.Context())
	v, _, err := s.workloads.Apply(r.Context(), e, sv.Name, spec, -1, u.ID)
	if err != nil {
		s.workloadError(w, "redeploy", err)
		return
	}
	s.audit(r, u.ID, "s3:SetServiceBindings", srnService(v), map[string]any{"bindings": len(bs), "revision": v.Revision})
	out, err := s.store.ServiceS3Bindings(r.Context(), sv.ID)
	if err != nil {
		s.internalError(w, "S3 bindings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "revision": v.Revision})
}
