// Package mimeutil provides MIME type detection and related utilities.
package mimeutil

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/image/draw"
)

// DetectMime detects the MIME type of data based on its content and extension.
func DetectMime(data []byte, ext string) string {
	// Check extension-specific types first
	switch strings.ToLower(ext) {
	case "css":
		return "text/css"
	case "js":
		return "application/javascript"
	}

	// Use http.DetectContentType for content-based detection
	mime := http.DetectContentType(data)

	// Strip charset suffix (e.g., "text/plain; charset=utf-8" -> "text/plain")
	if idx := strings.Index(mime, ";"); idx != -1 {
		mime = strings.TrimSpace(mime[:idx])
	}

	// Fix up some common misdetections
	if strings.HasPrefix(mime, "text/plain") {
		extLower := strings.ToLower(ext)
		switch extLower {
		case "json":
			return "application/json"
		case "xml", "html", "htm":
			return "text/html"
		case "svg":
			return "image/svg+xml"
		}
	}

	return mime
}

// IsImage returns true if the mime type is a supported image format.
func IsImage(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif":
		return true
	}
	return false
}

// IsAudioVideo returns true if the mime type is a supported audio/video format.
func IsAudioVideo(mime string) bool {
	mediaFormats := []string{
		"video/webm", "audio/webm",
		"audio/ogg", "video/ogg", "application/ogg",
		"audio/mpeg",
		"audio/aac", "audio/mp4", "video/mp4",
		"audio/wave", "audio/wav", "audio/x-wav", "audio/x-pn-wav",
	}
	return slices.Contains(mediaFormats, mime)
}

// GetLanguageFromExtension maps file extensions to syntax highlighter language names.
func GetLanguageFromExtension(ext string) string {
	switch strings.ToLower(ext) {
	case "sh":
		return "bash"
	case "c", "h":
		return "c"
	case "c++", "cpp", "hpp", "hxx", "cxx", "cc":
		return "cpp"
	case "cs":
		return "csharp"
	case "coffee":
		return "coffeescript"
	case "css":
		return "css"
	case "go":
		return "go"
	case "hs":
		return "haskell"
	case "ini":
		return "ini"
	case "java":
		return "java"
	case "js":
		return "javascript"
	case "tex":
		return "latex"
	case "xml", "html":
		return "markup"
	case "m", "mm":
		return "objectivec"
	case "php":
		return "php"
	case "py":
		return "python"
	case "rb":
		return "ruby"
	case "twig":
		return "twig"
	case "scss":
		return "scss"
	case "sql":
		return "sql"
	case "swift":
		return "swift"
	case "txt":
		return "none"
	default:
		return "none"
	}
}

// CodeExtensions is the list of extensions that trigger the syntax highlighter.
var CodeExtensions = []string{
	"txt", "sh", "c", "h", "c++", "cpp", "hpp", "hxx", "cxx", "cc", "cs",
	"coffee", "css", "go", "hs", "ini", "java", "js", "tex", "xml",
	"html", "m", "mm", "php", "py", "rb", "twig", "scss", "sql", "swift",
}

// IsCodeExtension returns true if the extension should trigger syntax highlighting.
func IsCodeExtension(ext string) bool {
	for _, e := range CodeExtensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// ThumbnailSize is the max dimension for generated thumbnails.
const ThumbnailSize = 150

// GenerateThumbnail creates a thumbnail from image data.
func GenerateThumbnail(data []byte, mime string) *ThumbnailResult {
	if !IsImage(mime) {
		return nil
	}

	var img image.Image
	var err error
	switch mime {
	case "image/jpeg":
		img, err = jpeg.Decode(bytes.NewReader(data))
	case "image/png":
		img, err = png.Decode(bytes.NewReader(data))
	case "image/gif":
		img, err = gif.Decode(bytes.NewReader(data))
	default:
		return nil
	}
	if err != nil || img == nil {
		return nil
	}

	bounds := img.Bounds()
	oldW := bounds.Dx()
	oldH := bounds.Dy()

	// Calculate new dimensions
	newDim := min(ThumbnailSize, max(oldW, oldH))
	ratio := float64(oldW) / float64(oldH)
	var newW, newH int
	if ratio < 1 {
		newW = int(float64(newDim) * ratio)
		newH = newDim
	} else {
		newW = newDim
		newH = int(float64(newDim) / ratio)
	}

	thumbnail := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.BiLinear.Scale(thumbnail, thumbnail.Bounds(), img, img.Bounds(), draw.Over, nil)

	// Encode to original format
	var buf bytes.Buffer
	switch mime {
	case "image/jpeg":
		err = jpeg.Encode(&buf, thumbnail, &jpeg.Options{Quality: 80})
	case "image/png":
		err = png.Encode(&buf, thumbnail)
	case "image/gif":
		err = gif.Encode(&buf, thumbnail, nil)
	}
	if err != nil {
		return nil
	}

	return &ThumbnailResult{
		Data:   buf.Bytes(),
		Width:  newW,
		Height: newH,
		Mime:   mime,
	}
}

// ThumbnailResult holds a generated thumbnail.
type ThumbnailResult struct {
	Data   []byte
	Width  int
	Height int
	Mime   string
}
