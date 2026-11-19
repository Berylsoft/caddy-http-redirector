package redirector

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

func init() {
	caddy.RegisterModule(RawEncoder{})
}

// RawEncoder is a Caddy log encoder that writes only the log message,
// followed by a newline. The redirector logs complete JSON event objects as
// its messages, so this encoder preserves the original one-line-per-event
// format while still letting Caddy own the log destination, rotation, and
// filtering.
//
// It is only needed when routing redirector events through a named Caddy
// log. Without it, Caddy's own encoder wraps the event JSON inside its usual
// envelope.
type RawEncoder struct {
	zapcore.Encoder `json:"-"`
}

// CaddyModule returns the Caddy module information.
func (RawEncoder) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.logging.encoders.redirector",
		New: func() caddy.Module { return new(RawEncoder) },
	}
}

// Provision sets up the embedded encoder, which is only used to satisfy the
// rest of zapcore.Encoder's interface; EncodeEntry does not use it.
func (e *RawEncoder) Provision(_ caddy.Context) error {
	e.Encoder = zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	return nil
}

// Clone implements zapcore.Encoder.
func (e RawEncoder) Clone() zapcore.Encoder {
	if e.Encoder == nil {
		return RawEncoder{Encoder: zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())}
	}
	return RawEncoder{Encoder: e.Encoder.Clone()}
}

var rawEncoderBufferPool = buffer.NewPool()

// EncodeEntry implements zapcore.Encoder by writing the message verbatim.
// All structured fields, timestamps, levels, and logger names are dropped
// on purpose: the message already is a complete JSON log event.
func (RawEncoder) EncodeEntry(ent zapcore.Entry, _ []zapcore.Field) (*buffer.Buffer, error) {
	buf := rawEncoderBufferPool.Get()
	buf.AppendString(ent.Message)
	buf.AppendByte('\n')
	return buf, nil
}

// UnmarshalCaddyfile sets up the encoder from Caddyfile tokens. It accepts
// no arguments and no subdirectives:
//
//	format redirector
func (e *RawEncoder) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume encoder name
	if d.NextArg() {
		return d.ArgErr()
	}
	for d.NextBlock(0) {
		return d.Errf("unrecognized subdirective %q", d.Val())
	}
	return nil
}

var (
	_ caddy.Module          = (*RawEncoder)(nil)
	_ caddy.Provisioner     = (*RawEncoder)(nil)
	_ zapcore.Encoder       = (*RawEncoder)(nil)
	_ caddyfile.Unmarshaler = (*RawEncoder)(nil)
)
