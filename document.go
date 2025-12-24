package jsonparse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/dustin/go-humanize"
)

const defaultMaxSize int64 = 10 << 20

var errInvalidJSON = errors.New("invalid JSON body")

type documentKey struct{}

// document is shared by all JSON handlers on a request. A placeholder provider
// reads value directly, so committing a transform also updates its placeholders.
type document struct {
	value        any
	body         *bufferedBody
	size         int64
	err          error
	placeholders bool
}

type bufferedBody struct{ *bytes.Reader }

func (*bufferedBody) Close() error { return nil }

func newBufferedBody(body []byte) *bufferedBody {
	return &bufferedBody{bytes.NewReader(body)}
}

func loadDocument(r *http.Request, maxSize int64) (*document, error) {
	doc, _ := r.Context().Value(documentKey{}).(*document)
	if doc == nil {
		doc = new(document)
		*r = *r.WithContext(context.WithValue(r.Context(), documentKey{}, doc))
	}

	if doc.body != nil && r.Body == doc.body {
		if doc.size > maxSize {
			return doc, bodyTooLarge(maxSize)
		}
		return doc, doc.err
	}

	// Another handler may have replaced the body since the last JSON handler.
	// Keep the document pointer (and its placeholder provider), but decode anew.
	if r.ContentLength > maxSize {
		return doc, bodyTooLarge(maxSize)
	}
	var body []byte
	if r.Body != nil {
		defer r.Body.Close()
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, maxSize+1))
		if err != nil {
			var sizeErr *http.MaxBytesError
			if errors.As(err, &sizeErr) {
				return doc, caddyhttp.Error(http.StatusRequestEntityTooLarge, err)
			}
			return doc, caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("reading JSON body: %w", err))
		}
	}
	if int64(len(body)) > maxSize {
		return doc, bodyTooLarge(maxSize)
	}

	doc.body = newBufferedBody(body)
	doc.size = int64(len(body))
	r.Body = doc.body
	doc.value, doc.err = decodeJSON(body)
	if doc.err != nil {
		doc.err = fmt.Errorf("%w: %v", errInvalidJSON, doc.err)
	}
	return doc, doc.err
}

func decodeJSON(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	// Decode must consume exactly one JSON value, including valid trailing space.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

// commit publishes a complete, successfully encoded transform.
func (d *document) commit(r *http.Request, value any, body []byte) {
	d.value, d.err = value, nil
	d.body = newBufferedBody(body)
	d.size = int64(len(body))
	r.Body = d.body
	r.ContentLength = d.size
	r.GetBody = func() (io.ReadCloser, error) { return newBufferedBody(body), nil }
	r.TransferEncoding = nil
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	r.Header.Set("Content-Length", strconv.FormatInt(d.size, 10))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Del("Transfer-Encoding")
}

func bodyTooLarge(maxSize int64) error {
	return caddyhttp.Error(http.StatusRequestEntityTooLarge,
		fmt.Errorf("JSON body exceeds max_size of %d bytes", maxSize))
}

func provisionMaxSize(size *int64) error {
	if *size == 0 {
		*size = defaultMaxSize
	}
	if *size < 0 || *size == math.MaxInt64 {
		return errors.New("max_size must be positive and less than MaxInt64")
	}
	return nil
}

func parseMaxSize(s string) (int64, error) {
	size, err := humanize.ParseBytes(s)
	if err != nil {
		return 0, err
	}
	if size == 0 || size >= math.MaxInt64 {
		return 0, errors.New("max_size must be positive and less than MaxInt64")
	}
	return int64(size), nil
}
