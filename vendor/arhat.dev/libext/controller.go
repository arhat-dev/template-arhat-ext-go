package libext

import (
	"context"
	"sync"

	"arhat.dev/arhat-proto/arhatgopb"
	"arhat.dev/pkg/log"

	"arhat.dev/libext/types"
)

// NewController creates a hub for message send/receive
func NewController(
	ctx context.Context,
	logger log.Interface,
	h types.Handler,
) (*Controller, error) {
	return &Controller{
		ctx:    ctx,
		logger: logger,

		handler:     h,
		chRefreshed: make(chan *channelBundle, 1),
		mu:          new(sync.RWMutex),
	}, nil
}

type Controller struct {
	ctx    context.Context
	logger log.Interface

	handler     types.Handler
	currentCB   *channelBundle
	chRefreshed chan *channelBundle

	closed bool
	mu     *sync.RWMutex
}

func (c *Controller) Start() error {
	go c.handleSession()

	return nil
}

func (c *Controller) handleSession() {
	for {
		var (
			cb   *channelBundle
			more bool
		)
		select {
		case <-c.ctx.Done():
			return
		case cb, more = <-c.chRefreshed:
			if !more {
				return
			}
		}

		// new session, register first

		sendMsg := func(msg *arhatgopb.Msg) (sent bool) {
			c.logger.V("sending msg")
			select {
			case <-cb.closed:
				return false
			case cb.msgCh <- msg:
				return true
			case <-c.ctx.Done():
				return false
			}
		}

		c.logger.I("receiving cmds")
	loop:
		// cmdCh will be closed once RefreshChannels called
		for cmd := range cb.cmdCh {
			ret, err := c.handler.HandleCmd(cmd.Id, cmd.Kind, cmd.Payload)
			if err != nil {
				c.logger.I("error happened when handling cmd",
					log.Uint64("id", cmd.Id),
					log.String("kind", cmd.Kind.String()),
				)
				ret = &arhatgopb.ErrorMsg{Description: err.Error()}
			}

			msg, err := arhatgopb.NewMsg(cmd.Id, cmd.Seq, ret)
			if err != nil {
				c.logger.I("failed to marshal response msg",
					log.Uint64("id", cmd.Id),
					log.Error(err),
				)

				// should not happen
				break loop
			}

			if !sendMsg(msg) {
				// not sent, connection error wait for channel refresh
				break loop
			}
		}
	}
}

func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.closed {
		c.closed = true
		close(c.chRefreshed)
	}
}

// RefreshChannels creates a new cmd and msg channel pair for new connection
// usually this function is called in conjunction with Client.ProcessNewStream
func (c *Controller) RefreshChannels() (cmdCh chan<- *arhatgopb.Cmd, msgCh <-chan *arhatgopb.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cb := newChannelBundle()

	select {
	case <-c.ctx.Done():
		return nil, nil
	case c.chRefreshed <- cb:
		if c.currentCB != nil {
			c.currentCB.Close()
		}
	}

	c.currentCB = cb

	return cb.cmdCh, cb.msgCh
}
