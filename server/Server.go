package server

import (
	"net"

	"github.com/weilun-shrimp/wlgows/connection"
)

func Run(service string) (*Server, error) {
	s := new(Server)
	tcpAddr, err := net.ResolveTCPAddr("tcp4", service)
	if err != nil {
		return s, err
	}
	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return s, err
	}
	s.TCPAddr = tcpAddr
	s.TCPListener = listener
	return s, nil
}

type Server struct {
	TCPAddr     *net.TCPAddr
	TCPListener *net.TCPListener
}

func (server *Server) Close() {
	server.TCPListener.Close()
}

func (server *Server) Accept() (*connection.Conn, error) {
	conn := new(connection.Conn)
	TCPConn, err := server.TCPListener.Accept()
	if err != nil {
		return conn, err
	}
	conn.TCP_connection = TCPConn
	return conn, nil
}
