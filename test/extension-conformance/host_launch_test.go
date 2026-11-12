package extensionconformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

// hostLaunchGaps are the SDKs that do not expose ctx.HostLaunch and ctx.ShutdownWithCode yet (D236). A gap row asserts that its fixture still lacks the probes, so the row fails when an SDK gains them and the entry must be deleted. Node extensions read Pi's own process.argv and call process.exit, which the Node runtime serves (process-identity.mjs).
var hostLaunchGaps = map[string]string{
	"subprocess-node":          "Node extensions use Pi's process.argv and process.stdin.isTTY (runtime-node/process-identity.mjs)",
	"subprocess-node-packed":   "Node extensions use Pi's process.argv and process.stdin.isTTY (runtime-node/process-identity.mjs)",
	"subprocess-rust":          "Rust Context has no host_launch or shutdown_with_code (D236 follow-up)",
	"subprocess-python":        "Python Context has no host_launch or shutdown_with_code (D236 follow-up)",
	"subprocess-lisp":          "Lisp has no ctx-host-launch or ctx-shutdown-with-code (D236 follow-up)",
	"subprocess-rust-packed":   "Rust Context has no host_launch or shutdown_with_code (D236 follow-up)",
	"subprocess-python-packed": "Python Context has no host_launch or shutdown_with_code (D236 follow-up)",
	"subprocess-lisp-packed":   "Lisp has no ctx-host-launch or ctx-shutdown-with-code (D236 follow-up)",
}

// A Pi extension reads how Pi was started from process.argv and process.stdin/stdout.isTTY in Pi's own process; a subprocess extension asks the host (ctx.HostLaunch, D236). The host is this test process, so its argv is os.Args[1:], which holds the go test flags and so differs from any empty fallback; the mode must agree with ctx.mode. ShutdownWithCode delivers its code to the mode and returns the host's range error for 256, which only the host writes.
func TestHostLaunchAndShutdownWithCodeSDKsMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping host launch conformance in short mode (builds subprocess fixtures)")
	}
	argv, err := json.Marshal(os.Args[1:])
	if err != nil {
		t.Fatal(err)
	}
	stdinTTY, stdoutTTY := term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd()))
	cases := append(sdkHarnessCases(), packedSDKHarnessCases()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.make(t)
			t.Cleanup(func() {
				if h.cleanup != nil {
					h.cleanup()
				}
				if h.host != nil {
					h.host.Shutdown("test done")
				}
			})
			launchProbe, launchOK := findCommand(h.runner, "host-launch-probe")
			shutdownProbe, shutdownOK := findCommand(h.runner, "shutdown-with-code-probe")
			if reason, gap := hostLaunchGaps[tc.name]; gap {
				if launchOK || shutdownOK {
					t.Fatalf("%s now registers the host launch probes; delete its hostLaunchGaps entry (%s)", tc.name, reason)
				}
				t.Skipf("known gap: %s", reason)
			}
			if !launchOK || !shutdownOK {
				t.Fatal("host-launch-probe or shutdown-with-code-probe not registered")
			}
			codes := make(chan int, 4)
			h.bridge.SetHostAction("shutdownWithCode", func(code int) { codes <- code })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := launchProbe.Handler(ctx, ""); err != nil {
				t.Fatal(err)
			}
			prefix := "host-launch=" + string(argv) + ":"
			var launch string
			waitFor(t, func() bool {
				for _, line := range h.ui.Recorded() {
					if strings.HasPrefix(line, prefix) {
						launch = line
						return true
					}
				}
				return false
			})
			fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(launch, prefix), ":info"), ":")
			if want := fmt.Sprint(stdinTTY) + ":" + fmt.Sprint(stdoutTTY); len(fields) != 4 || fields[0] != fields[1] || fields[2]+":"+fields[3] != want {
				t.Fatalf("launch %q, want the mode equal to ctx.mode and terminals %s", launch, want)
			}

			if err := shutdownProbe.Handler(ctx, ""); err != nil {
				t.Fatal(err)
			}
			want := "shutdown-with-code-invalid=shutdownWithCode: exit code 256 is outside 0..255:info"
			waitFor(t, func() bool { return slices.Contains(h.ui.Recorded(), want) })
			select {
			case code := <-codes:
				if code != 7 {
					t.Fatalf("mode received exit code %d, want 7", code)
				}
			default:
				t.Fatal("the mode received no shutdownWithCode request")
			}
			if len(codes) != 0 {
				t.Fatalf("the out-of-range code reached the mode")
			}
		})
	}
}
