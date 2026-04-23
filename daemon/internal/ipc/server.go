package ipc

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"sync"
)

// Handler is a function that processes a JSON-RPC method call.
// It receives raw params and returns a result or an error.
type Handler func(params *json.RawMessage) (interface{}, *RPCError)

// Server is a JSON-RPC 2.0 server over a Unix domain socket.
type Server struct {
	socketPath string
	listener   net.Listener
	handlers   map[string]Handler
	mu         sync.RWMutex
	done       chan struct{}
	wg         sync.WaitGroup
}

// NewServer creates a new IPC server bound to the given socket path.
func NewServer(socketPath string) *Server {
	return &Server{
		socketPath: socketPath,
		handlers:   make(map[string]Handler),
		done:       make(chan struct{}),
	}
}

// Register adds a handler for the given JSON-RPC method name.
func (s *Server) Register(method string, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = handler
}

// Start begins listening on the Unix domain socket and accepting connections.
func (s *Server) Start() error {
	// Remove stale socket file if it exists.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	s.listener = ln

	// Make the socket accessible.
	if err := os.Chmod(s.socketPath, 0660); err != nil {
		log.Printf("ipc: warning: chmod socket: %v", err)
	}

	s.wg.Add(1)
	go s.acceptLoop()
	log.Printf("ipc: listening on %s", s.socketPath)
	return nil
}

// Stop gracefully shuts down the server: stops accepting new connections
// and waits for in-flight requests to finish.
func (s *Server) Stop() {
	close(s.done)
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
	os.Remove(s.socketPath)
	log.Printf("ipc: server stopped")
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				log.Printf("ipc: accept error: %v", err)
				continue
			}
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			log.Printf("ipc: read error: %v", err)
			return
		}
		line = bytesTrimSpace(line)
		if len(line) == 0 {
			if err == io.EOF {
				return
			}
			continue
		}

		resp := s.processRequest(line)
		respBytes, err := json.Marshal(resp)
		if err != nil {
			log.Printf("ipc: marshal response error: %v", err)
			continue
		}
		respBytes = append(respBytes, '\n')
		if _, err := conn.Write(respBytes); err != nil {
			log.Printf("ipc: write error: %v", err)
			return
		}

		if err == io.EOF {
			return
		}
	}
}

func bytesTrimSpace(data []byte) []byte {
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	end := len(data)
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return data[start:end]
}

func (s *Server) processRequest(data []byte) *Response {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return NewErrorResponse(0, CodeParseError, "parse error: "+err.Error(), nil)
	}

	if rpcErr := req.Validate(); rpcErr != nil {
		return NewErrorResponse(req.ID, rpcErr.Code, rpcErr.Message, nil)
	}

	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		return NewErrorResponse(req.ID, CodeMethodNotFound,
			"method not found: "+req.Method, nil)
	}

	result, rpcErr := handler(req.Params)
	if rpcErr != nil {
		return NewErrorResponse(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
	}
	return NewResponse(req.ID, result)
}
