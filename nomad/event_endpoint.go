package nomad

import (
	"context"
	"io"
	"io/ioutil"
	"time"

	"github.com/hashicorp/go-msgpack/codec"
	"github.com/hashicorp/nomad/helper/pointer"
	"github.com/hashicorp/nomad/nomad/stream"
	"github.com/hashicorp/nomad/nomad/structs"
)

type Event struct {
	srv *Server
}

func (e *Event) register() {
	e.srv.streamingRpcs.Register("Event.Stream", e.stream)
}

func (e *Event) stream(conn io.ReadWriteCloser) {
	defer conn.Close()

	var args structs.EventStreamRequest
	decoder := codec.NewDecoder(conn, structs.MsgpackHandle)
	encoder := codec.NewEncoder(conn, structs.MsgpackHandle)

	if err := decoder.Decode(&args); err != nil {
		handleJsonResultError(err, pointer.Of(int64(500)), encoder)
		return
	}

	// forward to appropriate region
	if args.Region != e.srv.config.Region {
		err := e.forwardStreamingRPC(args.Region, "Event.Stream", args, conn)
		if err != nil {
			handleJsonResultError(err, pointer.Of(int64(500)), encoder)
		}
		return
	}

	// Generate the subscription request
	subReq := &stream.SubscribeRequest{
		Token:     args.AuthToken,
		Topics:    args.Topics,
		Index:     uint64(args.Index),
		Namespace: args.Namespace,
	}

	// Get the servers broker and subscribe
	publisher, err := e.srv.State().EventBroker()
	if err != nil {
		handleJsonResultError(err, pointer.Of(int64(500)), encoder)
		return
	}

	// start subscription to publisher
	var subscription *stream.Subscription
	var subErr error
	// Check required ACL permissions for requested Topics
	if e.srv.config.ACLEnabled {
		subscription, subErr = publisher.SubscribeWithACLCheck(subReq)
	} else {
		subscription, subErr = publisher.Subscribe(subReq)
	}
	if subErr != nil {
		handleJsonResultError(subErr, pointer.Of(int64(500)), encoder)
		return
	}
	defer subscription.Unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// goroutine to detect remote side closing
	go func() {
		io.Copy(ioutil.Discard, conn)
		cancel()
	}()

	jsonStream := stream.NewJsonStream(ctx, 30*time.Second)
	errCh := make(chan error)
	go func() {
		defer cancel()
		for {
			events, err := subscription.Next(ctx)
			if err != nil {
				select {
				case errCh <- err:
				case <-ctx.Done():
				}
				return
			}

			// Continue if there are no events
			if len(events.Events) == 0 {
				continue
			}

			if err := jsonStream.Send(events); err != nil {
				select {
				case errCh <- err:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	var streamErr error
OUTER:
	for {
		select {
		case streamErr = <-errCh:
			break OUTER
		case <-ctx.Done():
			break OUTER
		case eventJSON, ok := <-jsonStream.OutCh():
			// check if ndjson may have been closed when an error occurred,
			// check once more for an error.
			if !ok {
				select {
				case streamErr = <-errCh:
					// There was a pending error
				default:
				}
				break OUTER
			}

			var resp structs.EventStreamWrapper
			resp.Event = eventJSON

			if err := encoder.Encode(resp); err != nil {
				streamErr = err
				break OUTER
			}
			encoder.Reset(conn)
		}

	}

	if streamErr != nil {
		handleJsonResultError(streamErr, pointer.Of(int64(500)), encoder)
		return
	}

}

func (e *Event) forwardStreamingRPC(region string, method string, args interface{}, in io.ReadWriteCloser) error {
	server, err := e.srv.findRegionServer(region)
	if err != nil {
		return err
	}

	return e.forwardStreamingRPCToServer(server, method, args, in)
}

func (e *Event) forwardStreamingRPCToServer(server *serverParts, method string, args interface{}, in io.ReadWriteCloser) error {
	srvConn, err := e.srv.streamingRpc(server, method)
	if err != nil {
		return err
	}
	defer srvConn.Close()

	outEncoder := codec.NewEncoder(srvConn, structs.MsgpackHandle)
	if err := outEncoder.Encode(args); err != nil {
		return err
	}

	structs.Bridge(in, srvConn)
	return nil
}

// handleJsonResultError is a helper for sending an error with a potential
// error code. The transmission of the error is ignored if the error has been
// generated by the closing of the underlying transport.
func handleJsonResultError(err error, code *int64, encoder *codec.Encoder) {
	// Nothing to do as the conn is closed
	if err == io.EOF {
		return
	}

	encoder.Encode(&structs.EventStreamWrapper{
		Error: structs.NewRpcError(err, code),
	})
}
