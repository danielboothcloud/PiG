package cli

import (
	"errors"
	"sync"

	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
)

// rpcRelaunchInput decides between an extension's relaunch and the command reader: whichever comes first excludes the other. The reader buffers stdin, and the bytes it holds would be lost to the relaunched pig, so a relaunch is accepted only while RPC mode starts, before the reader attaches; a relaunch accepted then keeps the reader from ever reading, and the relaunched pig reads every command the client wrote.
// pig divergence (D238): a Pi extension relaunches Pi by spawning a child on the same stdin.
type rpcRelaunchInput struct {
	mu       sync.Mutex
	attached bool
}

// request accepts a relaunch and starts the shutdown, or returns why RPC mode cannot restart now.
func (g *rpcRelaunchInput) request(request subprocess.RelaunchRequest, shutdown func()) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.attached {
		return errors.New(subprocess.CallRelaunch + ": RPC mode already reads commands from stdin, and a relaunched pig could not read the ones this pig buffered; relaunch while the session starts")
	}
	if err := acceptRelaunch(request); err != nil {
		return err
	}
	shutdown()
	return nil
}

// attach reports whether the command reader may read stdin, and marks it attached when it may.
func (g *rpcRelaunchInput) attach() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if relaunchPending() {
		return false
	}
	g.attached = true
	return true
}
