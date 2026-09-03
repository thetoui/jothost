package security

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// What is listening on this host, read from /proc/net.
//
// # Why not ss, or netstat, or lsof
//
// Because none of them has to be here. The kernel publishes the socket tables
// as text, and parsing them is fifty lines. Shelling out would mean allowlisting
// a program whose output format varies by version, on a host where the answer
// matters most when something is wrong with the host.
//
// # What makes this worth a scanner
//
// A database on 0.0.0.0 is the single most common way a hosting box is taken
// over, and it is invisible from every other page in this panel. The firewall
// page shows what is *blocked*; the services page shows what is *running*. Only
// this shows that MySQL is running, bound to every interface, and that the
// firewall in front of it has a rule somebody added last year.

// Socket is one listening socket.
type Socket struct {
	Protocol string `json:"protocol"`
	// Address is the local address the socket is bound to, as the kernel
	// reports it: "0.0.0.0", "127.0.0.1", "::", "::1", or a specific address.
	Address string `json:"address"`
	Port    int    `json:"port"`
	// Public reports whether the socket is bound to something other than
	// loopback. This is the field that matters: a service on 127.0.0.1 is
	// reachable only from the machine itself, and one on 0.0.0.0 is reachable
	// from wherever the network allows.
	Public bool `json:"public"`
	// UID owns the socket.
	UID int `json:"uid"`
	// Process and PID name what is holding it, when the mapping can be made.
	// Both are empty or zero when it cannot: a socket held by a process this
	// Agent may not read is still a listening socket, and reporting it without
	// a name is far better than not reporting it.
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// PortReport is everything the port scan found.
type PortReport struct {
	Sockets []Socket `json:"sockets"`
	// Public is how many of them are reachable from off the machine.
	Public int `json:"public"`
	// ProcessesResolved reports whether socket-to-process mapping worked. When
	// it is false the sockets are still accurate and their owners are unknown,
	// which is a different thing from "nothing owns them".
	ProcessesResolved bool   `json:"processes_resolved"`
	Reason            string `json:"reason,omitempty"`
}

// tcpListen is the kernel's state code for a socket in LISTEN.
const tcpListen = "0A"

// ListeningPorts returns every socket this host is listening on.
func (s *Scanner) ListeningPorts() (PortReport, error) {
	report := PortReport{Sockets: []Socket{}}

	files := []struct {
		name     string
		protocol string
		tcp      bool
	}{
		{"net/tcp", "tcp", true},
		{"net/tcp6", "tcp6", true},
		{"net/udp", "udp", false},
		{"net/udp6", "udp6", false},
	}

	found := false
	byInode := map[uint64]*Socket{}

	for _, file := range files {
		sockets, err := s.readSocketTable(file.name, file.protocol, file.tcp, byInode)
		if err != nil {
			// A host with IPv6 disabled has no net/tcp6, which is not an error
			// and must not stop the IPv4 answer.
			continue
		}
		found = true
		report.Sockets = append(report.Sockets, sockets...)
	}

	if !found {
		return PortReport{}, fmt.Errorf("%w: /proc/net is not readable", ErrUnsupported)
	}

	// The inode-to-process mapping is best effort and is reported as such.
	// Walking every process's file descriptors needs to read /proc/<pid>/fd,
	// which only root may do; an Agent that has been dropped to a lesser
	// account still reports the sockets, without names.
	resolved, reason := s.resolveProcesses(byInode)
	report.ProcessesResolved = resolved
	report.Reason = reason

	for i := range report.Sockets {
		if report.Sockets[i].Public {
			report.Public++
		}
	}

	sort.Slice(report.Sockets, func(i, j int) bool {
		if report.Sockets[i].Port != report.Sockets[j].Port {
			return report.Sockets[i].Port < report.Sockets[j].Port
		}
		return report.Sockets[i].Protocol < report.Sockets[j].Protocol
	})
	return report, nil
}

// readSocketTable parses one of the kernel's socket tables.
//
// The format is fixed-width-ish text with a header line:
//
//	sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode
//	 0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000  00000000     0        0 12345
//
// local_address is hex, little-endian per 32-bit word for IPv4, and four such
// words for IPv6. Getting that byte order wrong produces addresses that look
// plausible and are wrong, which is why it is a function with a test rather
// than three lines inline.
func (s *Scanner) readSocketTable(name, protocol string, tcp bool,
	byInode map[uint64]*Socket,
) ([]Socket, error) {
	path := filepath.Join(s.procRoot, filepath.FromSlash(name))
	handle, err := os.Open(path) //nolint:gosec // a path under the configured proc root
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()

	sockets := []Socket{}
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	first := true
	for scanner.Scan() {
		if first {
			first = false // the header
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}

		// A TCP socket is only listening in state 0A. A UDP socket has no
		// listen state — an unconnected UDP socket is bound and receiving, so
		// state 07 (CLOSE) is the ordinary state for a DNS server's socket.
		if tcp && fields[3] != tcpListen {
			continue
		}
		if !tcp && fields[3] != "07" {
			continue
		}

		address, port, err := parseSocketAddress(fields[1])
		if err != nil {
			continue
		}
		uid, err := strconv.Atoi(fields[7])
		if err != nil {
			uid = -1
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			inode = 0
		}

		socket := Socket{
			Protocol: protocol,
			Address:  address,
			Port:     port,
			Public:   isPublicAddress(address),
			UID:      uid,
		}
		sockets = append(sockets, socket)
		if inode != 0 {
			byInode[inode] = &sockets[len(sockets)-1]
		}
	}

	return sockets, scanner.Err()
}

// parseSocketAddress decodes a "HEXADDR:HEXPORT" pair from /proc/net.
func parseSocketAddress(field string) (string, int, error) {
	parts := strings.Split(field, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("malformed address %q", field)
	}

	port, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return "", 0, fmt.Errorf("malformed port in %q", field)
	}

	raw, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", 0, fmt.Errorf("malformed address in %q", field)
	}

	switch len(raw) {
	case 4:
		// One 32-bit word, host byte order — little-endian everywhere this
		// panel runs. So the bytes come out reversed from the dotted form.
		ip := net.IPv4(raw[3], raw[2], raw[1], raw[0])
		return ip.String(), int(port), nil
	case 16:
		// Four 32-bit words, each in host byte order. The words are in
		// network order relative to each other; only the bytes within a word
		// are reversed.
		ordered := make(net.IP, 16)
		for word := 0; word < 4; word++ {
			for b := 0; b < 4; b++ {
				ordered[word*4+b] = raw[word*4+3-b]
			}
		}
		return ordered.String(), int(port), nil
	default:
		return "", 0, fmt.Errorf("unexpected address length %d", len(raw))
	}
}

// isPublicAddress reports whether a bound address is reachable from off the
// machine.
//
// The unspecified address — 0.0.0.0 or :: — is the one that matters: it means
// every interface the host has now and every interface it gains later.
func isPublicAddress(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return false
	}
	return true
}

// resolveProcesses maps socket inodes to the processes holding them.
//
// It walks /proc/<pid>/fd and reads each link, which is "socket:[12345]" for a
// socket. That is the only way to make the mapping without netlink, and it is
// what ss and lsof do.
func (s *Scanner) resolveProcesses(byInode map[uint64]*Socket) (bool, string) {
	if len(byInode) == 0 {
		return true, ""
	}

	entries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return false, "the process list could not be read, so sockets are reported without owners"
	}

	denied := 0
	matched := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		fdDir := filepath.Join(s.procRoot, entry.Name(), "fd")
		descriptors, err := os.ReadDir(fdDir)
		if err != nil {
			if os.IsPermission(err) {
				denied++
			}
			// A process that exited while the directory was being walked is
			// normal and must not fail the scan.
			continue
		}

		name := s.processName(entry.Name())
		for _, descriptor := range descriptors {
			link, err := os.Readlink(filepath.Join(fdDir, descriptor.Name()))
			if err != nil {
				continue
			}
			inode, ok := socketInode(link)
			if !ok {
				continue
			}
			if socket, found := byInode[inode]; found && socket.PID == 0 {
				socket.PID = pid
				socket.Process = name
				matched++
			}
		}
	}

	if matched == 0 && denied > 0 {
		return false, "this agent may not read other processes, so sockets are reported without owners"
	}
	return true, ""
}

// socketInode extracts the inode from a "socket:[12345]" symlink target.
func socketInode(link string) (uint64, bool) {
	const prefix = "socket:["
	if !strings.HasPrefix(link, prefix) || !strings.HasSuffix(link, "]") {
		return 0, false
	}
	inode, err := strconv.ParseUint(link[len(prefix):len(link)-1], 10, 64)
	if err != nil {
		return 0, false
	}
	return inode, true
}

// processName reads a process's name from /proc/<pid>/comm.
//
// comm rather than cmdline: it is one short line, it is always there, and it
// cannot be rewritten by the process to something arbitrarily long. A name that
// ends up in a security finding is a name somebody reads, and cmdline is
// attacker-controlled text of unbounded length.
func (s *Scanner) processName(pid string) string {
	raw, err := os.ReadFile(filepath.Join(s.procRoot, pid, "comm")) //nolint:gosec // proc root
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(raw))
	if len(name) > 64 {
		name = name[:64]
	}
	// Control characters would corrupt a log line and a listing.
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
}
