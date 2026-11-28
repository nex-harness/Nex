package runner

import "testing"

func TestResolveAppBundleFromExecutable(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "/Users/tylinux/.nex-runner/NexRunner.app/Contents/MacOS/nex-runner",
			want: "/Users/tylinux/.nex-runner/NexRunner.app",
		},
		{
			in:   "/Applications/Nex.app/Contents/MacOS/Nex",
			want: "/Applications/Nex.app",
		},
		{
			in:   "/usr/local/bin/nex-runner",
			want: "/usr/local/bin/nex-runner",
		},
		{
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		if got := resolveAppBundleFromExecutable(tc.in); got != tc.want {
			t.Fatalf("resolveAppBundleFromExecutable(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
