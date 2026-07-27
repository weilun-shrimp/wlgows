package wlgows

import (
	"net"
	"net/http"
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
	di          serverDI
}

type serverDI struct {
	newServerConn func(c net.Conn, req *http.Request) *ServerConn
}

func newServer(addr *net.TCPAddr, listener *net.TCPListener) *Server {
	return &Server{
		TCPAddr:     addr,
		TCPListener: listener,
		di: serverDI{
			newServerConn: NewServerConn,
		},
	}
}

func (server *Server) Close() {
	server.TCPListener.Close()
}

func (server *Server) Accept() (*ServerConn, error) {
	TCPConn, err := server.TCPListener.Accept()
	if err != nil {
		return nil, err
	}
	return server.di.newServerConn(TCPConn, nil), nil
}
