package hotreload

import "testing"

func TestVMServiceURL(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"flutter: The Dart VM service is listening on http://127.0.0.1:52361/kKx2tUbXYuM=/", "http://127.0.0.1:52361/kKx2tUbXYuM=/"},
		{"Observatory listening on http://127.0.0.1:1024/abc=/\n", "http://127.0.0.1:1024/abc=/"},
		{"flutter: hello", ""},
	}
	for _, tt := range tests {
		if got := VMServiceURL(tt.line); got != tt.want {
			t.Errorf("VMServiceURL(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}
