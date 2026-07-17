package session

import (
	"bytes"
	"testing"
)

func TestRing(t *testing.T) {
	r := newRing(8)
	if got := r.Bytes(); len(got) != 0 {
		t.Fatalf("empty ring: got %q", got)
	}
	r.Write([]byte("abc"))
	if got := r.Bytes(); !bytes.Equal(got, []byte("abc")) {
		t.Fatalf("partial: got %q", got)
	}
	r.Write([]byte("defgh")) // exactly full
	if got := r.Bytes(); !bytes.Equal(got, []byte("abcdefgh")) {
		t.Fatalf("full: got %q", got)
	}
	r.Write([]byte("XY")) // wrap
	if got := r.Bytes(); !bytes.Equal(got, []byte("cdefghXY")) {
		t.Fatalf("wrap: got %q", got)
	}
	r.Write([]byte("0123456789ABCDEF")) // larger than capacity
	if got := r.Bytes(); !bytes.Equal(got, []byte("89ABCDEF")) {
		t.Fatalf("oversize: got %q", got)
	}
	r.Write([]byte("zz"))
	if got := r.Bytes(); !bytes.Equal(got, []byte("ABCDEFzz")) {
		t.Fatalf("after oversize: got %q", got)
	}
}
