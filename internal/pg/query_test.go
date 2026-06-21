package pg

import "testing"

func TestFormatUUID(t *testing.T) {
	b := [16]byte{0x3f, 0x33, 0xc6, 0xb2, 0x99, 0x0a, 0x4f, 0xbb, 0x9a, 0x3e, 0x0d, 0x12, 0x34, 0x56, 0x78, 0x9a}
	got := formatUUID(b)
	want := "3f33c6b2-990a-4fbb-9a3e-0d123456789a"
	if got != want {
		t.Fatalf("formatUUID = %q, want %q", got, want)
	}
}

func TestNormalizeValue(t *testing.T) {
	// UUID: [16]byte renders as canonical text, not a Go byte-array literal.
	uuid := [16]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	if got := normalizeValue(uuid); got != "00112233-4455-6677-8899-aabbccddeeff" {
		t.Fatalf("uuid normalize = %v", got)
	}

	// []byte (bytea/text) becomes a string.
	if got := normalizeValue([]byte("hello")); got != "hello" {
		t.Fatalf("[]byte normalize = %v", got)
	}

	// json/jsonb composites render as JSON text, not Go map syntax.
	if got := normalizeValue(map[string]any{"k": "v"}); got != `{"k":"v"}` {
		t.Fatalf("map normalize = %v", got)
	}

	// scalars pass through unchanged.
	if got := normalizeValue(int64(42)); got != int64(42) {
		t.Fatalf("int normalize = %v", got)
	}
	if got := normalizeValue(nil); got != nil {
		t.Fatalf("nil normalize = %v", got)
	}
}
