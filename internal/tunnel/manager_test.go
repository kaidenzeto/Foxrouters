package tunnel

import "testing"

// TestModeValidity ensures the Mode enum's IsValid guard rejects
// unknown values (defense against typos in dashboard input).
func TestModeValidity(t *testing.T) {
	valid := []Mode{ModeNone, ModeQuick, ModeNamed, ModeHybrid}
	for _, m := range valid {
		if !m.IsValid() {
			t.Errorf("mode %q should be valid", m)
		}
	}
	invalid := []Mode{"", "bogus", "QUICK", "hybrid ", "off"}
	for _, m := range invalid {
		if m.IsValid() {
			t.Errorf("mode %q should NOT be valid", m)
		}
	}
}

// TestRingBufferOverwrite verifies the log ring evicts the oldest line
// once it reaches capacity — cloudflared can be noisy and we must not
// grow unbounded.
func TestRingBufferOverwrite(t *testing.T) {
	r := newRingBuffer(3)
	r.Add("a")
	r.Add("b")
	r.Add("c")
	if got := r.Snapshot(); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
	r.Add("d")
	got := r.Snapshot()
	if len(got) != 3 || got[0] != "b" || got[2] != "d" {
		t.Fatalf("after overflow got %v", got)
	}
	r.Reset()
	if got := r.Snapshot(); len(got) != 0 {
		t.Fatalf("after reset expected empty, got %v", got)
	}
}

// TestRingBufferLineCap ensures oversized lines are truncated so a
// runaway log entry can't blow memory.
func TestRingBufferLineCap(t *testing.T) {
	r := newRingBuffer(2)
	big := make([]byte, 8000)
	for i := range big {
		big[i] = 'x'
	}
	r.Add(string(big))
	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if len(got[0]) > 4100 { // 4096 + ellipsis rune
		t.Fatalf("line not truncated: %d bytes", len(got[0]))
	}
}

// TestMaskToken keeps prefix + suffix visible, hides the middle. Short
// tokens are fully masked.
func TestMaskToken(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"short":               "***",
		"abcdef1234567890":    "abcdef…7890",
		"    padded    ":      "***",
	}
	for in, want := range cases {
		if got := MaskToken(in); got != want {
			t.Errorf("MaskToken(%q) = %q, want %q", in, got, want)
		}
	}
}
