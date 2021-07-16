// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package node

import (
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/stretchr/testify/assert"
)

var (
	testNodeKey, _ = crypto.GenerateKey()
)

func testNodeConfig() *Config {
	return &Config{
		Name: "test node",
		P2P:  p2p.Config{PrivateKey: testNodeKey},
	}
}

// Tests that an empty protocol stack can be closed more than once.
func TestNodeCloseMultipleTimes(t *testing.T) {
	stack, err := New(testNodeConfig())
	require.Nil(t, err, "failed to create protocol stack")
	stack.Close()

	// Ensure that a stopped node can be stopped again
	for i := 0; i < 3; i++ {
		require.Equal(t, stack.Close(), ErrNodeStopped, "iter %d: stop failure mismatch", i)
	}
}

func TestNodeStartMultipleTimes(t *testing.T) {
	stack, err := New(testNodeConfig())
	require.Nil(t, err, "failed to create protocol stack")

	// Ensure that a node can be successfully started, but only once
	require.Nil(t, stack.Start(), "failed to start node")
	require.Equal(t, stack.Start(), ErrNodeRunning, "start failure mismatch")

	// Ensure that a node can be stopped, but only once
	require.Nil(t, stack.Close(), "failed to stop node")
	require.Equal(t, stack.Close(), ErrNodeStopped, "stop failure mismatch")
}

// Tests that if the data dir is already in use, an appropriate error is returned.
func TestNodeUsedDataDir(t *testing.T) {
	// Create a temporary folder to use as the data directory
	dir, err := ioutil.TempDir("", "")
	require.Nil(t, err, "failed to create temporary data directory")
	defer os.RemoveAll(dir)

	// Create a new node based on the data directory
	original, err := New(&Config{DataDir: dir})
	require.Nil(t, err, "failed to create original protocol stack")
	defer original.Close()
	require.Nil(t, original.Start(), "failed to start original protocol stack")

	// Create a second node based on the same data directory and ensure failure
	_, err = New(&Config{DataDir: dir})
	require.Equal(t, err, ErrDatadirUsed, "duplicate datadir failure mismatch")
}

// Tests whether a Lifecycle can be registered.
func TestLifecycleRegistry_Successful(t *testing.T) {
	stack, err := New(testNodeConfig())
	require.Nil(t, err, "failed to create protocol stack")
	defer stack.Close()

	noop := NewNoop()
	stack.RegisterLifecycle(noop)

	require.True(t, containsLifecycle(stack.lifecycles, noop), "lifecycle was not properly registered on the node")
}

// Tests whether a service's protocols can be registered properly on the node's p2p server.
func TestRegisterProtocols(t *testing.T) {
	stack, err := New(testNodeConfig())
	require.Nil(t, err, "failed to create protocol stack")
	defer stack.Close()

	fs, err := NewFullService(stack)
	require.Nil(t, err, "could not create full service")

	// codereview: p2p protocol as plugin
	for _, protocol := range fs.Protocols() {
		require.True(t, containsProtocol(stack.server.Protocols, protocol), "protocol %v was not successfully registered", protocol)
	}

	// codereview: rpc api as plugin
	for _, api := range fs.APIs() {
		require.True(t, containsAPI(stack.rpcAPIs, api), "api %v was not successfully registered", api)
	}
}

// This test checks that open databases are closed with node.
func TestNodeCloseClosesDB(t *testing.T) {
	stack, _ := New(testNodeConfig())
	defer stack.Close()

	db, err := stack.OpenDatabase("mydb", 0, 0, "", false)
	require.Nil(t, err, "can't open DB")
	require.Nil(t, db.Put([]byte{}, []byte{}), "can't Put on open DB")

	stack.Close()
	require.NotNil(t, db.Put([]byte{}, []byte{}), "Put succeeded after node is closed")
}

func TestNodeOpenLevelDB(t *testing.T) {
	// Create a temporary folder to use as the data directory
	dir, err := ioutil.TempDir("", "")
	require.Nil(t, err, "failed to create temporary data directory")
	defer os.RemoveAll(dir)

	// Create a new node based on the data directory
	stack, err := New(&Config{DataDir: dir})
	require.Nil(t, err)
	defer stack.Close()

	db, err := stack.OpenDatabase("mydb", 0, 0, "", false)
	require.Nil(t, err, "can't open DB")
	require.Nil(t, db.Put([]byte("hello"), []byte("world")), "can't Put on open DB")
}

// This test checks that OpenDatabase can be used from within a Lifecycle Start method.
func TestNodeOpenDatabaseFromLifecycleStart(t *testing.T) {
	stack, _ := New(testNodeConfig())
	defer stack.Close()

	var db ethdb.Database
	var err error
	stack.RegisterLifecycle(&InstrumentedService{
		startHook: func() {
			db, err = stack.OpenDatabase("mydb", 0, 0, "", false)
			if err != nil {
				t.Fatal("can't open DB:", err)
			}
		},
		stopHook: func() {
			db.Close()
		},
	})

	// codereview: node start, lifecycles start, node close, lifecycles stop
	stack.Start()
	stack.Close()
}

// This test checks that OpenDatabase can be used from within a Lifecycle Stop method.
func TestNodeOpenDatabaseFromLifecycleStop(t *testing.T) {
	stack, _ := New(testNodeConfig())
	defer stack.Close()

	stack.RegisterLifecycle(&InstrumentedService{
		stopHook: func() {
			db, err := stack.OpenDatabase("mydb", 0, 0, "", false)
			if err != nil {
				t.Fatal("can't open DB:", err)
			}
			db.Close()
		},
	})

	stack.Start()
	stack.Close()
}

// Tests that registered Lifecycles get started and stopped correctly.
func TestLifecycleLifeCycle(t *testing.T) {
	stack, _ := New(testNodeConfig())
	defer stack.Close()

	started := make(map[string]bool)
	stopped := make(map[string]bool)

	// Create a batch of instrumented services
	lifecycles := map[string]Lifecycle{
		"A": &InstrumentedService{
			startHook: func() { started["A"] = true },
			stopHook:  func() { stopped["A"] = true },
		},
		"B": &InstrumentedService{
			startHook: func() { started["B"] = true },
			stopHook:  func() { stopped["B"] = true },
		},
		"C": &InstrumentedService{
			startHook: func() { started["C"] = true },
			stopHook:  func() { stopped["C"] = true },
		},
	}
	// register lifecycles on node
	for _, lifecycle := range lifecycles {
		stack.RegisterLifecycle(lifecycle)
	}
	// Start the node and check that all services are running
	require.Nil(t, stack.Start(), "failed to start protocol stack")
	for id := range lifecycles {
		require.True(t, started[id], "service %s: freshly started service not running", id)
		require.False(t, stopped[id], "service %s: freshly started service already stopped", id)
	}
	// Stop the node and check that all services have been stopped
	require.Nil(t, stack.Close(), "failed to stop protocol stack")
	for id := range lifecycles {
		require.True(t, stopped[id], "service %s: freshly terminated service still running", id)
	}
}

// Tests that if a Lifecycle fails to start, all others started before it will be
// shut down.
func TestLifecycleStartupError(t *testing.T) {
	stack, err := New(testNodeConfig())
	if err != nil {
		t.Fatalf("failed to create protocol stack: %v", err)
	}
	defer stack.Close()

	started := make(map[string]bool)
	stopped := make(map[string]bool)

	// Create a batch of instrumented services
	lifecycles := map[string]Lifecycle{
		"A": &InstrumentedService{
			startHook: func() { started["A"] = true },
			stopHook:  func() { stopped["A"] = true },
		},
		"B": &InstrumentedService{
			startHook: func() { started["B"] = true },
			stopHook:  func() { stopped["B"] = true },
		},
		"C": &InstrumentedService{
			startHook: func() { started["C"] = true },
			stopHook:  func() { stopped["C"] = true },
		},
	}
	// register lifecycles on node
	for _, lifecycle := range lifecycles {
		stack.RegisterLifecycle(lifecycle)
	}

	// Register a service that fails to construct itself
	failure := errors.New("fail")
	failer := &InstrumentedService{start: failure}
	stack.RegisterLifecycle(failer)

	// Start the protocol stack and ensure all started services stop
	if err := stack.Start(); err != failure {
		t.Fatalf("stack startup failure mismatch: have %v, want %v", err, failure)
	}
	for id := range lifecycles {
		if started[id] && !stopped[id] {
			t.Fatalf("service %s: started but not stopped", id)
		}
		delete(started, id)
		delete(stopped, id)
	}
}

// Tests that even if a registered Lifecycle fails to shut down cleanly, it does
// not influence the rest of the shutdown invocations.
func TestLifecycleTerminationGuarantee(t *testing.T) {
	stack, err := New(testNodeConfig())
	require.Nil(t, err, "failed to create protocol stack")
	defer stack.Close()

	started := make(map[string]bool)
	stopped := make(map[string]bool)

	// Create a batch of instrumented services
	lifecycles := map[string]Lifecycle{
		"A": &InstrumentedService{
			startHook: func() { started["A"] = true },
			stopHook:  func() { stopped["A"] = true },
		},
		"B": &InstrumentedService{
			startHook: func() { started["B"] = true },
			stopHook:  func() { stopped["B"] = true },
		},
		"C": &InstrumentedService{
			startHook: func() { started["C"] = true },
			stopHook:  func() { stopped["C"] = true },
		},
	}
	// register lifecycles on node
	for _, lifecycle := range lifecycles {
		stack.RegisterLifecycle(lifecycle)
	}

	// Register a service that fails to shot down cleanly
	failure := errors.New("fail")
	failer := &InstrumentedService{stop: failure}
	stack.RegisterLifecycle(failer)

	// Start the protocol stack, and ensure that a failing shut down terminates all
	// Start the stack and make sure all is online
	if err := stack.Start(); err != nil {
		t.Fatalf("failed to start protocol stack: %v", err)
	}
	for id := range lifecycles {
		if !started[id] {
			t.Fatalf("service %s: service not running", id)
		}
		if stopped[id] {
			t.Fatalf("service %s: service already stopped", id)
		}
	}
	// Stop the stack, verify failure and check all terminations
	err = stack.Close()
	if err, ok := err.(*StopError); !ok {
		t.Fatalf("termination failure mismatch: have %v, want StopError", err)
	} else {
		failer := reflect.TypeOf(&InstrumentedService{})
		if err.Services[failer] != failure {
			t.Fatalf("failer termination failure mismatch: have %v, want %v", err.Services[failer], failure)
		}
		if len(err.Services) != 1 {
			t.Fatalf("failure count mismatch: have %d, want %d", len(err.Services), 1)
		}
	}
	for id := range lifecycles {
		if !stopped[id] {
			t.Fatalf("service %s: service not terminated", id)
		}
		delete(started, id)
		delete(stopped, id)
	}

	// TODO: for what?
	stack.server = &p2p.Server{}
	stack.server.PrivateKey = testNodeKey
}

// Tests whether a handler can be successfully mounted on the canonical HTTP server
// on the given prefix
func TestRegisterHandler_Successful(t *testing.T) {
	node := createNode(t, 7878, 7979)

	// create and mount handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("success"))
	})
	node.RegisterHandler("test", "/test", handler)

	// start node
	require.Nil(t, node.Start(), "could not start node")

	// create HTTP request
	httpReq, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:7878/test", nil)
	require.Nil(t, err, "could not issue new http request")

	// check response
	resp := doHTTPRequest(t, httpReq)
	buf := make([]byte, 7)
	_, err = io.ReadFull(resp.Body, buf)
	require.Nil(t, err, "could not read response")
	assert.Equal(t, "success", string(buf))
}

// Tests that the given handler will not be successfully mounted since no HTTP server
// is enabled for RPC
func TestRegisterHandler_Unsuccessful(t *testing.T) {
	node, err := New(&DefaultConfig)
	if err != nil {
		t.Fatalf("could not create new node: %v", err)
	}

	// create and mount handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("success"))
	})
	node.RegisterHandler("test", "/test", handler)
	// codereview: you do not know http port
}

// Tests whether websocket requests can be handled on the same port as a regular http server.
func TestWebsocketHTTPOnSamePort_WebsocketRequest(t *testing.T) {
	node := startHTTP(t, 0, 0)
	defer node.Close()

	ws := strings.Replace(node.HTTPEndpoint(), "http://", "ws://", 1)

	if node.WSEndpoint() != ws {
		t.Fatalf("endpoints should be the same")
	}
	if !checkRPC(ws) {
		t.Fatalf("ws request failed")
	}
	if !checkRPC(node.HTTPEndpoint()) {
		t.Fatalf("http request failed")
	}
}

func TestWebsocketHTTPOnSeparatePort_WSRequest(t *testing.T) {
	// try and get a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.Nil(t, err, "can't listen")
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	node := startHTTP(t, 0, port)
	defer node.Close()

	wsOnHTTP := strings.Replace(node.HTTPEndpoint(), "http://", "ws://", 1)
	ws := fmt.Sprintf("ws://127.0.0.1:%d", port)

	if node.WSEndpoint() == wsOnHTTP {
		t.Fatalf("endpoints should not be the same")
	}
	// ensure ws endpoint matches the expected endpoint
	if node.WSEndpoint() != ws {
		t.Fatalf("ws endpoint is incorrect: expected %s, got %s", ws, node.WSEndpoint())
	}

	if !checkRPC(ws) {
		t.Fatalf("ws request failed")
	}
	if !checkRPC(node.HTTPEndpoint()) {
		t.Fatalf("http request failed")
	}
}

type rpcPrefixTest struct {
	httpPrefix, wsPrefix string
	// These lists paths on which JSON-RPC should be served / not served.
	wantHTTP   []string
	wantNoHTTP []string
	wantWS     []string
	wantNoWS   []string
}

func TestNodeRPCPrefix(t *testing.T) {
	t.Parallel()

	tests := []rpcPrefixTest{
		// both off
		{
			httpPrefix: "", wsPrefix: "",
			wantHTTP:   []string{"/", "/?p=1"},
			wantNoHTTP: []string{"/test", "/test?p=1"},
			wantWS:     []string{"/", "/?p=1"},
			wantNoWS:   []string{"/test", "/test?p=1"},
		},
		// only http prefix
		{
			httpPrefix: "/testprefix", wsPrefix: "",
			wantHTTP:   []string{"/testprefix", "/testprefix?p=1", "/testprefix/x", "/testprefix/x?p=1"},
			wantNoHTTP: []string{"/", "/?p=1", "/test", "/test?p=1"},
			wantWS:     []string{"/", "/?p=1"},
			wantNoWS:   []string{"/testprefix", "/testprefix?p=1", "/test", "/test?p=1"},
		},
		// only ws prefix
		{
			httpPrefix: "", wsPrefix: "/testprefix",
			wantHTTP:   []string{"/", "/?p=1"},
			wantNoHTTP: []string{"/testprefix", "/testprefix?p=1", "/test", "/test?p=1"},
			wantWS:     []string{"/testprefix", "/testprefix?p=1", "/testprefix/x", "/testprefix/x?p=1"},
			wantNoWS:   []string{"/", "/?p=1", "/test", "/test?p=1"},
		},
		// both set
		{
			httpPrefix: "/testprefix", wsPrefix: "/testprefix",
			wantHTTP:   []string{"/testprefix", "/testprefix?p=1", "/testprefix/x", "/testprefix/x?p=1"},
			wantNoHTTP: []string{"/", "/?p=1", "/test", "/test?p=1"},
			wantWS:     []string{"/testprefix", "/testprefix?p=1", "/testprefix/x", "/testprefix/x?p=1"},
			wantNoWS:   []string{"/", "/?p=1", "/test", "/test?p=1"},
		},
	}

	for _, test := range tests {
		test := test
		name := fmt.Sprintf("http=%s ws=%s", test.httpPrefix, test.wsPrefix)
		t.Run(name, func(t *testing.T) {
			cfg := &Config{
				HTTPHost:       "127.0.0.1",
				HTTPPathPrefix: test.httpPrefix,
				WSHost:         "127.0.0.1",
				WSPathPrefix:   test.wsPrefix,
			}
			node, err := New(cfg)
			if err != nil {
				t.Fatal("can't create node:", err)
			}
			defer node.Close()
			if err := node.Start(); err != nil {
				t.Fatal("can't start node:", err)
			}
			test.check(t, node)
		})
	}
}

func (test rpcPrefixTest) check(t *testing.T, node *Node) {
	t.Helper()
	httpBase := "http://" + node.http.listenAddr()
	wsBase := "ws://" + node.http.listenAddr()

	if node.WSEndpoint() != wsBase+test.wsPrefix {
		t.Errorf("Error: node has wrong WSEndpoint %q", node.WSEndpoint())
	}

	for _, path := range test.wantHTTP {
		resp := rpcRequest(t, httpBase+path)
		if resp.StatusCode != 200 {
			t.Errorf("Error: %s: bad status code %d, want 200", path, resp.StatusCode)
		}
	}
	for _, path := range test.wantNoHTTP {
		resp := rpcRequest(t, httpBase+path)
		if resp.StatusCode != 404 {
			t.Errorf("Error: %s: bad status code %d, want 404", path, resp.StatusCode)
		}
	}
	for _, path := range test.wantWS {
		err := wsRequest(t, wsBase+path, "")
		if err != nil {
			t.Errorf("Error: %s: WebSocket connection failed: %v", path, err)
		}
	}
	for _, path := range test.wantNoWS {
		err := wsRequest(t, wsBase+path, "")
		if err == nil {
			t.Errorf("Error: %s: WebSocket connection succeeded for path in wantNoWS", path)
		}

	}
}

func createNode(t *testing.T, httpPort, wsPort int) *Node {
	conf := &Config{
		HTTPHost: "127.0.0.1",
		HTTPPort: httpPort,
		WSHost:   "127.0.0.1",
		WSPort:   wsPort,
	}
	node, err := New(conf)
	require.Nil(t, err, "could not create a new node")
	return node
}

func startHTTP(t *testing.T, httpPort, wsPort int) *Node {
	node := createNode(t, httpPort, wsPort)
	require.Nil(t, node.Start(), "could not start http service on node")

	return node
}

func doHTTPRequest(t *testing.T, req *http.Request) *http.Response {
	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("could not issue a GET request to the given endpoint: %v", err)

	}
	return resp
}

func containsProtocol(stackProtocols []p2p.Protocol, protocol p2p.Protocol) bool {
	for _, a := range stackProtocols {
		if reflect.DeepEqual(a, protocol) {
			return true
		}
	}
	return false
}

func containsAPI(stackAPIs []rpc.API, api rpc.API) bool {
	for _, a := range stackAPIs {
		if reflect.DeepEqual(a, api) {
			return true
		}
	}
	return false
}
