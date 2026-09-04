package provider

import (
	"net/http"

	"localcode/internal/debuglog"
)

// debugClient is the HTTP client every provider that speaks HTTP is built
// with: the default one, wrapped so that a request whose context carries
// a debug sink is written to it.
//
// Installed always rather than swapped in when "/debug-log" is on. The
// transport does nothing at all without a sink on the context, so the
// cost is one type assertion per request, and the alternative — building
// providers differently depending on a switch that moves at runtime —
// would mean a turn admitted before the switch moved talking through a
// client that cannot log, which is the class of bug the Smart Agent pin
// exists to prevent.
func debugClient() *http.Client {
	c := *http.DefaultClient
	c.Transport = debuglog.Transport{Base: http.DefaultTransport}
	return &c
}
