package wlgows

import (
	"net"
)

func Run(service string) (*Server, error) {
	return run(service, runDI{
		netResolveTCPAddr: net.ResolveTCPAddr,
		netListenTCP:      net.ListenTCP,
		newServer:         newServer,
	})
}

type runDI struct {
	netResolveTCPAddr func(network string, address string) (*net.TCPAddr, error)
	netListenTCP      func(network string, laddr *net.TCPAddr) (*net.TCPListener, error)
	newServer         func(addr *net.TCPAddr, listener *net.TCPListener) *Server
}

func run(service string, di runDI) (*Server, error) {
	tcpAddr, err := di.netResolveTCPAddr("tcp4", service)
	if err != nil {
		return nil, err
	}
	listener, err := di.netListenTCP("tcp", tcpAddr)
	if err != nil {
		return nil, err
	}
	return di.newServer(tcpAddr, listener), nil
}

type Server struct {
	TCPAddr     *net.TCPAddr
	TCPListener *net.TCPListener
}

func newServer(addr *net.TCPAddr, listener *net.TCPListener) *Server {
	return &Server{
		TCPAddr:     addr,
		TCPListener: listener,
	}
}

func (server *Server) Close() {
	server.TCPListener.Close()
}

/*
Accept takes the next raw TCP connection — nothing more. It does not read a
request or run the handshake, so you're free to do both yourself:

	netConn, err := server.Accept()
	r := bufio.NewReader(netConn)
	req, err := http.ReadRequest(r)
	res, err := wlgows.ServerHandShake(netConn, req)
	conn := wlgows.NewConn(netConn, r, false)

r must be the same *bufio.Reader you pass to both http.ReadRequest and
NewConn — see ClientHandShake's doc comment for why the same reasoning
applies here.
*/
func (server *Server) Accept() (net.Conn, error) {
	return server.TCPListener.Accept()
}
