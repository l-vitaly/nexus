package router

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gammazero/nexus/wamp"
)

const (
	testRealm       = wamp.URI("nexus.test.realm")
	testProcedure   = wamp.URI("nexus.test.endpoint")
	testProcedureWC = wamp.URI("nexus..endpoint")
)

func init() {
	DebugEnabled = true
}

var clientRoles = map[string]interface{}{
	"roles": map[string]interface{}{
		"subscriber": map[string]interface{}{
			"features": map[string]interface{}{
				"publisher_identification": true,
			},
		},
		"publisher": struct{}{},
		"callee":    struct{}{},
		"caller": map[string]interface{}{
			"features": map[string]interface{}{
				"call_timeout": true,
			},
		},
	},
	"authmethods": []string{"anonymous", "ticket"},
}

func newTestRouter() Router {
	const (
		autoRealm = false
		strictURI = false

		anonAuth      = true
		allowDisclose = false
	)
	r := NewRouter(autoRealm, strictURI)
	r.AddRealm(testRealm, anonAuth, allowDisclose)
	return r
}

func handShake(r Router, client, server wamp.Peer) (wamp.ID, error) {
	client.Send(&wamp.Hello{Realm: testRealm, Details: clientRoles})
	if err := r.Attach(server); err != nil {
		return 0, err
	}

	var sid wamp.ID
	select {
	case <-time.After(time.Second):
		return 0, errors.New("timed out waiting for welcome")
	case msg := <-client.Recv():
		if msg.MessageType() != wamp.WELCOME {
			return 0, fmt.Errorf("expected %v, got %v", wamp.WELCOME,
				msg.MessageType())
		}
		sid = msg.(*wamp.Welcome).ID
	}
	return sid, nil
}

func TestHandshake(t *testing.T) {
	client, server := LinkedPeers()
	r := newTestRouter()
	defer r.Close()
	_, err := handShake(r, client, server)
	if err != nil {
		t.Fatal(err)
	}

	client.Send(&wamp.Goodbye{})
	select {
	case <-time.After(time.Second):
		t.Fatal("no goodbye message after sending goodbye")
	case msg := <-client.Recv():
		if _, ok := msg.(*wamp.Goodbye); !ok {
			t.Fatal("expected GOODBYE, received: ", msg.MessageType())
		}
	}
}

func TestHandshakeBadRealm(t *testing.T) {
	r := NewRouter(false, false)
	defer r.Close()

	client, server := LinkedPeers()

	client.Send(&wamp.Hello{Realm: "does.not.exist"})
	err := r.Attach(server)
	if err == nil {
		t.Error(err)
	}

	if len(client.Recv()) != 1 {
		t.Fatal("Expected one message in the handshake, received ",
			len(client.Recv()))
	}

	msg := <-client.Recv()
	if msg.MessageType() != wamp.ABORT {
		t.Error("Expected ABORT after handshake")
	}
}

func TestRouterSubscribe(t *testing.T) {
	const testTopic = wamp.URI("some.uri")

	sub, subServer := LinkedPeers()
	r := newTestRouter()
	defer r.Close()
	_, err := handShake(r, sub, subServer)
	if err != nil {
		t.Fatal(err)
	}

	subscribeID := wamp.GlobalID()
	sub.Send(&wamp.Subscribe{Request: subscribeID, Topic: testTopic})

	var subscriptionID wamp.ID
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for SUBSCRIBED")
	case msg := <-sub.Recv():
		subMsg, ok := msg.(*wamp.Subscribed)
		if !ok {
			t.Fatal("Expected SUBSCRIBED, got: ", msg.MessageType())
		}
		if subMsg.Request != subscribeID {
			t.Fatal("wrong request ID")
		}
		subscriptionID = subMsg.Subscription
	}

	pub, pubServer := LinkedPeers()
	handShake(r, pub, pubServer)
	pubID := wamp.GlobalID()
	pub.Send(&wamp.Publish{Request: pubID, Topic: testTopic})

	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for EVENT")
	case msg := <-sub.Recv():
		event, ok := msg.(*wamp.Event)
		if !ok {
			t.Fatal("Expected EVENT, got: ", msg.MessageType())
		}
		if event.Subscription != subscriptionID {
			t.Fatal("wrong subscription ID")
		}
	}
}

func TestPublishAcknowledge(t *testing.T) {
	client, server := LinkedPeers()
	r := newTestRouter()
	defer r.Close()
	_, err := handShake(r, client, server)
	if err != nil {
		t.Fatal(err)
	}

	id := wamp.GlobalID()
	client.Send(&wamp.Publish{
		Request: id,
		Options: map[string]interface{}{"acknowledge": true},
		Topic:   "some.uri"})

	select {
	case <-time.After(time.Second):
		t.Fatal("sent acknowledge=true, timed out waiting for PUBLISHED")
	case msg := <-client.Recv():
		pub, ok := msg.(*wamp.Published)
		if !ok {
			t.Fatal("sent acknowledge=true, expected PUBLISHED, got: ",
				msg.MessageType())
		}
		if pub.Request != id {
			t.Fatal("wrong request id")
		}
	}
}

func TestPublishFalseAcknowledge(t *testing.T) {
	client, server := LinkedPeers()
	r := newTestRouter()
	defer r.Close()
	_, err := handShake(r, client, server)
	if err != nil {
		t.Fatal(err)
	}

	id := wamp.GlobalID()
	client.Send(&wamp.Publish{
		Request: id,
		Options: map[string]interface{}{"acknowledge": false},
		Topic:   "some.uri"})

	select {
	case <-time.After(200 * time.Millisecond):
	case msg := <-client.Recv():
		if _, ok := msg.(*wamp.Published); ok {
			t.Fatal("Sent acknowledge=false, but received PUBLISHED: ",
				msg.MessageType())
		}
	}
}

func TestPublishNoAcknowledge(t *testing.T) {
	client, server := LinkedPeers()
	r := newTestRouter()
	defer r.Close()
	_, err := handShake(r, client, server)
	if err != nil {
		t.Fatal(err)
	}

	id := wamp.GlobalID()
	client.Send(&wamp.Publish{Request: id, Topic: "some.uri"})
	select {
	case <-time.After(200 * time.Millisecond):
	case msg := <-client.Recv():
		if _, ok := msg.(*wamp.Published); ok {
			t.Fatal("Sent acknowledge=false, but received PUBLISHED: ",
				msg.MessageType())
		}
	}
}

func TestRouterCall(t *testing.T) {
	callee, calleeServer := LinkedPeers()
	r := newTestRouter()
	defer r.Close()
	_, err := handShake(r, callee, calleeServer)
	if err != nil {
		t.Fatal(err)
	}

	registerID := wamp.GlobalID()
	// Register remote procedure
	callee.Send(&wamp.Register{Request: registerID, Procedure: testProcedure})

	var registrationID wamp.ID
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for REGISTERED")
	case msg := <-callee.Recv():
		registered, ok := msg.(*wamp.Registered)
		if !ok {
			t.Fatal("expected REGISTERED,got: ", msg.MessageType())
		}
		if registered.Request != registerID {
			t.Fatal("wrong request ID")
		}
		registrationID = registered.Registration
	}

	caller, callerServer := LinkedPeers()
	caller.Send(&wamp.Hello{Realm: testRealm, Details: clientRoles})
	if err := r.Attach(callerServer); err != nil {
		t.Fatal("Error connecting caller:", err)
	}
	if msg := <-caller.Recv(); msg.MessageType() != wamp.WELCOME {
		t.Fatal("expected first message to be ", wamp.WELCOME)
	}
	callID := wamp.GlobalID()
	// Call remote procedure
	caller.Send(&wamp.Call{Request: callID, Procedure: testProcedure})

	var invocationID wamp.ID
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for INVOCATION")
	case msg := <-callee.Recv():
		invocation, ok := msg.(*wamp.Invocation)
		if !ok {
			t.Fatal("expected INVOCATION, got: ", msg.MessageType())
		}
		if invocation.Registration != registrationID {
			t.Fatal("wrong registration id")
		}
		invocationID = invocation.Request
	}

	// Returns result of remove procedure
	callee.Send(&wamp.Yield{Request: invocationID})

	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok := msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
}

func TestSessionMetaProcedures(t *testing.T) {
	r := newTestRouter()
	defer r.Close()

	caller, callerServer := LinkedPeers()
	sessID, err := handShake(r, caller, callerServer)
	if err != nil {
		t.Fatal(err)
	}
	var result *wamp.Result
	var ok bool

	// Call session meta-procedure to get session count.
	callID := wamp.GlobalID()
	caller.Send(&wamp.Call{Request: callID, Procedure: wamp.MetaProcSessionCount})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	count, ok := result.Arguments[0].(int)
	if !ok {
		t.Fatal("expected int arguemnt")
	}
	if count != 1 {
		t.Fatal("wrong session count")
	}

	// Call session meta-procedure to get session list.
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{Request: callID, Procedure: wamp.MetaProcSessionList})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	ids, ok := result.Arguments[0].([]wamp.ID)
	if !ok {
		t.Fatal("wrong arg type")
	}
	if len(ids) != count {
		t.Fatal("wrong number of session IDs")
	}
	if sessID != ids[0] {
		t.Fatal("wrong session ID")
	}

	// Call session meta-procedure with bad session ID
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{
		Request:   callID,
		Procedure: wamp.MetaProcSessionGet,
		Arguments: []interface{}{wamp.ID(123456789)},
	})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		errRsp, ok := msg.(*wamp.Error)
		if !ok {
			t.Fatal("expected ERROR, got ", msg.MessageType())
		}
		if errRsp.Error != wamp.ErrNoSuchSession {
			t.Fatal("wrong error value")
		}
	}

	// Call session meta-procedure to get session get.
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{
		Request:   callID,
		Procedure: wamp.MetaProcSessionGet,
		Arguments: []interface{}{sessID},
	})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	dict, ok := result.Arguments[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected dict type arg")
	}
	sid := wamp.ID(wamp.OptionInt64(dict, "session"))
	if sid != sessID {
		t.Fatal("wrong session ID")
	}
}

func TestRegistrationMetaProcedures(t *testing.T) {
	r := newTestRouter()
	defer r.Close()

	caller, callerServer := LinkedPeers()
	sessID, err := handShake(r, caller, callerServer)
	if err != nil {
		t.Fatal(err)
	}
	var result *wamp.Result
	var ok bool

	// ----- Test wamp.registration.list meta procedure -----
	callID := wamp.GlobalID()
	caller.Send(&wamp.Call{Request: callID, Procedure: wamp.MetaProcRegList})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	dict, ok := result.Arguments[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected map[string]interface{}")
	}
	exactPrev, ok := dict["exact"].([]wamp.ID)
	if !ok {
		t.Fatal("expected []wamp.ID")
	}
	prefixPrev, ok := dict["prefix"].([]wamp.ID)
	if !ok {
		t.Fatal("expected []wamp.ID")
	}
	wildcardPrev, ok := dict["wildcard"].([]wamp.ID)
	if !ok {
		t.Fatal("expected []wamp.ID")
	}

	callee, calleeServer := LinkedPeers()
	sessID, err = handShake(r, callee, calleeServer)
	if err != nil {
		t.Fatal(err)
	}
	// Register remote procedure
	registerID := wamp.GlobalID()
	callee.Send(&wamp.Register{Request: registerID, Procedure: testProcedure})

	var registrationID wamp.ID
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for REGISTERED")
	case msg := <-callee.Recv():
		registered, ok := msg.(*wamp.Registered)
		if !ok {
			t.Fatal("expected REGISTERED, got: ", msg.MessageType())
		}
		if registered.Request != registerID {
			t.Fatal("wrong request ID")
		}
		registrationID = registered.Registration
	}

	// Register remote procedure
	callee.Send(&wamp.Register{
		Request:   wamp.GlobalID(),
		Procedure: testProcedureWC,
		Options:   map[string]interface{}{"match": "wildcard"},
	})
	msg := <-callee.Recv()
	if _, ok := msg.(*wamp.Registered); !ok {
		t.Fatal("expected REGISTERED, got: ", msg.MessageType())
	}

	// Call session meta-procedure to get session count.
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{Request: callID, Procedure: wamp.MetaProcRegList})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	dict, ok = result.Arguments[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected map[string]interface{}")
	}
	exact := dict["exact"].([]wamp.ID)
	prefix := dict["prefix"].([]wamp.ID)
	wildcard := dict["wildcard"].([]wamp.ID)

	if len(exact) != len(exactPrev)+1 {
		t.Fatal("expected additional exact match")
	}
	if len(prefix) != len(prefixPrev) {
		t.Fatal("prefix matches should not have changed")
	}
	if len(wildcard) != len(wildcardPrev)+1 {
		t.Fatal("wildcard matches should not have changed")
	}

	var found bool
	for i := range exact {
		if exact[i] == registrationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing expected registration ID")
	}

	// ----- Test wamp.registration.lookup meta procedure -----
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{
		Request:   callID,
		Procedure: wamp.MetaProcRegLookup,
		Arguments: []interface{}{testProcedure},
	})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	regID, ok := result.Arguments[0].(wamp.ID)
	if !ok {
		t.Fatal("expected wamp.ID")
	}
	if regID != registrationID {
		t.Fatal("received wrong registration ID")
	}

	// ----- Test wamp.registration.match meta procedure -----
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{
		Request:   callID,
		Procedure: wamp.MetaProcRegMatch,
		Arguments: []interface{}{testProcedure},
	})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	regID, ok = wamp.AsID(result.Arguments[0])
	if !ok {
		t.Fatal("expected wamp.ID")
	}
	if regID != registrationID {
		t.Fatal("received wrong registration ID")
	}

	// ----- Test wamp.registration.get meta procedure -----
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{
		Request:   callID,
		Procedure: wamp.MetaProcRegGet,
		Arguments: []interface{}{registrationID},
	})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	dict, ok = result.Arguments[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected map[string]interface{}")
	}
	regID = wamp.OptionID(dict, "id")
	if regID != registrationID {
		t.Fatal("received wrong registration")
	}
	uri := wamp.OptionURI(dict, "uri")
	if uri != testProcedure {
		t.Fatal("registration has wrong uri: ", uri)
	}

	// ----- Test wamp.registration.list_callees meta procedure -----
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{
		Request:   callID,
		Procedure: wamp.MetaProcRegListCallees,
		Arguments: []interface{}{registrationID},
	})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	idList, ok := result.Arguments[0].([]wamp.ID)
	if !ok {
		t.Fatal("Expected []wamp.ID")
	}
	if len(idList) != 1 {
		t.Fatal("Expected 1 callee in list")
	}
	if idList[0] != sessID {
		t.Fatal("Wrong callee session ID")
	}

	// ----- Test wamp.registration.list_callees meta procedure -----
	callID = wamp.GlobalID()
	caller.Send(&wamp.Call{
		Request:   callID,
		Procedure: wamp.MetaProcRegCountCallees,
		Arguments: []interface{}{registrationID},
	})
	select {
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for RESULT")
	case msg := <-caller.Recv():
		result, ok = msg.(*wamp.Result)
		if !ok {
			t.Fatal("expected RESULT, got ", msg.MessageType())
		}
		if result.Request != callID {
			t.Fatal("wrong result ID")
		}
	}
	if len(result.Arguments) == 0 {
		t.Fatal("missing expected arguemnt")
	}
	count, ok := wamp.AsInt64(result.Arguments[0])
	if !ok {
		t.Fatal("Argument is not an int")
	}
	if count != 1 {
		t.Fatal("Wring number of callees")
	}
}
