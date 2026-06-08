// Package guac is a minimal Apache Guacamole protocol proxy. It connects to a
// running guacd daemon (TCP, default :4822), performs the Guacamole connection
// handshake for a remote-desktop protocol (vnc | rdp | ssh), and then tunnels the
// raw Guacamole instruction stream between guacd and a caller (the browser, over a
// websocket). This is what powers UniHV's in-app interactive VM console:
//
//	browser  <--websocket-->  UniHV  <--this proxy / TCP-->  guacd  <--vnc/rdp-->  VM
//
// guacamole-common-js in the browser speaks the same instruction stream, so the
// UniHV websocket handler just relays bytes in both directions after Connect()
// returns the established guacd connection.
//
// The Guacamole protocol is text: each instruction is a comma-separated list of
// elements, each element encoded as "<decimal length>.<value>", terminated by a
// semicolon. Example: "6.select,3.vnc;". See the Guacamole protocol reference.
package guac

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Params describes the remote connection guacd should open to the VM.
type Params struct {
	Protocol string // "vnc" | "rdp" | "ssh"
	Hostname string
	Port     int
	Password string // VNC password / RDP password (optional)
	Username string // RDP username (optional)
	// Width/Height/DPI are the initial display size negotiated in the handshake.
	Width  int
	Height int
	DPI    int
	// Extra protocol params (e.g. rdp "security"=any, "ignore-cert"=true).
	Extra map[string]string
}

// Conn is an established guacd connection ready to tunnel. Read/Write move raw
// Guacamole instruction bytes to/from guacd. Close releases the socket.
type Conn struct {
	tcp net.Conn
	br  *bufio.Reader
}

// Dial connects to guacd and performs the full handshake for p, returning a Conn
// whose Read/Write tunnel the instruction stream. guacdAddr is host:port (e.g.
// "guacd:4822"). On any handshake error the socket is closed.
func Dial(guacdAddr string, p Params) (*Conn, error) {
	if p.Width == 0 {
		p.Width = 1024
	}
	if p.Height == 0 {
		p.Height = 768
	}
	if p.DPI == 0 {
		p.DPI = 96
	}
	tcp, err := net.DialTimeout("tcp", guacdAddr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("guac: dial guacd %s: %w", guacdAddr, err)
	}
	c := &Conn{tcp: tcp, br: bufio.NewReader(tcp)}
	if err := c.handshake(p); err != nil {
		_ = tcp.Close()
		return nil, err
	}
	return c, nil
}

// handshake runs the guacd connection handshake (Guacamole protocol §"Handshake").
//
//	-> select,<protocol>
//	<- args,<arg1>,<arg2>,...        (the protocol's supported parameter names)
//	-> size,<w>,<h>,<dpi>
//	-> audio,  -> video,  -> image   (supported client mimetypes; we send empty)
//	-> connect,<value for each arg in the received args order>
//	<- ready,<connection id>
func (c *Conn) handshake(p Params) error {
	if err := c.writeInstr("select", p.Protocol); err != nil {
		return err
	}
	// Read the args instruction announcing the parameter order guacd expects.
	name, args, err := c.readInstr()
	if err != nil {
		return fmt.Errorf("guac: read args: %w", err)
	}
	if name != "args" {
		return fmt.Errorf("guac: expected 'args', got %q", name)
	}
	// args[0] is the protocol version (e.g. "VERSION_1_5_0"); the rest are param names.
	if err := c.writeInstr("size", itoa(p.Width), itoa(p.Height), itoa(p.DPI)); err != nil {
		return err
	}
	if err := c.writeInstr("audio"); err != nil {
		return err
	}
	if err := c.writeInstr("video"); err != nil {
		return err
	}
	if err := c.writeInstr("image"); err != nil {
		return err
	}

	// Build the connect args. The received instruction is:
	//   args,<elem1>,<elem2>,...      (elems[0]=="args" is the opcode)
	// The 'connect' instruction MUST send EXACTLY ONE value per element of `args`
	// EXCLUDING the opcode (guacd validates the count: "Client did not return the
	// expected number of arguments" otherwise). For each such element:
	//   - if it looks like a protocol version token (VERSION_x_y_z), echo our
	//     supported version back in that slot;
	//   - otherwise it is a parameter name -> send the value from Params (or "").
	argElems := args[1:] // drop the "args" opcode; keep version slot + all params
	values := make([]string, 0, len(argElems))
	for _, el := range argElems {
		if strings.HasPrefix(el, "VERSION_") {
			values = append(values, el) // echo the negotiated version in its slot
			continue
		}
		values = append(values, p.value(el))
	}
	if err := c.writeInstr2("connect", values); err != nil {
		return err
	}
	// Expect 'ready,<id>'.
	rname, _, err := c.readInstr()
	if err != nil {
		return fmt.Errorf("guac: read ready: %w", err)
	}
	if rname != "ready" {
		return fmt.Errorf("guac: expected 'ready', got %q", rname)
	}
	return nil
}

// value maps a guacd parameter name to the value from Params.
func (p Params) value(arg string) string {
	switch arg {
	case "hostname":
		return p.Hostname
	case "port":
		return itoa(p.Port)
	case "password":
		return p.Password
	case "username":
		return p.Username
	case "width":
		return itoa(p.Width)
	case "height":
		return itoa(p.Height)
	case "dpi":
		return itoa(p.DPI)
	case "ignore-cert", "disable-auth":
		// RDP against self-signed / lab hosts: don't fail on cert, allow NLA off.
		if p.Protocol == "rdp" {
			return "true"
		}
	case "security":
		if p.Protocol == "rdp" {
			return "any"
		}
	case "resize-method":
		return "display-update"
	}
	if p.Extra != nil {
		if v, ok := p.Extra[arg]; ok {
			return v
		}
	}
	return ""
}

// Read tunnels instruction bytes FROM guacd (server->client).
func (c *Conn) Read(b []byte) (int, error) { return c.br.Read(b) }

// Write tunnels instruction bytes TO guacd (client->server).
func (c *Conn) Write(b []byte) (int, error) { return c.tcp.Write(b) }

// Close closes the guacd socket.
func (c *Conn) Close() error { return c.tcp.Close() }

// --- Guacamole wire encoding ---

// writeInstr writes one instruction from string elements.
func (c *Conn) writeInstr(elems ...string) error { return c.writeInstr2(elems[0], elems[1:]) }

// writeInstr2 writes an instruction given an opcode and its remaining elements.
func (c *Conn) writeInstr2(op string, rest []string) error {
	var sb strings.Builder
	writeElem(&sb, op)
	for _, e := range rest {
		sb.WriteByte(',')
		writeElem(&sb, e)
	}
	sb.WriteByte(';')
	_, err := c.tcp.Write([]byte(sb.String()))
	return err
}

// writeElem writes "<len>.<value>" where len is the UTF-8 rune count.
func writeElem(sb *strings.Builder, v string) {
	sb.WriteString(strconv.Itoa(len([]rune(v))))
	sb.WriteByte('.')
	sb.WriteString(v)
}

// readInstr reads and parses one instruction into (opcode, allElements).
// allElements[0] == opcode. Parsing follows "<len>.<value>" elements separated by
// ',' and terminated by ';'.
func (c *Conn) readInstr() (op string, elems []string, err error) {
	for {
		// Read a length prefix (digits until '.').
		lenStr, err := c.readUntil('.')
		if err != nil {
			return "", nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(lenStr))
		if err != nil {
			return "", nil, fmt.Errorf("guac: bad element length %q: %w", lenStr, err)
		}
		// Read exactly n runes.
		val, err := c.readRunes(n)
		if err != nil {
			return "", nil, err
		}
		elems = append(elems, val)
		// Next byte is ',' (more elements) or ';' (end of instruction).
		sep, err := c.br.ReadByte()
		if err != nil {
			return "", nil, err
		}
		if sep == ';' {
			break
		}
		if sep != ',' {
			return "", nil, fmt.Errorf("guac: bad separator %q", sep)
		}
	}
	if len(elems) == 0 {
		return "", nil, fmt.Errorf("guac: empty instruction")
	}
	return elems[0], elems, nil
}

// readUntil reads bytes until (and excluding) delim.
func (c *Conn) readUntil(delim byte) (string, error) {
	s, err := c.br.ReadString(delim)
	if err != nil {
		return "", err
	}
	return s[:len(s)-1], nil // drop the delimiter
}

// readRunes reads exactly n UTF-8 runes.
func (c *Conn) readRunes(n int) (string, error) {
	if n == 0 {
		return "", nil
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		r, _, err := c.br.ReadRune()
		if err != nil {
			return "", err
		}
		sb.WriteRune(r)
	}
	return sb.String(), nil
}

func itoa(i int) string { return strconv.Itoa(i) }
