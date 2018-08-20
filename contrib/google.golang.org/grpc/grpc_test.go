package grpc

import (
	"fmt"
	"net"
	"testing"
	"time"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/ext"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/mocktracer"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"

	"github.com/stretchr/testify/assert"
	context "golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

func TestClient(t *testing.T) {
	assert := assert.New(t)
	mt := mocktracer.Start()
	defer mt.Stop()

	rig, err := newRig(true)
	if err != nil {
		t.Fatalf("error setting up rig: %s", err)
	}
	defer rig.Close()
	client := rig.client

	span, ctx := tracer.StartSpanFromContext(context.Background(), "a", tracer.ServiceName("b"), tracer.ResourceName("c"))

	resp, err := client.Ping(ctx, &FixtureRequest{Name: "pass"})
	assert.Nil(err)
	span.Finish()
	assert.Equal(resp.Message, "passed")

	spans := mt.FinishedSpans()
	assert.Len(spans, 3)

	var serverSpan, clientSpan, rootSpan mocktracer.Span

	for _, s := range spans {
		// order of traces in buffer is not garanteed
		switch s.OperationName() {
		case "grpc.server":
			serverSpan = s
		case "grpc.client":
			clientSpan = s
		case "a":
			rootSpan = s
		}
	}

	assert.NotNil(serverSpan)
	assert.NotNil(clientSpan)
	assert.NotNil(rootSpan)

	assert.Equal(clientSpan.Tag(ext.TargetHost), "127.0.0.1")
	assert.Equal(clientSpan.Tag(ext.TargetPort), rig.port)
	assert.Equal(clientSpan.Tag(tagCode), codes.OK.String())
	assert.Equal(clientSpan.TraceID(), rootSpan.TraceID())
	assert.Equal(serverSpan.Tag(ext.ServiceName), "grpc")
	assert.Equal(serverSpan.Tag(ext.ResourceName), "/grpc.Fixture/Ping")
	assert.Equal(serverSpan.TraceID(), rootSpan.TraceID())

}

func TestStreaming(t *testing.T) {
	// creates a stream, then sends/recvs two pings, then closes the stream
	runPings := func(t *testing.T, ctx context.Context, client FixtureClient) {
		stream, err := client.StreamPing(ctx)
		assert.NoError(t, err)

		for i := 0; i < 2; i++ {
			err = stream.Send(&FixtureRequest{Name: "pass"})
			assert.NoError(t, err)

			resp, err := stream.Recv()
			assert.NoError(t, err)
			assert.Equal(t, resp.Message, "passed")
		}
		stream.CloseSend()
		// to flush the spans
		stream.Recv()
	}

	checkSpans := func(t *testing.T, rig *rig, spans []mocktracer.Span) {
		var rootSpan mocktracer.Span
		for _, span := range spans {
			if span.OperationName() == "a" {
				rootSpan = span
			}
		}
		assert.NotNil(t, rootSpan)

		for _, span := range spans {
			if span != rootSpan {
				assert.Equal(t, rootSpan.TraceID(), span.TraceID(),
					"expected span to to have its trace id set to the root trace id (%d): %v",
					rootSpan.TraceID(), span)
				assert.Equal(t, ext.AppTypeRPC, span.Tag(ext.SpanType),
					"expected span type to be rpc in span: %v",
					span)
				assert.Equal(t, "grpc", span.Tag(ext.ServiceName),
					"expected service name to be grpc in span: %v",
					span)
			}

			switch span.OperationName() {
			case "grpc.client":
				// code is only set for the call, not the send/recv messages
				assert.Equal(t, codes.OK.String(), span.Tag(tagCode),
					"expected grpc code to be set in span: %v", span)
				assert.Equal(t, "127.0.0.1", span.Tag(ext.TargetHost),
					"expected target host tag to be set in span: %v", span)
				assert.Equal(t, rig.port, span.Tag(ext.TargetPort),
					"expected target host port to be set in span: %v", span)
				fallthrough
			case "grpc.server", "grpc.message":
				assert.Equal(t, "/grpc.Fixture/StreamPing", span.Tag(ext.ResourceName),
					"expected resource name to be set in span: %v", span)
				assert.Equal(t, "/grpc.Fixture/StreamPing", span.Tag(tagMethod),
					"expected grpc method name to be set in span: %v", span)
			}
		}
	}

	t.Run("All", func(t *testing.T) {
		mt := mocktracer.Start()
		defer mt.Stop()

		rig, err := newRig(true)
		if err != nil {
			t.Fatalf("error setting up rig: %s", err)
		}
		defer rig.Close()

		span, ctx := tracer.StartSpanFromContext(context.Background(), "a",
			tracer.ServiceName("b"),
			tracer.ResourceName("c"))

		runPings(t, ctx, rig.client)

		span.Finish()

		waitForSpans(mt, 13, 5*time.Second)

		spans := mt.FinishedSpans()
		assert.Len(t, spans, 13,
			"expected 4 client messages + 4 server messages + 1 server call + 1 client call + 1 error from empty recv + 1 parent ctx, but got %v",
			len(spans))
		checkSpans(t, rig, spans)
	})

	t.Run("CallsOnly", func(t *testing.T) {
		mt := mocktracer.Start()
		defer mt.Stop()

		rig, err := newRig(true, WithStreamMessages(false))
		if err != nil {
			t.Fatalf("error setting up rig: %s", err)
		}
		defer rig.Close()

		span, ctx := tracer.StartSpanFromContext(context.Background(), "a",
			tracer.ServiceName("b"),
			tracer.ResourceName("c"))

		runPings(t, ctx, rig.client)

		span.Finish()

		waitForSpans(mt, 3, 5*time.Second)

		spans := mt.FinishedSpans()
		assert.Len(t, spans, 3,
			"expected 1 server call + 1 client call + 1 parent ctx, but got %v",
			len(spans))
		checkSpans(t, rig, spans)
	})

	t.Run("MessagesOnly", func(t *testing.T) {
		mt := mocktracer.Start()
		defer mt.Stop()

		rig, err := newRig(true, WithStreamCalls(false))
		if err != nil {
			t.Fatalf("error setting up rig: %s", err)
		}
		defer rig.Close()

		span, ctx := tracer.StartSpanFromContext(context.Background(), "a",
			tracer.ServiceName("b"),
			tracer.ResourceName("c"))

		runPings(t, ctx, rig.client)

		span.Finish()

		waitForSpans(mt, 11, 5*time.Second)

		spans := mt.FinishedSpans()
		assert.Len(t, spans, 11,
			"expected 4 client messages + 4 server messages + 1 error from empty recv + 1 parent ctx, but got %v",
			len(spans))
		checkSpans(t, rig, spans)
	})
}

func TestChild(t *testing.T) {
	assert := assert.New(t)
	mt := mocktracer.Start()
	defer mt.Stop()

	rig, err := newRig(false)
	if err != nil {
		t.Fatalf("error setting up rig: %s", err)
	}
	defer rig.Close()

	client := rig.client
	resp, err := client.Ping(context.Background(), &FixtureRequest{Name: "child"})
	assert.Nil(err)
	assert.Equal(resp.Message, "child")

	spans := mt.FinishedSpans()
	assert.Len(spans, 2)

	var serverSpan, clientSpan mocktracer.Span

	for _, s := range spans {
		// order of traces in buffer is not garanteed
		switch s.OperationName() {
		case "grpc.server":
			serverSpan = s
		case "child":
			clientSpan = s
		}
	}

	assert.NotNil(clientSpan)
	assert.Nil(clientSpan.Tag(ext.Error))
	assert.Equal(clientSpan.Tag(ext.ServiceName), "grpc")
	assert.Equal(clientSpan.Tag(ext.ResourceName), "child")
	assert.True(clientSpan.FinishTime().Sub(clientSpan.StartTime()) > 0)

	assert.NotNil(serverSpan)
	assert.Nil(serverSpan.Tag(ext.Error))
	assert.Equal(serverSpan.Tag(ext.ServiceName), "grpc")
	assert.Equal(serverSpan.Tag(ext.ResourceName), "/grpc.Fixture/Ping")
	assert.True(serverSpan.FinishTime().Sub(serverSpan.StartTime()) > 0)
}

func TestPass(t *testing.T) {
	assert := assert.New(t)
	mt := mocktracer.Start()
	defer mt.Stop()

	rig, err := newRig(false)
	if err != nil {
		t.Fatalf("error setting up rig: %s", err)
	}
	defer rig.Close()

	client := rig.client

	resp, err := client.Ping(context.Background(), &FixtureRequest{Name: "pass"})
	assert.Nil(err)
	assert.Equal(resp.Message, "passed")

	spans := mt.FinishedSpans()
	assert.Len(spans, 1)

	s := spans[0]
	assert.Nil(s.Tag(ext.Error))
	assert.Equal(s.OperationName(), "grpc.server")
	assert.Equal(s.Tag(ext.ServiceName), "grpc")
	assert.Equal(s.Tag(ext.ResourceName), "/grpc.Fixture/Ping")
	assert.Equal(s.Tag(ext.SpanType), ext.AppTypeRPC)
	assert.True(s.FinishTime().Sub(s.StartTime()) > 0)
}

// fixtureServer a dummy implemenation of our grpc fixtureServer.
type fixtureServer struct{}

func (s *fixtureServer) StreamPing(srv Fixture_StreamPingServer) error {
	for {
		msg, err := srv.Recv()
		if err != nil {
			return err
		}

		reply, err := s.Ping(srv.Context(), msg)
		if err != nil {
			return err
		}

		err = srv.Send(reply)
		if err != nil {
			return err
		}
	}
}

func (s *fixtureServer) Ping(ctx context.Context, in *FixtureRequest) (*FixtureReply, error) {
	switch {
	case in.Name == "child":
		span, _ := tracer.StartSpanFromContext(ctx, "child")
		span.Finish()
		return &FixtureReply{Message: "child"}, nil
	case in.Name == "disabled":
		if _, ok := tracer.SpanFromContext(ctx); ok {
			panic("should be disabled")
		}
		return &FixtureReply{Message: "disabled"}, nil
	}
	return &FixtureReply{Message: "passed"}, nil
}

// ensure it's a fixtureServer
var _ FixtureServer = &fixtureServer{}

// rig contains all of the servers and connections we'd need for a
// grpc integration test
type rig struct {
	server   *grpc.Server
	port     string
	listener net.Listener
	conn     *grpc.ClientConn
	client   FixtureClient
}

func (r *rig) Close() {
	r.server.Stop()
	r.conn.Close()
	r.listener.Close()
}

func newRig(traceClient bool, interceptorOpts ...InterceptorOption) (*rig, error) {
	interceptorOpts = append([]InterceptorOption{WithServiceName("grpc")}, interceptorOpts...)

	server := grpc.NewServer(
		grpc.UnaryInterceptor(UnaryServerInterceptor(interceptorOpts...)),
		grpc.StreamInterceptor(StreamServerInterceptor(interceptorOpts...)),
	)

	RegisterFixtureServer(server, new(fixtureServer))

	li, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(li.Addr().String())
	// start our test fixtureServer.
	go server.Serve(li)

	opts := []grpc.DialOption{grpc.WithInsecure()}
	if traceClient {
		opts = append(opts,
			grpc.WithUnaryInterceptor(UnaryClientInterceptor(interceptorOpts...)),
			grpc.WithStreamInterceptor(StreamClientInterceptor(interceptorOpts...)),
		)
	}
	conn, err := grpc.Dial(li.Addr().String(), opts...)
	if err != nil {
		return nil, fmt.Errorf("error dialing: %s", err)
	}
	return &rig{
		listener: li,
		port:     port,
		server:   server,
		conn:     conn,
		client:   NewFixtureClient(conn),
	}, err
}

// waitForSpans polls the mock tracer until the expected number of spans
// appears
func waitForSpans(mt mocktracer.Tracer, sz int, maxWait time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	for len(mt.FinishedSpans()) < sz {
		select {
		case <-ctx.Done():
			return
		default:
		}
		time.Sleep(time.Millisecond * 100)
	}
}
