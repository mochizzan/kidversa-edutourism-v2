package internal

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Synthetic image builders (test-only, stdlib encoders — spec 09 decision 5:
// the codecs under test are ALSO the test codecs: stdlib + x/image/draw only).
// ---------------------------------------------------------------------------

// gradient is a deterministic w×h RGBA image (no transparency: JPEG decodes
// it identically for the cross-check tests).
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i] = byte(x * 31 % 256)
			img.Pix[i+1] = byte(y * 37 % 256)
			img.Pix[i+2] = byte((x + y) * 17 % 256)
			img.Pix[i+3] = 0xFF
		}
	}
	return img
}

// realPNG / realJPEG build genuinely decodable image bytes — DecodeConfig
// accepts them, unlike the retired fake `pngHead`-prefixed junk payloads.
func realPNG(w, h int) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient(w, h)); err != nil {
		panic("png encode: " + err.Error())
	}
	return buf.Bytes()
}

func realJPEG(w, h int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gradient(w, h), &jpeg.Options{Quality: 90}); err != nil {
		panic("jpeg encode: " + err.Error())
	}
	return buf.Bytes()
}

// noiseRand is seeded so the synthetic >10 MB payloads are deterministic
// (random pixels defeat JPEG/PNG predictors → the raw byte volume scales
// with the pixel count, which is what the ladder tests need).
var noiseRand = rand.New(rand.NewSource(9))

// noiseRGBA is a w×h random-noise RGBA image (existing alpha kept at 0xFF so
// PNG stays 4-channel and JPEG flips to opaque gray identically each run).
func noiseRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i] = byte(noiseRand.Intn(256))
			img.Pix[i+1] = byte(noiseRand.Intn(256))
			img.Pix[i+2] = byte(noiseRand.Intn(256))
			img.Pix[i+3] = 0xFF
		}
	}
	return img
}

// noiseJPEG encodes random-noise sizes upward until the JPEG exceeds
// minBytes (a fixed pixel count cannot guarantee a funny codec's output
// size; growth is bounded because noise encodes poorly).
func noiseJPEG(minBytes int64) []byte {
	for w := 1600; ; w += 400 {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, noiseRGBA(w, w*3/4), &jpeg.Options{Quality: 95}); err != nil {
			panic("jpeg encode: " + err.Error())
		}
		if int64(buf.Len()) >= minBytes {
			return buf.Bytes()
		}
	}
}

// noisePNG is the PNG counterpart of noiseJPEG (with Fatalf-safe sizing).
func noisePNG(t *testing.T, minBytes int64) []byte {
	t.Helper()
	for w := 800; ; w += 200 {
		var buf bytes.Buffer
		if err := png.Encode(&buf, noiseRGBA(w, w)); err != nil {
			t.Fatalf("png encode: %v", err)
		}
		if int64(buf.Len()) >= minBytes {
			return buf.Bytes()
		}
	}
}

// noiseRandPick is a self-seeded w×h noise image — deterministic per call,
// independent of the shared noiseRand stream the >10 MB builders consume (so
// the small ladder-mechanics tests don't shift each other's payloads).
func noiseRandPick(w, h int) *image.RGBA {
	rng := rand.New(rand.NewSource(int64(w*10000 + h)))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i] = byte(rng.Intn(256))
			img.Pix[i+1] = byte(rng.Intn(256))
			img.Pix[i+2] = byte(rng.Intn(256))
			img.Pix[i+3] = 0xFF
		}
	}
	return img
}

// ---------------------------------------------------------------------------
// Server whitelist (spec 09 decision 2): real decode-config + declared-type
// cross-check — the multipart Content-Type is never trusted. The 2026-10-11
// "anywhere-accepts, store-as-.bin" behavior is retired on the image path.
// ---------------------------------------------------------------------------

// TestStoreImageRejectsNonWhitelistedImages: WebP, ICO, GIF, HTML, corrupt
// headers and declared-type mismatches → errRejected (→ failStore → 200
// VALIDATION), and NOTHING lands on disk.
func TestStoreImageRejectsNonWhitelistedImages(t *testing.T) {
	s, _ := newTestServer(t)

	// A real RIFF/VP8 WebP header: DecodeConfig (without x/image/webp) fails.
	webp := append(webpHead, bytes.Repeat([]byte{0}, 32)...)

	cases := []struct {
		name     string
		declared string
		content  []byte
	}{
		{"webp bytes declared image/webp", "image/webp", webp},
		{"webp bytes masquerading as jpeg", "image/jpeg", webp},
		{"real png bytes declared image/jpeg (mismatch)", "image/jpeg", realPNG(8, 8)},
		{"real jpeg bytes declared image/png (mismatch)", "image/png", realJPEG(8, 8)},
		{"corrupt png header declared image/png (sniff-trusting junk)", "image/png",
			append(append([]byte{}, pngHead...), bytes.Repeat([]byte{0}, 64)...)},
		{"ico bytes declared image/x-icon", "image/x-icon", icoHead},
		{"gif bytes declared image/gif", "image/gif", gifHead},
		{"html declared text/html", "text/html; charset=utf-8", []byte("<html>")},
		{"absent declaration (cross-check impossible)", "", realPNG(8, 8)},
		{"empty declared type with unknown bytes", "application/octet-stream", []byte("junkjunk")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.storeImage("frames", bytes.NewReader(tc.content), tc.declared)
			require.ErrorIs(t, err, errRejected, "non-whitelisted upload must be rejected")
			FsxRequireNoImageFiles(t, s, "frames")
		})
	}
}

// TestStoreImageAcceptsDecodeConfigJPGPNG: decodable JPG/PNG (with or without
// a junk tail after the header) pass; ≤10 MB bytes are stored BYTE-FOR-BYTE
// as received, extension computed from the verified format.
func TestStoreImageAcceptsDecodeConfigJPGPNG(t *testing.T) {
	s, _ := newTestServer(t)

	t.Run("png round-trips byte-for-byte", func(t *testing.T) {
		content := append(append([]byte{}, realPNG(8, 8)...), bytes.Repeat([]byte{0xEE}, 4096)...)
		rel, err := s.storeImage("frames", bytes.NewReader(content), "image/png")
		require.NoError(t, err)
		require.Regexp(t, regexp.MustCompile(`^frames/[0-9a-f]{32}\.png$`), rel)
		stored, err := os.ReadFile(filepath.Join(s.upload, filepath.FromSlash(rel)))
		require.NoError(t, err)
		require.Equal(t, content, stored, "≤10 MB images are stored byte-for-byte")
	})

	t.Run("jpeg with content-type params stores as .jpg", func(t *testing.T) {
		content := realJPEG(16, 16)
		rel, err := s.storeImage("frames", bytes.NewReader(content), "image/jpeg; charset=utf-8")
		require.NoError(t, err)
		require.Regexp(t, regexp.MustCompile(`^frames/[0-9a-f]{32}\.jpg$`), rel)
		stored, err := os.ReadFile(filepath.Join(s.upload, filepath.FromSlash(rel)))
		require.NoError(t, err)
		require.Equal(t, content, stored)
	})
}

// TestStoreImageRejectsOverFiftyMegsSlipThrough: the client gate is ≤50 MB;
// a body that slips past it is bounded at the endpoint — the stored file
// itself rejects above 50 MB with errRejected (→ 200 VALIDATION).
func TestStoreImageRejectsOverFiftyMegsSlipThrough(t *testing.T) {
	s, _ := newTestServer(t)
	over := append(append([]byte{}, realPNG(8, 8)...), bytes.Repeat([]byte{7}, 50<<20 - 16)...)

	_, err := s.storeImage("frames", bytes.NewReader(over), "image/png")
	require.ErrorIs(t, err, errRejected, "over-size slip-through must be rejected")
	FsxRequireNoImageFiles(t, s, "frames")
}

// ---------------------------------------------------------------------------
// Compression (spec 09 decision 3): a stored image >10 MB goes through the
// format-preserving compression ladder inside storeImage — ≤10 MB is stored
// byte-for-byte. The server NEVER rejects an image for being unable to
// compress: an exhausted ladder logs and stores the best effort.
// ---------------------------------------------------------------------------

// decodableFormat DecodeConfigs data and returns the real format name — every
// compressed OUTPUT must stay a decodable image of the ORIGINAL format.
func decodableFormat(t *testing.T, data []byte) string {
	t.Helper()
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err, "stored bytes must remain decodable")
	return format
}

// TestStoreImageCompressesOversizeJPEG: a genuine >10 MB noise JPEG stores ≤
// 10 MB with the SAME format (extension recomputed from the verified OUTPUT
// bytes — still .jpg), decodable, strictly smaller than the input.
func TestStoreImageCompressesOversizeJPEG(t *testing.T) {
	s, _ := newTestServer(t)
	big := noiseJPEG(compressTriggerBytes + 1<<20)
	require.Greater(t, int64(len(big)), compressTriggerBytes, "test payload must exceed the trigger")

	rel, err := s.storeImage("frames", bytes.NewReader(big), "image/jpeg")
	require.NoError(t, err, "compression must never reject an upload")
	require.Regexp(t, regexp.MustCompile(`^frames/[0-9a-f]{32}\.jpg$`), rel)

	stored, err := os.ReadFile(filepath.Join(s.upload, filepath.FromSlash(rel)))
	require.NoError(t, err)
	require.Equal(t, "jpeg", decodableFormat(t, stored), "format must be preserved")
	require.LessOrEqual(t, int64(len(stored)), compressTriggerBytes, "output must fit the 10 MB trigger")
	require.Less(t, int64(len(stored)), int64(len(big)), "output must be strictly smaller")
}

// TestStoreImageCompressesOversizePNG: the PNG counterpart — noise PNG >
// 10 MB downscaled via CatmullRom + BestCompression re-encodes, stores as a
// decodable .png ≤ 10 MB.
func TestStoreImageCompressesOversizePNG(t *testing.T) {
	s, _ := newTestServer(t)
	big := noisePNG(t, compressTriggerBytes+1<<20)
	require.Greater(t, int64(len(big)), compressTriggerBytes, "test payload must exceed the trigger")

	rel, err := s.storeImage("frames", bytes.NewReader(big), "image/png")
	require.NoError(t, err, "compression must never reject an upload")
	require.Regexp(t, regexp.MustCompile(`^frames/[0-9a-f]{32}\.png$`), rel)

	stored, err := os.ReadFile(filepath.Join(s.upload, filepath.FromSlash(rel)))
	require.NoError(t, err)
	require.Equal(t, "png", decodableFormat(t, stored), "format must be preserved")
	require.LessOrEqual(t, int64(len(stored)), compressTriggerBytes, "output must fit the 10 MB trigger")
	require.Less(t, int64(len(stored)), int64(len(big)), "output must be strictly smaller")
}

// TestCompressImageJPEG exercises the ladder mechanics directly with a tiny
// maxBytes target: the quality ladder (…resolution backstop) must land ≤
// maxBytes while the output stays a decodable JPEG.
func TestCompressImageJPEG(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, noiseRandPick(180, 180), &jpeg.Options{Quality: 95}))
	src := buf.Bytes()
	require.Greater(t, int64(len(src)), int64(16384), "test source must exceed the tiny target")

	out, err := compressImage(src, "jpeg", 16384)
	require.NoError(t, err)
	require.Equal(t, "jpeg", decodableFormat(t, out))
	require.LessOrEqual(t, int64(len(out)), int64(16384))
	require.NotEmpty(t, out)
}

// TestCompressImagePNG: the CatmullRom downscale ladder for PNG, target
// semantics as above.
func TestCompressImagePNG(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, noiseRandPick(120, 120)))
	src := buf.Bytes()
	require.Greater(t, int64(len(src)), int64(8192), "test source must exceed the tiny target")

	out, err := compressImage(src, "png", 8192)
	require.NoError(t, err)
	require.Equal(t, "png", decodableFormat(t, out))
	require.LessOrEqual(t, int64(len(out)), int64(8192))
	require.NotEmpty(t, out)
}

// TestCompressImageBestEffortGuard: an impossible target (1 byte) must NEVER
// reject — the guard trips, the best effort is returned (non-empty, decodable,
// format preserved) and the error is nil.
func TestCompressImageBestEffortGuard(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, jpeg.Encode(&buf, noiseRandPick(64, 64), &jpeg.Options{Quality: 90}))
			if format == "png" {
				buf.Reset()
				require.NoError(t, png.Encode(&buf, noiseRandPick(64, 64)))
			}
			src := buf.Bytes()

			out, err := compressImage(src, format, 1)
			require.NoError(t, err, "the server never rejects for incompressibility")
			require.NotEmpty(t, out, "best effort must be returned")
			require.Equal(t, format, decodableFormat(t, out))
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrency cap (spec 09 decision 4): a buffered-channel semaphore (NOT a
// mutex) limits CONCURRENT compressions to 5 — handlers block while waiting;
// the slot is released on every outcome (defer).
// ---------------------------------------------------------------------------

// TestWithCompressSlotCapsConcurrency: 12 goroutines contend for the gate;
// exactly 5 run concurrently and every one of them completes (slot released
// on every outcome, no leak). The gate is injected to keep the test
// deterministic and collision-cheap (the real gate lives on Server).
func TestWithCompressSlotCapsConcurrency(t *testing.T) {
	s, _ := newTestServer(t)
	s.compressGate = make(chan struct{}, 5)

	var cur, peak atomic.Int64
	var wg sync.WaitGroup
	release := make(chan struct{})
	fiveIn := make(chan struct{})
	var fiveOnce sync.Once

	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.withCompressSlot(func() error {
				n := cur.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				if n == 5 {
					// once: after release, the seventh wave refills the cap
					// and a later 5th-concurrent enter closes it again.
					fiveOnce.Do(func() { close(fiveIn) })
				}
				<-release // hold the slot until the assert saw the cap
				cur.Add(-1)
				return nil
			})
			if err != nil {
				t.Errorf("withCompressSlot: %v", err)
			}
		}()
	}

	select {
	case <-fiveIn:
	case <-time.After(10 * time.Second):
		t.Fatalf("only %d goroutines entered the gate; cap must admit exactly 5 waiting holders", peak.Load())
	}
	require.Equal(t, int64(5), peak.Load(), "never more than 5 concurrent compressions")
	require.Equal(t, int64(5), cur.Load())

	close(release)
	wg.Wait()
	require.Equal(t, int64(5), peak.Load())
	require.Equal(t, int64(0), cur.Load(), "every slot is released on every outcome")
}
