package samsung

import (
	"context"
	"sync"
)

type remoteConn struct {
	connState
	w         *wsConn
	closeOnce sync.Once
}

func newRemoteConn(w *wsConn) *remoteConn {
	r := &remoteConn{connState: newConnState(), w: w}
	go r.run()
	return r
}

func (r *remoteConn) run() {
	for range r.w.in {
	}
	r.finish(r.w.closedError(channelRemote))
}

func (r *remoteConn) SendKey(ctx context.Context, key string, action KeyAction) error {
	select {
	case <-r.done:
		return r.Err()
	default:
	}
	return r.w.writeJSON(ctx, map[string]any{
		"method": "ms.remote.control",
		"params": map[string]any{
			"Cmd":          string(action),
			"DataOfCmd":    key,
			"Option":       "false",
			"TypeOfRemote": "SendRemoteKey",
		},
	})
}

func (r *remoteConn) Close() error {
	r.closeOnce.Do(func() {
		r.setErr(errClientClosed)
		r.w.close()
	})
	<-r.done
	return nil
}
