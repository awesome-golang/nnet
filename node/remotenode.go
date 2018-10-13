package node

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/nknorg/nnet/log"
	"github.com/nknorg/nnet/protobuf"
	"github.com/nknorg/nnet/util"
)

const (
	// Max number of msg received that can be buffered
	remoteRxMsgChanLen = 23333

	// Max number of msg to be sent that can be buffered
	remoteTxMsgChanLen = 23333

	// Maximum buffer to receive message
	rxBufLen = 1024 * 16

	// Timeout for reply message
	replyTimeout = 5 * time.Second

	// Time interval between keep-alive msg
	keepAliveInterval = 3 * time.Second

	// Max idle time before considering node dead and closing connection
	keepAliveTimeout = 10 * time.Second
)

// RemoteNode is a remote node
type RemoteNode struct {
	*Node
	LocalNode  *LocalNode
	IsOutbound bool
	conn       net.Conn
	rxBuf      struct {
		buf []byte
		len int
	}
	rxMsgChan chan *protobuf.Message
	txMsgChan chan *protobuf.Message
}

// NewRemoteNode creates a remote node
func NewRemoteNode(localNode *LocalNode, conn net.Conn, isOutbound bool) (*RemoteNode, error) {
	if localNode == nil {
		return nil, errors.New("Local node is nil")
	}
	if conn == nil {
		return nil, errors.New("conn is nil")
	}

	node, err := NewNode(nil, "")
	if err != nil {
		return nil, err
	}

	remoteNode := &RemoteNode{
		Node:       node,
		LocalNode:  localNode,
		conn:       conn,
		IsOutbound: isOutbound,
		rxMsgChan:  make(chan *protobuf.Message, remoteRxMsgChanLen),
		txMsgChan:  make(chan *protobuf.Message, remoteTxMsgChanLen),
	}

	return remoteNode, nil
}

// Start starts the runtime loop of the remote node
func (rn *RemoteNode) Start() error {
	rn.StartOnce.Do(func() {
		if rn.IsStopped() {
			return
		}

		go rn.rx()
		go rn.tx()
		go rn.handleMsg()

		go func() {
			n, err := rn.GetNode()
			if err != nil {
				rn.Stop(fmt.Errorf("Get node error: %s", err))
				return
			}

			host, port, err := net.SplitHostPort(n.Addr)
			if err != nil {
				rn.Stop(fmt.Errorf("Parse node addr %s error: %s", n.Addr, err))
				return
			}
			if port == "" {
				rn.Stop(errors.New("Node addr port is empty"))
				return
			}

			if host == "" {
				connAddr := rn.conn.RemoteAddr().String()
				host, _, err = net.SplitHostPort(connAddr)
				if err != nil {
					rn.Stop(fmt.Errorf("Parse conn remote addr %s error: %s", connAddr, err))
					return
				}
				n.Addr = net.JoinHostPort(host, port)
			}

			rn.Node.Node = n

			for _, f := range rn.LocalNode.middlewareStore.remoteNodeReady {
				if !f(rn) {
					break
				}
			}
		}()
	})

	return nil
}

// Stop stops the runtime loop of the remote node
func (rn *RemoteNode) Stop(err error) {
	rn.StopOnce.Do(func() {
		if err != nil {
			log.Warnf("Remote node %v stops because of error: %s", rn.Node, err)
		} else {
			log.Infof("Remote node %v stops", rn.Node)
		}

		rn.LifeCycle.Stop()

		if rn.conn != nil {
			rn.conn.Close()
		}

		for _, f := range rn.LocalNode.middlewareStore.remoteNodeDisconnected {
			if !f(rn) {
				break
			}
		}
	})
}

// handleMsg starts a loop that handles received msg
func (rn *RemoteNode) handleMsg() {
	var msg *protobuf.Message
	var remoteMsg *RemoteMessage
	var msgChan chan *RemoteMessage
	var err error
	keepAliveTimeoutTimer := time.NewTimer(keepAliveTimeout)

	for {
		if rn.IsStopped() {
			util.StopTimer(keepAliveTimeoutTimer)
			return
		}

		select {
		case msg = <-rn.rxMsgChan:
			remoteMsg, err = NewRemoteMessage(rn, msg)
			if err != nil {
				log.Error(err)
				continue
			}

			msgChan, err = rn.LocalNode.GetRxMsgChan(msg.RoutingType)
			if err != nil {
				log.Error(err)
				continue
			}

			select {
			case msgChan <- remoteMsg:
			default:
				log.Warnf("Msg chan full for routing type %d, discarding msg", msg.RoutingType)
			}
		case <-keepAliveTimeoutTimer.C:
			rn.Stop(errors.New("keepalive timeout"))
		}

		util.ResetTimer(keepAliveTimeoutTimer, keepAliveTimeout)
	}
}

// handleMsgBuf unmarshal buf to msg and send it to msg chan of the local node
func (rn *RemoteNode) handleMsgBuf(buf []byte) {
	msg := &protobuf.Message{}
	err := proto.Unmarshal(buf, msg)
	if err != nil {
		log.Error("unmarshal msg error:", err)
		return
	}

	select {
	case rn.rxMsgChan <- msg:
	default:
		log.Warn("Rx msg chan full, discarding msg")
	}
}

// readBuf read buffer and handle the whole message
func (rn *RemoteNode) readBuf(buf []byte) error {
	if len(buf) == 0 {
		return nil
	}

	if rn.rxBuf.len == 0 {
		length := msgLenBytes - len(rn.rxBuf.buf)
		if length > len(buf) {
			length = len(buf)
			rn.rxBuf.buf = append(rn.rxBuf.buf, buf[0:length]...)
			return nil
		}

		rn.rxBuf.buf = append(rn.rxBuf.buf, buf[0:length]...)
		rn.rxBuf.len = int(binary.BigEndian.Uint32(rn.rxBuf.buf))
		if rn.rxBuf.len < 0 {
			return fmt.Errorf("Message length %d overflow", rn.rxBuf.len)
		}
		buf = buf[length:]
	}

	msgLen := rn.rxBuf.len
	if len(buf) == msgLen {
		rn.handleMsgBuf(buf)
		rn.rxBuf.buf = nil
		rn.rxBuf.len = 0
	} else if len(buf) < msgLen {
		rn.rxBuf.buf = append(rn.rxBuf.buf, buf[:]...)
		rn.rxBuf.len = msgLen - len(buf)
	} else {
		rn.handleMsgBuf(buf[0:msgLen])
		rn.rxBuf.buf = nil
		rn.rxBuf.len = 0
		return rn.readBuf(buf[msgLen:])
	}

	return nil
}

// rx receives and handle data from RemoteNode rn
func (rn *RemoteNode) rx() {
	buf := make([]byte, rxBufLen)
	for {
		if rn.IsStopped() {
			return
		}

		len, err := rn.conn.Read(buf[0 : rxBufLen-1])
		buf[rxBufLen-1] = 0 // Prevent overflow

		switch err {
		case nil:
			err = rn.readBuf(buf[0:len])
			if err != nil {
				log.Warn("Read buffer error:", err)
			}
		case io.EOF:
			rn.Stop(errors.New("Rx get io.EOF"))
		default:
			rn.Stop(fmt.Errorf("Read connection error: %s", err))
		}
	}
}

// tx marshals and sends data in txMsgChan to RemoteNode rn
func (rn *RemoteNode) tx() {
	var msg *protobuf.Message
	var buf []byte
	var err error
	msgLenBuf := make([]byte, msgLenBytes)
	keepAliveTimer := time.NewTimer(keepAliveInterval)

	for {
		if rn.IsStopped() {
			util.StopTimer(keepAliveTimer)
			return
		}

		select {
		case msg = <-rn.txMsgChan:
			buf, err = proto.Marshal(msg)
			if err != nil {
				log.Error(err)
				continue
			}

			binary.BigEndian.PutUint32(msgLenBuf, uint32(len(buf)))

			_, err = rn.conn.Write(msgLenBuf)
			if err != nil {
				rn.Stop(fmt.Errorf("Write to conn error: %s", err))
			}

			_, err = rn.conn.Write(buf)
			if err != nil {
				rn.Stop(fmt.Errorf("Write to conn error: %s", err))
			}
		case <-keepAliveTimer.C:
			rn.keepAlive()
		}

		util.ResetTimer(keepAliveTimer, keepAliveInterval)
	}
}

// SendMessage marshals and sends msg, will returns a RemoteMessage chan if hasReply is true
func (rn *RemoteNode) SendMessage(msg *protobuf.Message, hasReply bool) (chan *RemoteMessage, error) {
	select {
	case rn.txMsgChan <- msg:
	default:
		return nil, errors.New("Tx msg chan full, discarding msg")
	}

	if hasReply {
		replyChan, err := rn.LocalNode.AllocReplyChan(msg.MessageId)
		if err != nil {
			return nil, err
		}

		return replyChan, nil
	}

	return nil, nil
}

// SendMessageAsync sends msg and returns if there is an error
func (rn *RemoteNode) SendMessageAsync(msg *protobuf.Message) error {
	_, err := rn.SendMessage(msg, false)
	return err
}

// SendMessageSync sends msg, returns reply message or error if timeout
func (rn *RemoteNode) SendMessageSync(msg *protobuf.Message) (*RemoteMessage, error) {
	replyChan, err := rn.SendMessage(msg, true)
	if err != nil {
		return nil, err
	}

	select {
	case replyMsg := <-replyChan:
		return replyMsg, nil
	case <-time.After(replyTimeout):
		return nil, errors.New("Wait for reply timeout")
	}
}

func (rn *RemoteNode) keepAlive() error {
	msg, err := NewPingMessage()
	if err != nil {
		return err
	}

	err = rn.SendMessageAsync(msg)
	if err != nil {
		return err
	}

	return nil
}

// Ping sends a Ping message to remote node and wait for reply
func (rn *RemoteNode) Ping() error {
	msg, err := NewPingMessage()
	if err != nil {
		return err
	}

	_, err = rn.SendMessageSync(msg)
	if err != nil {
		return err
	}

	return nil
}

// GetNode sends a GetNode message to remote node and wait for reply
func (rn *RemoteNode) GetNode() (*protobuf.Node, error) {
	msg, err := NewGetNodeMessage()
	if err != nil {
		return nil, err
	}

	reply, err := rn.SendMessageSync(msg)
	if err != nil {
		return nil, err
	}

	replyBody := &protobuf.GetNodeReply{}
	err = proto.Unmarshal(reply.Msg.Message, replyBody)
	if err != nil {
		return nil, err
	}

	return replyBody.Node, nil
}
