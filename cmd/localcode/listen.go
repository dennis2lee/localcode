package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// Typing "localcode" used to fail outright when something already held
// port 4096:
//
//	error: daemon failed to start: listen tcp 127.0.0.1:4096: bind: address already in use
//
// and that was the whole of it. No TUI, no explanation of what had the
// port, nothing to do about it but read the source for the flag that
// changes it. The most common thing holding that port is another
// localcode, which makes the failure especially poor: the daemon is
// designed as a shared core with clients attaching over HTTP, and this
// is a client, refusing to start next to exactly the thing it attaches
// to.
//
// The desktop window never had the problem, and how it avoids it is the
// hint: it binds 127.0.0.1:0 and lets the OS pick. It can do that
// because nobody types its address. The terminal's daemon cannot, quite,
// since 4096 is where the Web UI is expected to be, so the rule is a
// little longer.

// listenResult says what happened when the daemon tried to take its
// address: it got one, or somebody else's daemon is already there.
type listenResult struct {
	// ln is the listener this process owns, or nil when attaching.
	ln net.Listener
	// attachTo is the base URL of a localcode daemon already running at
	// the requested address, when there is one.
	attachTo string
	// moved is set when the daemon ended up somewhere other than the
	// address asked for, so the caller can say where the Web UI went.
	moved bool
	// otherDaemon is set when a localcode daemon holds the address and
	// works somewhere else, which is why this one did not attach to it.
	// The caller says so and then binds its own port.
	//
	// A flag of its own rather than "elsewhere is not empty", because a
	// daemon that answers as localcode and will not say where it works
	// is exactly this case with nothing to name. Deriving it from the
	// string left that one falling out of the switch below with no
	// listener bound, which the caller then dereferenced.
	otherDaemon bool
	// elsewhere is that daemon's directory when it said, and empty when
	// it did not.
	elsewhere string
}

// takeListener binds addr, or works out what to do instead when it is
// taken.
//
// Three outcomes, in the order they are checked:
//
//   - The address is free. Ordinary case, nothing to say.
//   - A localcode daemon answers there, working in this directory.
//     Attach to it. That is what this program does over --server, and a
//     daemon on this machine is no different from one on another; the
//     alternative is refusing to run beside the thing designed to be run
//     beside.
//   - A localcode daemon answers there and works somewhere else. Do not
//     attach. A daemon stamps its own directory onto every session
//     created on it, so attaching from another project would open a
//     conversation that edits the first project's files while the
//     terminal sits in the second. Sharing a daemon is a convenience;
//     working where you are is the promise.
//   - Something else holds it. Bind a free port instead, so the terminal
//     still works, and let the caller report where the Web UI moved to.
//
// The last two only when the address was the default: an explicitly
// requested --listen is a request, and quietly serving somewhere else
// would answer a different one.
func takeListener(addr, workdir string, explicit bool) (listenResult, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return listenResult{ln: ln}, nil
	}
	if !addrInUse(err) {
		return listenResult{}, err
	}

	their, isDaemon := daemonWorkspace(addr)
	switch {
	case isDaemon && sameDir(their, workdir):
		return listenResult{attachTo: "http://" + addr}, nil
	case isDaemon && explicit:
		return listenResult{}, fmt.Errorf(
			"%w\n\na localcode daemon is listening there, but it works in %s "+
				"and this terminal is in %s. Run it from there, or choose another "+
				"address with --listen", err, their, workdir)
	case explicit:
		return listenResult{}, fmt.Errorf(
			"%w\n\nsomething other than localcode is listening there. "+
				"Choose another address with --listen, or stop what is using it", err)
	}

	// Either a stranger has the address, or a localcode working in
	// another project does. Neither is ours to use, and we were not
	// asked for this address by name. The port only matters for reaching
	// the Web UI, and the caller prints where it went.
	if isDaemon {
		return listenResult{otherDaemon: true, elsewhere: their}, nil
	}
	free, ferr := net.Listen("tcp", freePortOn(addr))
	if ferr != nil {
		return listenResult{}, err
	}
	return listenResult{ln: free, moved: true}, nil
}

// bindElsewhere finishes the case takeListener could only describe: a
// free port for a daemon of our own. Split out so the reason for moving
// can be reported before the move happens.
func bindElsewhere(addr string) (net.Listener, error) {
	return net.Listen("tcp", freePortOn(addr))
}

// sameDir reports whether two paths name the same directory, comparing
// what they resolve to rather than how they are spelled: a symlinked
// project directory and its target are one place, and "/tmp" on macOS is
// a link to "/private/tmp", which is exactly the sort of difference that
// would otherwise start a second daemon for no reason.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// freePortOn keeps the requested host and asks the OS for any port on
// it, so "--listen 0.0.0.0:4096" falls back to another port on every
// interface rather than quietly retreating to loopback.
func freePortOn(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1:0"
	}
	return net.JoinHostPort(host, "0")
}

// daemonWorkspace reports whether a localcode daemon is answering at
// addr, and which directory it works in.
//
// It asks, rather than assuming. Anything can hold a port, and attaching
// a TUI to a stranger would turn a clear bind error into an obscure one
// somewhere further in. GET /api/version is the cheapest endpoint that
// only this program serves, and its answer has to parse as this
// program's answer; the workspace then decides whether attaching would
// put a conversation in the wrong project.
//
// A short timeout, because this runs before anything is on screen. A
// daemon on loopback that cannot answer in a second is one this client
// would not get far with anyway.
func daemonWorkspace(addr string) (dir string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var version struct {
		Version string `json:"version"`
	}
	if err := getJSON(ctx, "http://"+addr+"/api/version", &version); err != nil || version.Version == "" {
		return "", false
	}
	var ws struct {
		Path string `json:"path"`
	}
	if err := getJSON(ctx, "http://"+addr+"/api/workspace", &ws); err != nil {
		// It is a localcode daemon and it will not say where it works.
		// Treating that as "somewhere else" is the safe reading: the
		// cost is a second daemon, and the cost of guessing wrong the
		// other way is a session editing the wrong project.
		return "", true
	}
	return ws.Path, true
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
