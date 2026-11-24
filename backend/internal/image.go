package internal

import (
	"bytes"
	"fmt"
	"image"
	// Registered decode formats for DecodeConfig: the upload whitelist is
	// JPG/JPEG/PNG only (09-PROFILE-PHOTOS, 2026-11-10, supersedes the
	// 2026-10-11 "validate client-side only" decision). x/image/webp is
	// deliberately NOT registered — WebP bytes stay undecodable → rejected,
	// while previously stored WebP files keep serving.
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"strings"

	"golang.org/x/image/draw"
)

const (
	// compressTriggerBytes (10 MB): stored images ABOVE this size go through
	// the format-preserving compression ladder; files at/below it are stored
	// byte-for-byte as received (spec 09 decision 3).
	compressTriggerBytes int64 = 10 << 20
	// maxStoredImageBytes (50 MB): the client gate's server-side bound — an
	// upload FILE larger than this is a slip-through and is rejected
	// (errRejected → failStore → 200 VALIDATION).
	maxStoredImageBytes int64 = 50 << 20
)

// declaredFormat maps an accepted multipart Content-Type to the format name
// it claims: image/png → "png", image/jpeg|image/jpg → "jpeg". Anything else
// (WebP retired from new uploads, ICO, GIF, HTML, absent) is not accepted —
// cross-checking the real decoded format against a missing/foreign claim is
// impossible.
func declaredFormat(declaredType string) (string, bool) {
	if i := strings.IndexByte(declaredType, ';'); i >= 0 {
		declaredType = declaredType[:i]
	}
	switch strings.ToLower(strings.TrimSpace(declaredType)) {
	case "image/png":
		return "png", true
	case "image/jpeg", "image/jpg":
		return "jpeg", true
	}
	return "", false
}

// rejectf wraps an upload-validation failure as errRejected → failStore →
// 200 VALIDATION (per the no-HTTP-errors rule the multipart caller already
// uses for disk-full).
func rejectf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errRejected}, args...)...)
}

// validateImageBytes enforces the platform-wide upload whitelist by decoding
// the REAL image header and cross-checking it against the declared multipart
// Content-Type (never trusting the declaration alone). Returns the verified
// format name ("png"/"jpeg").
func validateImageBytes(data []byte, declaredType string) (string, error) {
	declared, ok := declaredFormat(declaredType)
	if !ok {
		return "", rejectf("declared type %q is not an accepted upload format (jpg/jpeg/png only)", declaredType)
	}
	_, actual, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", rejectf("not a decodable image (only JPG/JPEG/PNG accepted): %v", err)
	}
	if actual != declared {
		return "", rejectf("declared type %q does not match the actual image format %q", declaredType, actual)
	}
	return actual, nil
}

// readUploadPayload fully reads one upload file, bounded at the 50 MB gate:
// a strict server-side byte cap for the "≤50 MB goes to the server" rule.
// A body bigger than the gate is a slip-through and is rejected. Raw read
// errors propagate (ENOSPC is remapped disk-full by the caller).
func readUploadPayload(src io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(src, maxStoredImageBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxStoredImageBytes {
		return nil, rejectf("image exceeds the 50 MB upload limit (%d bytes)", len(data))
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// Compression (spec 09 decision 3): format-preserving ladder for stored
// images > compressTriggerBytes — NEVER a rejection; the guard (below) logs
// and returns the best effort when the target is unreachable.
// ---------------------------------------------------------------------------

const (
	// compressJPEGQualityStart / step: the JPEG ladder ~90→80→70…; a ladder
	// restart at the start quality after each resolution step is the backstop.
	compressJPEGQualityStart = 90
	compressJPEGQualityStep  = 10
	compressJPEGQualityFloor = 10
	// compressMaxPasses bounds every ladder (each pass is one re-encode):
	// the resolution lever is expected to reach the target long before it
	// trips; if it ever does, the BEST output seen so far is stored.
	compressMaxPasses = 16
	// compressSemaphoreCap (spec 09 decision 4): at most 5 concurrent
	// compressions; handlers block while waiting (FIFO beyond 5), HTTP
	// timeouts are the backstop.
	compressSemaphoreCap = 5
)

// compressHalve downscales src by half (min 1×1) with CatmullRom — the pure-Go
// x/image/draw resampler (spec 09 decision 5, CGO_ENABLED=0 safe).
func compressHalve(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// compressJPEG runs the quality ladder until ≤ maxBytes (or the guard trips →
// best effort). Resolution halving + a ladder restart is the backstop. Pixels
// already halved keep shrinking; a 1×1 JPEG re-encode is a handful of bytes,
// so the target is reachable long before the guard.
func compressJPEG(src image.Image, maxBytes int64) []byte {
	if maxBytes < 1 {
		maxBytes = 1
	}
	var best []byte
	quality := compressJPEGQualityStart
	for pass := 0; pass < compressMaxPasses; pass++ {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality}); err != nil {
			log.Printf("image compression: jpeg re-encode failed, keeping best effort: %v", err)
			return best
		}
		out := buf.Bytes()
		if best == nil || len(out) < len(best) {
			best = out
		}
		if int64(len(out)) <= maxBytes {
			return out
		}
		quality -= compressJPEGQualityStep
		if quality < compressJPEGQualityFloor {
			// Backstop: halve the resolution, restart the quality ladder.
			src = compressHalve(src)
			quality = compressJPEGQualityStart
		}
	}
	return best
}

// compressPNG downscales (CatmullRom) + re-encodes at BestCompression until
// ≤ maxBytes (or the guard trips → best effort).
func compressPNG(src image.Image, maxBytes int64) []byte {
	if maxBytes < 1 {
		maxBytes = 1
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	var best []byte
	for pass := 0; pass < compressMaxPasses; pass++ {
		var buf bytes.Buffer
		if err := enc.Encode(&buf, src); err != nil {
			log.Printf("image compression: png re-encode failed, keeping best effort: %v", err)
			return best
		}
		out := buf.Bytes()
		if best == nil || len(out) < len(best) {
			best = out
		}
		if int64(len(out)) <= maxBytes {
			return out
		}
		src = compressHalve(src)
	}
	return best
}

// withCompressSlot runs fn while holding one of the 5 compression slots
// (spec 09 decision 4): a buffered-channel semaphore, NOT a mutex. Handlers
// block here while waiting; the slot is released on EVERY outcome (defer),
// and fn's error (or the panic case via defer ordering) propagates.
func (s *Server) withCompressSlot(fn func() error) error {
	s.compressGate <- struct{}{}
	defer func() { <-s.compressGate }()
	return fn()
}

// compressImage re-encodes data (already whitelist-validated) of the given
// format IN THAT FORMAT to ≤ maxBytes. A full-image decode failure after a
// passing DecodeConfig (pathological truncated body) is logged and the
// ORIGINAL bytes are returned unchanged — the honest file still stores, never
// a rejection.
func compressImage(data []byte, format string, maxBytes int64) ([]byte, error) {
	if int64(len(data)) <= maxBytes {
		return data, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		log.Printf("image compression: decode-after-config failed, storing original: %v", err)
		return data, nil
	}
	var out []byte
	switch format {
	case "jpeg":
		out = compressJPEG(src, maxBytes)
	case "png":
		out = compressPNG(src, maxBytes)
	default:
		return nil, fmt.Errorf("compressImage: unsupported format %q", format)
	}
	if int64(len(out)) > maxBytes {
		log.Printf("image compression: guard tripped for %q (%d → %d bytes), storing best effort", format, len(data), len(out))
	}
	return out, nil
}
