package provider

import (
	"bytes"
	"encoding/hex"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// Three WebP files cwebp wrote, one per container layout: a 300x123
// lossy image, the same image lossless, and a 301x124 one with alpha,
// which cwebp writes as an extended file.
const (
	webpLossyHex    = "5249464694000000574542505650382088000000d00b009d012a2c017b003e6d369a49a42322a1204800800d89696ee176b11edc07e0000030c6a4d76da2e106410d5526bb6d170832086aa935db68b84190435549aedb45c20c821aaa4d76da2e106410d5526bb6d170832086aa935db68b84190435549aedb34000fefd0a7ffff9b02b9ae7e96fffff6777e8cdfa777f7cec7c10f0050000000000"
	webpLosslessHex = "5249464628000000574542505650384c1b0000002f2b811e000750e42ad4a3ff0105691b30f52f7f37a2ffc9fd571000"
	webpExtendedHex = "52494646c400000057454250565038580a000000100000002c01007b0000414c504815000000010750c088082848db80a97ff9bb11fd4feebfce00005650382088000000d00b009d012a2d017c003e6d369a49a42322a1204800800d89696ee176b11edc07e0000030c6a4d76da2e106410d5526bb6d170832086aa935db68b84190435549aedb45c20c821aaa4d76da2e106410d5526bb6d170832086aa935db68b84190435549aedb34000fefd0a7ffff9b02b9ae7e96fffff6777e8cdfa777f7cec7c10f0050000000000"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func encoded(t *testing.T, w, h int, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageSizeReadsEveryFormatAnAdapterCarries(t *testing.T) {
	pngOf := func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }
	jpegOf := func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) }
	gifOf := func(b *bytes.Buffer, m image.Image) error { return gif.Encode(b, m, nil) }
	cases := []struct {
		name string
		data []byte
		w, h int
	}{
		{"png", encoded(t, 31, 9001, pngOf), 31, 9001},
		{"jpeg", encoded(t, 17, 23, jpegOf), 17, 23},
		{"gif", encoded(t, 40, 2, gifOf), 40, 2},
		{"webp lossy", mustHex(t, webpLossyHex), 300, 123},
		{"webp lossless", mustHex(t, webpLosslessHex), 300, 123},
		{"webp extended", mustHex(t, webpExtendedHex), 301, 124},
	}
	for _, c := range cases {
		w, h, ok := ImageSize(c.data)
		if !ok || w != c.w || h != c.h {
			t.Errorf("%s: got %dx%d ok=%v, want %dx%d", c.name, w, h, ok, c.w, c.h)
		}
	}
}

// A header that cannot be read is not a size: no guess, no panic, for
// bytes cut short anywhere in a WebP file or anything that is no image.
func TestImageSizeReadsNothingFromAHeaderItCannotRead(t *testing.T) {
	// The header ends at byte 30 of a lossy or extended file and at byte
	// 25 of a lossless one.
	for full, header := range map[string]int{webpLossyHex: 30, webpLosslessHex: 25, webpExtendedHex: 30} {
		b := mustHex(t, full)
		for n := 0; n < header; n++ {
			if _, _, ok := ImageSize(b[:n]); ok {
				t.Errorf("%d bytes of %s... read as a size", n, full[24:32])
			}
		}
		if _, _, ok := ImageSize(b[:header]); !ok {
			t.Errorf("the %d header bytes of %s... did not read as a size", header, full[24:32])
		}
	}
	for _, junk := range [][]byte{nil, []byte("not an image"), []byte("RIFF\x00\x00\x00\x00WEBPABCD0000000000000000")} {
		if _, _, ok := ImageSize(junk); ok {
			t.Errorf("%q read as a size", junk)
		}
	}
}

func TestImageTooBigNamesTheLimitItIsPast(t *testing.T) {
	pngOf := func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }
	if why := ImageTooBig(encoded(t, MaxImageSide, MaxImageSide, pngOf)); why != "" {
		t.Errorf("an image at the side limit: %q", why)
	}
	if why := ImageTooBig(encoded(t, MaxImageSide+1, 1, pngOf)); !strings.Contains(why, "8001x1 pixels") {
		t.Errorf("an image one pixel too wide: %q", why)
	}
	if why := ImageTooBig(encoded(t, 1, MaxImageSide+1, pngOf)); !strings.Contains(why, "1x8001 pixels") {
		t.Errorf("an image one pixel too tall: %q", why)
	}
	small := encoded(t, 4, 4, pngOf)
	exact := append(append([]byte(nil), small...), make([]byte, MaxImageBytes-len(small))...)
	if why := ImageTooBig(exact); why != "" {
		t.Errorf("an image of exactly %d bytes: %q", MaxImageBytes, why)
	}
	if why := ImageTooBig(append(exact, 0)); !strings.Contains(why, "3750001 bytes") {
		t.Errorf("an image one byte over: %q", why)
	}
	// Bytes whose size cannot be read are judged on their length alone.
	if why := ImageTooBig([]byte("not an image")); why != "" {
		t.Errorf("unreadable bytes under the byte limit: %q", why)
	}
}

// An error about an image's size is not a model without vision: the hint
// would have the person switch images off on a model that takes them.
func TestASizeRefusalIsNotAVisionRefusal(t *testing.T) {
	for _, msg := range []string{
		"anthropic API returned 400: messages.0.content.1.image.source.base64: image exceeds 5 MB maximum: 7012345 bytes > 5242880 bytes",
		"anthropic API returned 400: messages.0.content.0.image.source.base64.data: At least one of the image dimensions exceed max allowed size: 8000 pixels",
		"anthropic API returned 413: request_too_large: Request exceeds the maximum allowed number of bytes.",
		"bedrock ConverseStream: ValidationException: The image is too large.",
		"anthropic API returned 400: messages.3.content.1.image: image dimensions exceed the limit for many-image requests: 2000 pixels",
	} {
		if isVisionRefusal(errString(msg), "") {
			t.Errorf("read as a vision refusal: %s", msg)
		}
	}
	// The refusals it exists for still are.
	for _, msg := range []string{
		"openai-compat endpoint returned 400: this model does not support image input",
		"bedrock ConverseStream: ValidationException: This model doesn't support the image content block that you provided.",
		"openai-compat endpoint returned 400: Input should be a valid string: expected a string",
	} {
		if !isVisionRefusal(errString(msg), "") {
			t.Errorf("no longer read as a vision refusal: %s", msg)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// Found in review (agy): a server that is down, loading, or has no such
// model often names the model, and a model whose name says "vision" read
// as refusing images, kept from them for an hour by a 503. Bedrock's
// validation words for an image's size read the same way.
func TestAFailureThatNamesAVisionModelIsNotARefusal(t *testing.T) {
	const model = "meta-llama/Llama-3.2-11B-Vision-Instruct"
	for _, msg := range []string{
		`openai-compat endpoint returned 503: {"error":{"message":"Model meta-llama/Llama-3.2-11B-Vision-Instruct is loading, try again shortly"}}`,
		`openai-compat endpoint returned 500: {"error":{"message":"Internal server error invoking llama-3.2-11b-vision-instruct"}}`,
		`openai-compat endpoint returned 404: {"error":{"message":"The model 'llama-3.2-11b-vision-instruct' does not exist"}}`,
		`openai-compat endpoint returned 502: upstream llama-3.2-11b-vision-instruct`,
		"operation error Bedrock Runtime: ConverseStream, https response error StatusCode: 503, ServiceUnavailableException: llama3-2-11b-vision-instruct",
		"bedrock ConverseStream: ValidationException: 1 validation error detected: Value at 'messages.1.member.content.1.member.image.source.bytes' failed to satisfy constraint: Member must have length less than or equal to 3932160",
		"bedrock ConverseStream: ValidationException: Maximum number of images per request is 20.",
	} {
		if isVisionRefusal(errString(msg), model) {
			t.Errorf("read as a vision refusal: %s", msg)
		}
	}
	// The model's name is taken out only where it stands as a word: a
	// model called "m" leaves "image" alone, and a refusal that names a
	// vision model is still a refusal.
	if !isVisionRefusal(errString("openai-compat endpoint returned 400: this model does not support image input"), "m") {
		t.Error("a model called m took the refusal's words with it")
	}
	if !isVisionRefusal(errString("openai-compat endpoint returned 400: Llama-3.2-11B-Vision-Instruct: image input is not supported by this deployment"), model) {
		t.Error("a refusal naming the model is no longer one")
	}
	if got := withoutName("a llama-3.2-11b-vision-instruct, llama-3.2-11b-vision-instructs", "llama-3.2-11b-vision-instruct"); got != "a  , llama-3.2-11b-vision-instructs" {
		t.Errorf("withoutName = %q", got)
	}
}
