package subprocess

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// pig divergence (D238): a Pi extension restarts Pi with another extension set or model scope by spawning a new Pi on its own process's terminal and exiting when that child exits. A subprocess extension's stdio is the host wire, so that spawn starts a second pig on pipes. CallRelaunch asks the host to end the current runtime and replace the pig process image with a pig started with the given arguments and environment.

// CallRelaunch (ext→host) ends the runtime and restarts pig in the same process. The args are RelaunchArgs and the result is null.
const CallRelaunch = "relaunch"

// RelaunchArgs is the argument of CallRelaunch. Args is the complete argument list after the pig executable; it is required and may be empty. Env holds the environment changes over pig's own environment: a string sets the variable and null removes it.
type RelaunchArgs struct {
	Args *[]string          `json:"args"`
	Env  map[string]*string `json:"env,omitempty"`
}

// RelaunchRequest is a validated CallRelaunch request as the mode receives it. SessionFile is the current session's file, empty when the session has none.
type RelaunchRequest struct {
	Args        []string
	Env         map[string]*string
	SessionFile string
}

// validateRelaunchEnvKey rejects names a process environment cannot hold and the variables the Host owns, as the shared environment does.
func validateRelaunchEnvKey(key string) error {
	switch {
	case key == "":
		return errors.New(CallRelaunch + ": environment key is empty")
	case strings.ContainsAny(key, "=\x00"):
		return fmt.Errorf("%s: environment key %q contains '=' or NUL", CallRelaunch, key)
	case strings.HasPrefix(strings.ToUpper(key), "PIG_EXT_"), strings.HasPrefix(strings.ToUpper(key), "PIG_HARNESS_"):
		return fmt.Errorf("%s: environment key %q is owned by the extension host", CallRelaunch, key)
	}
	return nil
}

// decodeRelaunchArgs decodes and validates the CallRelaunch arguments. An argument or value with a NUL byte cannot reach execve.
func decodeRelaunchArgs(raw json.RawMessage) (RelaunchRequest, error) {
	var parsed RelaunchArgs
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return RelaunchRequest{}, fmt.Errorf("decode %s: %w", CallRelaunch, err)
	}
	if parsed.Args == nil {
		return RelaunchRequest{}, errors.New(CallRelaunch + " requires args")
	}
	for i, arg := range *parsed.Args {
		if strings.ContainsRune(arg, 0) {
			return RelaunchRequest{}, fmt.Errorf("%s: argument %d contains NUL", CallRelaunch, i)
		}
	}
	for key, value := range parsed.Env {
		if err := validateRelaunchEnvKey(key); err != nil {
			return RelaunchRequest{}, err
		}
		if value != nil && strings.ContainsRune(*value, 0) {
			return RelaunchRequest{}, fmt.Errorf("%s: environment value of %q contains NUL", CallRelaunch, key)
		}
	}
	args := *parsed.Args
	if args == nil {
		args = []string{}
	}
	return RelaunchRequest{Args: args, Env: parsed.Env}, nil
}

// handleRelaunch serves CallRelaunch. The mode decides whether the process can restart now; its error returns to the extension and the runtime keeps running.
func (b *UIBridge) handleRelaunch(actions *HostCallbacks, args json.RawMessage) (*CallResultPayload, error) {
	request, err := decodeRelaunchArgs(args)
	if err != nil {
		return nil, err
	}
	if actions == nil || actions.Relaunch == nil {
		return nil, errors.New(CallRelaunch + " is not available in this host")
	}
	if actions.GetSessionFile != nil {
		request.SessionFile = actions.GetSessionFile()
	}
	if err := actions.Relaunch(request); err != nil {
		return nil, err
	}
	return &CallResultPayload{}, nil
}
