//go:build windows

package sysproxy

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Sources names where a proxy can be set on this platform, for a message
// that tells somebody where to set one.
const Sources = "HTTPS_PROXY or the Windows proxy settings"

// WinHTTP, not WinINet. WinHTTP is the one meant for programs that are
// not a browser, it can run a proxy script without a window, and its
// answer for a script is the same one the system's own proxy service
// caches. The calls are made through the DLL directly, as
// internal/gui does with user32, so the default build stays pure Go.
var (
	winhttp                                   = windows.NewLazySystemDLL("winhttp.dll")
	procWinHttpGetIEProxyConfigForCurrentUser = winhttp.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procWinHttpOpen                           = winhttp.NewProc("WinHttpOpen")
	procWinHttpSetTimeouts                    = winhttp.NewProc("WinHttpSetTimeouts")
	procWinHttpGetProxyForUrl                 = winhttp.NewProc("WinHttpGetProxyForUrl")
	procWinHttpCloseHandle                    = winhttp.NewProc("WinHttpCloseHandle")

	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalFree = kernel32.NewProc("GlobalFree")
)

// ieProxyConfig is WINHTTP_CURRENT_USER_IE_PROXY_CONFIG. The strings are
// allocated by WinHTTP and freed with GlobalFree.
type ieProxyConfig struct {
	autoDetect    int32 // BOOL
	autoConfigURL *uint16
	proxy         *uint16
	proxyBypass   *uint16
}

// autoProxyOptions is WINHTTP_AUTOPROXY_OPTIONS.
type autoProxyOptions struct {
	flags                 uint32
	autoDetectFlags       uint32
	autoConfigURL         *uint16
	reserved              uintptr
	reservedDword         uint32
	autoLogonIfChallenged int32 // BOOL
}

// proxyInfo is WINHTTP_PROXY_INFO. The strings are allocated by WinHTTP
// and freed with GlobalFree.
type proxyInfo struct {
	accessType  uint32
	proxy       *uint16
	proxyBypass *uint16
}

const (
	accessTypeNoProxy   = 1   // WINHTTP_ACCESS_TYPE_NO_PROXY
	autoProxyAutoDetect = 0x1 // WINHTTP_AUTOPROXY_AUTO_DETECT
	autoProxyConfigURL  = 0x2 // WINHTTP_AUTOPROXY_CONFIG_URL
	autoDetectTypeDHCP  = 0x1 // WINHTTP_AUTO_DETECT_TYPE_DHCP
	autoDetectTypeDNSA  = 0x2 // WINHTTP_AUTO_DETECT_TYPE_DNS_A
)

// scriptTimeout bounds each step WinHTTP takes to fetch a proxy script:
// resolving its host, connecting, sending and receiving. Longer than
// lookupWait on purpose: a request stops waiting first and goes direct,
// and the lookup it left behind still finishes, so its answer is there
// for the next one.
const scriptTimeout = 10 * time.Second

// The WinHTTP errors a lookup ends in, which Windows' own message table
// does not describe: their text is in winhttp.dll, and FormatMessage on
// the system table gives only "winapi error #12180".
var winhttpErrors = map[windows.Errno]string{
	12002: "the Windows proxy script did not download in time",
	12005: "the Windows proxy script address is not a valid URL",
	12015: "the Windows proxy script's server refused the sign-in",
	12166: "the Windows proxy script has an error in it",
	12167: "the Windows proxy script could not be downloaded",
	12178: "the Windows proxy service failed",
	12180: "Windows automatic proxy detection found no proxy script",
}

const errLoginFailure windows.Errno = 12015 // ERROR_WINHTTP_LOGIN_FAILURE

func winhttpError(err error) error {
	var errno windows.Errno
	if errors.As(err, &errno) {
		if text, ok := winhttpErrors[errno]; ok {
			return errors.New(text)
		}
		return fmt.Errorf("the Windows proxy lookup failed with WinHTTP error %d", uint32(errno))
	}
	return err
}

func globalFree(p *uint16) {
	if p != nil {
		_, _, _ = procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
	}
}

// readSettings reads the current user's Internet Options, which is where
// a browser on Windows reads its proxy from.
func readSettings() (settings, error) {
	if err := procWinHttpGetIEProxyConfigForCurrentUser.Find(); err != nil {
		return settings{}, err
	}
	var c ieProxyConfig
	r, _, err := procWinHttpGetIEProxyConfigForCurrentUser.Call(uintptr(unsafe.Pointer(&c)))
	if r == 0 {
		// ERROR_FILE_NOT_FOUND is an account that has never had any
		// settings, a service account for one: nothing to read is the
		// same as nothing set.
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return settings{}, nil
		}
		return settings{}, fmt.Errorf("WinHttpGetIEProxyConfigForCurrentUser: %w", err)
	}
	defer globalFree(c.autoConfigURL)
	defer globalFree(c.proxy)
	defer globalFree(c.proxyBypass)
	return settings{
		AutoDetect: c.autoDetect != 0,
		ScriptURL:  windows.UTF16PtrToString(c.autoConfigURL),
		Proxy:      windows.UTF16PtrToString(c.proxy),
		Bypass:     windows.UTF16PtrToString(c.proxyBypass),
	}, nil
}

// autoProxy asks WinHTTP to run the proxy script for target: the one
// automatic detection finds, the one at the settings' address, or both
// in that order. Windows evaluates the script, not this program, so a
// script written for browsers gives the answer a browser would get.
func autoProxy(target string, s settings) (autoAnswer, error) {
	for _, p := range []*windows.LazyProc{procWinHttpOpen, procWinHttpSetTimeouts, procWinHttpGetProxyForUrl, procWinHttpCloseHandle, procGlobalFree} {
		if err := p.Find(); err != nil {
			return autoAnswer{}, err
		}
	}
	agent, err := windows.UTF16PtrFromString("localcode")
	if err != nil {
		return autoAnswer{}, err
	}
	targetW, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return autoAnswer{}, err
	}
	opts := autoProxyOptions{}
	if s.AutoDetect {
		opts.flags |= autoProxyAutoDetect
		opts.autoDetectFlags = autoDetectTypeDHCP | autoDetectTypeDNSA
	}
	if s.ScriptURL != "" {
		scriptW, err := windows.UTF16PtrFromString(s.ScriptURL)
		if err != nil {
			return autoAnswer{}, err
		}
		opts.flags |= autoProxyConfigURL
		opts.autoConfigURL = scriptW
	}

	// A session of its own, with no proxy: it only ever fetches the
	// script, and a script fetched through the proxy it configures is a
	// loop.
	h, _, err := procWinHttpOpen.Call(uintptr(unsafe.Pointer(agent)), accessTypeNoProxy, 0, 0, 0)
	if h == 0 {
		return autoAnswer{}, fmt.Errorf("WinHttpOpen: %w", err)
	}
	defer procWinHttpCloseHandle.Call(h)
	ms := uintptr(scriptTimeout / time.Millisecond)
	_, _, _ = procWinHttpSetTimeouts.Call(h, ms, ms, ms, ms)

	var info proxyInfo
	r, _, err := procWinHttpGetProxyForUrl.Call(h, uintptr(unsafe.Pointer(targetW)), uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&info)))
	if r == 0 && errors.Is(err, errLoginFailure) {
		// Microsoft's documented order: ask without the user's
		// credentials first, and offer them only to a script server
		// that asks for them.
		opts.autoLogonIfChallenged = 1
		r, _, err = procWinHttpGetProxyForUrl.Call(h, uintptr(unsafe.Pointer(targetW)), uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&info)))
	}
	if r == 0 {
		return autoAnswer{}, winhttpError(err)
	}
	defer globalFree(info.proxy)
	defer globalFree(info.proxyBypass)
	if info.accessType == accessTypeNoProxy {
		return autoAnswer{Direct: true}, nil
	}
	return autoAnswer{
		Proxy:  windows.UTF16PtrToString(info.proxy),
		Bypass: windows.UTF16PtrToString(info.proxyBypass),
	}, nil
}
