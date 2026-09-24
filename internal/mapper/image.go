package mapper

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// EmbeddedImage is a data: URI image the Slides API cannot fetch directly.
// Its CreateImage request carries Placeholder as URL; the caller must host
// the bytes somewhere public and substitute the real URL (see
// convert.Result.Images) or drop the request.
type EmbeddedImage struct {
	Placeholder string
	MIME        string
	Data        []byte
}

// ImagePlaceholderPrefix prefixes the placeholder URL of embedded images.
const ImagePlaceholderPrefix = "svg2gslide-embedded://"

// Images returns the embedded images referenced by the requests.
func (m *Mapper) Images() []EmbeddedImage { return m.images }

// mapImage maps <image> onto a native image: http(s) URLs are used as is,
// data: URIs are registered as embedded images.
func (m *Mapper) mapImage(e *svgpkg.Element, mat svgpkg.Matrix) {
	href := strings.TrimSpace(e.Attr("href"))
	w, h := e.FloatAttr("width", 0), e.FloatAttr("height", 0)
	if href == "" || w <= 0 || h <= 0 {
		return
	}
	var src string
	switch {
	case strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://"):
		src = href
	case strings.HasPrefix(href, "data:"):
		mime, data, err := decodeDataURI(href)
		if err != nil {
			m.warnf("image data: illisible (%v)", err)
			return
		}
		if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" {
			m.warnf("image %s ignorée (formats acceptés par Slides : PNG, JPEG, GIF)", mime)
			return
		}
		// Simple monochrome icons are redrawn natively (no hosting).
		if m.mapSilhouette(data, e.FloatAttr("x", 0), e.FloatAttr("y", 0), w, h, mat) {
			m.warnf("image %dx%d redessinée en formes natives", int(w), int(h))
			return
		}
		src = fmt.Sprintf("%s%d", ImagePlaceholderPrefix, len(m.images))
		m.images = append(m.images, EmbeddedImage{Placeholder: src, MIME: mime, Data: data})
	default:
		m.warnf("image %q ignorée (URL relative)", href)
		return
	}
	x, y := mat.Apply(e.FloatAttr("x", 0), e.FloatAttr("y", 0))
	sx, sy := mat.ScaleFactors()
	ex, ey := m.toEMU(x, y)
	m.reqs = append(m.reqs, &slides.Request{CreateImage: &slides.CreateImageRequest{
		ObjectId: m.nextID(),
		Url:      src,
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(m.lenEMU(w*sx), m.lenEMU(h*sy)),
			Transform: &slides.AffineTransform{
				ScaleX: 1, ScaleY: 1, TranslateX: ex, TranslateY: ey, Unit: "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "TranslateX", "TranslateY"},
			},
		},
	}})
}

// decodeDataURI decodes "data:<mime>[;base64],<payload>".
func decodeDataURI(uri string) (string, []byte, error) {
	meta, payload, ok := strings.Cut(strings.TrimPrefix(uri, "data:"), ",")
	if !ok {
		return "", nil, fmt.Errorf("no payload")
	}
	parts := strings.Split(meta, ";")
	mime := strings.ToLower(strings.TrimSpace(parts[0]))
	isB64 := false
	for _, p := range parts[1:] {
		if strings.TrimSpace(p) == "base64" {
			isB64 = true
		}
	}
	if !isB64 {
		s, err := url.PathUnescape(payload)
		return mime, []byte(s), err
	}
	payload = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, payload)
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "="))
	}
	return mime, data, err
}
