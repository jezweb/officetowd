package selfupdate

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.2.2", "v0.2.3", true},
		{"0.2.2", "0.2.3", true},
		{"v0.2.3", "v0.2.3", false},
		{"v0.2.3", "v0.2.2", false},
		{"v0.2.3", "v0.3.0", true},
		{"v0.3.0", "v0.2.9", false},
		{"v1.0.0", "v0.9.9", false},
		{"dev", "v0.2.3", true},         // local build → any release is newer
		{"dev", "dev", false},           // identical strings short-circuit
		{"v0.2.3", "garbage", false},    // unparseable remote → don't update
		{"v0.2.3-rc1", "v0.2.3", false}, // pre-release suffix dropped → equal
		{"v0.2.0", "v0.2.10", true},     // numeric, not lexical (10 > 2)
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	checksums := "abc123  officetowd-darwin-arm64.tar.gz\n" +
		"def456  officetowd-linux-amd64.tar.gz\n"
	if got := checksumFor(checksums, "officetowd-darwin-arm64.tar.gz"); got != "abc123" {
		t.Errorf("darwin-arm64 = %q, want abc123", got)
	}
	if got := checksumFor(checksums, "officetowd-linux-amd64.tar.gz"); got != "def456" {
		t.Errorf("linux-amd64 = %q, want def456", got)
	}
	if got := checksumFor(checksums, "officetowd-windows-amd64.tar.gz"); got != "" {
		t.Errorf("missing entry should be empty, got %q", got)
	}
}

func TestAssetNameShape(t *testing.T) {
	// Sanity: matches the release.yml asset naming.
	if got := AssetName(); got == "officetowd--.tar.gz" || len(got) < len("officetowd-x-y.tar.gz") {
		t.Errorf("AssetName looks wrong: %q", got)
	}
}
