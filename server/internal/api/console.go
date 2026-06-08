package api

import (
	"context"
	"io"
	"strconv"
	"strings"

	"net/http"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/guac"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// VMConsoleWS is the integrated interactive console: it bridges the browser
// (guacamole-common-js over this websocket) to guacd, which in turn speaks VNC
// (KVM/ESXi/Xen) or RDP (Hyper-V) to the VM. Flow:
//
//	browser <--ws--> [this handler] <--TCP--> guacd <--vnc/rdp--> VM
//
// The handler resolves the VM's ConsoleEndpoint from its provider, dials guacd
// with the right protocol+params, then relays the Guacamole instruction stream in
// both directions until either side closes. Gated by vm.console on the route.
func (s *Server) VMConsoleWS(w http.ResponseWriter, r *http.Request) {
	user := authz.UserFrom(r)
	if user == nil {
		authz.WriteError(w, r, authz.ErrUnauthenticated)
		return
	}
	if !s.authz.CheckOrigin(r) {
		authz.WriteError(w, r, authz.Errorf(authz.ErrCSRFFailed, "Origin not allowed for WebSocket."))
		return
	}
	if s.cfg.GuacdAddr == "" {
		authz.WriteError(w, r, authz.Errorf(authz.ErrMethodNotAllowed, "Integrated console is not enabled (no guacd)."))
		return
	}

	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	cp, impl := p.(vprovider.ConsoleProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapConsole) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ep, err := cp.Console(r.Context(), chi.URLParam(r, "vmID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}

	params, perr := guacParamsFor(ep)
	if perr != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, perr.Error()))
		return
	}
	// Allow the browser to request an initial size via query (?w=&h=&dpi=).
	if v, e := strconv.Atoi(r.URL.Query().Get("w")); e == nil && v > 0 {
		params.Width = v
	}
	if v, e := strconv.Atoi(r.URL.Query().Get("h")); e == nil && v > 0 {
		params.Height = v
	}
	if v, e := strconv.Atoi(r.URL.Query().Get("dpi")); e == nil && v > 0 {
		params.DPI = v
	}

	// Dial guacd + handshake BEFORE upgrading, so a failure is a clean HTTP error.
	gconn, err := guac.Dial(s.cfg.GuacdAddr, params)
	if err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrInternal, "console backend unavailable: "+err.Error()))
		return
	}
	defer gconn.Close()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: originPatterns(s.cfg.AllowedOrigins),
		// guacamole-common-js uses the "guacamole" subprotocol over the tunnel.
		Subprotocols: []string{"guacamole"},
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// guacd -> browser: read guac instruction bytes, write as ws text messages.
	go func() {
		defer cancel()
		buf := make([]byte, 8192)
		for {
			n, rerr := gconn.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageText, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// browser -> guacd: read ws messages, write to guacd.
	for {
		typ, data, rerr := conn.Read(ctx)
		if rerr != nil {
			break
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}
		if _, werr := gconn.Write(data); werr != nil {
			break
		}
	}
	cancel()
	_ = io.Discard
}

// guacParamsFor maps a normalized ConsoleEndpoint to guac.Params (protocol + host/
// port/credentials). VNC/SPICE -> "vnc" (guacd's vnc client also drives SPICE-less
// VNC; SPICE proper would need a spice client — we surface VNC which libvirt also
// exposes); RDP -> "rdp".
func guacParamsFor(ep *vprovider.ConsoleEndpoint) (guac.Params, error) {
	host := ep.Host
	// A wildcard/zero listen address is not dialable; fall back to loopback only if
	// nothing better is provided (the provider should give a reachable host).
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	switch ep.Kind {
	case vprovider.ConsoleVNC, vprovider.ConsoleSPICE:
		return guac.Params{
			Protocol: "vnc", Hostname: host, Port: ep.Port, Password: ep.Password,
		}, nil
	case vprovider.ConsoleRDP:
		port := ep.Port
		if port == 0 {
			port = 3389
		}
		return guac.Params{
			Protocol: "rdp", Hostname: host, Port: port, Password: ep.Password,
			Extra: map[string]string{"ignore-cert": "true", "security": "any", "resize-method": "display-update"},
		}, nil
	default:
		return guac.Params{}, errUnknownConsole(string(ep.Kind))
	}
}

type consoleErr string

func (e consoleErr) Error() string { return string(e) }
func errUnknownConsole(k string) error {
	return consoleErr("unsupported console kind: " + strings.TrimSpace(k))
}
