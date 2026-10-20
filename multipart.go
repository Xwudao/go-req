package req

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
)

// MultipartFile describes one file part. Reader belongs to the caller and is
// never closed or buffered by req. ContentType defaults to application/octet-stream.
// Multiple files may use the same FieldName.
type MultipartFile struct {
	FieldName   string
	FileName    string
	ContentType string
	Reader      io.Reader
}

// Multipart sets a streaming multipart/form-data body, copying fields and file
// metadata immediately. Readers are consumed once, just like Body readers;
// repeated builds do not rewind them. Explicit Content-Type overrides the
// generated type, so it must include the matching boundary to be usable.
// Build starts no goroutines and does not read files. The caller must close
// files, including on cancellation. A blocked user Reader must be unblocked by
// its owner; context cancellation is checked between reads.
func (r *Request) Multipart(fields url.Values, files ...MultipartFile) *Request {
	if r.err != nil {
		return r
	}
	var framing bytes.Buffer
	w := multipart.NewWriter(&framing)
	var parts []io.Reader
	flush := func() {
		if framing.Len() != 0 {
			parts = append(parts, bytes.NewReader(bytes.Clone(framing.Bytes())))
			framing.Reset()
		}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.ContainsAny(key, "\r\n") {
			r.setErr(errors.New("req: multipart field name must not contain CR/LF"))
			return r
		}
		for _, value := range fields[key] {
			_, err := w.CreateFormField(key)
			if err != nil {
				r.setErr(err)
				return r
			}
			flush()
			// Keep even large field values out of the framing buffer.
			parts = append(parts, strings.NewReader(value))
		}
	}
	for _, file := range files {
		if file.Reader == nil || file.FieldName == "" {
			r.setErr(errors.New("req: multipart file requires a field name and reader"))
			return r
		}
		if strings.ContainsAny(file.FieldName+file.FileName+file.ContentType, "\r\n") {
			r.setErr(errors.New("req: multipart metadata must not contain CR/LF"))
			return r
		}
		contentType := file.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if _, _, err := mime.ParseMediaType(contentType); err != nil {
			r.setErr(err)
			return r
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": file.FieldName, "filename": file.FileName}))
		header.Set("Content-Type", contentType)
		if _, err := w.CreatePart(header); err != nil {
			r.setErr(err)
			return r
		}
		flush()
		parts = append(parts, file.Reader)
	}
	if err := w.Close(); err != nil {
		r.setErr(err)
		return r
	}
	flush()
	r.bodyFactory = nil
	r.body = &contextReader{ctx: r.ctx, reader: io.MultiReader(parts...)}
	r.setDefaultContentType(w.FormDataContentType())
	return r
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	return r.reader.Read(p)
}
