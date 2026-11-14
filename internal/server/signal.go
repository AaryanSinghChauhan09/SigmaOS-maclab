package server

// The lab's Signal sender is signal-cli on the mini, a daemon of the lab
// account that labd reaches over its Unix socket. labd only sends: it asks
// which account signal-cli holds and hands it one message for one group.
// What signal-cli receives it keeps to itself; labd decodes none of it.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"time"
)

// Sender hands one message to a Signal group.
type Sender interface {
	Send(ctx context.Context, groupID, text string) error
}

// ErrNotReached is a send that failed before the message was handed over:
// sending it again cannot post it twice.
var ErrNotReached = errors.New("signal-cli was not reached")

type signalCLI struct{ socket string }

// NewSignalSender is the sender behind signal-cli's daemon socket.
func NewSignalSender(socket string) Sender { return &signalCLI{socket} }

type rpcRequest struct {
	Version string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
	ID      string         `json:"id"`
}

// rpcReply is a line from signal-cli. A message it received arrives as a
// line with a method and params; params has no field here.
type rpcReply struct {
	ID     any             `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var phoneNumber = regexp.MustCompile(`\+\d{6,}`)

// scrub takes phone numbers out of what signal-cli says: labd keeps none.
func scrub(s string) string { return phoneNumber.ReplaceAllString(s, "+…") }

func (c *signalCLI) Send(ctx context.Context, groupID, text string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotReached, err)
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(sendTimeout)
	}
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	rd := bufio.NewReaderSize(conn, 1<<16)

	account, err := signalAccount(conn, rd)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotReached, scrub(err.Error()))
	}
	if err := writeRPC(conn, rpcRequest{"2.0", "send", map[string]any{"account": account, "groupId": groupID, "message": text}, "send"}); err != nil {
		return fmt.Errorf("handing the message to signal-cli: %s", scrub(err.Error()))
	}
	reply, err := readRPC(rd, "send")
	if err != nil {
		return fmt.Errorf("signal-cli took the message and gave no answer: %s", scrub(err.Error()))
	}
	if reply.Error != nil {
		return fmt.Errorf("signal-cli: %s", scrub(reply.Error.Message))
	}
	return nil
}

// signalAccount is the one account signal-cli holds, the lab's sender.
func signalAccount(conn net.Conn, rd *bufio.Reader) (string, error) {
	if err := writeRPC(conn, rpcRequest{"2.0", "listAccounts", nil, "accounts"}); err != nil {
		return "", err
	}
	reply, err := readRPC(rd, "accounts")
	if err != nil {
		return "", err
	}
	if reply.Error != nil {
		return "", fmt.Errorf("signal-cli: %s", reply.Error.Message)
	}
	var accounts []struct {
		Number string `json:"number"`
		ACI    string `json:"aci"`
	}
	if err := json.Unmarshal(reply.Result, &accounts); err != nil {
		return "", fmt.Errorf("signal-cli's accounts: %v", err)
	}
	switch len(accounts) {
	case 1:
	case 0:
		return "", errors.New("signal-cli holds no account: the lab's number is not registered yet")
	default:
		return "", fmt.Errorf("signal-cli holds %d accounts: the lab sends from one", len(accounts))
	}
	if a := accounts[0]; a.Number != "" {
		return a.Number, nil
	} else if a.ACI != "" {
		return a.ACI, nil
	}
	return "", errors.New("signal-cli names no account")
}

func writeRPC(w io.Writer, req rpcRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// readRPC reads lines until the answer to the request id. Every other line
// is dropped unread: the messages signal-cli received are among them.
func readRPC(rd *bufio.Reader, id string) (*rpcReply, error) {
	for {
		line, err := rd.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = rd.ReadSlice('\n')
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		var reply rpcReply
		if json.Unmarshal(line, &reply) != nil || reply.Method != "" {
			continue
		}
		if got, ok := reply.ID.(string); ok && got == id {
			return &reply, nil
		}
	}
}

// signalTestLine is what SignalTest sends.
const signalTestLine = "A test line from Omarchy Mac Lab's Signal sender."

// SignalTest sends one test line to each group that has an id in dir and
// says on w what became of it. It records nothing.
func SignalTest(ctx context.Context, sender Sender, dir string, w io.Writer) error {
	failed, sent := 0, 0
	for _, g := range []struct {
		group lineGroup
		name  string
	}{{lineLab, `the Lab group ("Omarchy Mac Lab")`}, {lineAgents, `"Agents"`}} {
		id := groupID(dir, g.group)
		if id == "" {
			fmt.Fprintf(w, "%s: no group id in %s: nothing sent\n", g.name, groupFiles[g.group])
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := sender.Send(sctx, id, signalTestLine)
		cancel()
		if err != nil {
			failed++
			fmt.Fprintf(w, "%s: not sent: %v\n", g.name, err)
			continue
		}
		sent++
		fmt.Fprintf(w, "%s: sent\n", g.name)
	}
	switch {
	case failed > 0:
		return fmt.Errorf("%d of the test lines did not go", failed)
	case sent == 0:
		return errors.New("no group has an id: nothing was sent")
	}
	return nil
}
