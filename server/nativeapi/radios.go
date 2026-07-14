package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/deluan/rest"
	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server"
	"github.com/navidrome/navidrome/server/radiobrowser"
)

func (api *Router) addRadioRoute(r chi.Router) {
	constructor := func(ctx context.Context) rest.Repository {
		return api.ds.Resource(ctx, model.Radio{})
	}
	r.Route("/radio", func(r chi.Router) {
		r.Get("/", rest.GetAll(constructor))
		r.Post("/", api.createRadio(constructor))
		r.Get("/browser/search", api.searchRadioBrowser())
		r.Post("/browser/click", api.radioBrowserClick())
		r.Route("/{id}", func(r chi.Router) {
			r.Use(server.URLParamsMiddleware)
			r.Get("/", rest.Get(constructor))
			r.Put("/", rest.Put(constructor))
			r.Delete("/", rest.Delete(constructor))
			r.Post("/image", api.uploadRadioImage())
			r.Post("/image/url", api.uploadRadioImageFromURL())
			r.Delete("/image", api.deleteRadioImage())
		})
	})
}

func (api *Router) saveRadioImageFromURL(ctx context.Context, radio *model.Radio, imageURL string) error {
	reader, ext, err := downloadImageFromURL(ctx, imageURL)
	if err != nil {
		return err
	}
	return api.saveRadioImageFromReader(ctx, radio, reader, ext)
}

func (api *Router) saveRadioImageFromReader(ctx context.Context, radio *model.Radio, reader io.Reader, ext string) error {
	oldPath := radio.UploadedImagePath()
	filename, err := api.imgUpload.SetImage(ctx, consts.EntityRadio, radio.ID, radio.Name, oldPath, reader, ext)
	if err != nil {
		return err
	}
	radio.UploadedImage = filename
	return api.ds.Radio(ctx).Put(radio, "UploadedImage")
}

type radioCreateRequest struct {
	Name        string `json:"name"`
	StreamUrl   string `json:"streamUrl"`
	HomePageUrl string `json:"homePageUrl"`
	FaviconURL  string `json:"faviconUrl"`
}

func (api *Router) createRadio(constructor func(ctx context.Context) rest.Repository) http.HandlerFunc {
	standardPost := rest.Post(constructor)
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		var req radioCreateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		radioBody, err := json.Marshal(&model.Radio{
			Name:        req.Name,
			StreamUrl:   req.StreamUrl,
			HomePageUrl: req.HomePageUrl,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		postReq := r.Clone(ctx)
		postReq.Body = io.NopCloser(bytes.NewReader(radioBody))
		postReq.ContentLength = int64(len(radioBody))
		rec := httptest.NewRecorder()
		standardPost(rec, postReq)
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.Copy(w, rec.Body)
		if rec.Code != http.StatusOK {
			return
		}

		var resp struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.ID == "" {
			return
		}

		faviconURL := strings.TrimSpace(req.FaviconURL)
		if faviconURL == "" {
			return
		}

		radio, err := api.ds.Radio(ctx).Get(resp.ID)
		if err != nil {
			log.Warn(ctx, "Could not load radio for favicon import", "radio", resp.ID, err)
			return
		}
		if err := api.saveRadioImageFromURL(ctx, radio, faviconURL); err != nil {
			log.Warn(ctx, "Could not import radio favicon on create", "radio", resp.ID, "url", faviconURL, err)
		}
	}
}

func (api *Router) uploadRadioImage() http.HandlerFunc {
	return handleImageUpload(func(ctx context.Context, reader io.Reader, ext string) error {
		radioID := chi.URLParamFromCtx(ctx, "id")
		radio, err := api.ds.Radio(ctx).Get(radioID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return model.ErrNotFound
			}
			return err
		}
		return api.saveRadioImageFromReader(ctx, radio, reader, ext)
	})
}

func (api *Router) uploadRadioImageFromURL() http.HandlerFunc {
	return handleImageUploadFromURL(func(ctx context.Context, reader io.Reader, ext string) error {
		radioID := chi.URLParamFromCtx(ctx, "id")
		radio, err := api.ds.Radio(ctx).Get(radioID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return model.ErrNotFound
			}
			return err
		}
		return api.saveRadioImageFromReader(ctx, radio, reader, ext)
	})
}

func (api *Router) deleteRadioImage() http.HandlerFunc {
	return handleImageDelete(func(ctx context.Context) error {
		radioID := chi.URLParamFromCtx(ctx, "id")
		radio, err := api.ds.Radio(ctx).Get(radioID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return model.ErrNotFound
			}
			return err
		}
		if err := api.imgUpload.RemoveImage(ctx, radio.UploadedImagePath()); err != nil {
			return err
		}
		radio.UploadedImage = ""
		return api.ds.Radio(ctx).Put(radio, "UploadedImage")
	})
}

func (api *Router) searchRadioBrowser() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		limit := 0
		if ls := strings.TrimSpace(r.URL.Query().Get("limit")); ls != "" {
			if n, err := strconv.Atoi(ls); err == nil {
				limit = n
			}
		}
		stations, err := radiobrowser.Search(r.Context(), q, limit)
		if err != nil {
			if errors.Is(err, radiobrowser.ErrQueryTooShort) || errors.Is(err, radiobrowser.ErrQueryTooLong) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"stations": stations})
	}
}

func (api *Router) radioBrowserClick() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			StreamURL string `json:"streamUrl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		streamURL := strings.TrimSpace(body.StreamURL)
		if streamURL == "" {
			http.Error(w, "streamUrl required", http.StatusBadRequest)
			return
		}
		go func(u string) {
			defer func() { _ = recover() }()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			radiobrowser.NotifyClick(ctx, u)
		}(streamURL)
		w.WriteHeader(http.StatusNoContent)
	}
}
