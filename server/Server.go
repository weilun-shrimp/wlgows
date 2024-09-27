package server

import (
	"net"

	"github.com/weilun-shrimp/wlgows/connection"
)

func Run(service string) (*Server, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp4", service)
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return nil, err
	}
	return &Server{
		TCPAddr:     tcpAddr,
		TCPListener: listener,
	}, nil
}

type Server struct {
	TCPAddr     *net.TCPAddr
	TCPListener *net.TCPListener
}

func (server *Server) Close() {
	server.TCPListener.Close()
}

func (server *Server) Accept() (*connection.ServerConn, error) {
	TCPConn, err := server.TCPListener.Accept()
	if err != nil {
		return nil, err
	}
	return &connection.ServerConn{Conn: connection.Conn{Conn: TCPConn}}, nil
}
