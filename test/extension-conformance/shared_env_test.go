package extensionconformance

import "testing"

// pig divergence (D234): Pi runs every extension and provider in one Node process, so a value one extension writes to process.env is what every other extension reads next. Pig runs each extension in its own process; the Host's shared environment carries a native SDK's SetSharedEnv/UnsetSharedEnv into the process.env of every Node realm before the call returns. Each row reads the value back from Node realms in a separate isolated cell and in a shared cell, in the next request handler after the call. The value contains a space and '=' so that no fallback or inherited value can match, and a deletion must leave the key absent, not empty.
func TestSharedEnvReachesNodeProcessEnvAcrossSDKs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in short mode (builds subprocess fixtures)")
	}
	const key = "PIG_CONFORMANCE_SHARED_ENV_D234"
	eachBusRealm(t, false, func(t *testing.T, language, isolation string) {
		rig := newBusRig(t,
			busSpec(language, isolation, "setter"),
			busSpec("node", "isolated", "alone"),
			busSpec("node", isolation, "cellmate"),
		)
		read := func() {
			rig.must("alone", "envget", key)
			rig.must("cellmate", "envget", key)
		}
		read()
		rig.expect(`alone|env|{"`+key+`":null}`, `cellmate|env|{"`+key+`":null}`)
		rig.reset()

		rig.must("setter", "envset", key+" bridge on=1")
		read()
		rig.expect(`alone|env|{"`+key+`":"bridge on=1"}`, `cellmate|env|{"`+key+`":"bridge on=1"}`)
		rig.reset()

		rig.must("setter", "envset", key+" second")
		read()
		rig.expect(`alone|env|{"`+key+`":"second"}`, `cellmate|env|{"`+key+`":"second"}`)
		rig.reset()

		rig.must("setter", "envunset", key)
		read()
		rig.expect(`alone|env|{"`+key+`":null}`, `cellmate|env|{"`+key+`":null}`)
	})
}

// The Host rejects a key a process environment cannot hold and the variables it owns, in every SDK, and changes nothing.
func TestSharedEnvRejectsInvalidAndHostOwnedKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in short mode (builds subprocess fixtures)")
	}
	eachBusRealm(t, false, func(t *testing.T, language, isolation string) {
		rig := newBusRig(t, busSpec(language, isolation, "setter"), busSpec("node", "isolated", "reader"))
		for _, key := range []string{"PIG_EXT_SOCKET", "pig_harness_argv_file", "A=B"} {
			if err := rig.run("setter", "envset", key+" x"); err == nil {
				t.Fatalf("envset %s succeeded", key)
			}
		}
		rig.must("reader", "envget", "A")
		rig.expect(`reader|env|{"A":null}`)
	})
}
