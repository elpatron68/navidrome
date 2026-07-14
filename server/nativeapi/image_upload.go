package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	_ "golang.org/x/image/webp"
)

func maxImageUploadSize() int64 {
	if size, err := humanize.ParseBytes(conf.Server.MaxImageUploadSize); err == nil && size > 0 {
		return int64(size)
	}
	size, _ := humanize.ParseBytes(consts.DefaultMaxImageUploadSize)
	return int64(size)
}

func checkImageUploadPermission(w http.ResponseWriter, r *http.Request) bool {
	user, _ := request.UserFrom(r.Context())
	if !conf.Server.EnableArtworkUpload && !user.IsAdmin {
		http.Error(w, "artwork upload is disabled", http.StatusForbidden)
		return false
	}
	return true
}

func handleImageUpload(saveFn func(ctx context.Context, reader io.Reader, ext string) error) http.HandlerFunc {
	maxImageSize := maxImageUploadSize()
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !checkImageUploadPermission(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxImageSize)
		if err := r.ParseMultipartForm(min(maxImageSize, 10<<20)); err != nil {
			log.Error(ctx, "Error parsing multipart form", err)
			http.Error(w, "file too large or invalid form", http.StatusBadRequest)
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				if err := r.MultipartForm.RemoveAll(); err != nil {
					log.Warn(ctx, "Error removing multipart temp files", err)
				}
			}
		}()
		file, header, err := r.FormFile("image")
		if err != nil {
			log.Error(ctx, "Error reading uploaded file", err)
			http.Error(w, "missing image file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		_, format, err := image.DecodeConfig(file)
		if err != nil {
			log.Error(ctx, "Uploaded file is not a valid image", err)
			http.Error(w, "invalid image file", http.StatusBadRequest)
			return
		}
		if seeker, ok := file.(io.Seeker); ok {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				log.Error(ctx, "Error seeking file", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		ext := "." + format
		if ext == "." {
			ext = strings.ToLower(filepath.Ext(header.Filename))
		}
		if ext == "" || ext == "." {
			log.Error(ctx, "Could not determine image type", "filename", header.Filename)
			http.Error(w, "could not determine image type", http.StatusBadRequest)
			return
		}
		if err := saveFn(ctx, file, ext); err != nil {
			if errors.Is(err, model.ErrNotAuthorized) {
				http.Error(w, "not authorized", http.StatusForbidden)
				return
			}
			if errors.Is(err, model.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			log.Error(ctx, "Error saving image", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"ok"}`)
	}
}

func handleImageDelete(deleteFn func(ctx context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !checkImageUploadPermission(w, r) {
			return
		}
		if err := deleteFn(ctx); err != nil {
			if errors.Is(err, model.ErrNotAuthorized) {
				http.Error(w, "not authorized", http.StatusForbidden)
				return
			}
			if errors.Is(err, model.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			log.Error(ctx, "Error removing image", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"ok"}`)
	}
}

var imageDownloadClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return validateImageDownloadURL(req.URL)
	},
}

func validateImageDownloadURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid image URL scheme")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("invalid image URL host")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("could not resolve image URL host")
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("image URL host is not allowed")
		}
	}
	return nil
}

func downloadImageFromURL(ctx context.Context, rawURL string) (io.Reader, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, "", fmt.Errorf("invalid image URL")
	}
	if err := validateImageDownloadURL(u); err != nil {
		return nil, "", err
	}

	maxSize := maxImageUploadSize()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", consts.HTTPUserAgent)

	resp, err := imageDownloadClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("downloading image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("downloading image: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading image: %w", err)
	}
	if int64(len(body)) > maxSize {
		return nil, "", fmt.Errorf("image too large")
	}
	if len(body) == 0 {
		return nil, "", fmt.Errorf("empty image response")
	}

	_, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("invalid image file")
	}
	ext := "." + format
	if ext == "." {
		return nil, "", fmt.Errorf("could not determine image type")
	}
	return bytes.NewReader(body), ext, nil
}

func handleImageUploadFromURL(saveFn func(ctx context.Context, reader io.Reader, ext string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !checkImageUploadPermission(w, r) {
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		imageURL := strings.TrimSpace(body.URL)
		if imageURL == "" {
			http.Error(w, "url required", http.StatusBadRequest)
			return
		}
		reader, ext, err := downloadImageFromURL(ctx, imageURL)
		if err != nil {
			log.Warn(ctx, "Could not download image from URL", "url", imageURL, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := saveFn(ctx, reader, ext); err != nil {
			if errors.Is(err, model.ErrNotAuthorized) {
				http.Error(w, "not authorized", http.StatusForbidden)
				return
			}
			if errors.Is(err, model.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			log.Error(ctx, "Error saving image from URL", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"ok"}`)
	}
}
