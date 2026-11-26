package daemon

import (
	"bytes"
	"io"
	"mime/multipart"
	"testing"
)

// A frame on the stream is complete as soon as it is written: the head of
// the part after it is already there. A browser shows a part of a
// multipart/x-mixed-replace stream only once the next part's head arrives,
// so a frame that waited for the next one to be written was not shown,
// and the last frame of a page that stopped changing never was.
func TestAFrameIsCompleteAsSoonAsItIsWritten(t *testing.T) {
	var buf bytes.Buffer
	flushes := 0
	s := &frameStream{w: &buf, flush: func() { flushes++ }}
	s.open()
	if flushes != 1 {
		t.Fatalf("open flushed %d times; the response headers have to reach the client before any frame", flushes)
	}
	// No part before the first frame: a stream that ends with none, as for
	// a conversation with no tab, would end in an unfinished part. An empty
	// frame is not a frame.
	s.frame(nil)
	s.frame([]byte{})
	if buf.Len() != 0 {
		t.Fatalf("before any frame the stream holds %q", buf.String())
	}

	frames := [][]byte{[]byte("\xff\xd8first frame\xff\xd9"), []byte("\xff\xd8second\r\n--not-a-boundary\xff\xd9")}
	for i, frame := range frames {
		s.frame(frame)
		if flushes != i+2 {
			t.Fatalf("frame %d was not flushed", i+1)
		}
		// Read with what has been written so far, and nothing more: the
		// stream is still open, so the reader sees the end of the buffer as
		// a connection that has sent nothing since.
		r := multipart.NewReader(bytes.NewReader(buf.Bytes()), frameBoundary)
		var got []byte
		for n := 0; n <= i; n++ {
			part, err := r.NextPart()
			if err != nil {
				t.Fatalf("after %d frame(s), part %d: %v", i+1, n+1, err)
			}
			if ct := part.Header.Get("Content-Type"); ct != "image/jpeg" {
				t.Errorf("part %d Content-Type = %q", n+1, ct)
			}
			got, err = io.ReadAll(part)
			if err != nil {
				t.Fatalf("after %d frame(s), part %d is not complete: %v", i+1, n+1, err)
			}
		}
		if !bytes.Equal(got, frame) {
			t.Errorf("frame %d read back as %q, want %q", i+1, got, frame)
		}
	}
}
